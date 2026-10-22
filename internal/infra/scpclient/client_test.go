package scpclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"tui-ssh-workbench/internal/infra/remotefs"
)

func TestUploadStagesRemoteFileAndReportsProgress(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "payload.txt")
	contents := []byte("hello over scp\n")
	if err := os.WriteFile(localPath, contents, 0o640); err != nil {
		t.Fatal(err)
	}
	remotePath := "/remote/a'b.txt"
	partialPath := remoteUploadPartialPath(remotePath, 0)
	remoteFiles := map[string][]byte{}
	var streamCommand string
	var commitCommand string
	client, closeClient := newSequencedTestClient(func(command string, input io.Reader, output io.Writer) error {
		switch {
		case strings.Contains(command, "printf 'missing\\n'"):
			_, err := io.WriteString(output, "missing\n")
			return err
		case strings.Contains(command, "cat >> \"$partial\""):
			streamCommand = command
			payload, err := io.ReadAll(input)
			if err != nil {
				return err
			}
			remoteFiles[partialPath] = append(remoteFiles[partialPath], payload...)
			return nil
		case strings.Contains(command, "chmod 0640"):
			commitCommand = command
			remoteFiles[remotePath] = append([]byte(nil), remoteFiles[partialPath]...)
			delete(remoteFiles, partialPath)
			return nil
		default:
			return fmt.Errorf("unexpected upload command: %s", command)
		}
	})
	defer closeClient()

	var progressCurrent, progressTotal int64
	err := client.Upload(context.Background(), localPath, remotePath, false, func(current, total int64) {
		progressCurrent, progressTotal = current, total
	})
	if err != nil {
		t.Fatalf("upload SCP file: %v", err)
	}
	if string(remoteFiles[remotePath]) != string(contents) || progressCurrent != int64(len(contents)) || progressTotal != int64(len(contents)) {
		t.Fatalf("uploaded=%q progress=%d/%d", remoteFiles[remotePath], progressCurrent, progressTotal)
	}
	if !strings.Contains(streamCommand, "cat >> \"$partial\"") || !strings.Contains(streamCommand, shellQuote(partialPath)) || !strings.Contains(commitCommand, "ln \"$partial\" \"$target\"") {
		t.Fatalf("upload did not use resumable staging and no-replace commit: stream=%s commit=%s", streamCommand, commitCommand)
	}
	if strings.Contains(streamCommand, "'\"'\"'") || !strings.Contains(commitCommand, "'\"'\"'") {
		t.Fatalf("remote quote was not shell-escaped: stream=%s commit=%s", streamCommand, commitCommand)
	}
}

func TestUploadResumesOnlyMatchingRemotePrefix(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "payload.bin")
	contents := []byte("a verified source payload")
	if err := os.WriteFile(localPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	remotePath := "/remote/payload.bin"
	partialPath := remoteUploadPartialPath(remotePath, 0)
	partial := append([]byte(nil), contents[:9]...)
	remoteFiles := map[string][]byte{partialPath: partial}
	var firstProgress int64 = -1
	client, closeClient := newSequencedTestClient(func(command string, input io.Reader, output io.Writer) error {
		switch {
		case strings.Contains(command, "if command -v sha256sum") && strings.Contains(command, "printf 'file %s %s\\n'"):
			digest := sha256.Sum256(remoteFiles[partialPath])
			_, err := fmt.Fprintf(output, "file %d %x\n", len(remoteFiles[partialPath]), digest)
			return err
		case strings.Contains(command, "cat >> \"$partial\""):
			var suffix []byte
			suffix, err := io.ReadAll(input)
			if err != nil {
				return err
			}
			remoteFiles[partialPath] = append(remoteFiles[partialPath], suffix...)
			return nil
		case strings.Contains(command, "chmod 0600"):
			remoteFiles[remotePath] = append([]byte(nil), remoteFiles[partialPath]...)
			delete(remoteFiles, partialPath)
			return nil
		default:
			return fmt.Errorf("unexpected resume command: %s", command)
		}
	})
	defer closeClient()
	if err := client.Upload(context.Background(), localPath, remotePath, false, func(current, _ int64) {
		if firstProgress < 0 {
			firstProgress = current
		}
	}); err != nil {
		t.Fatalf("upload with matching partial: %v", err)
	}
	if firstProgress != int64(len(partial)) {
		t.Fatalf("resumed upload started at %d bytes, want %d", firstProgress, len(partial))
	}
	if !bytes.Equal(remoteFiles[remotePath], contents) {
		t.Fatalf("resumed upload content = %q, want %q", remoteFiles[remotePath], contents)
	}
}

func TestRemoteEpochTimePreservesNanoseconds(t *testing.T) {
	for _, test := range []struct {
		value string
		want  time.Time
	}{
		{value: "1700000000", want: time.Unix(1700000000, 0)},
		{value: "1700000000.123456", want: time.Unix(1700000000, 123456000)},
		{value: "1700000000.123456789", want: time.Unix(1700000000, 123456789)},
	} {
		got, err := parseRemoteEpochTime(test.value)
		if err != nil || !got.Equal(test.want) {
			t.Errorf("parseRemoteEpochTime(%q)=%s err=%v; want %s", test.value, got, err, test.want)
		}
		if formatted := formatRemoteEpochTime(test.want); formatted != formatRemoteEpochTime(got) {
			t.Errorf("formatRemoteEpochTime(%s)=%q, parsed as %q", test.want, formatted, formatRemoteEpochTime(got))
		}
	}
	for _, invalid := range []string{"", "-1", "1700000000.", "1700000000.1234567890", "no-time"} {
		if _, err := parseRemoteEpochTime(invalid); err == nil {
			t.Errorf("parseRemoteEpochTime(%q) succeeded; want error", invalid)
		}
	}
}

func TestDownloadStagesFileAndHonorsOverwrite(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "download.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents := []byte("new remote contents")
	digest := sha256.Sum256(contents)
	var streamCommand string
	client, closeClient := newSequencedTestClient(func(command string, _ io.Reader, output io.Writer) error {
		switch {
		case strings.Contains(command, "printf 'file %s %s %s %s\\n'"):
			_, err := fmt.Fprintf(output, "file 750 %d 1700000000 %x\n", len(contents), digest)
			return err
		case strings.Contains(command, "tail -c +"):
			streamCommand = command
			if _, err := output.Write(contents); err != nil {
				return err
			}
			_, err := io.WriteString(output, "\x00")
			return err
		default:
			return fmt.Errorf("unexpected download command: %s", command)
		}
	})
	defer closeClient()

	if err := client.Download(context.Background(), "/var/tmp/remote-name.txt", target, true, nil); err != nil {
		t.Fatalf("download SCP file: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(contents) {
		t.Fatalf("downloaded contents = %q, err=%v", got, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("downloaded permissions = %v, err=%v; want 0750", info, err)
	}
	if !strings.Contains(streamCommand, "source='/var/tmp/remote-name.txt'") || !strings.Contains(streamCommand, "tail -c +1") {
		t.Fatalf("download command did not pass a quoted remote path or stream file content: %s", streamCommand)
	}
}

func TestDownloadResumesOnlyMatchingLocalPrefix(t *testing.T) {
	root := t.TempDir()
	target, err := filepath.Abs(filepath.Join(root, "download.bin"))
	if err != nil {
		t.Fatal(err)
	}
	remotePath := "/remote/payload.bin"
	contents := []byte("a remote payload that can be resumed")
	offset := int64(11)
	partialPath := localDownloadPartialPath(target, remotePath, 0)
	partial := append([]byte(nil), contents[:offset]...)
	if err := os.WriteFile(partialPath, partial, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	prefixDigest := sha256.Sum256(partial)
	firstProgress := int64(-1)
	var streamCommand string
	client, closeClient := newSequencedTestClient(func(command string, _ io.Reader, output io.Writer) error {
		switch {
		case strings.Contains(command, "printf 'file %s %s %s %s\\n'"):
			_, err := fmt.Fprintf(output, "file 600 %d 1700000000 %x\n", len(contents), digest)
			return err
		case strings.Contains(command, "head -c "+strconv.FormatInt(offset, 10)) && strings.Contains(command, "sha256sum"):
			_, err := fmt.Fprintf(output, "%x\n", prefixDigest)
			return err
		case strings.Contains(command, "tail -c +"):
			streamCommand = command
			if _, err := output.Write(contents[offset:]); err != nil {
				return err
			}
			_, err := io.WriteString(output, "\x00")
			return err
		default:
			return fmt.Errorf("unexpected download resume command: %s", command)
		}
	})
	defer closeClient()

	if err := client.Download(context.Background(), remotePath, target, false, func(current, _ int64) {
		if firstProgress < 0 {
			firstProgress = current
		}
	}); err != nil {
		t.Fatalf("download with matching partial: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("resumed download contents=%q err=%v", got, err)
	}
	if firstProgress != offset || !strings.Contains(streamCommand, "tail -c +12") {
		t.Fatalf("resumed download started at %d with command %q; want offset %d", firstProgress, streamCommand, offset)
	}
}

func TestDownloadDoesNotResumeMismatchedLocalPrefix(t *testing.T) {
	root := t.TempDir()
	target, err := filepath.Abs(filepath.Join(root, "download.bin"))
	if err != nil {
		t.Fatal(err)
	}
	remotePath := "/remote/payload.bin"
	contents := []byte("the expected remote payload")
	wrongPartial := []byte("not this")
	partialPath := localDownloadPartialPath(target, remotePath, 0)
	if err := os.WriteFile(partialPath, wrongPartial, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	prefixDigest := sha256.Sum256(contents[:len(wrongPartial)])
	var streamCommand string
	client, closeClient := newSequencedTestClient(func(command string, _ io.Reader, output io.Writer) error {
		switch {
		case strings.Contains(command, "printf 'file %s %s %s %s\\n'"):
			_, err := fmt.Fprintf(output, "file 600 %d 1700000000 %x\n", len(contents), digest)
			return err
		case strings.Contains(command, "head -c 8") && strings.Contains(command, "sha256sum"):
			_, err := fmt.Fprintf(output, "%x\n", prefixDigest)
			return err
		case strings.Contains(command, "tail -c +"):
			streamCommand = command
			if _, err := output.Write(contents); err != nil {
				return err
			}
			_, err := io.WriteString(output, "\x00")
			return err
		default:
			return fmt.Errorf("unexpected mismatched-prefix download command: %s", command)
		}
	})
	defer closeClient()

	if err := client.Download(context.Background(), remotePath, target, false, nil); err != nil {
		t.Fatalf("download after mismatched partial: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("download contents=%q err=%v", got, err)
	}
	stillThere, err := os.ReadFile(partialPath)
	if err != nil || !bytes.Equal(stillThere, wrongPartial) {
		t.Fatalf("mismatched partial was changed: contents=%q err=%v", stillThere, err)
	}
	if !strings.Contains(streamCommand, "tail -c +1") {
		t.Fatalf("mismatched partial was incorrectly resumed: %s", streamCommand)
	}
}

func TestDownloadRejectsOverwriteBeforeOpeningSSH(t *testing.T) {
	target := filepath.Join(t.TempDir(), "exists.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := false
	client, err := New(func() (Command, error) {
		opened = true
		return nil, errors.New("unexpected connection")
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Download(context.Background(), "/remote/file", target, false, nil)
	if !errors.Is(err, os.ErrExist) || opened {
		t.Fatalf("download error=%v opened=%t; want no-overwrite rejection before SSH", err, opened)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "keep" {
		t.Fatalf("existing destination changed: contents=%q err=%v", got, err)
	}
}

func TestUploadCancellationClosesTheSSHExecChannel(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "cancel.txt")
	if err := os.WriteFile(localPath, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	var readyOnce sync.Once
	client, closeClient := newSequencedTestClient(func(commandText string, _ io.Reader, output io.Writer) error {
		if strings.Contains(commandText, "printf 'missing\\n'") {
			_, err := io.WriteString(output, "missing\n")
			return err
		}
		if !strings.Contains(commandText, "cat >> \"$partial\"") {
			return fmt.Errorf("unexpected cancellation command: %s", commandText)
		}
		readyOnce.Do(func() { close(ready) })
		time.Sleep(100 * time.Millisecond)
		return nil
	})
	defer closeClient()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- client.Upload(ctx, localPath, "/remote/cancel.txt", false, nil)
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("SCP server did not reach the blocked transfer point")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled upload error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceling SCP did not close the SSH exec channel")
	}
}

func TestDownloadRejectsUnexpectedRemoteMetadata(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "download")
	client, peer := newTestClient(func(_ string, _ io.Reader, output io.Writer) error {
		_, err := io.WriteString(output, "C0644 1 ../outside\n")
		return err
	})
	defer peer.close()
	if err := client.Download(context.Background(), "/remote/file", target, false, nil); err == nil {
		t.Fatal("unsafe SCP filename was accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe file escaped destination: stat error=%v", err)
	}
}

func TestParseRemoteDownloadListingRejectsTraversalName(t *testing.T) {
	listing := []byte("d\x00755\x000\x001700000000\x00../escape\x00")
	if _, err := parseRemoteDownloadListing(listing); err == nil || !strings.Contains(err.Error(), "文件名无效") {
		t.Fatalf("traversal listing error = %v", err)
	}
}

func TestDownloadDirectoryResumesInterruptedNestedFile(t *testing.T) {
	root := t.TempDir()
	localRoot, err := filepath.Abs(filepath.Join(root, "download"))
	if err != nil {
		t.Fatal(err)
	}
	remoteRoot := "/remote/tree"
	remoteTree := testSCPDownloadTree()
	interrupted := true
	client, closeClient := newSequencedTestClient(testSCPDownloadPeer(remoteTree, &interrupted))
	defer closeClient()

	err = client.DownloadDirectory(context.Background(), remoteRoot, localRoot, false, nil)
	if err == nil || !strings.Contains(err.Error(), "下载 SCP 文件") {
		t.Fatalf("first directory download error = %v, want interrupted file error", err)
	}
	firstFile := remoteTree["/remote/tree/a.txt"]
	localFirst := filepath.Join(localRoot, "a.txt")
	partialPath := localDownloadPartialPath(localFirst, "/remote/tree/a.txt", 0)
	partialInfo, err := os.Stat(partialPath)
	if err != nil || partialInfo.Size() == 0 || partialInfo.Size() >= int64(len(firstFile.contents)) {
		t.Fatalf("interrupted partial info=%v err=%v", partialInfo, err)
	}
	markerPath := directoryDownloadResumeMarkerPath(localRoot, remoteRoot)
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("incomplete directory download marker missing: %v", err)
	}

	var detailed []remotefs.TransferProgress
	ctx := remotefs.WithTransferProgressReporter(context.Background(), func(progress remotefs.TransferProgress) {
		detailed = append(detailed, progress)
	})
	if err := client.DownloadDirectory(ctx, remoteRoot, localRoot, false, nil); err != nil {
		t.Fatalf("resume directory download without overwrite: %v", err)
	}
	for remotePath, node := range remoteTree {
		if node.kind != 'f' {
			continue
		}
		relative := strings.TrimPrefix(remotePath, remoteRoot+"/")
		localPath := filepath.Join(localRoot, filepath.FromSlash(relative))
		got, err := os.ReadFile(localPath)
		if err != nil || !bytes.Equal(got, node.contents) {
			t.Fatalf("downloaded %s=%q err=%v; want %q", relative, got, err, node.contents)
		}
	}
	if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed directory download kept resume marker: %v", err)
	}
	if !hasProgressPath(detailed, "sub/b.bin") {
		t.Fatalf("recursive progress did not report relative file path sub/b.bin: %#v", detailed)
	}
}

func TestDownloadAutoDetectsDirectoryAndResumesCompleteFiles(t *testing.T) {
	parent := t.TempDir()
	localRoot, err := filepath.Abs(filepath.Join(parent, "tree"))
	if err != nil {
		t.Fatal(err)
	}
	remoteRoot := "/remote/tree"
	remoteTree := testSCPDownloadTree()
	client, closeClient := newSequencedTestClient(testSCPDownloadPeer(remoteTree, new(bool)))
	defer closeClient()
	if err := client.Download(context.Background(), remoteRoot, localRoot, false, nil); err != nil {
		t.Fatalf("auto-detected directory download: %v", err)
	}
	for remotePath, node := range remoteTree {
		if node.kind != 'f' {
			continue
		}
		relative := strings.TrimPrefix(remotePath, remoteRoot+"/")
		got, err := os.ReadFile(filepath.Join(localRoot, filepath.FromSlash(relative)))
		if err != nil || !bytes.Equal(got, node.contents) {
			t.Fatalf("auto-detected file %s=%q err=%v", relative, got, err)
		}
	}
}

func TestUploadDirectoryRejectsUnrepresentableLocalNameBeforeOpeningSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot create a filename containing a newline")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bad\nname"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := false
	client, err := New(func() (Command, error) {
		opened = true
		return nil, errors.New("unexpected SCP connection")
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.UploadDirectory(context.Background(), root, "remote/tree", false, nil); err == nil {
		t.Fatal("directory containing an unrepresentable filename was accepted")
	}
	if opened {
		t.Fatal("SCP opened a remote command before validating the directory tree")
	}
}

func TestDownloadDirectoryRejectsExistingTargetWithoutOpeningSSH(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "tree")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	opened := false
	client, err := New(func() (Command, error) {
		opened = true
		return nil, errors.New("unexpected SCP connection")
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.DownloadDirectory(context.Background(), "/remote/tree", target, false, nil)
	if !errors.Is(err, os.ErrExist) || opened {
		t.Fatalf("download error=%v opened=%t; want no-overwrite rejection before SSH", err, opened)
	}
}

func TestDownloadDirectoryRejectsFileChangedAfterTreeScan(t *testing.T) {
	parent := t.TempDir()
	localRoot, err := filepath.Abs(filepath.Join(parent, "tree"))
	if err != nil {
		t.Fatal(err)
	}
	remoteRoot := "/remote/tree"
	remoteTree := testSCPDownloadTree()
	peer := testSCPDownloadPeer(remoteTree, nil)
	changed := false
	client, closeClient := newSequencedTestClient(func(command string, input io.Reader, output io.Writer) error {
		err := peer(command, input, output)
		if err == nil && strings.Contains(command, "for entry in ./*") {
			if directory, ok := testShellAssignment(command, "directory"); ok && directory == remoteRoot && !changed {
				node := remoteTree["/remote/tree/a.txt"]
				node.contents = []byte("changed after listing")
				node.modTime = node.modTime.Add(time.Second)
				remoteTree["/remote/tree/a.txt"] = node
				changed = true
			}
		}
		return err
	})
	defer closeClient()

	err = client.DownloadDirectory(context.Background(), remoteRoot, localRoot, false, nil)
	if !errors.Is(err, remotefs.ErrRemoteFileChanged) {
		t.Fatalf("changed tree download error = %v, want ErrRemoteFileChanged", err)
	}
	if _, err := os.Stat(filepath.Join(localRoot, "a.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changed remote file was committed locally: %v", err)
	}
}

type testSCPRemoteNode struct {
	kind     byte
	mode     os.FileMode
	modTime  time.Time
	contents []byte
}

func testSCPDownloadTree() map[string]testSCPRemoteNode {
	return map[string]testSCPRemoteNode{
		"/remote/tree": {
			kind: 'd', mode: 0o750, modTime: time.Unix(1_700_000_000, 123_456_789),
		},
		"/remote/tree/a.txt": {
			kind: 'f', mode: 0o640, modTime: time.Unix(1_700_000_001, 333_333_333), contents: []byte("a nested transfer can resume safely"),
		},
		"/remote/tree/sub": {
			kind: 'd', mode: 0o750, modTime: time.Unix(1_700_000_002, 444_444_444),
		},
		"/remote/tree/sub/b.bin": {
			kind: 'f', mode: 0o600, modTime: time.Unix(1_700_000_003, 555_555_555), contents: []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		},
	}
}

func testSCPDownloadPeer(tree map[string]testSCPRemoteNode, interrupted *bool) func(string, io.Reader, io.Writer) error {
	return func(command string, _ io.Reader, output io.Writer) error {
		switch {
		case strings.Contains(command, "printf 'directory %s %s"):
			remotePath, ok := testShellAssignment(command, "source")
			if !ok || tree[remotePath].kind != 'd' {
				return fmt.Errorf("unexpected directory metadata command: %s", command)
			}
			node := tree[remotePath]
			_, err := fmt.Fprintf(output, "directory %04o %s\n", node.mode.Perm(), formatRemoteEpochTime(node.modTime))
			return err
		case strings.Contains(command, "printf 'file %s %s %s %s"):
			remotePath, ok := testShellAssignment(command, "source")
			if !ok {
				return fmt.Errorf("missing source path in file metadata command: %s", command)
			}
			node, exists := tree[remotePath]
			if !exists {
				return fmt.Errorf("unknown remote file %s", remotePath)
			}
			if node.kind == 'd' {
				_, err := io.WriteString(output, "directory\n")
				return err
			}
			digest := sha256.Sum256(node.contents)
			_, err := fmt.Fprintf(output, "file %04o %d %s %x\n", node.mode.Perm(), len(node.contents), formatRemoteEpochTime(node.modTime), digest)
			return err
		case strings.Contains(command, "for entry in ./*"):
			directory, ok := testShellAssignment(command, "directory")
			if !ok || tree[directory].kind != 'd' {
				return fmt.Errorf("unexpected directory listing command: %s", command)
			}
			for remotePath, node := range tree {
				if path.Dir(remotePath) != directory {
					continue
				}
				if _, err := fmt.Fprintf(output, "%c\x00%04o\x00%d\x00%s\x00%s\x00", node.kind, node.mode.Perm(), len(node.contents), formatRemoteEpochTime(node.modTime), path.Base(remotePath)); err != nil {
					return err
				}
			}
			return nil
		case strings.Contains(command, "tail -c +"):
			remotePath, ok := testShellAssignment(command, "source")
			if !ok {
				return fmt.Errorf("missing source path in stream command: %s", command)
			}
			node, exists := tree[remotePath]
			if !exists || node.kind != 'f' {
				return fmt.Errorf("unknown stream source %s", remotePath)
			}
			tailOffset, err := testShellNumberAfter(command, "tail -c +")
			if err != nil || tailOffset <= 0 {
				return fmt.Errorf("invalid tail offset %d: %v", tailOffset, err)
			}
			start := int(tailOffset - 1)
			if start > len(node.contents) {
				start = len(node.contents)
			}
			payload := node.contents[start:]
			if interrupted != nil && *interrupted && remotePath == "/remote/tree/a.txt" {
				*interrupted = false
				_, err := output.Write(payload[:min(7, len(payload))])
				return err
			}
			if _, err := output.Write(payload); err != nil {
				return err
			}
			_, err = io.WriteString(output, "\x00")
			return err
		case strings.Contains(command, "head -c ") && strings.Contains(command, "sha256sum"):
			remotePath, ok := testShellAssignment(command, "source")
			if !ok {
				return fmt.Errorf("missing source path in prefix command: %s", command)
			}
			node, exists := tree[remotePath]
			if !exists || node.kind != 'f' {
				return fmt.Errorf("unknown prefix source %s", remotePath)
			}
			prefixSize, err := testShellNumberAfter(command, "head -c ")
			if err != nil || prefixSize < 0 || prefixSize > int64(len(node.contents)) {
				return fmt.Errorf("invalid prefix size %d: %v", prefixSize, err)
			}
			digest := sha256.Sum256(node.contents[:prefixSize])
			_, err = fmt.Fprintf(output, "%x\n", digest)
			return err
		default:
			return fmt.Errorf("unexpected SCP tree download command: %s", command)
		}
	}
}

func testShellAssignment(command, name string) (string, bool) {
	needle := name + "='"
	start := strings.Index(command, needle)
	if start < 0 {
		return "", false
	}
	start += len(needle)
	end := strings.IndexByte(command[start:], '\'')
	if end < 0 {
		return "", false
	}
	return command[start : start+end], true
}

func testShellNumberAfter(command, marker string) (int64, error) {
	index := strings.Index(command, marker)
	if index < 0 {
		return 0, errors.New("marker not found")
	}
	value := strings.Fields(command[index+len(marker):])
	if len(value) == 0 {
		return 0, errors.New("value not found")
	}
	return strconv.ParseInt(value[0], 10, 64)
}

func hasProgressPath(progress []remotefs.TransferProgress, expected string) bool {
	for _, item := range progress {
		if item.CurrentPath == expected {
			return true
		}
	}
	return false
}

func TestRemotePathsAndSCPNamesRejectControlCharacters(t *testing.T) {
	for _, value := range []string{"", "/tmp/a\nfile", "/tmp/a\x00file", "/tmp/a\tfile", "/"} {
		if _, err := cleanRemotePath(value); err == nil {
			t.Errorf("cleanRemotePath(%q) unexpectedly succeeded", value)
		}
	}
	for _, value := range []string{"", ".", "..", "../x", "a/b", "a\nfile", "a\tfile", "CON.txt", "bad:name"} {
		if err := validName(value); err == nil {
			t.Errorf("validName(%q) unexpectedly succeeded", value)
		}
	}
}

func TestReadProtocolLineEnforcesSizeLimit(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", maxProtocolLine+1)+"\n"), 128)
	if _, err := readProtocolLine(reader); err == nil || !strings.Contains(err.Error(), "长度限制") {
		t.Fatalf("oversized SCP protocol line error = %v", err)
	}
}

type fakeCommand struct {
	inputReader  *io.PipeReader
	inputWriter  *io.PipeWriter
	outputReader *io.PipeReader
	outputWriter *io.PipeWriter
	errorReader  *io.PipeReader
	errorWriter  *io.PipeWriter
	peer         func(string, io.Reader, io.Writer) error
	wait         chan error
	closed       chan struct{}
	closeOnce    sync.Once
	command      string
}

func newTestClient(peer func(string, io.Reader, io.Writer) error) (*Client, *fakeCommand) {
	command := newFakeCommand(peer)
	client, err := New(func() (Command, error) { return command, nil })
	if err != nil {
		panic(err)
	}
	return client, command
}

func newSequencedTestClient(peer func(string, io.Reader, io.Writer) error) (*Client, func()) {
	var mu sync.Mutex
	commands := make([]*fakeCommand, 0)
	client, err := New(func() (Command, error) {
		command := newFakeCommand(peer)
		mu.Lock()
		commands = append(commands, command)
		mu.Unlock()
		return command, nil
	})
	if err != nil {
		panic(err)
	}
	return client, func() {
		mu.Lock()
		defer mu.Unlock()
		for _, command := range commands {
			command.close()
		}
	}
}

func newFakeCommand(peer func(string, io.Reader, io.Writer) error) *fakeCommand {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	errorReader, errorWriter := io.Pipe()
	return &fakeCommand{
		inputReader: inputReader, inputWriter: inputWriter,
		outputReader: outputReader, outputWriter: outputWriter,
		errorReader: errorReader, errorWriter: errorWriter,
		peer: peer, wait: make(chan error, 1), closed: make(chan struct{}),
	}
}

func (command *fakeCommand) StdinPipe() (io.WriteCloser, error) { return command.inputWriter, nil }
func (command *fakeCommand) StdoutPipe() (io.Reader, error)     { return command.outputReader, nil }
func (command *fakeCommand) StderrPipe() (io.Reader, error)     { return command.errorReader, nil }

func (command *fakeCommand) Start(text string) error {
	command.command = text
	go func() {
		err := command.peer(text, command.inputReader, command.outputWriter)
		_ = command.outputWriter.Close()
		_ = command.errorWriter.Close()
		command.wait <- err
	}()
	return nil
}

func (command *fakeCommand) Wait() error { return <-command.wait }

func (command *fakeCommand) Close() error {
	command.closeOnce.Do(func() {
		close(command.closed)
		_ = command.inputWriter.Close()
		_ = command.inputReader.Close()
		_ = command.outputWriter.Close()
		_ = command.outputReader.Close()
		_ = command.errorWriter.Close()
		_ = command.errorReader.Close()
	})
	return nil
}

func (command *fakeCommand) close() { _ = command.Close() }
