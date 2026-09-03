package cloud

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cloudnvr/internal/domain"
	"cloudnvr/internal/id"
	"cloudnvr/internal/security"
	"cloudnvr/internal/store"
)

const deviceCookie = "cloudnvr_device"

func (s *Server) createMobilePairing(w http.ResponseWriter, r *http.Request) {
	token, err := security.RandomToken()
	if err != nil {
		s.fail(w, err)
		return
	}
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	if err := s.store.CreateMobilePairing(r.Context(), security.TokenHash(token), expiresAt); err != nil {
		s.fail(w, err)
		return
	}
	pairingURL := s.publicURL + "/?pair=" + url.QueryEscape(token)
	writeJSON(w, http.StatusCreated, map[string]any{"pairing_url": pairingURL, "expires_at": expiresAt})
}

func (s *Server) claimMobilePairing(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code       string `json:"code"`
		DeviceName string `json:"device_name"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Code = strings.TrimSpace(in.Code)
	in.DeviceName = strings.TrimSpace(in.DeviceName)
	if in.Code == "" {
		problem(w, http.StatusBadRequest, "pairing code is required")
		return
	}
	if in.DeviceName == "" {
		in.DeviceName = "iPhone"
	}
	if len(in.DeviceName) > 100 {
		problem(w, http.StatusBadRequest, "device name is too long")
		return
	}
	sessionID, err := id.New()
	if err != nil {
		s.fail(w, err)
		return
	}
	sessionToken, err := security.RandomToken()
	if err != nil {
		s.fail(w, err)
		return
	}
	session := domain.DeviceSession{ID: sessionID, Name: in.DeviceName, ExpiresAt: time.Now().UTC().Add(180 * 24 * time.Hour)}
	err = s.store.ClaimMobilePairing(r.Context(), security.TokenHash(in.Code), session, security.TokenHash(sessionToken))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusUnauthorized, "pairing code is invalid, expired or already used")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.setDeviceCookie(w, sessionToken, session.ExpiresAt)
	writeJSON(w, http.StatusCreated, map[string]any{"status": "paired", "device": session})
}

func (s *Server) listMobileDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.store.ListDeviceSessions(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (s *Server) revokeMobileDevice(w http.ResponseWriter, r *http.Request) {
	err := s.store.RevokeDeviceSession(r.Context(), r.PathValue("deviceID"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "device not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sessionInfo(w http.ResponseWriter, r *http.Request) {
	identity, _ := r.Context().Value(adminIdentityKey{}).(adminIdentity)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "method": identity.Method, "device_id": identity.DeviceID})
}

func (s *Server) sessionLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(deviceCookie); err == nil && cookie.Value != "" {
		if err := s.store.RevokeDeviceSessionByTokenHash(r.Context(), security.TokenHash(cookie.Value)); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.setDeviceCookie(w, "", time.Unix(1, 0))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setDeviceCookie(w http.ResponseWriter, token string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if token == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name: deviceCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: strings.HasPrefix(s.publicURL, "https://"), SameSite: http.SameSiteLaxMode,
		Expires: expires, MaxAge: maxAge,
	})
}
