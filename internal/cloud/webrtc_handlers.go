package cloud

import (
	"context"
	"net/http"
	"time"

	"cloudnvr/internal/domain"
)

func (s *Server) nextWebRTCRequest(w http.ResponseWriter, r *http.Request) {
	identity := r.Context().Value(identityKey{}).(agentIdentity)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	request, ok := s.webrtc.next(ctx, identity.SiteID)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, request)
}

func (s *Server) completeWebRTCRequest(w http.ResponseWriter, r *http.Request) {
	identity := r.Context().Value(identityKey{}).(agentIdentity)
	var response domain.WebRTCResponse
	if !decode(w, r, &response) {
		return
	}
	response.ID = r.PathValue("requestID")
	if !s.webrtc.resolve(identity.SiteID, response) {
		problem(w, http.StatusNotFound, "WebRTC request expired or not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
