package cloud

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"cloudnvr/internal/config"
	"cloudnvr/internal/domain"
	"cloudnvr/internal/id"
	"cloudnvr/internal/security"
	"cloudnvr/internal/store"
)

type Server struct {
	store                *store.Store
	cipher               *security.Cipher
	adminAPIKey          string
	publicURL            string
	logger               *slog.Logger
	webProxy             *httputil.ReverseProxy
	webrtcProxy          *httputil.ReverseProxy
	hlsProxy             *httputil.ReverseProxy
	mediaPublishUser     string
	mediaPublishPassword string
	mediaPublicRTSPURL   string
	recordingsPath       string
	agentUploadMaxBytes  int64
	playbacks            *playbackBroker
	ptz                  *ptzBroker
	webrtc               *webRTCBroker
}

func NewServer(s *store.Store, cipher *security.Cipher, cfg config.Cloud, logger *slog.Logger) *Server {
	server := &Server{store: s, cipher: cipher, adminAPIKey: cfg.AdminAPIKey, publicURL: strings.TrimRight(cfg.PublicURL, "/"), logger: logger,
		mediaPublishUser: cfg.MediaPublishUser, mediaPublishPassword: cfg.MediaPublishPassword}
	server.mediaPublicRTSPURL = cfg.MediaPublicRTSPURL
	server.recordingsPath = filepath.Clean(cfg.RecordingsPath)
	server.agentUploadMaxBytes = cfg.AgentUploadMaxBytes
	server.playbacks = newPlaybackBroker(server.recordingsPath, cfg.PlaybackCacheTTL, cfg.PlaybackCacheMaxBytes)
	server.ptz = newPTZBroker()
	server.webrtc = newWebRTCBroker()
	server.webProxy = reverseProxy(cfg.WebUpstream, "web interface", logger)
	server.webrtcProxy = reverseProxy(cfg.MediaWebRTCUpstream, "WebRTC service", logger)
	server.hlsProxy = reverseProxy(cfg.MediaHLSUpstream, "HLS service", logger)
	rewriteMediaRedirects(server.webrtcProxy, "/media/webrtc")
	rewriteMediaRedirects(server.hlsProxy, "/media/hls")
	return server
}

func rewriteMediaRedirects(proxy *httputil.ReverseProxy, prefix string) {
	if proxy == nil {
		return
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		if location := response.Header.Get("Location"); strings.HasPrefix(location, "/") {
			response.Header.Set("Location", prefix+location)
		}
		return nil
	}
}

func reverseProxy(upstream, label string, logger *slog.Logger) *httputil.ReverseProxy {
	target, err := url.Parse(upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		logger.Error(label+" unavailable", "error", err)
		problem(w, http.StatusBadGateway, label+" unavailable")
	}
	return proxy
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.Handle("POST /api/sites", s.admin(http.HandlerFunc(s.createSite)))
	mux.Handle("GET /api/sites", s.admin(http.HandlerFunc(s.listSites)))
	mux.Handle("POST /api/sites/{siteID}/enrollment-token", s.admin(http.HandlerFunc(s.resetEnrollmentToken)))
	mux.Handle("POST /api/sites/{siteID}/cameras", s.admin(http.HandlerFunc(s.createCamera)))
	mux.Handle("GET /api/sites/{siteID}/cameras", s.admin(http.HandlerFunc(s.listCameras)))
	mux.Handle("PUT /api/cameras/{cameraID}", s.admin(http.HandlerFunc(s.updateCamera)))
	mux.Handle("PUT /api/cameras/{cameraID}/policy", s.admin(http.HandlerFunc(s.updateCameraPolicy)))
	mux.Handle("GET /api/cameras/{cameraID}/stream", s.admin(http.HandlerFunc(s.cameraStream)))
	mux.Handle("POST /api/cameras/{cameraID}/recording", s.admin(http.HandlerFunc(s.setManualRecording)))
	mux.Handle("POST /api/cameras/{cameraID}/ptz", s.admin(http.HandlerFunc(s.controlPTZ)))
	mux.Handle("GET /api/recordings", s.admin(http.HandlerFunc(s.listRecordings)))
	mux.Handle("POST /api/recordings/{recordingID}/prepare", s.admin(http.HandlerFunc(s.prepareRecording)))
	mux.Handle("GET /api/recordings/{recordingID}/prepare", s.admin(http.HandlerFunc(s.recordingPreparation)))
	mux.Handle("POST /api/recordings/{recordingID}/export", s.admin(http.HandlerFunc(s.exportRecording)))
	mux.Handle("POST /api/recordings/export-range", s.admin(http.HandlerFunc(s.exportRecordingRange)))
	mux.Handle("GET /recordings/{recordingID}", s.mediaViewer(http.HandlerFunc(s.playRecording)))
	mux.Handle("POST /api/mobile/pairings", s.admin(http.HandlerFunc(s.createMobilePairing)))
	mux.HandleFunc("POST /api/mobile/claim", s.claimMobilePairing)
	mux.Handle("GET /api/mobile/devices", s.admin(http.HandlerFunc(s.listMobileDevices)))
	mux.Handle("DELETE /api/mobile/devices/{deviceID}", s.admin(http.HandlerFunc(s.revokeMobileDevice)))
	mux.Handle("GET /api/session", s.admin(http.HandlerFunc(s.sessionInfo)))
	mux.Handle("POST /api/session/logout", s.admin(http.HandlerFunc(s.sessionLogout)))
	mux.HandleFunc("POST /api/agent/enroll", s.enrollAgent)
	mux.Handle("POST /api/agent/heartbeat", s.agent(http.HandlerFunc(s.heartbeat)))
	mux.Handle("GET /api/agent/config", s.agent(http.HandlerFunc(s.agentConfig)))
	mux.Handle("POST /api/agent/recordings", s.agent(http.HandlerFunc(s.syncAgentRecordings)))
	mux.Handle("GET /api/agent/recording-requests", s.agent(http.HandlerFunc(s.nextRecordingRequest)))
	mux.Handle("PUT /api/agent/recording-requests/{requestID}", s.agent(http.HandlerFunc(s.uploadRequestedRecording)))
	mux.Handle("DELETE /api/agent/recording-requests/{requestID}", s.agent(http.HandlerFunc(s.rejectRequestedRecording)))
	mux.Handle("GET /api/agent/ptz-commands", s.agent(http.HandlerFunc(s.nextPTZCommand)))
	mux.Handle("POST /api/agent/ptz-commands/{commandID}", s.agent(http.HandlerFunc(s.completePTZCommand)))
	mux.Handle("GET /api/agent/webrtc-requests", s.agent(http.HandlerFunc(s.nextWebRTCRequest)))
	mux.Handle("POST /api/agent/webrtc-requests/{requestID}", s.agent(http.HandlerFunc(s.completeWebRTCRequest)))
	mux.HandleFunc("POST /internal/media/auth", s.mediaAuth)
	mux.Handle("/media/agent-webrtc/{cameraID}/{resource...}", s.mediaViewer(http.HandlerFunc(s.agentWebRTC)))
	mux.Handle("/media/webrtc/", s.mediaViewer(http.StripPrefix("/media/webrtc", http.HandlerFunc(s.webRTC))))
	mux.Handle("/media/hls/", s.mediaViewer(http.StripPrefix("/media/hls", http.HandlerFunc(s.hls))))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { problem(w, http.StatusNotFound, "API route not found") })
	mux.HandleFunc("/", s.web)
	return s.logging(mux)
}

func (s *Server) setManualRecording(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Active bool `json:"active"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.store.SetManualRecording(r.Context(), r.PathValue("cameraID"), in.Active)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusConflict, "camera is not in manual recording mode")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"active": in.Active})
}

func (s *Server) web(w http.ResponseWriter, r *http.Request) {
	if s.webProxy == nil {
		problem(w, http.StatusNotFound, "web interface is not configured")
		return
	}
	s.webProxy.ServeHTTP(w, r)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) createSite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name                 string               `json:"name"`
		Location             string               `json:"location"`
		DefaultRecordingMode domain.RecordingMode `json:"default_recording_mode"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.DefaultRecordingMode == "" {
		in.DefaultRecordingMode = domain.ModeHybrid
	}
	if in.Name == "" || !in.DefaultRecordingMode.Valid() {
		problem(w, http.StatusBadRequest, "name and a valid default_recording_mode are required")
		return
	}
	siteID, err := id.New()
	if err != nil {
		s.fail(w, err)
		return
	}
	token, err := security.RandomToken()
	if err != nil {
		s.fail(w, err)
		return
	}
	site := domain.Site{ID: siteID, Name: in.Name, Location: in.Location, DefaultRecordingMode: in.DefaultRecordingMode, CreatedAt: time.Now().UTC()}
	if err := s.store.CreateSite(r.Context(), site, security.TokenHash(token)); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"site": site, "enrollment_token": token,
		"agent_environment": map[string]string{"CLOUD_URL": s.publicURL, "SITE_ID": siteID, "ENROLLMENT_TOKEN": token},
	})
}

func (s *Server) resetEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	token, err := security.RandomToken()
	if err != nil {
		s.fail(w, err)
		return
	}
	err = s.store.ResetEnrollmentToken(r.Context(), r.PathValue("siteID"), security.TokenHash(token))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "site not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enrollment_token": token,
		"agent_environment": map[string]string{
			"CLOUD_URL": s.publicURL, "SITE_ID": r.PathValue("siteID"), "ENROLLMENT_TOKEN": token,
		},
	})
}

func (s *Server) listSites(w http.ResponseWriter, r *http.Request) {
	sites, err := s.store.ListSites(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": sites})
}

func (s *Server) createCamera(w http.ResponseWriter, r *http.Request) {
	var in domain.Camera
	if !decode(w, r, &in) {
		return
	}
	in.SiteID = r.PathValue("siteID")
	in.Name = strings.TrimSpace(in.Name)
	if in.RecordingMode == "" {
		in.RecordingMode = domain.ModeHybrid
	}
	if in.TransferPolicy == "" {
		in.TransferPolicy = domain.TransferEventsAndManual
	}
	if in.AccessMode == "" {
		in.AccessMode = domain.AccessAgent
	}
	if in.LocalRetentionDays == 0 {
		in.LocalRetentionDays = 7
	}
	if in.CloudRetentionDays == 0 {
		in.CloudRetentionDays = 30
	}
	if in.Name == "" || !validStreamURL(in.StreamURL) || !in.AccessMode.Valid() || !in.RecordingMode.Valid() || !in.TransferPolicy.Valid() {
		problem(w, http.StatusBadRequest, "name, a valid rtsp/rtsps URL, access_mode, recording_mode and transfer_policy are required")
		return
	}
	in.ID, _ = id.New()
	in.CreatedAt = time.Now().UTC()
	encrypted, err := s.cipher.Encrypt(in.StreamURL)
	if err != nil {
		s.fail(w, err)
		return
	}
	in.Enabled = true
	encryptedPTZPassword := ""
	if in.PTZEnabled {
		if in.PTZEndpoint == "" {
			in.PTZEndpoint = defaultONVIFEndpoint(in.StreamURL)
		}
		if !validHTTPURL(in.PTZEndpoint) {
			problem(w, http.StatusBadRequest, "ptz_endpoint must use http or https")
			return
		}
		if in.PTZPassword != "" {
			encryptedPTZPassword, err = s.cipher.Encrypt(in.PTZPassword)
			if err != nil {
				s.fail(w, err)
				return
			}
		}
	}
	if err := s.store.CreateCamera(r.Context(), in, encrypted, encryptedPTZPassword); err != nil {
		s.fail(w, err)
		return
	}
	in.StreamURL = ""
	in.PTZPassword = ""
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) listCameras(w http.ResponseWriter, r *http.Request) {
	cameras, secrets, err := s.store.ListCameras(r.Context(), r.PathValue("siteID"), true, "")
	if err != nil {
		s.fail(w, err)
		return
	}
	for index := range cameras {
		streamURL, err := s.cipher.Decrypt(secrets[cameras[index].ID])
		if err != nil {
			s.fail(w, err)
			return
		}
		cameras[index].StreamURL = streamURL
	}
	writeJSON(w, http.StatusOK, map[string]any{"cameras": cameras})
}

func (s *Server) updateCameraPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RecordingMode      domain.RecordingMode  `json:"recording_mode"`
		LocalRetentionDays uint                  `json:"local_retention_days"`
		CloudRetentionDays uint                  `json:"cloud_retention_days"`
		TransferPolicy     domain.TransferPolicy `json:"transfer_policy"`
		Enabled            bool                  `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !in.RecordingMode.Valid() || !in.TransferPolicy.Valid() {
		problem(w, http.StatusBadRequest, "invalid recording_mode or transfer_policy")
		return
	}
	err := s.store.UpdateCameraPolicy(r.Context(), r.PathValue("cameraID"), in.RecordingMode, in.LocalRetentionDays, in.CloudRetentionDays, in.TransferPolicy, in.Enabled)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "camera not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) updateCamera(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name               string                `json:"name"`
		StreamURL          string                `json:"stream_url"`
		AccessMode         domain.AccessMode     `json:"access_mode"`
		RecordingMode      domain.RecordingMode  `json:"recording_mode"`
		LocalRetentionDays uint                  `json:"local_retention_days"`
		CloudRetentionDays uint                  `json:"cloud_retention_days"`
		TransferPolicy     domain.TransferPolicy `json:"transfer_policy"`
		Enabled            bool                  `json:"enabled"`
		PTZEnabled         bool                  `json:"ptz_enabled"`
		PTZEndpoint        string                `json:"ptz_endpoint"`
		PTZUsername        string                `json:"ptz_username"`
		PTZPassword        string                `json:"ptz_password"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.StreamURL = strings.TrimSpace(in.StreamURL)
	if in.Name == "" || !in.AccessMode.Valid() || !in.RecordingMode.Valid() || !in.TransferPolicy.Valid() {
		problem(w, http.StatusBadRequest, "name, access_mode, recording_mode and transfer_policy are required")
		return
	}
	var encryptedURL *string
	if in.StreamURL != "" {
		if !validStreamURL(in.StreamURL) {
			problem(w, http.StatusBadRequest, "stream_url must use rtsp or rtsps")
			return
		}
		encrypted, err := s.cipher.Encrypt(in.StreamURL)
		if err != nil {
			s.fail(w, err)
			return
		}
		encryptedURL = &encrypted
	}
	in.PTZEndpoint = strings.TrimSpace(in.PTZEndpoint)
	in.PTZUsername = strings.TrimSpace(in.PTZUsername)
	if in.PTZEnabled && in.PTZEndpoint == "" && in.StreamURL != "" {
		in.PTZEndpoint = defaultONVIFEndpoint(in.StreamURL)
	}
	if in.PTZEnabled && !validHTTPURL(in.PTZEndpoint) {
		problem(w, http.StatusBadRequest, "ptz_endpoint must use http or https")
		return
	}
	var encryptedPTZPassword *string
	if in.PTZPassword != "" {
		encrypted, err := s.cipher.Encrypt(in.PTZPassword)
		if err != nil {
			s.fail(w, err)
			return
		}
		encryptedPTZPassword = &encrypted
	}
	err := s.store.UpdateCamera(r.Context(), r.PathValue("cameraID"), in.Name, encryptedURL, encryptedPTZPassword, in.AccessMode, in.RecordingMode,
		in.LocalRetentionDays, in.CloudRetentionDays, in.TransferPolicy, in.Enabled, in.PTZEnabled, in.PTZEndpoint, in.PTZUsername)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "camera not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validStreamURL(v string) bool {
	lower := strings.ToLower(strings.TrimSpace(v))
	return strings.HasPrefix(lower, "rtsp://") || strings.HasPrefix(lower, "rtsps://")
}

func validHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func defaultONVIFEndpoint(streamURL string) string {
	parsed, err := url.Parse(streamURL)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	host := parsed.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "http://" + host + "/onvif/device_service"
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.logger.Error("request failed", "error", err)
	problem(w, http.StatusInternalServerError, "internal server error")
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		problem(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func problem(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (s *Server) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.adminAPIKey)) == 1 {
			ctx := context.WithValue(r.Context(), adminIdentityKey{}, adminIdentity{Method: "admin_key"})
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		cookie, err := r.Cookie(deviceCookie)
		if err == nil && cookie.Value != "" {
			session, sessionErr := s.store.DeviceSessionByTokenHash(r.Context(), security.TokenHash(cookie.Value))
			if sessionErr == nil {
				ctx := context.WithValue(r.Context(), adminIdentityKey{}, adminIdentity{Method: "paired_device", DeviceID: session.ID})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if !errors.Is(sessionErr, store.ErrNotFound) {
				s.fail(w, sessionErr)
				return
			}
		}
		problem(w, http.StatusUnauthorized, "invalid admin session")
	})
}

type agentIdentity struct{ AgentID, SiteID string }
type identityKey struct{}
type adminIdentity struct {
	Method   string
	DeviceID string
}
type adminIdentityKey struct{}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}
