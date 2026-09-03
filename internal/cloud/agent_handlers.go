package cloud

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"cloudnvr/internal/domain"
	"cloudnvr/internal/id"
	"cloudnvr/internal/media"
	"cloudnvr/internal/security"
	"cloudnvr/internal/store"
)

func (s *Server) enrollAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SiteID          string `json:"site_id"`
		EnrollmentToken string `json:"enrollment_token"`
		Name            string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	agentID, err := id.New()
	if err != nil {
		s.fail(w, err)
		return
	}
	agentToken, err := security.RandomToken()
	if err != nil {
		s.fail(w, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = "site-agent"
	}
	if err := s.store.EnrollAgent(r.Context(), agentID, in.SiteID, in.Name, security.TokenHash(agentToken), security.TokenHash(in.EnrollmentToken)); errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusUnauthorized, "invalid or already used enrollment credentials")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"agent_id": agentID, "agent_token": agentToken})
}

func (s *Server) agent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			problem(w, http.StatusUnauthorized, "missing agent token")
			return
		}
		agentID, siteID, err := s.store.AgentByTokenHash(r.Context(), security.TokenHash(token))
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusUnauthorized, "invalid agent token")
			return
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		if err := s.store.TouchAgentPresence(r.Context(), agentID); err != nil {
			s.fail(w, err)
			return
		}
		ctx := context.WithValue(r.Context(), identityKey{}, agentIdentity{AgentID: agentID, SiteID: siteID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	identity := r.Context().Value(identityKey{}).(agentIdentity)
	var in struct {
		Version       string             `json:"version"`
		WebRTCEnabled bool               `json:"webrtc_enabled"`
		Health        domain.AgentHealth `json:"health"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Health.Error) > 1000 {
		in.Health.Error = in.Health.Error[:1000]
	}
	if err := s.store.Heartbeat(r.Context(), identity.AgentID, in.Version, in.WebRTCEnabled, in.Health); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) agentConfig(w http.ResponseWriter, r *http.Request) {
	identity := r.Context().Value(identityKey{}).(agentIdentity)
	cameras, secrets, err := s.store.ListCameras(r.Context(), identity.SiteID, true, domain.AccessAgent)
	if err != nil {
		s.fail(w, err)
		return
	}
	for i := range cameras {
		streamURL, err := s.cipher.Decrypt(secrets[cameras[i].ID])
		if err != nil {
			s.fail(w, err)
			return
		}
		cameras[i].StreamURL = streamURL
		if encryptedPTZPassword := secrets[cameras[i].ID+":ptz"]; encryptedPTZPassword != "" {
			ptzPassword, err := s.cipher.Decrypt(encryptedPTZPassword)
			if err != nil {
				s.fail(w, err)
				return
			}
			cameras[i].PTZPassword = ptzPassword
		}
		publishURL, err := media.PublishURL(s.mediaPublicRTSPURL, s.mediaPublishUser, s.mediaPublishPassword, cameras[i].ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		cameras[i].PublishURL = publishURL
	}
	writeJSON(w, http.StatusOK, map[string]any{"site_id": identity.SiteID, "cameras": cameras})
}
