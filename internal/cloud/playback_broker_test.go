package cloud

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlaybackCacheEvictsOldestFile(t *testing.T) {
	broker := newPlaybackBroker(t.TempDir(), time.Hour, 6)
	if err := os.MkdirAll(broker.cacheDir, 0750); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(broker.cacheDir, "old.mp4")
	newPath := filepath.Join(broker.cacheDir, "new.mp4")
	if err := os.WriteFile(oldPath, []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("5678"), 0600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Minute)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	broker.mu.Lock()
	broker.enforceCacheLimitLocked()
	broker.mu.Unlock()
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old cache entry not evicted: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new cache entry was removed: %v", err)
	}
}
