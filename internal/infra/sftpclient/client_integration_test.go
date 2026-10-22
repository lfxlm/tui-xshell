package sftpclient

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"tui-ssh-workbench/internal/infra/remotefs"
)

func TestSFTPClientFileAndDirectoryOperations(t *testing.T) {
	remoteRoot := t.TempDir()
	client := newTestSFTPClient(t, remoteRoot)
	ctx := context.Background()

	root, err := client.WorkingDirectory()
	if err != nil {
		t.Fatalf("get remote working directory: %v", err)
	}
	entries, err := client.List(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("list empty remote directory = %v, %v; want no entries", entries, err)
	}

	if err := client.MakeDirectory(filepath.ToSlash(filepath.Join(root, "files"))); err != nil {
		t.Fatalf("create remote directory: %v", err)
	}
	remoteFile := filepath.ToSlash(filepath.Join(root, "files", "note.txt"))
	localSource := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(localSource, []byte("first version\n"), 0o600); err != nil {
		t.Fatalf("write local source: %v", err)
	}
	var progress int64
	if err := client.Upload(ctx, localSource, remoteFile, false, func(done, _ int64) { progress = done }); err != nil {
		t.Fatalf("upload file: %v", err)
	}
	if progress != int64(len("first version\n")) {
		t.Fatalf("upload progress = %d, want %d", progress, len("first version\n"))
	}
	if exists, err := client.Exists(remoteFile); err != nil || !exists {
		t.Fatalf("remote file exists = %v, %v; want true", exists, err)
	}
	if err := client.Upload(ctx, localSource, remoteFile, false, nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("upload without overwrite error = %v, want os.ErrExist", err)
	}

	content, version, err := client.ReadTextFile(remoteFile, MaxEditableFileSize)
	if err != nil || string(content) != "first version\n" {
		t.Fatalf("read remote text = %q, %v; want original content", content, err)
	}
	updatedVersion, err := client.SaveTextFile(ctx, remoteFile, []byte("edited remotely\n"), version, nil)
	if err != nil {
		t.Fatalf("save remote text: %v", err)
	}
	if updatedVersion.Size != int64(len("edited remotely\n")) {
		t.Fatalf("saved file size = %d, want %d", updatedVersion.Size, len("edited remotely\n"))
	}
	if _, err := client.SaveTextFile(ctx, remoteFile, []byte("stale edit\n"), version, nil); !errors.Is(err, ErrRemoteFileChanged) {
		t.Fatalf("stale save error = %v, want ErrRemoteFileChanged", err)
	}

	localDownload := filepath.Join(t.TempDir(), "download.txt")
	if err := client.Download(ctx, remoteFile, localDownload, false, nil); err != nil {
		t.Fatalf("download file: %v", err)
	}
	downloaded, err := os.ReadFile(localDownload)
	if err != nil || string(downloaded) != "edited remotely\n" {
		t.Fatalf("downloaded content = %q, %v; want updated content", downloaded, err)
	}

	localTree := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(localTree, "nested"), 0o755); err != nil {
		t.Fatalf("create local tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localTree, "nested", "item.txt"), []byte("tree item"), 0o644); err != nil {
		t.Fatalf("write local tree item: %v", err)
	}
	remoteTree := filepath.ToSlash(filepath.Join(root, "tree"))
	if err := client.UploadDirectory(ctx, localTree, remoteTree, false, nil); err != nil {
		t.Fatalf("upload directory: %v", err)
	}
	localTreeCopy := filepath.Join(t.TempDir(), "tree-copy")
	if err := client.DownloadDirectory(ctx, remoteTree, localTreeCopy, false, nil); err != nil {
		t.Fatalf("download directory: %v", err)
	}
	item, err := os.ReadFile(filepath.Join(localTreeCopy, "nested", "item.txt"))
	if err != nil || string(item) != "tree item" {
		t.Fatalf("downloaded tree item = %q, %v; want original content", item, err)
	}

	if err := client.Remove(remoteFile, false); err != nil {
		t.Fatalf("remove remote file: %v", err)
	}
	if err := client.Remove(filepath.ToSlash(filepath.Join(root, "files")), true); err != nil {
		t.Fatalf("remove empty remote directory: %v", err)
	}
}

func TestSFTPDirectoryTransferReportsFileProgress(t *testing.T) {
	client := newTestSFTPClient(t, t.TempDir())
	localRoot := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(localRoot, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localRoot, "first.txt"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localRoot, "nested", "second.txt"), []byte("wxyz"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := client.WorkingDirectory()
	if err != nil {
		t.Fatal(err)
	}
	remoteRoot := path.Join(root, "progress-tree")
	var reports []remotefs.TransferProgress
	ctx := remotefs.WithTransferProgressReporter(context.Background(), func(progress remotefs.TransferProgress) {
		reports = append(reports, progress)
	})
	if err := client.UploadDirectory(ctx, localRoot, remoteRoot, false, nil); err != nil {
		t.Fatalf("upload directory: %v", err)
	}
	assertDirectoryProgressReports(t, reports, 7)

	reports = nil
	localTarget := filepath.Join(t.TempDir(), "download-tree")
	if err := client.DownloadDirectory(ctx, remoteRoot, localTarget, false, nil); err != nil {
		t.Fatalf("download directory: %v", err)
	}
	assertDirectoryProgressReports(t, reports, 7)
}

func assertDirectoryProgressReports(t *testing.T, reports []remotefs.TransferProgress, totalBytes int64) {
	t.Helper()
	seenPaths := make(map[string]bool)
	var final remotefs.TransferProgress
	for _, report := range reports {
		if report.FilesTotal != 2 || report.Total != totalBytes {
			t.Fatalf("directory progress = %#v; want 2 files and %d bytes", report, totalBytes)
		}
		if report.CurrentPath != "" {
			seenPaths[report.CurrentPath] = true
		}
		final = report
	}
	for _, name := range []string{"first.txt", "nested/second.txt"} {
		if !seenPaths[name] {
			t.Fatalf("progress omitted file %q: %#v", name, reports)
		}
	}
	if final.FilesCompleted != 2 || final.FilesTotal != 2 || final.Transferred != totalBytes {
		t.Fatalf("final directory progress = %#v; want 2/2 files and %d/%d bytes", final, totalBytes, totalBytes)
	}
}

func TestSFTPClientSetPermissionsAndRejectSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX rwx permissions through os.FileMode")
	}
	remoteRoot := t.TempDir()
	remotePath := filepath.Join(remoteRoot, "script.sh")
	if err := os.WriteFile(remotePath, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := newTestSFTPClient(t, remoteRoot)
	remoteWorkingDirectory, err := client.WorkingDirectory()
	if err != nil {
		t.Fatal(err)
	}
	remoteFile := path.Join(remoteWorkingDirectory, "script.sh")
	if err := client.SetPermissions(remoteFile, 0o750); err != nil {
		t.Fatalf("set remote permissions: %v", err)
	}
	info, err := os.Stat(remotePath)
	if err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("remote file mode = %v, %v; want 0750", info, err)
	}
	if err := client.SetPermissions(remoteFile, 0o1000); err == nil {
		t.Fatal("permission bits outside rwx were accepted")
	}

	target := filepath.Join(remoteRoot, "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(remoteRoot, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if err := client.SetPermissions(path.Join(remoteWorkingDirectory, "link"), 0o644); err == nil {
		t.Fatal("symbolic-link permissions were changed")
	}
	info, err = os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("symlink target mode = %v, %v; want unchanged 0600", info, err)
	}
}

func TestSFTPClientSearchIsRecursiveBoundedAndDoesNotFollowSymlinks(t *testing.T) {
	remoteRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(remoteRoot, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Needle.log", filepath.Join("nested", "needle.conf")} {
		if err := os.WriteFile(filepath.Join(remoteRoot, name), []byte("match"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	externalRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(externalRoot, "needle.external"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalRoot, filepath.Join(remoteRoot, "linked")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	client := newTestSFTPClient(t, remoteRoot)
	root, err := client.WorkingDirectory()
	if err != nil {
		t.Fatal(err)
	}
	results, truncated, err := client.Search(context.Background(), root, "needle", 10)
	if err != nil || truncated {
		t.Fatalf("search results = %#v, truncated=%v, err=%v", results, truncated, err)
	}
	if len(results) != 2 || filepath.Base(results[0].Path) != "Needle.log" || filepath.Base(results[1].Path) != "needle.conf" {
		t.Fatalf("recursive search returned %#v; want only the two in-root matches", results)
	}
	limited, truncated, err := client.Search(context.Background(), root, "needle", 1)
	if err != nil || !truncated || len(limited) != 1 {
		t.Fatalf("limited search = %#v, truncated=%v, err=%v; want one result and truncation", limited, truncated, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := client.Search(cancelled, root, "needle", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search error = %v, want context.Canceled", err)
	}
}

func TestSFTPFileTransfersPreservePermissionsAndModificationTime(t *testing.T) {
	ctx := context.Background()
	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "preserved.txt")
	if err := os.WriteFile(sourcePath, []byte("metadata survives transfer\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	wantTime := time.Unix(1_700_000_000, 0)
	if err := os.Chmod(sourcePath, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(sourcePath, wantTime, wantTime); err != nil {
		t.Fatal(err)
	}
	remoteServerRoot := t.TempDir()
	sourceClient := newTestSFTPClient(t, remoteServerRoot)
	sourceRootRemote, err := sourceClient.WorkingDirectory()
	if err != nil {
		t.Fatal(err)
	}
	remotePath := path.Join(sourceRootRemote, "preserved.txt")
	if err := sourceClient.Upload(ctx, sourcePath, remotePath, false, nil); err != nil {
		t.Fatalf("upload with metadata preservation: %v", err)
	}
	remoteInfo, err := os.Stat(filepath.Join(remoteServerRoot, "preserved.txt"))
	if err != nil {
		t.Fatalf("stat uploaded file in test server filesystem: %v", err)
	}
	if remoteInfo.ModTime().Unix() != wantTime.Unix() {
		t.Fatalf("uploaded modification time = %v, want %v", remoteInfo.ModTime(), wantTime)
	}
	if runtime.GOOS != "windows" && remoteInfo.Mode().Perm() != 0o640 {
		t.Fatalf("uploaded permissions = %04o, want 0640", remoteInfo.Mode().Perm())
	}

	downloadRoot := t.TempDir()
	downloadPath := filepath.Join(downloadRoot, "preserved.txt")
	if err := sourceClient.Download(ctx, remotePath, downloadPath, false, nil); err != nil {
		t.Fatalf("download with metadata preservation: %v", err)
	}
	localInfo, err := os.Stat(downloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if localInfo.ModTime().Unix() != wantTime.Unix() {
		t.Fatalf("downloaded modification time = %v, want %v", localInfo.ModTime(), wantTime)
	}
	if runtime.GOOS != "windows" && localInfo.Mode().Perm() != 0o640 {
		t.Fatalf("downloaded permissions = %04o, want 0640", localInfo.Mode().Perm())
	}

	destinationRoot := t.TempDir()
	destinationClient := newTestSFTPClient(t, destinationRoot)
	destinationWorkingDirectory, err := destinationClient.WorkingDirectory()
	if err != nil {
		t.Fatal(err)
	}
	destinationPath := path.Join(destinationWorkingDirectory, "preserved.txt")
	if err := sourceClient.CopyRemoteFileTo(ctx, destinationClient, remotePath, destinationPath, false, nil); err != nil {
		t.Fatalf("remote-to-remote copy with metadata preservation: %v", err)
	}
	remoteCopyInfo, err := os.Stat(filepath.Join(destinationRoot, "preserved.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if remoteCopyInfo.ModTime().Unix() != wantTime.Unix() {
		t.Fatalf("remote copy modification time = %v, want %v", remoteCopyInfo.ModTime(), wantTime)
	}
	if runtime.GOOS != "windows" && remoteCopyInfo.Mode().Perm() != 0o640 {
		t.Fatalf("remote copy permissions = %04o, want 0640", remoteCopyInfo.Mode().Perm())
	}

	treeRoot := t.TempDir()
	nestedDirectory := filepath.Join(treeRoot, "nested")
	if err := os.Mkdir(nestedDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedDirectory, "item.txt"), []byte("tree item"), 0o600); err != nil {
		t.Fatal(err)
	}
	directoryTime := time.Unix(1_650_000_000, 0)
	if err := os.Chmod(nestedDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(nestedDirectory, directoryTime, directoryTime); err != nil {
		t.Fatal(err)
	}
	remoteTree := path.Join(sourceRootRemote, "metadata-tree")
	if err := sourceClient.UploadDirectory(ctx, treeRoot, remoteTree, false, nil); err != nil {
		t.Fatalf("upload directory with metadata preservation: %v", err)
	}
	remoteNestedInfo, err := os.Stat(filepath.Join(remoteServerRoot, "metadata-tree", "nested"))
	if err != nil {
		t.Fatal(err)
	}
	if remoteNestedInfo.ModTime().Unix() != directoryTime.Unix() {
		t.Fatalf("uploaded directory modification time = %v, want %v", remoteNestedInfo.ModTime(), directoryTime)
	}
	if runtime.GOOS != "windows" && remoteNestedInfo.Mode().Perm() != 0o750 {
		t.Fatalf("uploaded directory permissions = %04o, want 0750", remoteNestedInfo.Mode().Perm())
	}
	downloadedTree := filepath.Join(t.TempDir(), "tree")
	if err := sourceClient.DownloadDirectory(ctx, remoteTree, downloadedTree, false, nil); err != nil {
		t.Fatalf("download directory with metadata preservation: %v", err)
	}
	downloadedNestedInfo, err := os.Stat(filepath.Join(downloadedTree, "nested"))
	if err != nil {
		t.Fatal(err)
	}
	if downloadedNestedInfo.ModTime().Unix() != directoryTime.Unix() {
		t.Fatalf("downloaded directory modification time = %v, want %v", downloadedNestedInfo.ModTime(), directoryTime)
	}
	if runtime.GOOS != "windows" && downloadedNestedInfo.Mode().Perm() != 0o750 {
		t.Fatalf("downloaded directory permissions = %04o, want 0750", downloadedNestedInfo.Mode().Perm())
	}
}

func TestCopyRemoteFilesAndDirectoriesBetweenTransports(t *testing.T) {
	ctx := context.Background()
	source := newTestSFTPClient(t, t.TempDir())
	destination := newTestSFTPClient(t, t.TempDir())
	sourceRoot, err := source.WorkingDirectory()
	if err != nil {
		t.Fatalf("get source working directory: %v", err)
	}
	destinationRoot, err := destination.WorkingDirectory()
	if err != nil {
		t.Fatalf("get destination working directory: %v", err)
	}
	if err := source.MakeDirectory(path.Join(sourceRoot, "tree")); err != nil {
		t.Fatalf("create remote source tree: %v", err)
	}
	if err := source.MakeDirectory(path.Join(sourceRoot, "tree", "nested")); err != nil {
		t.Fatalf("create nested remote source directory: %v", err)
	}
	localSource := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(localSource, []byte("remote to remote\n"), 0o600); err != nil {
		t.Fatalf("write local staging source: %v", err)
	}
	remoteFile := path.Join(sourceRoot, "tree", "nested", "payload.txt")
	if err := source.Upload(ctx, localSource, remoteFile, false, nil); err != nil {
		t.Fatalf("seed remote source file: %v", err)
	}

	fileDestination := path.Join(destinationRoot, "copy.txt")
	var finalProgress int64
	if err := source.CopyRemoteFileTo(ctx, destination, remoteFile, fileDestination, false, func(done, _ int64) {
		finalProgress = done
	}); err != nil {
		t.Fatalf("copy remote file to second server: %v", err)
	}
	if finalProgress != int64(len("remote to remote\n")) {
		t.Fatalf("final transfer progress = %d, want %d", finalProgress, len("remote to remote\n"))
	}
	if err := source.CopyRemoteFileTo(ctx, destination, remoteFile, fileDestination, false, nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("copy over existing destination without approval = %v, want os.ErrExist", err)
	}
	localCopy := filepath.Join(t.TempDir(), "downloaded.txt")
	if err := destination.Download(ctx, fileDestination, localCopy, false, nil); err != nil {
		t.Fatalf("verify copied remote file: %v", err)
	}
	if content, err := os.ReadFile(localCopy); err != nil || string(content) != "remote to remote\n" {
		t.Fatalf("copied remote file content = %q, %v", content, err)
	}

	treeDestination := path.Join(destinationRoot, "tree-copy")
	if err := source.CopyRemoteDirectoryTo(ctx, destination, path.Join(sourceRoot, "tree"), treeDestination, false, nil); err != nil {
		t.Fatalf("copy remote directory to second server: %v", err)
	}
	localTree := filepath.Join(t.TempDir(), "tree")
	if err := destination.DownloadDirectory(ctx, treeDestination, localTree, false, nil); err != nil {
		t.Fatalf("verify copied remote directory: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(localTree, "nested", "payload.txt")); err != nil || string(content) != "remote to remote\n" {
		t.Fatalf("copied remote directory file content = %q, %v", content, err)
	}
}

func TestResumableTransfersPreserveAndContinuePartialFiles(t *testing.T) {
	ctx := context.Background()
	payload := make([]byte, 256*1024+37)
	for index := range payload {
		payload[index] = byte((index*31 + index/17) % 251)
	}

	localSource := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(localSource, payload, 0o600); err != nil {
		t.Fatalf("write local source: %v", err)
	}
	uploadRoot := t.TempDir()
	uploadClient := newTestSFTPClient(t, uploadRoot)
	uploadRootPath, err := uploadClient.WorkingDirectory()
	if err != nil {
		t.Fatalf("get upload working directory: %v", err)
	}
	remoteUpload := path.Join(uploadRootPath, "payload.bin")
	cancelledUpload, cancelUpload := context.WithCancel(ctx)
	uploadErr := uploadClient.Upload(cancelledUpload, localSource, remoteUpload, false, func(done, _ int64) {
		if done >= 64*1024 {
			cancelUpload()
		}
	})
	cancelUpload()
	if !errors.Is(uploadErr, context.Canceled) {
		t.Fatalf("interrupted upload error = %v, want context.Canceled", uploadErr)
	}
	uploadPartial := path.Join(uploadRootPath, ".payload.bin.upload.part")
	partialInfo, err := uploadClient.client.Lstat(uploadPartial)
	if err != nil || partialInfo.Size() < 64*1024 || partialInfo.Size() >= int64(len(payload)) {
		t.Fatalf("upload partial = %#v, %v; want a non-empty incomplete file", partialInfo, err)
	}
	var firstUploadProgress int64 = -1
	if err := uploadClient.Upload(ctx, localSource, remoteUpload, false, func(done, _ int64) {
		if firstUploadProgress < 0 {
			firstUploadProgress = done
		}
	}); err != nil {
		t.Fatalf("resume upload: %v", err)
	}
	if firstUploadProgress != partialInfo.Size() {
		t.Fatalf("upload resumed at %d bytes, want %d", firstUploadProgress, partialInfo.Size())
	}
	remoteFile, err := uploadClient.client.Open(remoteUpload)
	if err != nil {
		t.Fatalf("open uploaded remote file: %v", err)
	}
	gotUpload, err := io.ReadAll(remoteFile)
	_ = remoteFile.Close()
	if err != nil || !bytes.Equal(gotUpload, payload) {
		t.Fatalf("resumed upload content mismatch: read error %v", err)
	}

	localDownload := filepath.Join(t.TempDir(), "download.bin")
	cancelledDownload, cancelDownload := context.WithCancel(ctx)
	downloadErr := uploadClient.Download(cancelledDownload, remoteUpload, localDownload, false, func(done, _ int64) {
		if done >= 64*1024 {
			cancelDownload()
		}
	})
	cancelDownload()
	if !errors.Is(downloadErr, context.Canceled) {
		t.Fatalf("interrupted download error = %v, want context.Canceled", downloadErr)
	}
	downloadPartial := localPartialPath(localDownload, "download")
	localPartialInfo, err := os.Stat(downloadPartial)
	if err != nil || localPartialInfo.Size() < 64*1024 || localPartialInfo.Size() >= int64(len(payload)) {
		t.Fatalf("download partial = %#v, %v; want a non-empty incomplete file", localPartialInfo, err)
	}
	var firstDownloadProgress int64 = -1
	if err := uploadClient.Download(ctx, remoteUpload, localDownload, false, func(done, _ int64) {
		if firstDownloadProgress < 0 {
			firstDownloadProgress = done
		}
	}); err != nil {
		t.Fatalf("resume download: %v", err)
	}
	if firstDownloadProgress != localPartialInfo.Size() {
		t.Fatalf("download resumed at %d bytes, want %d", firstDownloadProgress, localPartialInfo.Size())
	}
	gotDownload, err := os.ReadFile(localDownload)
	if err != nil || !bytes.Equal(gotDownload, payload) {
		t.Fatalf("resumed download content mismatch: read error %v", err)
	}

	destinationRoot := t.TempDir()
	destinationClient := newTestSFTPClient(t, destinationRoot)
	destinationRootPath, err := destinationClient.WorkingDirectory()
	if err != nil {
		t.Fatalf("get remote destination root: %v", err)
	}
	remoteCopy := path.Join(destinationRootPath, "copy.bin")
	cancelledCopy, cancelCopy := context.WithCancel(ctx)
	copyErr := uploadClient.CopyRemoteFileTo(cancelledCopy, destinationClient, remoteUpload, remoteCopy, false, func(done, _ int64) {
		if done >= 64*1024 {
			cancelCopy()
		}
	})
	cancelCopy()
	if !errors.Is(copyErr, context.Canceled) {
		t.Fatalf("interrupted remote copy error = %v, want context.Canceled", copyErr)
	}
	copyPartial := path.Join(destinationRootPath, ".copy.bin.copy.part")
	copyPartialInfo, err := destinationClient.client.Lstat(copyPartial)
	if err != nil || copyPartialInfo.Size() < 64*1024 || copyPartialInfo.Size() >= int64(len(payload)) {
		t.Fatalf("remote copy partial = %#v, %v; want a non-empty incomplete file", copyPartialInfo, err)
	}
	var firstCopyProgress int64 = -1
	if err := uploadClient.CopyRemoteFileTo(ctx, destinationClient, remoteUpload, remoteCopy, false, func(done, _ int64) {
		if firstCopyProgress < 0 {
			firstCopyProgress = done
		}
	}); err != nil {
		t.Fatalf("resume remote copy: %v", err)
	}
	if firstCopyProgress != copyPartialInfo.Size() {
		t.Fatalf("remote copy resumed at %d bytes, want %d", firstCopyProgress, copyPartialInfo.Size())
	}
	remoteCopyFile, err := destinationClient.client.Open(remoteCopy)
	if err != nil {
		t.Fatalf("open copied remote file: %v", err)
	}
	gotCopy, err := io.ReadAll(remoteCopyFile)
	_ = remoteCopyFile.Close()
	if err != nil || !bytes.Equal(gotCopy, payload) {
		t.Fatalf("resumed remote copy content mismatch: read error %v", err)
	}
}

func TestResumableUploadResetsMismatchedPartial(t *testing.T) {
	client := newTestSFTPClient(t, t.TempDir())
	root, err := client.WorkingDirectory()
	if err != nil {
		t.Fatalf("get remote working directory: %v", err)
	}
	localSource := filepath.Join(t.TempDir(), "source.bin")
	content := []byte("the correct source payload")
	if err := os.WriteFile(localSource, content, 0o600); err != nil {
		t.Fatalf("write local source: %v", err)
	}
	partialPath := path.Join(root, ".target.bin.upload.part")
	partial, err := client.client.Create(partialPath)
	if err != nil {
		t.Fatalf("create mismatched partial: %v", err)
	}
	if _, err := partial.Write([]byte("a stale source prefix")); err != nil {
		_ = partial.Close()
		t.Fatalf("write mismatched partial: %v", err)
	}
	_ = partial.Close()
	firstProgress := int64(-1)
	if err := client.Upload(context.Background(), localSource, path.Join(root, "target.bin"), false, func(done, _ int64) {
		if firstProgress < 0 {
			firstProgress = done
		}
	}); err != nil {
		t.Fatalf("upload over mismatched partial: %v", err)
	}
	if firstProgress != 0 {
		t.Fatalf("mismatched partial resumed at %d bytes, want restart at zero", firstProgress)
	}
	remote, err := client.client.Open(path.Join(root, "target.bin"))
	if err != nil {
		t.Fatalf("open uploaded file: %v", err)
	}
	got, err := io.ReadAll(remote)
	_ = remote.Close()
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("uploaded content = %q, %v; want %q", got, err, content)
	}
}

func TestConcurrentUploadsShareOneSFTPClient(t *testing.T) {
	client := newTestSFTPClient(t, t.TempDir())
	root, err := client.WorkingDirectory()
	if err != nil {
		t.Fatalf("get remote working directory: %v", err)
	}
	const fileCount = 6
	localRoot := t.TempDir()
	contents := make([][]byte, fileCount)
	paths := make([]string, fileCount)
	for index := 0; index < fileCount; index++ {
		contents[index] = bytes.Repeat([]byte{byte(index + 1)}, 128*1024+index)
		localPath := filepath.Join(localRoot, string(rune('a'+index))+".bin")
		if err := os.WriteFile(localPath, contents[index], 0o600); err != nil {
			t.Fatalf("write source %d: %v", index, err)
		}
		paths[index] = path.Join(root, string(rune('a'+index))+".bin")
	}
	var wait sync.WaitGroup
	errCh := make(chan error, fileCount)
	for index := 0; index < fileCount; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			localPath := filepath.Join(localRoot, string(rune('a'+index))+".bin")
			errCh <- client.Upload(context.Background(), localPath, paths[index], false, nil)
		}(index)
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent upload: %v", err)
		}
	}
	for index, remotePath := range paths {
		remote, err := client.client.Open(remotePath)
		if err != nil {
			t.Fatalf("open remote file %d: %v", index, err)
		}
		got, readErr := io.ReadAll(remote)
		_ = remote.Close()
		if readErr != nil || !bytes.Equal(got, contents[index]) {
			t.Fatalf("concurrent upload %d content mismatch: %v", index, readErr)
		}
	}
}

func TestRemoveTreeDoesNotFollowRemoteSymlinks(t *testing.T) {
	remoteRoot := t.TempDir()
	externalRoot := t.TempDir()
	client := newTestSFTPClient(t, remoteRoot)
	root, err := client.WorkingDirectory()
	if err != nil {
		t.Fatalf("get remote working directory: %v", err)
	}
	tree := path.Join(root, "delete-tree")
	nested := filepath.Join(remoteRoot, "delete-tree", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create remote test tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatalf("write nested remote file: %v", err)
	}
	externalFile := filepath.Join(externalRoot, "keep.txt")
	if err := os.WriteFile(externalFile, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	if err := os.Symlink(externalRoot, filepath.Join(remoteRoot, "delete-tree", "external-link")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if err := client.RemoveTree(tree); err != nil {
		t.Fatalf("remove remote tree: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(remoteRoot, "delete-tree")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remote tree still exists or could not be inspected: %v", err)
	}
	if content, err := os.ReadFile(externalFile); err != nil || string(content) != "keep" {
		t.Fatalf("recursive delete followed a symlink: content=%q err=%v", content, err)
	}
	if err := client.RemoveTree("/"); err == nil {
		t.Fatal("recursive delete accepted the remote filesystem root")
	}
}

func newTestSFTPClient(t *testing.T, workingDirectory string) *Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for test SSH server: %v", err)
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test SSH host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("create test SSH host signer: %v", err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveTestSSHConnection(connection, serverConfig, workingDirectory)
		}
	}()

	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		t.Fatalf("connect test SSH client: %v", err)
	}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, listener.Addr().String(), &ssh.ClientConfig{
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		_ = listener.Close()
		t.Fatalf("negotiate test SSH connection: %v", err)
	}
	sshClient := ssh.NewClient(clientConnection, channels, requests)
	client, err := New(sshClient)
	if err != nil {
		_ = sshClient.Close()
		_ = listener.Close()
		t.Fatalf("open test SFTP subsystem: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = sshClient.Close()
		_ = listener.Close()
	})
	return client
}

func serveTestSSHConnection(connection net.Conn, config *ssh.ServerConfig, workingDirectory string) {
	_, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		_ = connection.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported channel")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for request := range channelRequests {
				if request.Type != "subsystem" {
					_ = request.Reply(false, nil)
					continue
				}
				var payload struct{ Name string }
				if err := ssh.Unmarshal(request.Payload, &payload); err != nil || payload.Name != "sftp" {
					_ = request.Reply(false, nil)
					continue
				}
				if err := request.Reply(true, nil); err != nil {
					return
				}
				server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(workingDirectory))
				if err == nil {
					_ = server.Serve()
					_ = server.Close()
				}
				return
			}
		}()
	}
	_ = connection.Close()
}
