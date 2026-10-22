package scpclient

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func newShellBackedWorkspaceClient(t *testing.T) *Client {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the SCP workspace adapter targets POSIX OpenSSH servers")
	}
	var mu sync.Mutex
	var peers []*fakeCommand
	client, err := New(func() (Command, error) {
		inputReader, inputWriter := io.Pipe()
		outputReader, outputWriter := io.Pipe()
		errorReader, errorWriter := io.Pipe()
		peer := &fakeCommand{
			inputReader: inputReader, inputWriter: inputWriter,
			outputReader: outputReader, outputWriter: outputWriter,
			errorReader: errorReader, errorWriter: errorWriter,
			wait: make(chan error, 1), closed: make(chan struct{}),
		}
		peer.peer = func(command string, input io.Reader, output io.Writer) error {
			process := exec.Command("/bin/sh", "-c", command)
			_ = input
			process.Stdout = output
			process.Stderr = io.Discard
			return process.Run()
		}
		mu.Lock()
		peers = append(peers, peer)
		mu.Unlock()
		return peer, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, peer := range peers {
			peer.close()
		}
	})
	return client
}

func TestWorkspaceListsUnicodeHiddenAndSymlinkEntries(t *testing.T) {
	client := newShellBackedWorkspaceClient(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "remote 'directory")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"普通文件.txt", ".hidden", "quote'name.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("payload"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "nested"), filepath.Join(root, "linked-dir")); err != nil {
		t.Fatal(err)
	}

	entries, err := client.List(root)
	if err != nil {
		t.Fatalf("list remote directory: %v", err)
	}
	byName := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	for _, name := range []string{"普通文件.txt", ".hidden", "quote'name.txt", "nested", "linked-dir"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("remote listing omitted %q: %#v", name, entries)
		}
	}
	if !byName["nested"].IsDir || !byName["nested"].ModeKnown {
		t.Errorf("directory metadata = %#v", byName["nested"])
	}
	if !byName["linked-dir"].IsSymlink || byName["linked-dir"].IsDir {
		t.Errorf("symlink metadata = %#v", byName["linked-dir"])
	}
}

func TestWorkspaceListingCommandSupportsZshDefaults(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	root := t.TempDir()
	for _, name := range []string{"visible.txt", ".hidden"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("payload"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(zsh, "-c", workspaceListCommand(root))
	output, err := command.Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			t.Fatalf("list with zsh: %v; stderr=%s", err, exitError.Stderr)
		}
		t.Fatalf("list with zsh: %v", err)
	}
	entries, err := parseWorkspaceListing(output)
	if err != nil {
		t.Fatalf("parse zsh listing: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("zsh listing entries = %#v, want visible and hidden files", entries)
	}
	for _, entry := range entries {
		if entry.Mode.Perm() != 0o640 || entry.Size != int64(len("payload")) {
			t.Errorf("zsh metadata for %q = %#v, want mode 0640 and payload size", entry.Name, entry)
		}
	}
}

func TestWorkspaceSearchAndBasicOperations(t *testing.T) {
	client := newShellBackedWorkspaceClient(t)
	root := t.TempDir()
	remoteFile := filepath.Join(root, "source.txt")
	if err := os.WriteFile(remoteFile, []byte("workspace payload"), 0o640); err != nil {
		t.Fatal(err)
	}

	matches, truncated, err := client.Search(context.Background(), root, "source", 10)
	if err != nil || truncated || len(matches) != 1 || filepath.Base(matches[0].Path) != "source.txt" {
		t.Fatalf("search matches=%#v truncated=%t err=%v", matches, truncated, err)
	}
	if exists, err := client.Exists(remoteFile); err != nil || !exists {
		t.Fatalf("existing file exists=%t err=%v", exists, err)
	}
	if err := client.MakeDirectory(filepath.Join(root, "new-dir")); err != nil {
		t.Fatalf("make directory: %v", err)
	}
	if err := client.Rename(remoteFile, filepath.Join(root, "renamed.txt")); err != nil {
		t.Fatalf("rename file: %v", err)
	}
	if err := client.SetPermissions(filepath.Join(root, "renamed.txt"), 0o600); err != nil {
		t.Fatalf("set permissions: %v", err)
	}
	entry, err := client.entryForPath(filepath.Join(root, "renamed.txt"))
	if err != nil || entry.Mode.Perm() != 0o600 {
		t.Fatalf("updated file metadata=%#v err=%v", entry, err)
	}
	content, version, err := client.ReadTextFile(filepath.Join(root, "renamed.txt"), 1024)
	if err != nil || string(content) != "workspace payload" || version.Size != int64(len(content)) {
		t.Fatalf("read content=%q version=%#v err=%v", content, version, err)
	}
	if err := client.RemoveTreeContext(context.Background(), filepath.Join(root, "new-dir")); err != nil {
		t.Fatalf("remove tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new-dir")); !os.IsNotExist(err) {
		t.Fatalf("removed directory still exists: %v", err)
	}
}

func TestWorkspaceListingParserRejectsMalformedMetadataAndSkipsUnsafeNames(t *testing.T) {
	if _, err := parseWorkspaceListing([]byte("f\x00abc\x00")); err == nil {
		t.Fatal("incomplete metadata record was accepted")
	}
	output := []byte("f\x00644\x001\x0010\x00ok.txt\x00f\x00644\x001\x0010\x00bad\nname\x00")
	entries, err := parseWorkspaceListing(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "ok.txt" {
		t.Fatalf("parsed entries = %#v", entries)
	}
}

func TestWorkspaceTextReadRejectsFilesOverLimit(t *testing.T) {
	client := newShellBackedWorkspaceClient(t)
	remoteFile := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(remoteFile, []byte(strings.Repeat("x", 12)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.ReadTextFile(remoteFile, 8); err == nil {
		t.Fatal("oversized remote file was accepted")
	}
}

func TestWorkspaceSaveDetectsConcurrentRemoteEditBeforeTransfer(t *testing.T) {
	client := newShellBackedWorkspaceClient(t)
	remoteFile := filepath.Join(t.TempDir(), "conflict.txt")
	if err := os.WriteFile(remoteFile, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, expected, err := client.ReadTextFile(remoteFile, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteFile, []byte("changed elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SaveTextFile(context.Background(), remoteFile, []byte("my edit"), expected, nil); !errors.Is(err, ErrRemoteFileChanged) {
		t.Fatalf("stale save error = %v, want ErrRemoteFileChanged", err)
	}
	content, err := os.ReadFile(remoteFile)
	if err != nil || string(content) != "changed elsewhere" {
		t.Fatalf("concurrent remote edit was overwritten: content=%q err=%v", content, err)
	}
}

func TestWorkspaceListingPreservesModificationTimeAndPermissions(t *testing.T) {
	client := newShellBackedWorkspaceClient(t)
	remoteFile := filepath.Join(t.TempDir(), "metadata.txt")
	if err := os.WriteFile(remoteFile, []byte("x"), 0o604); err != nil {
		t.Fatal(err)
	}
	want := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(remoteFile, want, want); err != nil {
		t.Fatal(err)
	}
	entry, err := client.entryForPath(remoteFile)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Mode.Perm() != 0o604 || !entry.ModTime.Equal(want) {
		t.Fatalf("metadata mode=%#o mtime=%v, want %#o %v", entry.Mode.Perm(), entry.ModTime, 0o604, want)
	}
}
