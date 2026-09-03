package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type RecordingStream struct {
	ID            string
	Name          string
	InputURL      string
	RetentionDays uint
	Enabled       bool
}

type RecordingFile struct {
	CameraID       string
	StorageKey     string
	Path           string
	StartedAt      time.Time
	EndedAt        time.Time
	SizeBytes      uint64
	ChecksumSHA256 string
}

type RecorderManager struct {
	logger      *slog.Logger
	basePath    string
	segmentTime time.Duration
	mu          sync.Mutex
	workers     map[string]worker
}

func NewRecorderManager(logger *slog.Logger, basePath string, segmentTime time.Duration) *RecorderManager {
	return &RecorderManager{logger: logger, basePath: basePath, segmentTime: segmentTime, workers: make(map[string]worker)}
}

func (m *RecorderManager) Reconcile(parent context.Context, streams []RecordingStream) {
	desired := make(map[string]RecordingStream, len(streams))
	for _, stream := range streams {
		if stream.Enabled && stream.ID != "" && stream.InputURL != "" {
			desired[stream.ID] = stream
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for cameraID, current := range m.workers {
		stream, ok := desired[cameraID]
		if !ok || current.fingerprint != recordingFingerprint(stream) {
			current.cancel()
			delete(m.workers, cameraID)
		}
	}
	for cameraID, stream := range desired {
		if _, ok := m.workers[cameraID]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(parent)
		m.workers[cameraID] = worker{fingerprint: recordingFingerprint(stream), cancel: cancel}
		go m.run(ctx, stream)
	}
}

func (m *RecorderManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for cameraID, current := range m.workers {
		current.cancel()
		delete(m.workers, cameraID)
	}
}

func (m *RecorderManager) WorkerCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.workers)
}

func recordingFingerprint(stream RecordingStream) string {
	return stream.InputURL + "\x00" + strconv.FormatUint(uint64(stream.RetentionDays), 10)
}

func (m *RecorderManager) run(ctx context.Context, stream RecordingStream) {
	directory := filepath.Join(m.basePath, ".staging", stream.ID)
	if err := os.MkdirAll(directory, 0750); err != nil {
		m.logger.Error("recording directory unavailable", "camera_id", stream.ID, "error", err)
		return
	}
	pattern := filepath.Join(directory, "%Y%m%dT%H%M%S.mp4")
	failures := 0
	for ctx.Err() == nil {
		started := time.Now()
		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-hide_banner", "-loglevel", "warning", "-nostdin", "-rtsp_transport", "tcp",
			"-fflags", "+discardcorrupt", "-i", stream.InputURL,
			"-map", "0:v:0", "-map", "0:a:0?", "-c:v", "copy", "-c:a", "aac", "-b:a", "96k", "-ar", "48000",
			"-f", "segment", "-segment_time", fmt.Sprintf("%.0f", m.segmentTime.Seconds()), "-reset_timestamps", "1",
			"-segment_atclocktime", "1", "-strftime", "1", "-movflags", "+faststart", pattern)
		cmd.Stdout = io.Discard
		cmd.Stderr = &logWriter{logger: m.logger, cameraID: stream.ID}
		m.logger.Info("recording started", "camera_id", stream.ID, "directory", directory)
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		m.logger.Warn("recording stopped; retrying", "camera_id", stream.ID, "error", err)
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

func ScanRecordings(basePath string, streams []RecordingStream, settle time.Duration) ([]RecordingFile, error) {
	now := time.Now()
	var files []RecordingFile
	for _, stream := range streams {
		if err := organizeStagedRecordings(basePath, stream, settle, now); err != nil {
			return nil, err
		}
	}
	err := filepath.WalkDir(basePath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".staging" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".mp4") {
			return nil
		}
		relative, err := filepath.Rel(basePath, path)
		if err != nil {
			return err
		}
		stream, started, ok := recordingMetadata(relative, streams)
		if !ok {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if now.Sub(info.ModTime()) < settle {
			return nil
		}
		if stream.RetentionDays > 0 && info.ModTime().Before(now.Add(-time.Duration(stream.RetentionDays)*24*time.Hour)) {
			return os.Remove(path)
		}
		files = append(files, RecordingFile{CameraID: stream.ID, StorageKey: filepath.ToSlash(relative), Path: path,
			StartedAt: started, EndedAt: info.ModTime().UTC(), SizeBytes: uint64(info.Size()), ChecksumSHA256: readChecksumSidecar(path, info)})
		return nil
	})
	if os.IsNotExist(err) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	return files, nil
}

// PopulateChecksums hashes only a small number of legacy files per inventory
// pass. New values are cached beside the segment, avoiding repeated reads of
// large files on NAS storage.
func PopulateChecksums(files []RecordingFile, maximumNew int) error {
	created := 0
	for index := range files {
		if files[index].ChecksumSHA256 != "" || created >= maximumNew {
			continue
		}
		info, err := os.Stat(files[index].Path)
		if err != nil {
			continue
		}
		file, err := os.Open(files[index].Path)
		if err != nil {
			continue
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			continue
		}
		checksum := hex.EncodeToString(digest.Sum(nil))
		metadata := fmt.Sprintf("%d %d %s\n", info.Size(), info.ModTime().UnixNano(), checksum)
		temporary, err := os.CreateTemp(filepath.Dir(files[index].Path), ".checksum-*")
		if err != nil {
			continue
		}
		temporaryPath := temporary.Name()
		if _, err = temporary.WriteString(metadata); err == nil {
			err = temporary.Close()
		} else {
			_ = temporary.Close()
		}
		if err == nil {
			err = os.Rename(temporaryPath, files[index].Path+".sha256")
		}
		if err != nil {
			_ = os.Remove(temporaryPath)
			continue
		}
		files[index].ChecksumSHA256 = checksum
		created++
	}
	return nil
}

func readChecksumSidecar(path string, info os.FileInfo) string {
	data, err := os.ReadFile(path + ".sha256")
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) != 3 {
		return ""
	}
	size, sizeErr := strconv.ParseInt(fields[0], 10, 64)
	modified, modifiedErr := strconv.ParseInt(fields[1], 10, 64)
	checksum := strings.ToLower(fields[2])
	if sizeErr != nil || modifiedErr != nil || size != info.Size() || modified != info.ModTime().UnixNano() || len(checksum) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(checksum); err != nil {
		return ""
	}
	return checksum
}

func organizeStagedRecordings(basePath string, stream RecordingStream, settle time.Duration, now time.Time) error {
	directory := filepath.Join(basePath, ".staging", stream.ID)
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".mp4") {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < settle {
			continue
		}
		basename := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		started, err := time.ParseInLocation("20060102T150405", basename, time.Local)
		if err != nil {
			started, err = time.ParseInLocation("20060102T150405Z", basename, time.UTC)
			if err != nil {
				started = info.ModTime()
			}
		}
		targetDirectory := filepath.Join(basePath, cameraDirectory(stream), started.Format("2006"), started.Format("01"), started.Format("02"), started.Format("15"))
		if err := os.MkdirAll(targetDirectory, 0750); err != nil {
			return err
		}
		target := filepath.Join(targetDirectory, started.Format("04-05")+".mp4")
		if _, err := os.Stat(target); err == nil {
			target = filepath.Join(targetDirectory, started.Format("04-05")+"-"+strconv.FormatInt(started.Unix(), 10)+".mp4")
		}
		if err := os.Rename(filepath.Join(directory, entry.Name()), target); err != nil {
			return err
		}
	}
	return nil
}

func recordingMetadata(relative string, streams []RecordingStream) (RecordingStream, time.Time, bool) {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	for _, stream := range streams {
		if len(parts) == 2 && parts[0] == stream.ID {
			started, err := time.ParseInLocation("20060102T150405Z", strings.TrimSuffix(parts[1], filepath.Ext(parts[1])), time.UTC)
			if err != nil {
				return RecordingStream{}, time.Time{}, false
			}
			return stream, started, true
		}
		if len(parts) == 6 && strings.HasSuffix(parts[0], "__"+stream.ID) {
			filename := strings.TrimSuffix(parts[5], filepath.Ext(parts[5]))
			clock := strings.Split(filename, "-")
			if len(clock) < 2 {
				continue
			}
			started, err := time.ParseInLocation("2006/01/02/15/04/05", strings.Join([]string{parts[1], parts[2], parts[3], parts[4], clock[0], clock[1]}, "/"), time.Local)
			if err == nil {
				return stream, started, true
			}
		}
	}
	return RecordingStream{}, time.Time{}, false
}

func cameraDirectory(stream RecordingStream) string {
	name := strings.TrimSpace(stream.Name)
	if name == "" {
		name = "camera"
	}
	var result strings.Builder
	separator := false
	for _, char := range name {
		if unicode.IsLetter(char) || unicode.IsNumber(char) {
			result.WriteRune(unicode.ToLower(char))
			separator = false
		} else if result.Len() > 0 && !separator {
			result.WriteByte('-')
			separator = true
		}
		if result.Len() >= 60 {
			break
		}
	}
	slug := strings.Trim(result.String(), "-")
	if slug == "" {
		slug = "camera"
	}
	return slug + "__" + stream.ID
}
