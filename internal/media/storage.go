package media

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const storageMarkerName = ".cloudnvr-storage-id"

type StorageStats struct {
	TotalBytes uint64
	FreeBytes  uint64
}

// EnsureRecordingStorage binds the configured path to a persistent identity.
// If a NAS mount disappears and exposes an empty mountpoint, the missing marker
// stops recording and inventory reconciliation instead of erasing the timeline.
func EnsureRecordingStorage(basePath, identityFile string) error {
	if err := os.MkdirAll(filepath.Dir(identityFile), 0700); err != nil {
		return err
	}
	identity, identityErr := readIdentity(identityFile)
	markerPath := filepath.Join(basePath, storageMarkerName)
	marker, markerErr := readIdentity(markerPath)

	switch {
	case identityErr == nil && markerErr == nil:
		if identity != marker {
			return errors.New("recording storage identity mismatch")
		}
		return nil
	case identityErr == nil && os.IsNotExist(markerErr):
		return errors.New("recording storage is unavailable (identity marker missing)")
	case identityErr == nil && markerErr != nil:
		return fmt.Errorf("read recording storage identity: %w", markerErr)
	case os.IsNotExist(identityErr) && markerErr == nil:
		return writeIdentity(identityFile, marker, 0600)
	case !os.IsNotExist(identityErr):
		return fmt.Errorf("read local storage identity: %w", identityErr)
	case !os.IsNotExist(markerErr):
		return fmt.Errorf("read recording storage identity: %w", markerErr)
	}

	if err := os.MkdirAll(basePath, 0750); err != nil {
		return err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	identity = hex.EncodeToString(random)
	if err := writeIdentity(markerPath, identity, 0600); err != nil {
		return err
	}
	return writeIdentity(identityFile, identity, 0600)
}

func RecordingStorageStats(basePath, identityFile string) (StorageStats, error) {
	if err := EnsureRecordingStorage(basePath, identityFile); err != nil {
		return StorageStats{}, err
	}
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs(basePath, &filesystem); err != nil {
		return StorageStats{}, err
	}
	return StorageStats{TotalBytes: filesystem.Blocks * uint64(filesystem.Bsize), FreeBytes: filesystem.Bavail * uint64(filesystem.Bsize)}, nil
}

// EnforceMinimumFreeSpace removes the oldest finalized segments before the
// filesystem becomes full. Staging files are never touched, so FFmpeg can
// finish the segment it is currently writing.
func EnforceMinimumFreeSpace(basePath, identityFile string, minimumFreeBytes uint64, settle time.Duration) (int, error) {
	if minimumFreeBytes == 0 {
		return 0, nil
	}
	stats, err := RecordingStorageStats(basePath, identityFile)
	if err != nil {
		return 0, err
	}
	if stats.FreeBytes >= minimumFreeBytes {
		return 0, nil
	}
	type candidate struct {
		path     string
		size     uint64
		modified time.Time
	}
	var files []candidate
	now := time.Now()
	err = filepath.WalkDir(basePath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".staging" || entry.Name() == "_playback_cache" || entry.Name() == ".gitdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".mp4") {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || now.Sub(info.ModTime()) < settle {
			return nil
		}
		files = append(files, candidate{path: path, size: uint64(info.Size()), modified: info.ModTime()})
		return nil
	})
	if err != nil {
		return 0, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modified.Before(files[j].modified) })
	deleted := 0
	available := stats.FreeBytes
	for _, file := range files {
		if available >= minimumFreeBytes {
			break
		}
		if err := os.Remove(file.path); err != nil {
			continue
		}
		_ = os.Remove(file.path + ".sha256")
		available += file.size
		deleted++
	}
	if available < minimumFreeBytes {
		return deleted, fmt.Errorf("recording storage remains below minimum free space: %d < %d bytes", available, minimumFreeBytes)
	}
	return deleted, nil
}

func readIdentity(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if len(value) != 32 {
		return "", errors.New("invalid storage identity")
	}
	return value, nil
}

func writeIdentity(path, value string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(value+"\n"), mode)
}
