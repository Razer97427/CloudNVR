package media

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type Stream struct {
	ID            string
	Name          string
	InputURL      string
	OutputURL     string
	RecordingPath string
	SegmentTime   time.Duration
	Enabled       bool
}

type worker struct {
	fingerprint string
	cancel      context.CancelFunc
}

// RelayManager keeps one resilient FFmpeg relay per enabled camera.
type RelayManager struct {
	logger  *slog.Logger
	mu      sync.Mutex
	workers map[string]worker
}

func NewRelayManager(logger *slog.Logger) *RelayManager {
	return &RelayManager{logger: logger, workers: make(map[string]worker)}
}

func (m *RelayManager) Reconcile(parent context.Context, streams []Stream) {
	desired := make(map[string]Stream, len(streams))
	for _, stream := range streams {
		if stream.Enabled && stream.ID != "" && stream.InputURL != "" && stream.OutputURL != "" {
			desired[stream.ID] = stream
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for id, current := range m.workers {
		stream, ok := desired[id]
		if !ok || current.fingerprint != fingerprint(stream) {
			current.cancel()
			delete(m.workers, id)
		}
	}
	for id, stream := range desired {
		if _, ok := m.workers[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(parent)
		m.workers[id] = worker{fingerprint: fingerprint(stream), cancel: cancel}
		go m.run(ctx, stream)
	}
}

func (m *RelayManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, worker := range m.workers {
		worker.cancel()
		delete(m.workers, id)
	}
}

func (m *RelayManager) WorkerCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.workers)
}

func fingerprint(stream Stream) string {
	return stream.InputURL + "\x00" + stream.OutputURL + "\x00" + stream.RecordingPath + "\x00" + stream.SegmentTime.String()
}

func (m *RelayManager) run(ctx context.Context, stream Stream) {
	failures := 0
	for ctx.Err() == nil {
		started := time.Now()
		args := []string{
			"-hide_banner", "-loglevel", "warning", "-nostdin",
			"-rtsp_transport", "tcp", "-fflags", "+discardcorrupt", "-i", stream.InputURL,
			"-map", "0:v:0", "-map", "0:a:0?",
			"-c:v", "copy", "-c:a", "libopus", "-b:a", "64k", "-ar", "48000",
			"-f", "rtsp", "-rtsp_transport", "tcp", stream.OutputURL,
		}
		if stream.RecordingPath != "" {
			directory := filepath.Join(stream.RecordingPath, ".staging", stream.ID)
			if err := os.MkdirAll(directory, 0750); err != nil {
				m.logger.Error("recording directory unavailable", "camera_id", stream.ID, "error", err)
				return
			}
			segmentTime := stream.SegmentTime
			if segmentTime < 10*time.Second {
				segmentTime = time.Minute
			}
			args = append(args,
				"-map", "0:v:0", "-map", "0:a:0?", "-c:v", "copy", "-c:a", "aac", "-b:a", "96k", "-ar", "48000",
				"-f", "segment", "-segment_time", strconv.FormatFloat(segmentTime.Seconds(), 'f', 0, 64), "-reset_timestamps", "1",
				"-segment_atclocktime", "1", "-strftime", "1", "-movflags", "+faststart", filepath.Join(directory, "%Y%m%dT%H%M%S.mp4"))
		}
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)
		cmd.Stdout = io.Discard
		cmd.Stderr = &logWriter{logger: m.logger, cameraID: stream.ID}
		m.logger.Info("media relay starting", "camera_id", stream.ID)
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		m.logger.Warn("media relay stopped; retrying", "camera_id", stream.ID, "error", err)
		failures++
		delay := restartDelay(failures, time.Since(started))
		if time.Since(started) > time.Minute {
			failures = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func restartDelay(failures int, uptime time.Duration) time.Duration {
	if uptime > time.Minute {
		return time.Second
	}
	if failures < 1 {
		failures = 1
	}
	shift := min(failures-1, 5)
	return min(time.Duration(1<<shift)*time.Second, 30*time.Second)
}

type logWriter struct {
	logger   *slog.Logger
	cameraID string
}

func (w *logWriter) Write(p []byte) (int, error) {
	message := string(p)
	if len(message) > 1000 {
		message = message[:1000]
	}
	w.logger.Debug("ffmpeg", "camera_id", w.cameraID, "message", message)
	return len(p), nil
}

func PublishURL(base, username, password, cameraID string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if username != "" {
		u.User = url.UserPassword(username, password)
	} else {
		u.User = nil
	}
	u.Path = "/camera-" + cameraID
	return u.String(), nil
}

func ValidatePublishBase(base string) error {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "rtsp" && u.Scheme != "rtsps") || u.Host == "" {
		return fmt.Errorf("invalid media RTSP URL")
	}
	return nil
}
