package media

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanRecordingsIndexesAndExpiresSegments(t *testing.T) {
	base := t.TempDir()
	cameraID := "camera-test"
	directory := filepath.Join(base, cameraID)
	if err := os.MkdirAll(directory, 0750); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(directory, "20260828T120000Z.mp4")
	expired := filepath.Join(directory, "20260820T120000Z.mp4")
	if err := os.WriteFile(current, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(expired, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(current, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(expired, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	files, err := ScanRecordings(base, []RecordingStream{{ID: cameraID, RetentionDays: 1}}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].StorageKey != cameraID+"/20260828T120000Z.mp4" {
		t.Fatalf("unexpected inventory: %#v", files)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("expired segment was not removed: %v", err)
	}
}

func TestScanRecordingsOrganizesStagedSegmentsByCameraAndLocalTime(t *testing.T) {
	base := t.TempDir()
	cameraID := "7b847dd2-224d-4e68-8d0f-f501b563b0bd"
	staging := filepath.Join(base, ".staging", cameraID)
	if err := os.MkdirAll(staging, 0750); err != nil {
		t.Fatal(err)
	}
	segment := filepath.Join(staging, "20260901T143527.mp4")
	if err := os.WriteFile(segment, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(segment, old, old); err != nil {
		t.Fatal(err)
	}

	files, err := ScanRecordings(base, []RecordingStream{{ID: cameraID, Name: "Entrée principale", RetentionDays: 1}}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	expected := "entrée-principale__" + cameraID + "/2026/09/01/14/35-27.mp4"
	if len(files) != 1 || files[0].StorageKey != expected {
		t.Fatalf("unexpected organized inventory: %#v", files)
	}
	if _, err := os.Stat(filepath.Join(base, filepath.FromSlash(expected))); err != nil {
		t.Fatalf("organized segment missing: %v", err)
	}
}

func TestPopulateChecksumsCachesAndReloadsDigest(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "segment.mp4")
	if err := os.WriteFile(path, []byte("video-content"), 0600); err != nil {
		t.Fatal(err)
	}
	files := []RecordingFile{{Path: path}}
	if err := PopulateChecksums(files, 1); err != nil {
		t.Fatal(err)
	}
	if len(files[0].ChecksumSHA256) != 64 {
		t.Fatalf("invalid checksum: %q", files[0].ChecksumSHA256)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if cached := readChecksumSidecar(path, info); cached != files[0].ChecksumSHA256 {
		t.Fatalf("cached checksum mismatch: %q", cached)
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if cached := readChecksumSidecar(path, changed); cached != "" {
		t.Fatalf("stale checksum was reused: %q", cached)
	}
}
