package terminalog

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoggerWritesRawOutputToPrivateFile(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "session-logs")
	logger, err := New(directory, "prod:database / host")
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("\x1b[32mready\x1b[0m\r\n")
	if err := logger.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(logger.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("log contents = %q, want raw terminal bytes %q", got, content)
	}
	if filepath.Dir(logger.Path()) != directory {
		t.Fatalf("log path = %q, expected parent %q", logger.Path(), directory)
	}
	if strings.Contains(filepath.Base(logger.Path()), ":") || strings.Contains(filepath.Base(logger.Path()), "/") {
		t.Fatalf("host label was not sanitized in log path %q", logger.Path())
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(logger.Path())
		if err != nil {
			t.Fatal(err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
			t.Fatalf("log file mode = %04o, want %04o", got, want)
		}
		info, err = os.Stat(directory)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(0o700); got != want {
			t.Fatalf("log directory mode = %04o, want %04o", got, want)
		}
	}
	if err := logger.Write([]byte("late")); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after close error = %v, want ErrClosed", err)
	}
}

func TestListReturnsNewestRegularLogsAndIgnoresSymlinks(t *testing.T) {
	directory := t.TempDir()
	older := filepath.Join(directory, "older.log")
	newer := filepath.Join(directory, "newer.log")
	ignored := filepath.Join(directory, "notes.txt")
	for _, path := range []string{older, newer, ignored} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(older, time.Unix(10, 0), time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, time.Unix(20, 0), time.Unix(20, 0)); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "linked.log")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	entries, err := List(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "newer.log" || entries[1].Name != "older.log" {
		t.Fatalf("log entries = %#v, want newer.log then older.log", entries)
	}
	if entries[0].Size != 1 || entries[0].Path != newer {
		t.Fatalf("newest log entry = %#v, want size 1 and path %q", entries[0], newer)
	}
}

func TestListRejectsSymlinkDirectoryAndAllowsMissingDirectory(t *testing.T) {
	entries, err := List(filepath.Join(t.TempDir(), "missing"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("missing log directory returned entries=%v err=%v, want empty and nil", entries, err)
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "logs")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := List(link); err == nil {
		t.Fatal("log listing accepted a symlink directory")
	}
}

func TestRemoveOnlyDeletesRegularLogFilesInsideDirectory(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "session.log")
	if err := os.WriteFile(logPath, []byte("output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Remove(directory, "session.log"); err != nil {
		t.Fatalf("remove regular log: %v", err)
	}
	if _, err := os.Lstat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed log still exists or returned an unexpected error: %v", err)
	}

	protected := filepath.Join(directory, "protected.log")
	if err := os.WriteFile(protected, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, invalidName := range []string{"../protected.log", "..\\protected.log", "notes.txt", ""} {
		if err := Remove(directory, invalidName); err == nil {
			t.Errorf("Remove(%q) succeeded, want invalid-name error", invalidName)
		}
	}
	if got, err := os.ReadFile(protected); err != nil || string(got) != "keep" {
		t.Fatalf("invalid names changed the protected file: content=%q err=%v", got, err)
	}

	linkTarget := filepath.Join(directory, "target")
	if err := os.WriteFile(linkTarget, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "linked.log")
	if err := os.Symlink(linkTarget, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := Remove(directory, "linked.log"); err == nil {
		t.Fatal("Remove accepted a symlink log")
	}
	if got, err := os.ReadFile(linkTarget); err != nil || string(got) != "target" {
		t.Fatalf("symlink target changed: content=%q err=%v", got, err)
	}
	if err := Remove(filepath.Dir(directory), filepath.Base(directory)); err == nil {
		t.Fatal("Remove accepted a directory outside the log root")
	}
}

func TestLoggerRejectsSymlinkDirectory(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "logs")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := New(link, "host"); err == nil {
		t.Fatal("logger accepted a symlink directory")
	}
}

func TestLoggerRotatesInsteadOfStoppingAtTheSegmentLimit(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "bounded-*.log")
	if err != nil {
		t.Fatal(err)
	}
	redactor, err := NewRedactor(nil)
	if err != nil {
		t.Fatal(err)
	}
	logger := newConfiguredLogger(file, Options{MaxBytes: 5, RetainedFiles: 3}, redactor)
	if err := logger.Write([]byte("0123456789ABCDEFGHI")); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("close after rotation: %v", err)
	}
	var content strings.Builder
	for _, path := range []string{logger.archivePath(2), logger.archivePath(1), logger.Path()} {
		segment, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(segment) > 5 {
			t.Fatalf("segment %q exceeded its 5-byte cap: %d", path, len(segment))
		}
		content.Write(segment)
	}
	if got, want := content.String(), "56789ABCDEFGHI"; got != want {
		t.Fatalf("retained rotated output = %q, want %q", got, want)
	}
	entries, err := List(filepath.Dir(logger.Path()))
	if err != nil || len(entries) != 3 {
		t.Fatalf("rotated log entries = %d, %v; want 3", len(entries), err)
	}
	stamp := time.Unix(100, 0)
	for _, path := range []string{logger.Path(), logger.archivePath(1), logger.archivePath(2)} {
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	entries, err = List(filepath.Dir(logger.Path()))
	if err != nil {
		t.Fatal(err)
	}
	for index, expected := range []string{logger.Path(), logger.archivePath(1), logger.archivePath(2)} {
		if entries[index].Path != expected {
			t.Fatalf("equal-time log entry %d = %q, want %q", index, entries[index].Path, expected)
		}
	}
}

func TestSafeLabelRemovesPathAndControlCharacters(t *testing.T) {
	if got, want := safeLabel("../../prod\nsecret"), "prodsecret"; got != want {
		t.Fatalf("safe label = %q, want %q", got, want)
	}
}
