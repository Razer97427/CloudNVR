package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloudnvr/internal/domain"
	"cloudnvr/internal/id"
	"cloudnvr/internal/store"
)

func (s *Server) listRecordings(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	recordings, err := s.store.ListRecordings(r.Context(), r.URL.Query().Get("site_id"), r.URL.Query().Get("camera_id"), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.setStreamCookie(w)
	for index := range recordings {
		if recordings[index].Source == "cloud" {
			recordings[index].PlaybackURL = "/recordings/" + recordings[index].ID
		}
		recordings[index].StorageKey = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"recordings": recordings})
}

func (s *Server) playRecording(w http.ResponseWriter, r *http.Request) {
	recording, err := s.store.RecordingByID(r.Context(), r.PathValue("recordingID"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "recording not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	path := s.playbacks.cachePath(recording.ID)
	if recording.Source == "cloud" {
		path = filepath.Join(s.recordingsPath, filepath.FromSlash(recording.StorageKey))
	}
	relative, err := filepath.Rel(s.recordingsPath, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		problem(w, http.StatusBadRequest, "invalid recording path")
		return
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		problem(w, http.StatusNotFound, "recording file expired")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeFile(w, r, path)
}

func (s *Server) prepareRecording(w http.ResponseWriter, r *http.Request) {
	recording, err := s.store.RecordingByID(r.Context(), r.PathValue("recordingID"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "recording not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if recording.Source == "cloud" {
		s.setStreamCookie(w)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "playback_url": "/recordings/" + recording.ID})
		return
	}
	request, err := s.playbacks.prepare(recording)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.writePreparation(w, request)
}

func (s *Server) exportRecording(w http.ResponseWriter, r *http.Request) {
	recording, err := s.store.RecordingByID(r.Context(), r.PathValue("recordingID"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "recording not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	from, fromErr := strconv.ParseFloat(r.URL.Query().Get("from"), 64)
	to, toErr := strconv.ParseFloat(r.URL.Query().Get("to"), 64)
	if fromErr != nil || toErr != nil || from < 0 || to <= from || to-from > 2*60*60 {
		problem(w, http.StatusBadRequest, "invalid export range")
		return
	}
	if recording.EndedAt != nil {
		duration := recording.EndedAt.Sub(recording.StartedAt).Seconds()
		if from >= duration || to > duration+1 {
			problem(w, http.StatusBadRequest, "export range exceeds the recording")
			return
		}
	}
	path := filepath.Join(s.recordingsPath, filepath.FromSlash(recording.StorageKey))
	if recording.Source == "agent" {
		path = s.playbacks.cachePath(recording.ID)
	}
	base := s.recordingsPath
	relative, pathErr := filepath.Rel(base, path)
	if pathErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		problem(w, http.StatusBadRequest, "invalid recording path")
		return
	}
	if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
		if recording.Source == "agent" {
			problem(w, http.StatusConflict, "prepare the agent recording before exporting")
		} else {
			problem(w, http.StatusNotFound, "recording file expired")
		}
		return
	}
	if err := os.MkdirAll(s.playbacks.cacheDir, 0750); err != nil {
		s.fail(w, err)
		return
	}
	temporary, err := os.CreateTemp(s.playbacks.cacheDir, ".export-*.mp4")
	if err != nil {
		s.fail(w, err)
		return
	}
	temporaryPath := temporary.Name()
	_ = temporary.Close()
	defer os.Remove(temporaryPath)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", strconv.FormatFloat(from, 'f', 3, 64), "-i", path, "-t", strconv.FormatFloat(to-from, 'f', 3, 64),
		"-map", "0:v:0", "-map", "0:a:0?", "-c:v", "libx264", "-preset", "veryfast", "-crf", "21",
		"-c:a", "aac", "-movflags", "+faststart", "-y", temporaryPath)
	if output, runErr := command.CombinedOutput(); runErr != nil {
		s.logger.Warn("recording export failed", "recording_id", recording.ID, "error", runErr, "ffmpeg", strings.TrimSpace(string(output)))
		problem(w, http.StatusInternalServerError, "video export failed")
		return
	}
	filename := safeFilename(recording.CameraName) + "-" + recording.StartedAt.Local().Format("20060102-150405") + ".mp4"
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, temporaryPath)
}

func (s *Server) exportRecordingRange(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CameraID string    `json:"camera_id"`
		From     time.Time `json:"from"`
		To       time.Time `json:"to"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.CameraID == "" || in.From.IsZero() || in.To.IsZero() || !in.To.After(in.From) || in.To.Sub(in.From) > 2*time.Hour {
		problem(w, http.StatusBadRequest, "invalid export range")
		return
	}
	recordings, err := s.store.ListRecordings(r.Context(), "", in.CameraID, 5000)
	if err != nil {
		s.fail(w, err)
		return
	}
	selected := make([]domain.Recording, 0)
	for _, recording := range recordings {
		end := recording.StartedAt.Add(time.Minute)
		if recording.EndedAt != nil {
			end = *recording.EndedAt
		}
		if end.After(in.From) && recording.StartedAt.Before(in.To) {
			selected = append(selected, recording)
		}
	}
	if len(selected) == 0 {
		problem(w, http.StatusNotFound, "no recording in the selected range")
		return
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].StartedAt.Before(selected[j].StartedAt) })

	if err := os.MkdirAll(s.playbacks.cacheDir, 0750); err != nil {
		s.fail(w, err)
		return
	}
	listFile, err := os.CreateTemp(s.playbacks.cacheDir, ".export-list-*.txt")
	if err != nil {
		s.fail(w, err)
		return
	}
	listPath := listFile.Name()
	defer os.Remove(listPath)
	for _, recording := range selected {
		path := filepath.Join(s.recordingsPath, filepath.FromSlash(recording.StorageKey))
		if recording.Source == "agent" {
			path = s.playbacks.cachePath(recording.ID)
		}
		relative, pathErr := filepath.Rel(s.recordingsPath, path)
		if pathErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			_ = listFile.Close()
			problem(w, http.StatusBadRequest, "invalid recording path")
			return
		}
		if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
			_ = listFile.Close()
			problem(w, http.StatusConflict, "prepare all agent recordings before exporting")
			return
		}
		if _, err := listFile.WriteString("file '" + strings.ReplaceAll(path, "'", "'\\''") + "'\n"); err != nil {
			_ = listFile.Close()
			s.fail(w, err)
			return
		}
	}
	if err := listFile.Close(); err != nil {
		s.fail(w, err)
		return
	}

	firstStart := selected[0].StartedAt
	offset := in.From.Sub(firstStart).Seconds()
	if offset < 0 {
		offset = 0
	}
	temporary, err := os.CreateTemp(s.playbacks.cacheDir, ".export-range-*.mp4")
	if err != nil {
		s.fail(w, err)
		return
	}
	temporaryPath := temporary.Name()
	_ = temporary.Close()
	defer os.Remove(temporaryPath)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "concat", "-safe", "0", "-i", listPath, "-ss", strconv.FormatFloat(offset, 'f', 3, 64),
		"-t", strconv.FormatFloat(in.To.Sub(in.From).Seconds(), 'f', 3, 64), "-map", "0:v:0", "-map", "0:a:0?",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-c:a", "aac", "-movflags", "+faststart", "-y", temporaryPath)
	if output, runErr := command.CombinedOutput(); runErr != nil {
		s.logger.Warn("recording range export failed", "camera_id", in.CameraID, "error", runErr, "ffmpeg", strings.TrimSpace(string(output)))
		problem(w, http.StatusInternalServerError, "video export failed")
		return
	}
	filename := safeFilename(selected[0].CameraName) + "-" + in.From.Local().Format("20060102-150405") + "-" + in.To.Local().Format("150405") + ".mp4"
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, temporaryPath)
}

func safeFilename(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "cloudnvr"
	}
	var result strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			result.WriteRune(char)
		} else if result.Len() > 0 {
			result.WriteByte('-')
		}
	}
	filename := strings.Trim(result.String(), "-")
	if filename == "" {
		return "cloudnvr"
	}
	return filename
}

func (s *Server) recordingPreparation(w http.ResponseWriter, r *http.Request) {
	recording, err := s.store.RecordingByID(r.Context(), r.PathValue("recordingID"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "recording not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if recording.Source == "cloud" {
		s.writePreparation(w, &playbackRequest{RecordingID: recording.ID, Status: "ready"})
		return
	}
	request := s.playbacks.status(recording.ID)
	if request == nil {
		problem(w, http.StatusNotFound, "playback request not found")
		return
	}
	s.writePreparation(w, request)
}

func (s *Server) writePreparation(w http.ResponseWriter, request *playbackRequest) {
	response := map[string]string{"status": request.Status}
	if request.Error != "" {
		response["error"] = request.Error
	}
	if request.Status == "ready" {
		s.setStreamCookie(w)
		response["playback_url"] = "/recordings/" + request.RecordingID
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) nextRecordingRequest(w http.ResponseWriter, r *http.Request) {
	identity := r.Context().Value(identityKey{}).(agentIdentity)
	request := s.playbacks.next(identity.SiteID)
	if request == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, request)
}

func (s *Server) uploadRequestedRecording(w http.ResponseWriter, r *http.Request) {
	identity := r.Context().Value(identityKey{}).(agentIdentity)
	request, ok := s.playbacks.request(r.PathValue("requestID"), identity.SiteID)
	if !ok {
		problem(w, http.StatusNotFound, "playback request not found")
		return
	}
	if r.ContentLength <= 0 {
		s.playbacks.requeue(request.ID)
		problem(w, http.StatusLengthRequired, "recording size is required")
		return
	}
	if r.ContentLength > s.agentUploadMaxBytes {
		s.playbacks.fail(request.ID, "recording is too large")
		problem(w, http.StatusRequestEntityTooLarge, "recording is too large")
		return
	}
	if err := os.MkdirAll(s.playbacks.cacheDir, 0750); err != nil {
		s.playbacks.fail(request.ID, "cloud cache unavailable")
		s.fail(w, err)
		return
	}
	temporary, err := os.CreateTemp(s.playbacks.cacheDir, ".upload-*.mp4")
	if err != nil {
		s.playbacks.fail(request.ID, "cloud cache unavailable")
		s.fail(w, err)
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(r.Body, s.agentUploadMaxBytes+1))
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil || written != r.ContentLength || written > s.agentUploadMaxBytes {
		s.playbacks.requeue(request.ID)
		problem(w, http.StatusConflict, "upload interrupted; retry allowed")
		return
	}
	actualChecksum := hex.EncodeToString(digest.Sum(nil))
	expectedChecksum := strings.ToLower(strings.TrimSpace(request.ChecksumSHA256))
	headerChecksum := strings.ToLower(strings.TrimSpace(r.Header.Get("X-CloudNVR-SHA256")))
	if expectedChecksum != "" && (headerChecksum != expectedChecksum || actualChecksum != expectedChecksum) {
		s.playbacks.fail(request.ID, "recording integrity verification failed")
		problem(w, http.StatusUnprocessableEntity, "recording checksum mismatch")
		return
	}
	if err := os.Rename(temporaryPath, s.playbacks.cachePath(request.RecordingID)); err != nil {
		s.playbacks.requeue(request.ID)
		s.fail(w, err)
		return
	}
	s.playbacks.ready(request.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) rejectRequestedRecording(w http.ResponseWriter, r *http.Request) {
	identity := r.Context().Value(identityKey{}).(agentIdentity)
	request, ok := s.playbacks.request(r.PathValue("requestID"), identity.SiteID)
	if !ok {
		problem(w, http.StatusNotFound, "playback request not found")
		return
	}
	var in struct {
		Error string `json:"error"`
	}
	if !decode(w, r, &in) {
		return
	}
	message := strings.TrimSpace(in.Error)
	if message == "" {
		message = "recording unavailable on agent"
	}
	s.playbacks.fail(request.ID, message)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) syncAgentRecordings(w http.ResponseWriter, r *http.Request) {
	identity := r.Context().Value(identityKey{}).(agentIdentity)
	var in struct {
		Recordings        []domain.Recording `json:"recordings"`
		DeletedRecordings []domain.Recording `json:"deleted_recordings"`
		SyncID            string             `json:"sync_id"`
		Complete          bool               `json:"complete"`
	}
	if !decode(w, r, &in) {
		return
	}
	cameras, _, err := s.store.ListCameras(r.Context(), identity.SiteID, false, domain.AccessAgent)
	if err != nil {
		s.fail(w, err)
		return
	}
	allowed := make(map[string]bool, len(cameras))
	for _, camera := range cameras {
		allowed[camera.ID] = true
	}
	for _, recording := range in.Recordings {
		if !allowed[recording.CameraID] || recording.StorageKey == "" || !validRecordingChecksum(recording.ChecksumSHA256) {
			problem(w, http.StatusBadRequest, "invalid recording metadata")
			return
		}
		recording.ID, _ = id.New()
		recording.Source = "agent"
		recording.EventType = "continuous"
		var storeErr error
		if in.SyncID == "" {
			storeErr = s.store.UpsertRecording(r.Context(), recording)
		} else {
			storeErr = s.store.UpsertRecordingInventory(r.Context(), recording, in.SyncID)
		}
		if storeErr != nil {
			s.fail(w, storeErr)
			return
		}
	}
	for _, recording := range in.DeletedRecordings {
		if !allowed[recording.CameraID] || recording.StorageKey == "" {
			problem(w, http.StatusBadRequest, "invalid deleted recording metadata")
			return
		}
		if err := s.store.DeleteAgentRecording(r.Context(), recording.CameraID, recording.StorageKey); err != nil {
			s.fail(w, err)
			return
		}
	}
	if in.Complete {
		if len(in.SyncID) != 36 {
			problem(w, http.StatusBadRequest, "invalid recording inventory")
			return
		}
		cameraIDs := make([]string, 0, len(cameras))
		for _, camera := range cameras {
			cameraIDs = append(cameraIDs, camera.ID)
		}
		deleted, reconcileErr := s.store.ReconcileRecordingInventory(r.Context(), "agent", cameraIDs, in.SyncID)
		if reconcileErr != nil {
			s.fail(w, reconcileErr)
			return
		}
		if deleted > 0 {
			s.logger.Info("orphaned agent recordings removed", "site_id", identity.SiteID, "count", deleted)
		}
	}
	for _, camera := range cameras {
		if camera.LocalRetentionDays > 0 {
			if err := s.store.PruneAgentRecordings(r.Context(), camera.ID, time.Now().Add(-time.Duration(camera.LocalRetentionDays)*24*time.Hour)); err != nil {
				s.fail(w, err)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"indexed": len(in.Recordings), "deleted": len(in.DeletedRecordings)})
}

func validRecordingChecksum(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
