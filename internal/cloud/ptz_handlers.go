package cloud

import (
	"context"
	"errors"
	"net/http"
	"time"

	"cloudnvr/internal/domain"
	"cloudnvr/internal/id"
	"cloudnvr/internal/onvif"
	"cloudnvr/internal/store"
)

func (s *Server) controlPTZ(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string  `json:"action"`
		Pan    float64 `json:"pan"`
		Tilt   float64 `json:"tilt"`
		Zoom   float64 `json:"zoom"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Action == "" {
		in.Action = "move"
	}
	if in.Action != "move" && in.Action != "set_home" && in.Action != "goto_home" {
		problem(w, http.StatusBadRequest, "invalid PTZ action")
		return
	}
	if in.Action == "move" && (in.Pan < -1 || in.Pan > 1 || in.Tilt < -1 || in.Tilt > 1 || in.Zoom < -1 || in.Zoom > 1 || (in.Pan == 0 && in.Tilt == 0 && in.Zoom == 0)) {
		problem(w, http.StatusBadRequest, "invalid PTZ movement")
		return
	}
	camera, encryptedPassword, err := s.store.CameraPTZ(r.Context(), r.PathValue("cameraID"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "camera not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if !camera.Enabled || !camera.PTZEnabled || !validHTTPURL(camera.PTZEndpoint) {
		problem(w, http.StatusConflict, "PTZ is not configured for this camera")
		return
	}
	password := ""
	if encryptedPassword != "" {
		password, err = s.cipher.Decrypt(encryptedPassword)
		if err != nil {
			s.fail(w, err)
			return
		}
	}
	commandID, err := id.New()
	if err != nil {
		s.fail(w, err)
		return
	}
	command := domain.PTZCommand{ID: commandID, CameraID: camera.ID, Action: in.Action, Endpoint: camera.PTZEndpoint,
		Username: camera.PTZUsername, Password: password, Pan: in.Pan, Tilt: in.Tilt, Zoom: in.Zoom}
	if camera.AccessMode == domain.AccessAgent {
		result := s.ptz.enqueue(camera.SiteID, command)
		defer s.ptz.abandon(command.ID)
		select {
		case completed := <-result:
			if !completed.Success {
				message := completed.Error
				if message == "" {
					message = "camera rejected the PTZ command"
				}
				problem(w, http.StatusBadGateway, message)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "executed"})
		case <-time.After(10 * time.Second):
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
		case <-r.Context().Done():
			return
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if err := executePTZ(ctx, command); err != nil {
		s.logger.Warn("direct PTZ command failed", "camera_id", camera.ID, "error", err)
		problem(w, http.StatusBadGateway, "camera rejected the PTZ command")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "executed"})
}

func executePTZ(ctx context.Context, command domain.PTZCommand) error {
	switch command.Action {
	case "set_home":
		return onvif.SetHome(ctx, command.Endpoint, command.Username, command.Password)
	case "goto_home":
		return onvif.GotoHome(ctx, command.Endpoint, command.Username, command.Password)
	default:
		return onvif.Move(ctx, command.Endpoint, command.Username, command.Password, command.Pan, command.Tilt, command.Zoom)
	}
}

func (s *Server) nextPTZCommand(w http.ResponseWriter, r *http.Request) {
	identity := r.Context().Value(identityKey{}).(agentIdentity)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	command, ok := s.ptz.next(ctx, identity.SiteID)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, command)
}

func (s *Server) completePTZCommand(w http.ResponseWriter, r *http.Request) {
	identity := r.Context().Value(identityKey{}).(agentIdentity)
	var result domain.PTZResult
	if !decode(w, r, &result) {
		return
	}
	result.ID = r.PathValue("commandID")
	if len(result.Error) > 500 {
		result.Error = result.Error[:500]
	}
	if !s.ptz.resolve(identity.SiteID, result) {
		problem(w, http.StatusNotFound, "PTZ command not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
