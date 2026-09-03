package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"cloudnvr/internal/agent"
	"cloudnvr/internal/config"
	"cloudnvr/internal/domain"
	"cloudnvr/internal/id"
	"cloudnvr/internal/media"
	"cloudnvr/internal/onvif"
)

const version = "0.5.0"

type state struct {
	AgentID    string `json:"agent_id"`
	AgentToken string `json:"agent_token"`
}

type remoteState struct {
	mu     sync.RWMutex
	config agent.RemoteConfig
}

type inventoryState struct {
	Recordings map[string]domain.Recording `json:"recordings"`
}

func (s *remoteState) Set(value agent.RemoteConfig) {
	s.mu.Lock()
	s.config = value
	s.mu.Unlock()
}

func (s *remoteState) Get() agent.RemoteConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value := s.config
	value.Cameras = append([]domain.Camera(nil), value.Cameras...)
	return value
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.AgentFromEnv()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	client := agent.NewClient(cfg.CloudURL)
	current, err := loadState(cfg.StateFile)
	if errors.Is(err, os.ErrNotExist) {
		if cfg.EnrollmentToken == "" {
			logger.Error("ENROLLMENT_TOKEN is required for first startup")
			os.Exit(1)
		}
		enrollment, enrollErr := client.Enroll(context.Background(), cfg.SiteID, cfg.EnrollmentToken, cfg.Name)
		if enrollErr != nil {
			logger.Error("agent enrollment failed", "error", enrollErr)
			os.Exit(1)
		}
		current = state{AgentID: enrollment.AgentID, AgentToken: enrollment.AgentToken}
		if err := saveState(cfg.StateFile, current); err != nil {
			logger.Error("could not persist agent credentials", "error", err)
			os.Exit(1)
		}
		logger.Info("agent enrolled", "agent_id", current.AgentID)
	} else if err != nil {
		logger.Error("could not load agent state", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	relays := media.NewRelayManager(logger)
	defer relays.Stop()
	var localRelays *media.RelayManager
	if cfg.WebRTCEnabled {
		localRelays = media.NewRelayManager(logger)
		defer localRelays.Stop()
	}
	recorders := media.NewRecorderManager(logger, cfg.RecordingsPath, cfg.SegmentTime)
	defer recorders.Stop()
	remote := &remoteState{}
	if cached, cacheErr := loadRemoteConfig(cfg.ConfigCacheFile); cacheErr == nil {
		remote.Set(cached)
		if mediaErr := reconcileMedia(ctx, relays, localRelays, recorders, cfg, cached); mediaErr != nil {
			logger.Error("recording storage unavailable; recorder paused", "error", mediaErr)
		}
		logger.Info("cached configuration restored", "camera_count", len(cached.Cameras))
	} else if !errors.Is(cacheErr, os.ErrNotExist) {
		logger.Warn("cached configuration unavailable", "error", cacheErr)
	}
	run(ctx, client, current.AgentToken, cfg, relays, localRelays, recorders, remote, logger)
}

func run(ctx context.Context, client *agent.Client, token string, cfg config.Agent, relays, localRelays *media.RelayManager, recorders *media.RecorderManager, remote *remoteState, logger *slog.Logger) {
	go runHeartbeat(ctx, client, token, cfg, relays, localRelays, recorders, logger)
	go runPTZCommands(ctx, client, token, logger)
	go runPlaybackRequests(ctx, client, token, cfg, logger)
	go runInventorySync(ctx, client, token, cfg, relays, localRelays, recorders, remote, logger)
	if cfg.WebRTCEnabled {
		for worker := 0; worker < cfg.WebRTCWorkers; worker++ {
			go runWebRTCRequests(ctx, client, token, cfg, logger)
		}
	}
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := syncCloudConfiguration(ctx, client, token, cfg, relays, localRelays, recorders, remote); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("cloud synchronization failed", "error", err)
		} else {
			logger.Info("configuration synchronized", "site_id", remote.Get().SiteID, "camera_count", len(remote.Get().Cameras))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runHeartbeat(ctx context.Context, client *agent.Client, token string, cfg config.Agent, relays, localRelays *media.RelayManager, recorders *media.RecorderManager, logger *slog.Logger) {
	interval := cfg.PollInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		health := domain.AgentHealth{RecordingWorkers: recorders.WorkerCount(), RelayWorkers: relays.WorkerCount()}
		if localRelays != nil {
			health.RelayWorkers += localRelays.WorkerCount()
		}
		if stats, err := media.RecordingStorageStats(cfg.RecordingsPath, cfg.StorageIdentityFile); err != nil {
			health.Error = err.Error()
		} else {
			health.StorageOK = true
			health.StorageTotalBytes = stats.TotalBytes
			health.StorageFreeBytes = stats.FreeBytes
		}
		if err := client.Heartbeat(ctx, token, version, cfg.WebRTCEnabled, health); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("heartbeat failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runPlaybackRequests(ctx context.Context, client *agent.Client, token string, cfg config.Agent, logger *slog.Logger) {
	delay := time.Second
	for ctx.Err() == nil {
		err := servePlaybackRequest(ctx, client, token, cfg, logger)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("playback request channel unavailable", "error", err)
			delay = min(delay*2, 30*time.Second)
		} else {
			delay = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func runInventorySync(ctx context.Context, client *agent.Client, token string, cfg config.Agent, relays, localRelays *media.RelayManager, recorders *media.RecorderManager, remote *remoteState, logger *slog.Logger) {
	ticker := time.NewTicker(cfg.InventoryInterval)
	defer ticker.Stop()
	previous, err := loadInventoryState(cfg.InventoryStateFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("local inventory state unavailable; a full synchronization will run", "error", err)
	}
	if previous.Recordings == nil {
		previous.Recordings = make(map[string]domain.Recording)
	}
	var lastFullSync time.Time
	for {
		current := remote.Get()
		if len(current.Cameras) > 0 {
			if err := reconcileMedia(ctx, relays, localRelays, recorders, cfg, current); err != nil {
				logger.Warn("recording storage unavailable; recorder paused", "error", err)
			} else if recordings, scanErr := localRecordingInventory(cfg, current); scanErr != nil {
				logger.Warn("recording inventory scan failed; local recording continues", "error", scanErr)
			} else {
				next := inventorySnapshot(recordings)
				full := lastFullSync.IsZero() || time.Since(lastFullSync) >= cfg.FullInventoryInterval
				var syncErr error
				if full {
					syncErr = syncFullRecordingInventory(ctx, client, token, recordings)
				} else {
					syncErr = syncRecordingChanges(ctx, client, token, previous.Recordings, next.Recordings)
				}
				if syncErr != nil && !errors.Is(syncErr, context.Canceled) {
					logger.Warn("recording inventory synchronization failed; local recording continues", "error", syncErr)
				} else if syncErr == nil {
					if saveErr := saveJSONAtomic(cfg.InventoryStateFile, next); saveErr != nil {
						logger.Warn("could not persist local inventory state", "error", saveErr)
					} else {
						previous = next
						if full {
							lastFullSync = time.Now()
						}
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runPTZCommands(ctx context.Context, client *agent.Client, token string, logger *slog.Logger) {
	for ctx.Err() == nil {
		command, ok, err := client.NextPTZCommand(ctx, token)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warn("PTZ command channel unavailable", "error", err)
			time.Sleep(time.Second)
			continue
		}
		if !ok {
			continue
		}
		moveCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		switch command.Action {
		case "set_home":
			err = onvif.SetHome(moveCtx, command.Endpoint, command.Username, command.Password)
		case "goto_home":
			err = onvif.GotoHome(moveCtx, command.Endpoint, command.Username, command.Password)
		default:
			err = onvif.Move(moveCtx, command.Endpoint, command.Username, command.Password, command.Pan, command.Tilt, command.Zoom)
		}
		cancel()
		result := domain.PTZResult{ID: command.ID, Success: err == nil}
		if err != nil {
			result.Error = err.Error()
		}
		if reportErr := client.CompletePTZCommand(ctx, token, result); reportErr != nil && !errors.Is(reportErr, context.Canceled) {
			logger.Warn("PTZ result could not be reported", "camera_id", command.CameraID, "error", reportErr)
		}
		if err != nil {
			logger.Warn("PTZ command failed", "camera_id", command.CameraID, "action", command.Action, "error", err)
		} else {
			logger.Info("PTZ command executed", "camera_id", command.CameraID, "action", command.Action)
		}
	}
}

func runWebRTCRequests(ctx context.Context, client *agent.Client, token string, cfg config.Agent, logger *slog.Logger) {
	delay := time.Second
	for ctx.Err() == nil {
		request, ok, err := client.NextWebRTCRequest(ctx, token)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warn("WebRTC signaling channel unavailable", "error", err)
			delay = min(delay*2, 15*time.Second)
		} else if ok {
			delay = time.Second
			response := proxyLocalWebRTC(ctx, cfg.MediaWebRTCURL, request)
			if err := client.CompleteWebRTCRequest(ctx, token, response); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("WebRTC signaling response failed", "camera_id", request.CameraID, "error", err)
			}
		} else {
			delay = time.Second
		}
		if delay > time.Second {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}
}

func proxyLocalWebRTC(parent context.Context, baseURL string, request domain.WebRTCRequest) domain.WebRTCResponse {
	response := domain.WebRTCResponse{ID: request.ID, Status: http.StatusBadGateway, Headers: map[string]string{"Content-Type": "text/plain; charset=utf-8"}}
	if request.CameraID == "" || (request.Path != "/reader.js" && request.Path != "/camera-"+request.CameraID &&
		request.Path != "/camera-"+request.CameraID+"/whep" && !strings.HasPrefix(request.Path, "/camera-"+request.CameraID+"/whep/")) {
		response.Status = http.StatusBadRequest
		response.Body = []byte("invalid local WebRTC path")
		return response
	}
	switch request.Method {
	case http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodOptions:
	default:
		response.Status = http.StatusMethodNotAllowed
		response.Body = []byte("invalid local WebRTC method")
		return response
	}
	target, err := url.Parse(baseURL)
	if err != nil {
		response.Body = []byte("invalid local WebRTC service URL")
		return response
	}
	target.Path = request.Path
	target.RawQuery = request.Query
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, request.Method, target.String(), bytes.NewReader(request.Body))
	if err != nil {
		response.Body = []byte(err.Error())
		return response
	}
	for name, value := range request.Headers {
		httpRequest.Header.Set(name, value)
	}
	localResponse, err := (&http.Client{Timeout: 12 * time.Second}).Do(httpRequest)
	if err != nil {
		response.Body = []byte(fmt.Sprintf("local WebRTC service unavailable: %v", err))
		return response
	}
	defer localResponse.Body.Close()
	body, err := io.ReadAll(io.LimitReader(localResponse.Body, maxLocalWebRTCResponse+1))
	if err != nil {
		response.Body = []byte(err.Error())
		return response
	}
	if len(body) > maxLocalWebRTCResponse {
		response.Status = http.StatusBadGateway
		response.Body = []byte("local WebRTC response is too large")
		return response
	}
	response.Status = localResponse.StatusCode
	response.Body = body
	response.Headers = make(map[string]string)
	for _, name := range []string{"Content-Type", "Cache-Control", "ETag", "Accept-Patch", "Link", "Location"} {
		if value := localResponse.Header.Get(name); value != "" {
			response.Headers[name] = value
		}
	}
	return response
}

const maxLocalWebRTCResponse = 512 << 10

func syncCloudConfiguration(ctx context.Context, client *agent.Client, token string, cfg config.Agent, relays, localRelays *media.RelayManager, recorders *media.RecorderManager, current *remoteState) error {
	remote, err := client.Config(ctx, token)
	if err != nil {
		return err
	}
	if err := saveRemoteConfig(cfg.ConfigCacheFile, remote); err != nil {
		return err
	}
	current.Set(remote)
	return reconcileMedia(ctx, relays, localRelays, recorders, cfg, remote)
}

func reconcileMedia(ctx context.Context, relays, localRelays *media.RelayManager, recorders *media.RecorderManager, cfg config.Agent, remote agent.RemoteConfig) error {
	streams := make([]media.Stream, 0, len(remote.Cameras))
	localStreams := make([]media.Stream, 0, len(remote.Cameras))
	recordingStreams := make([]media.RecordingStream, 0, len(remote.Cameras))
	for _, camera := range remote.Cameras {
		localRecording := recordsLocally(camera)
		inputURL := camera.StreamURL
		if cfg.WebRTCEnabled {
			localURL, err := media.PublishURL(cfg.MediaRTSPURL, "", "", camera.ID)
			if err == nil {
				localStreams = append(localStreams, media.Stream{ID: camera.ID, Name: camera.Name, InputURL: camera.StreamURL, OutputURL: localURL, Enabled: camera.Enabled})
				inputURL = localURL
			}
		}
		stream := media.Stream{ID: camera.ID, Name: camera.Name, InputURL: inputURL, OutputURL: camera.PublishURL, Enabled: camera.Enabled}
		streams = append(streams, stream)
		recordingStreams = append(recordingStreams, media.RecordingStream{ID: camera.ID, Name: camera.Name, InputURL: inputURL,
			RetentionDays: camera.LocalRetentionDays, Enabled: camera.Enabled && localRecording})
	}
	if localRelays != nil {
		localRelays.Reconcile(ctx, localStreams)
	}
	relays.Reconcile(ctx, streams)
	if err := media.EnsureRecordingStorage(cfg.RecordingsPath, cfg.StorageIdentityFile); err != nil {
		recorders.Reconcile(ctx, nil)
		return err
	}
	if deleted, err := media.EnforceMinimumFreeSpace(cfg.RecordingsPath, cfg.StorageIdentityFile, cfg.MinimumFreeBytes, 2*cfg.SegmentTime); err != nil {
		recorders.Reconcile(ctx, nil)
		return err
	} else if deleted > 0 {
		// The following inventory pass removes the matching rows from MariaDB.
	}
	recorders.Reconcile(ctx, recordingStreams)
	return nil
}

func localRecordingInventory(cfg config.Agent, remote agent.RemoteConfig) ([]domain.Recording, error) {
	if err := media.EnsureRecordingStorage(cfg.RecordingsPath, cfg.StorageIdentityFile); err != nil {
		return nil, err
	}
	recordingStreams := make([]media.RecordingStream, 0, len(remote.Cameras))
	for _, camera := range remote.Cameras {
		localRecording := recordsLocally(camera)
		recordingStreams = append(recordingStreams, media.RecordingStream{ID: camera.ID, Name: camera.Name, InputURL: camera.StreamURL,
			RetentionDays: camera.LocalRetentionDays, Enabled: camera.Enabled && localRecording})
	}
	files, err := media.ScanRecordings(cfg.RecordingsPath, recordingStreams, 10*time.Second)
	if err != nil {
		return nil, err
	}
	_ = media.PopulateChecksums(files, 4)
	recordings := make([]domain.Recording, 0, len(files))
	for _, file := range files {
		ended := file.EndedAt
		recordings = append(recordings, domain.Recording{CameraID: file.CameraID, StorageKey: file.StorageKey,
			StartedAt: file.StartedAt, EndedAt: &ended, SizeBytes: file.SizeBytes, ChecksumSHA256: file.ChecksumSHA256, EventType: "continuous"})
	}
	return recordings, nil
}

func syncFullRecordingInventory(ctx context.Context, client *agent.Client, token string, recordings []domain.Recording) error {
	syncID, err := id.New()
	if err != nil {
		return err
	}
	for start := 0; start < len(recordings); start += 200 {
		end := min(start+200, len(recordings))
		if err := client.ReportRecordingInventory(ctx, token, syncID, recordings[start:end], false); err != nil {
			return err
		}
	}
	if err := client.ReportRecordingInventory(ctx, token, syncID, nil, true); err != nil {
		return err
	}
	return nil
}

func inventorySnapshot(recordings []domain.Recording) inventoryState {
	result := inventoryState{Recordings: make(map[string]domain.Recording, len(recordings))}
	for _, recording := range recordings {
		result.Recordings[recording.StorageKey] = recording
	}
	return result
}

func syncRecordingChanges(ctx context.Context, client *agent.Client, token string, previous, next map[string]domain.Recording) error {
	var changed []domain.Recording
	var deleted []domain.Recording
	for key, recording := range next {
		old, ok := previous[key]
		if !ok || old.SizeBytes != recording.SizeBytes || !old.StartedAt.Equal(recording.StartedAt) || !sameTime(old.EndedAt, recording.EndedAt) {
			changed = append(changed, recording)
		}
	}
	for key, recording := range previous {
		if _, ok := next[key]; !ok {
			deleted = append(deleted, domain.Recording{CameraID: recording.CameraID, StorageKey: recording.StorageKey})
		}
	}
	for len(changed) > 0 || len(deleted) > 0 {
		changedEnd := min(200, len(changed))
		deletedEnd := min(200, len(deleted))
		if err := client.ReportRecordingChanges(ctx, token, changed[:changedEnd], deleted[:deletedEnd]); err != nil {
			return err
		}
		changed = changed[changedEnd:]
		deleted = deleted[deletedEnd:]
	}
	return nil
}

func sameTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func recordsLocally(camera domain.Camera) bool {
	return camera.RecordingMode == domain.ModeLocal || camera.RecordingMode == domain.ModeHybrid ||
		(camera.RecordingMode == domain.ModeManual && camera.ManualRecording)
}

func servePlaybackRequest(ctx context.Context, client *agent.Client, token string, cfg config.Agent, logger *slog.Logger) error {
	request, ok, err := client.NextPlaybackRequest(ctx, token)
	if err != nil || !ok {
		return err
	}
	base := filepath.Clean(cfg.RecordingsPath)
	path := filepath.Join(base, filepath.FromSlash(request.StorageKey))
	relative, err := filepath.Rel(base, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return client.RejectPlayback(ctx, token, request.ID, "invalid recording path")
	}
	file, err := os.Open(path)
	if err != nil {
		_ = client.RejectPlayback(ctx, token, request.ID, "recording no longer exists on agent")
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	logger.Info("uploading local recording for playback", "recording_id", request.RecordingID, "size_bytes", info.Size())
	if err := client.UploadPlayback(ctx, token, request.ID, request.ChecksumSHA256, file, info.Size(), cfg.UploadMbps, cfg.UploadRetries); err != nil {
		return err
	}
	logger.Info("local recording ready for cloud playback", "recording_id", request.RecordingID)
	return nil
}

func loadRemoteConfig(path string) (agent.RemoteConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return agent.RemoteConfig{}, err
	}
	var value agent.RemoteConfig
	err = json.Unmarshal(data, &value)
	return value, err
}

func saveRemoteConfig(path string, value agent.RemoteConfig) error {
	return saveJSONAtomic(path, value)
}

func loadInventoryState(path string) (inventoryState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return inventoryState{}, err
	}
	var value inventoryState
	err = json.Unmarshal(data, &value)
	return value, err
}

func saveJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func loadState(path string) (state, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return state{}, err
	}
	var value state
	err = json.Unmarshal(data, &value)
	return value, err
}

func saveState(path string, value state) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
