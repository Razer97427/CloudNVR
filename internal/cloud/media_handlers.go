package cloud

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cloudnvr/internal/domain"
	"cloudnvr/internal/id"
	"cloudnvr/internal/store"
)

const streamCookie = "cloudnvr_stream"

func (s *Server) cameraStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("cameraID")
	camera, agentOnline, agentWebRTC, err := s.store.CameraLive(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "camera not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if !camera.Enabled {
		problem(w, http.StatusConflict, "camera is disabled")
		return
	}
	s.setStreamCookie(w)
	path := "camera-" + id
	cloudWebRTCURL := "/media/webrtc/" + path + "?autoplay=true&muted=false&controls=true&playsInline=true"
	response := map[string]string{
		"webrtc_url": cloudWebRTCURL, "cloud_webrtc_url": cloudWebRTCURL,
		"hls_url": "/media/hls/" + path + "?autoplay=true&muted=false&controls=true", "webrtc_mode": "cloud",
	}
	if camera.AccessMode == domain.AccessAgent && agentOnline && agentWebRTC {
		response["agent_webrtc_url"] = "/media/agent-webrtc/" + id + "/view?autoplay=true&muted=false&controls=true&playsInline=true"
		response["webrtc_url"] = response["agent_webrtc_url"]
		response["webrtc_mode"] = "agent_direct"
	}
	writeJSON(w, http.StatusOK, response)
}

const maxWebRTCSignalBody = 2 << 20

func (s *Server) agentWebRTC(w http.ResponseWriter, r *http.Request) {
	cameraID := r.PathValue("cameraID")
	camera, online, capable, err := s.store.CameraLive(r.Context(), cameraID)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "camera not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if !camera.Enabled || camera.AccessMode != domain.AccessAgent || !online || !capable {
		s.agentWebRTCFallback(w, r, cameraID)
		return
	}
	resource := strings.TrimPrefix(r.PathValue("resource"), "/")
	localPath := "/camera-" + cameraID
	switch {
	case resource == "view" || resource == "":
	case resource == "reader.js":
		localPath = "/reader.js"
	case resource == "whep" || strings.HasPrefix(resource, "whep/"):
		localPath += "/" + resource
	default:
		problem(w, http.StatusNotFound, "invalid WebRTC resource")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebRTCSignalBody))
	if err != nil {
		problem(w, http.StatusRequestEntityTooLarge, "WebRTC signaling body is too large")
		return
	}
	requestID, err := id.New()
	if err != nil {
		s.fail(w, err)
		return
	}
	headers := make(map[string]string)
	for _, name := range []string{"Accept", "Content-Type", "If-Match"} {
		if value := r.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	request := domain.WebRTCRequest{ID: requestID, CameraID: cameraID, Method: r.Method, Path: localPath,
		Query: r.URL.RawQuery, Headers: headers, Body: body}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	response, ok := s.webrtc.exchange(ctx, camera.SiteID, request)
	if !ok {
		s.agentWebRTCFallback(w, r, cameraID)
		return
	}
	if response.Status >= 400 && r.Method == http.MethodGet && resource == "view" {
		s.agentWebRTCFallback(w, r, cameraID)
		return
	}
	for name, value := range response.Headers {
		switch strings.ToLower(name) {
		case "content-type", "cache-control", "etag", "accept-patch", "link":
			w.Header().Set(name, value)
		case "location":
			w.Header().Set("Location", rewriteAgentWebRTCLocation(value, cameraID))
		}
	}
	status := response.Status
	if status < 100 || status > 599 {
		status = http.StatusBadGateway
	}
	w.WriteHeader(status)
	_, _ = w.Write(response.Body)
}

func (s *Server) agentWebRTCFallback(w http.ResponseWriter, r *http.Request, cameraID string) {
	if r.Method == http.MethodGet && r.PathValue("resource") == "view" {
		target := "/media/webrtc/camera-" + cameraID
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		return
	}
	problem(w, http.StatusServiceUnavailable, "agent WebRTC is unavailable")
}

func rewriteAgentWebRTCLocation(location, cameraID string) string {
	parsed, err := url.Parse(location)
	if err != nil {
		return location
	}
	prefix := "/camera-" + cameraID + "/"
	resource := strings.TrimPrefix(parsed.Path, prefix)
	if resource == parsed.Path {
		resource = strings.TrimPrefix(parsed.Path, "/")
	}
	result := "/media/agent-webrtc/" + cameraID + "/" + resource
	if parsed.RawQuery != "" {
		result += "?" + parsed.RawQuery
	}
	return result
}

func (s *Server) setStreamCookie(w http.ResponseWriter) {
	expires := time.Now().Add(time.Hour)
	http.SetCookie(w, &http.Cookie{
		Name: streamCookie, Value: s.signStreamCookie(expires), Path: "/", HttpOnly: true,
		Secure: strings.HasPrefix(s.publicURL, "https://"), SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 3600,
	})
}

func (s *Server) signStreamCookie(expires time.Time) string {
	payload := strconv.FormatInt(expires.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(s.adminAPIKey))
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) validStreamCookie(r *http.Request) bool {
	cookie, err := r.Cookie(streamCookie)
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() >= expires {
		return false
	}
	expected := s.signStreamCookie(time.Unix(expires, 0))
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(expected)) == 1
}

func (s *Server) mediaViewer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.validStreamCookie(r) {
			problem(w, http.StatusUnauthorized, "stream session expired")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) webRTC(w http.ResponseWriter, r *http.Request) {
	if s.webrtcProxy == nil {
		problem(w, http.StatusServiceUnavailable, "WebRTC service is not configured")
		return
	}
	s.webrtcProxy.ServeHTTP(w, r)
}

func (s *Server) hls(w http.ResponseWriter, r *http.Request) {
	if s.hlsProxy == nil {
		problem(w, http.StatusServiceUnavailable, "HLS service is not configured")
		return
	}
	s.hlsProxy.ServeHTTP(w, r)
}

func (s *Server) mediaAuth(w http.ResponseWriter, r *http.Request) {
	var in struct {
		User      string `json:"user"`
		Password  string `json:"password"`
		Token     string `json:"token"`
		Action    string `json:"action"`
		Protocol  string `json:"protocol"`
		Path      string `json:"path"`
		ID        string `json:"id"`
		IP        string `json:"ip"`
		Query     string `json:"query"`
		UserAgent string `json:"userAgent"`
	}
	if !decode(w, r, &in) {
		return
	}
	allowed := false
	if in.Action == "publish" && strings.HasPrefix(in.Path, "camera-") {
		allowed = subtle.ConstantTimeCompare([]byte(in.User), []byte(s.mediaPublishUser)) == 1 &&
			subtle.ConstantTimeCompare([]byte(in.Password), []byte(s.mediaPublishPassword)) == 1
	} else if in.Action == "read" && in.Protocol == "rtsp" && strings.HasPrefix(in.Path, "camera-") {
		allowed = subtle.ConstantTimeCompare([]byte(in.User), []byte(s.mediaPublishUser)) == 1 &&
			subtle.ConstantTimeCompare([]byte(in.Password), []byte(s.mediaPublishPassword)) == 1
	} else if in.Action == "read" && (in.Protocol == "webrtc" || in.Protocol == "hls") {
		allowed = strings.HasPrefix(in.Path, "camera-")
	}
	if !allowed {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusOK)
}
