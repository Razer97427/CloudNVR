package media

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureRecordingStorageDetectsMissingMountMarker(t *testing.T) {
	root := t.TempDir()
	storage := filepath.Join(root, "recordings")
	identity := filepath.Join(root, "state", "storage.id")
	if err := EnsureRecordingStorage(storage, identity); err != nil {
		t.Fatal(err)
	}
	if err := EnsureRecordingStorage(storage, identity); err != nil {
		t.Fatalf("existing storage rejected: %v", err)
	}
	if err := os.Remove(filepath.Join(storage, storageMarkerName)); err != nil {
		t.Fatal(err)
	}
	if err := EnsureRecordingStorage(storage, identity); err == nil {
		t.Fatal("missing storage marker was accepted")
	}
}

func TestEnforceMinimumFreeSpaceDeletesFinalizedButNotStaging(t *testing.T) {
	root := t.TempDir()
	storage := filepath.Join(root, "recordings")
	identity := filepath.Join(root, "state", "storage.id")
	if err := EnsureRecordingStorage(storage, identity); err != nil {
		t.Fatal(err)
	}
	finalized := filepath.Join(storage, "camera", "segment.mp4")
	staging := filepath.Join(storage, ".staging", "camera", "active.mp4")
	for _, path := range []string{finalized, staging} {
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("video"), 0600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := EnforceMinimumFreeSpace(storage, identity, math.MaxUint64, time.Second)
	if err == nil || deleted != 1 {
		t.Fatalf("unexpected cleanup result: deleted=%d err=%v", deleted, err)
	}
	if _, err := os.Stat(finalized); !os.IsNotExist(err) {
		t.Fatalf("finalized segment was not deleted: %v", err)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("active staging segment was deleted: %v", err)
	}
}
