package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cloudnvr/internal/domain"
)

type Client struct {
	baseURL    string
	http       *http.Client
	uploadHTTP *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 30 * time.Second}, uploadHTTP: &http.Client{}}
}

type Enrollment struct {
	AgentID    string `json:"agent_id"`
	AgentToken string `json:"agent_token"`
}

func (c *Client) Enroll(ctx context.Context, siteID, enrollmentToken, name string) (Enrollment, error) {
	var out Enrollment
	err := c.call(ctx, http.MethodPost, "/api/agent/enroll", "", map[string]string{
		"site_id": siteID, "enrollment_token": enrollmentToken, "name": name,
	}, &out)
	return out, err
}

func (c *Client) Heartbeat(ctx context.Context, token, version string, webRTCEnabled bool, health domain.AgentHealth) error {
	return c.call(ctx, http.MethodPost, "/api/agent/heartbeat", token, map[string]any{"version": version, "webrtc_enabled": webRTCEnabled, "health": health}, nil)
}

type RemoteConfig struct {
	SiteID  string          `json:"site_id"`
	Cameras []domain.Camera `json:"cameras"`
}

type PlaybackRequest struct {
	ID             string `json:"id"`
	RecordingID    string `json:"recording_id"`
	StorageKey     string `json:"storage_key"`
	ChecksumSHA256 string `json:"checksum_sha256,omitempty"`
}

func (c *Client) Config(ctx context.Context, token string) (RemoteConfig, error) {
	var out RemoteConfig
	err := c.call(ctx, http.MethodGet, "/api/agent/config", token, nil, &out)
	return out, err
}

func (c *Client) ReportRecordings(ctx context.Context, token string, recordings []domain.Recording) error {
	return c.call(ctx, http.MethodPost, "/api/agent/recordings", token, map[string]any{"recordings": recordings}, nil)
}

func (c *Client) ReportRecordingInventory(ctx context.Context, token, syncID string, recordings []domain.Recording, complete bool) error {
	return c.call(ctx, http.MethodPost, "/api/agent/recordings", token, map[string]any{
		"recordings": recordings, "sync_id": syncID, "complete": complete,
	}, nil)
}

func (c *Client) ReportRecordingChanges(ctx context.Context, token string, recordings, deleted []domain.Recording) error {
	return c.call(ctx, http.MethodPost, "/api/agent/recordings", token, map[string]any{
		"recordings": recordings, "deleted_recordings": deleted,
	}, nil)
}

func (c *Client) NextPlaybackRequest(ctx context.Context, token string) (PlaybackRequest, bool, error) {
	var out PlaybackRequest
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/agent/recording-requests", nil)
	if err != nil {
		return out, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return out, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return out, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return out, false, fmt.Errorf("cloud returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, false, err
	}
	return out, true, nil
}

func (c *Client) NextPTZCommand(ctx context.Context, token string) (domain.PTZCommand, bool, error) {
	var out domain.PTZCommand
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/agent/ptz-commands", nil)
	if err != nil {
		return out, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return out, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return out, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return out, false, fmt.Errorf("cloud returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, false, err
	}
	return out, true, nil
}

func (c *Client) CompletePTZCommand(ctx context.Context, token string, result domain.PTZResult) error {
	return c.call(ctx, http.MethodPost, "/api/agent/ptz-commands/"+result.ID, token, result, nil)
}

func (c *Client) NextWebRTCRequest(ctx context.Context, token string) (domain.WebRTCRequest, bool, error) {
	var out domain.WebRTCRequest
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/agent/webrtc-requests", nil)
	if err != nil {
		return out, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return out, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return out, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return out, false, fmt.Errorf("cloud returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, false, err
	}
	return out, true, nil
}

func (c *Client) CompleteWebRTCRequest(ctx context.Context, token string, response domain.WebRTCResponse) error {
	return c.call(ctx, http.MethodPost, "/api/agent/webrtc-requests/"+response.ID, token, response, nil)
}

func (c *Client) UploadPlayback(ctx context.Context, token, requestID, checksum string, file io.ReadSeeker, size int64, megabitsPerSecond float64, attempts int) error {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		var body io.Reader = file
		if megabitsPerSecond > 0 {
			body = newRateLimitedReader(file, int64(megabitsPerSecond*1_000_000/8))
		}
		attemptTimeout := 10 * time.Minute
		if megabitsPerSecond > 0 {
			bytesPerSecond := megabitsPerSecond * 1_000_000 / 8
			attemptTimeout = time.Duration(float64(size)/bytesPerSecond*float64(time.Second)) + 2*time.Minute
		}
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodPut, c.baseURL+"/api/agent/recording-requests/"+requestID, body)
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "video/mp4")
		if checksum != "" {
			req.Header.Set("X-CloudNVR-SHA256", checksum)
		}
		req.ContentLength = size
		resp, err := c.uploadHTTP.Do(req)
		if err == nil {
			message, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			cancel()
			if readErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			if readErr != nil {
				err = readErr
			} else {
				err = fmt.Errorf("cloud returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
				if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusConflict {
					return err
				}
			}
		} else {
			cancel()
		}
		lastErr = err
		if attempt == attempts {
			break
		}
		delay := time.Duration(1<<(attempt-1)) * time.Second
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return lastErr
}

func (c *Client) RejectPlayback(ctx context.Context, token, requestID, message string) error {
	return c.call(ctx, http.MethodDelete, "/api/agent/recording-requests/"+requestID, token, map[string]string{"error": message}, nil)
}

func (c *Client) call(ctx context.Context, method, path, token string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("cloud returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
