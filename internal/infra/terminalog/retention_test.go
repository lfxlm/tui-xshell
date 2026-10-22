package terminalog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneRemovesExpiredLogs(t *testing.T) {
	directory := t.TempDir()
	now := time.Now()
	writeTestLog(t, directory, "expired.log", "expired", now.Add(-48*time.Hour))
	writeTestLog(t, directory, "recent.log", "recent", now)

	result, err := Prune(directory, Options{MaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := List(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "recent.log" {
		t.Fatalf("retained logs = %#v, want only recent.log", entries)
	}
	if result.RemovedFiles != 1 || result.RemovedBytes != int64(len("expired")) || result.Remaining != int64(len("recent")) {
		t.Fatalf("prune result = %#v, want one expired log removed", result)
	}
}

func TestPruneRemovesOldestLogsUntilUnderCapacity(t *testing.T) {
	directory := t.TempDir()
	now := time.Now()
	writeTestLog(t, directory, "oldest.log", "1234", now.Add(-2*time.Hour))
	writeTestLog(t, directory, "middle.log", "5678", now.Add(-time.Hour))
	writeTestLog(t, directory, "newest.log", "xyz", now)

	result, err := Prune(directory, Options{MaxTotalBytes: 6})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := List(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "newest.log" {
		t.Fatalf("retained logs = %#v, want only newest.log", entries)
	}
	if result.RemovedFiles != 2 || result.RemovedBytes != 8 || result.Remaining != 3 {
		t.Fatalf("prune result = %#v, want two oldest logs removed and 3 bytes remaining", result)
	}
}

func TestPruneProtectsActiveLogAndItsRotatedSegments(t *testing.T) {
	directory := t.TempDir()
	options := Options{MaxBytes: 4, RetainedFiles: 3, MaxTotalBytes: 1}
	logger, err := NewWithOptions(directory, "active", options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.Close() }()
	if err := logger.Write([]byte("abcdefghijkl")); err != nil {
		t.Fatal(err)
	}
	waitForLogSegments(t, logger, 3)
	writeTestLog(t, directory, "old.log", "old", time.Now().Add(-time.Hour))

	result, err := Prune(directory, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "old.log")); !os.IsNotExist(err) {
		t.Fatalf("inactive log still exists or returned an unexpected error: %v", err)
	}
	for _, path := range []string{logger.Path(), logger.archivePath(1), logger.archivePath(2)} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("active log segment %q was removed: %v", filepath.Base(path), err)
		}
	}
	if result.RemovedFiles != 1 || result.ActiveBytes != 12 || result.Remaining != 12 {
		t.Fatalf("prune result = %#v, want active output protected above the 1-byte cap", result)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("close logger after retention: %v", err)
	}
}

func writeTestLog(t *testing.T, directory, name, content string, modified time.Time) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func waitForLogSegments(t *testing.T, logger *Logger, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := List(filepath.Dir(logger.Path()))
		if err == nil && len(entries) >= count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("logger did not create %d segments before timeout", count)
}
