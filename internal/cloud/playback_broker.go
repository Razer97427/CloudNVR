package cloud

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"cloudnvr/internal/domain"
	"cloudnvr/internal/id"
)

const (
	playbackRequestTTL  = 10 * time.Minute
	playbackUploadLease = 30 * time.Minute
)

type playbackRequest struct {
	ID             string    `json:"id"`
	RecordingID    string    `json:"recording_id"`
	SiteID         string    `json:"-"`
	StorageKey     string    `json:"storage_key,omitempty"`
	ChecksumSHA256 string    `json:"checksum_sha256,omitempty"`
	Status         string    `json:"status"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"-"`
	UpdatedAt      time.Time `json:"-"`
}

type playbackBroker struct {
	mu            sync.Mutex
	cacheDir      string
	cacheTTL      time.Duration
	cacheMaxBytes int64
	requests      map[string]*playbackRequest
}

func newPlaybackBroker(recordingsPath string, cacheTTL time.Duration, cacheMaxBytes int64) *playbackBroker {
	return &playbackBroker{cacheDir: filepath.Join(recordingsPath, "_playback_cache"), cacheTTL: cacheTTL,
		cacheMaxBytes: cacheMaxBytes, requests: make(map[string]*playbackRequest)}
}

func (b *playbackBroker) prepare(recording domain.Recording) (*playbackRequest, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupLocked()
	if b.cacheReadyLocked(recording.ID) {
		return &playbackRequest{RecordingID: recording.ID, Status: "ready"}, nil
	}
	for _, request := range b.requests {
		if request.RecordingID == recording.ID && request.Status != "error" {
			copy := *request
			return &copy, nil
		}
	}
	requestID, err := id.New()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	request := &playbackRequest{ID: requestID, RecordingID: recording.ID, SiteID: recording.SiteID, StorageKey: recording.StorageKey,
		ChecksumSHA256: recording.ChecksumSHA256, Status: "queued", CreatedAt: now, UpdatedAt: now}
	b.requests[request.ID] = request
	copy := *request
	return &copy, nil
}

func (b *playbackBroker) status(recordingID string) *playbackRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupLocked()
	if b.cacheReadyLocked(recordingID) {
		return &playbackRequest{RecordingID: recordingID, Status: "ready"}
	}
	for _, request := range b.requests {
		if request.RecordingID == recordingID {
			copy := *request
			return &copy
		}
	}
	return nil
}

func (b *playbackBroker) next(siteID string) *playbackRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupLocked()
	var candidates []*playbackRequest
	for _, request := range b.requests {
		if request.SiteID == siteID && request.Status == "queued" {
			candidates = append(candidates, request)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CreatedAt.Before(candidates[j].CreatedAt) })
	request := candidates[0]
	request.Status = "uploading"
	request.UpdatedAt = time.Now().UTC()
	copy := *request
	return &copy
}

func (b *playbackBroker) request(requestID, siteID string) (*playbackRequest, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	request, ok := b.requests[requestID]
	if !ok || request.SiteID != siteID || (request.Status != "uploading" && request.Status != "queued") {
		return nil, false
	}
	request.Status = "uploading"
	request.Error = ""
	request.UpdatedAt = time.Now().UTC()
	copy := *request
	return &copy, true
}

func (b *playbackBroker) requeue(requestID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if request := b.requests[requestID]; request != nil {
		request.Status = "queued"
		request.Error = ""
		request.UpdatedAt = time.Now().UTC()
	}
}

func (b *playbackBroker) ready(requestID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if request := b.requests[requestID]; request != nil {
		request.Status = "ready"
		request.UpdatedAt = time.Now().UTC()
	}
	b.enforceCacheLimitLocked()
}

func (b *playbackBroker) fail(requestID, message string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if request := b.requests[requestID]; request != nil {
		request.Status = "error"
		request.Error = message
		request.UpdatedAt = time.Now().UTC()
	}
}

func (b *playbackBroker) cachePath(recordingID string) string {
	return filepath.Join(b.cacheDir, recordingID+".mp4")
}

func (b *playbackBroker) cacheReadyLocked(recordingID string) bool {
	info, err := os.Stat(b.cachePath(recordingID))
	if err != nil {
		return false
	}
	if time.Since(info.ModTime()) > b.cacheTTL {
		_ = os.Remove(b.cachePath(recordingID))
		return false
	}
	return info.Mode().IsRegular() && info.Size() > 0
}

func (b *playbackBroker) cleanupLocked() {
	now := time.Now()
	for requestID, request := range b.requests {
		if request.Status == "ready" && !b.cacheReadyLocked(request.RecordingID) {
			request.Status = "queued"
			request.UpdatedAt = now
			continue
		}
		if request.Status == "uploading" && now.Sub(request.UpdatedAt) > playbackUploadLease {
			request.Status = "queued"
			request.UpdatedAt = now
			continue
		}
		ttl := playbackRequestTTL
		if request.Status == "ready" {
			ttl = b.cacheTTL
		}
		if now.Sub(request.UpdatedAt) > ttl {
			if request.Status == "ready" {
				_ = os.Remove(b.cachePath(request.RecordingID))
			}
			delete(b.requests, requestID)
		}
	}
	b.enforceCacheLimitLocked()
}

func (b *playbackBroker) enforceCacheLimitLocked() {
	entries, err := os.ReadDir(b.cacheDir)
	if err != nil {
		return
	}
	type cachedFile struct {
		path     string
		size     int64
		modified time.Time
	}
	var files []cachedFile
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".mp4" {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		path := filepath.Join(b.cacheDir, entry.Name())
		if time.Since(info.ModTime()) > b.cacheTTL {
			_ = os.Remove(path)
			continue
		}
		files = append(files, cachedFile{path: path, size: info.Size(), modified: info.ModTime()})
		total += info.Size()
	}
	if total <= b.cacheMaxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modified.Before(files[j].modified) })
	for _, file := range files {
		if total <= b.cacheMaxBytes {
			break
		}
		if os.Remove(file.path) == nil {
			total -= file.size
		}
	}
}

var errPlaybackNotFound = errors.New("playback request not found")
