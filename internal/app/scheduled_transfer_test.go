package app

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"tui-ssh-workbench/internal/infra/ftpclient"
	"tui-ssh-workbench/internal/infra/remotefs"
	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/logic/scheduledtransfer"
	"tui-ssh-workbench/internal/model"
	"tui-ssh-workbench/internal/testsupport/ftpserver"
)

type fakeScheduledTransferNotifier struct {
	title string
	body  string
	err   error
}

func (notifier *fakeScheduledTransferNotifier) Notify(title, body string) error {
	notifier.title, notifier.body = title, body
	return notifier.err
}

func TestScheduledTransferEventHandlerNotifiesWithoutAffectingCallback(t *testing.T) {
	notifier := &fakeScheduledTransferNotifier{err: fmt.Errorf("desktop service unavailable")}
	var callbackEvent scheduledtransfer.Event
	callbackCalls := 0
	handler := scheduledTransferEventHandler(true, notifier, func(event scheduledtransfer.Event) {
		callbackCalls++
		callbackEvent = event
	})
	event := scheduledtransfer.Event{
		Phase: "finished", Status: model.ScheduledTransferFailed,
		Transfer: model.ScheduledTransfer{Name: "夜间备份"},
	}
	handler(event)
	if notifier.title != "计划传输未完成" || notifier.body != "计划任务：夜间备份" {
		t.Fatalf("desktop notification = %q / %q", notifier.title, notifier.body)
	}
	if callbackCalls != 1 || callbackEvent.Phase != event.Phase || callbackEvent.Transfer.Name != event.Transfer.Name {
		t.Fatalf("event callback calls=%d event=%#v", callbackCalls, callbackEvent)
	}
	notifier.title, notifier.body = "", ""
	handler(scheduledtransfer.Event{Phase: "started", Transfer: event.Transfer})
	if notifier.title != "" || notifier.body != "" {
		t.Fatalf("started event should not notify: %q / %q", notifier.title, notifier.body)
	}
}

func TestScheduledTransferEventHandlerRespectsOptIn(t *testing.T) {
	notifier := &fakeScheduledTransferNotifier{}
	handler := scheduledTransferEventHandler(false, notifier, nil)
	handler(scheduledtransfer.Event{Phase: "finished", Status: model.ScheduledTransferSucceeded})
	if notifier.title != "" || notifier.body != "" {
		t.Fatalf("disabled notification was sent: %q / %q", notifier.title, notifier.body)
	}
}

func TestRunScheduledTransfersHonorsCancellationBeforeStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunScheduledTransfers(ctx, filepath.Join(t.TempDir(), "workbench.db"), model.DefaultConfig(), nil); err != nil {
		t.Fatalf("run canceled scheduler: %v", err)
	}
}

func TestRunScheduledTransfersStopsCleanlyOnContextCancellation(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workbench.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunScheduledTransfers(ctx, databasePath, model.DefaultConfig(), nil)
	}()
	waitForScheduledDatabase(t, databasePath)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run scheduled-transfer service: %v", err)
	}
	if _, err := os.Stat(databasePath); err != nil {
		t.Fatalf("scheduler did not initialize database: %v", err)
	}
}

func TestWorkbenchRunnerUsesApplicationStoreAndStopsOnCancellation(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindFTP, Name: "runner lifecycle", IP: "127.0.0.1", Port: 21, User: "anonymous",
	})
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := store.SaveScheduledTransfer(ctx, model.ScheduledTransfer{
		Name: "runner lifecycle check", HostId: host.Id, Direction: model.ScheduledTransferUpload,
		LocalPath: filepath.Join(t.TempDir(), "missing-source"), RemotePath: "/upload/missing-source",
		CronSpec: "@every 1h", TimeZone: "UTC",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RequestScheduledTransferNow(ctx, transfer.Id); err != nil {
		t.Fatal(err)
	}

	runnerContext, cancelRunner := context.WithCancel(context.Background())
	runnerDone := startScheduledTransferRunner(runnerContext, store, model.DefaultConfig(), nil)
	defer func() {
		cancelRunner()
		if err := <-runnerDone; err != nil {
			t.Errorf("stop workbench runner: %v", err)
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		updated, err := store.GetScheduledTransfer(ctx, transfer.Id)
		if err != nil {
			t.Fatal(err)
		}
		if updated.LastStatus == model.ScheduledTransferFailed {
			if !strings.Contains(updated.LastError, "读取本地源路径失败") {
				t.Fatalf("runner used a different or unexpected execution path: status=%q error=%q", updated.LastStatus, updated.LastError)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("workbench runner did not execute the immediate task against the shared in-memory database")
}

func waitForScheduledDatabase(t *testing.T, databasePath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		database, err := sql.Open("sqlite", databasePath)
		if err == nil {
			var version int
			err = database.QueryRow(`PRAGMA user_version`).Scan(&version)
			_ = database.Close()
			if err == nil && version >= 12 {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("scheduled runner did not finish initializing the database")
}

type listingRemote struct {
	remotefs.Client
	entries []remotefs.Entry
	err     error
}

func (client listingRemote) List(string) ([]remotefs.Entry, error) {
	return client.entries, client.err
}

func TestScheduledRemoteEntry(t *testing.T) {
	client := listingRemote{entries: []remotefs.Entry{
		{Name: "backup.tar", Size: 42},
		{Name: "logs", IsDir: true},
		{Name: "latest", IsSymlink: true},
	}}

	directory, err := scheduledRemoteEntry(client, "/")
	if err != nil || !directory.IsDir {
		t.Fatalf("root entry = %#v, err=%v; want directory", directory, err)
	}
	file, err := scheduledRemoteEntry(client, "/incoming/backup.tar")
	if err != nil || file.Name != "backup.tar" || file.IsDir {
		t.Fatalf("file entry = %#v, err=%v", file, err)
	}
	link, err := scheduledRemoteEntry(client, "/incoming/latest")
	if err != nil || !link.IsSymlink {
		t.Fatalf("link entry = %#v, err=%v", link, err)
	}
	if _, err := scheduledRemoteEntry(client, "/incoming/missing"); err == nil {
		t.Fatal("missing remote entry did not return an error")
	}
}

func TestScheduledTransferExecutorUploadsAndDownloadsOverSFTP(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	remoteRoot := t.TempDir()
	remoteIncoming := filepath.Join(remoteRoot, "incoming")
	if err := os.Mkdir(remoteIncoming, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, signer := startScheduledSFTPTestServer(t, remoteRoot)
	port := listener.Addr().(*net.TCPAddr).Port
	host := model.Host{Kind: model.HostKindSSH, Name: "local sftp", IP: "127.0.0.1", Port: port, User: "tester"}
	if err := sshclient.TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust test host: %v", err)
	}

	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "scheduled-transfer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err = store.CreateHost(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	executor := scheduledTransferExecutor{storage: store, sshOptions: sshclient.DefaultConnectOptions()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	localRoot := t.TempDir()
	source := filepath.Join(localRoot, "source.bin")
	payload := []byte("scheduled SFTP transfer verification\n")
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	remotePath := "incoming/source.bin"
	if err := executor.Execute(ctx, model.ScheduledTransfer{
		HostId: host.Id, Direction: model.ScheduledTransferUpload,
		LocalPath: source, RemotePath: remotePath,
	}); err != nil {
		t.Fatalf("scheduled upload: %v", err)
	}
	remotePayload, err := os.ReadFile(filepath.Join(remoteIncoming, "source.bin"))
	if err != nil || string(remotePayload) != string(payload) {
		t.Fatalf("uploaded file = %q, err=%v; want %q", remotePayload, err, payload)
	}

	destination := filepath.Join(localRoot, "downloaded.bin")
	if err := executor.Execute(ctx, model.ScheduledTransfer{
		HostId: host.Id, Direction: model.ScheduledTransferDownload,
		LocalPath: destination, RemotePath: remotePath,
	}); err != nil {
		t.Fatalf("scheduled download: %v", err)
	}
	downloadedPayload, err := os.ReadFile(destination)
	if err != nil || string(downloadedPayload) != string(payload) {
		t.Fatalf("downloaded file = %q, err=%v; want %q", downloadedPayload, err, payload)
	}

	sourceDirectory := filepath.Join(localRoot, "bundle")
	if err := os.MkdirAll(filepath.Join(sourceDirectory, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	directoryPayload := []byte("recursive scheduled transfer\n")
	if err := os.WriteFile(filepath.Join(sourceDirectory, "child", "note.txt"), directoryPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := executor.Execute(ctx, model.ScheduledTransfer{
		HostId: host.Id, Direction: model.ScheduledTransferUpload,
		LocalPath: sourceDirectory, RemotePath: "incoming/bundle",
	}); err != nil {
		t.Fatalf("scheduled directory upload: %v", err)
	}
	remoteDirectoryPayload, err := os.ReadFile(filepath.Join(remoteIncoming, "bundle", "child", "note.txt"))
	if err != nil || string(remoteDirectoryPayload) != string(directoryPayload) {
		t.Fatalf("uploaded directory file = %q, err=%v; want %q", remoteDirectoryPayload, err, directoryPayload)
	}
	downloadDirectory := filepath.Join(localRoot, "downloaded-bundle")
	if err := executor.Execute(ctx, model.ScheduledTransfer{
		HostId: host.Id, Direction: model.ScheduledTransferDownload,
		LocalPath: downloadDirectory, RemotePath: "incoming/bundle",
	}); err != nil {
		t.Fatalf("scheduled directory download: %v", err)
	}
	downloadedDirectoryPayload, err := os.ReadFile(filepath.Join(downloadDirectory, "child", "note.txt"))
	if err != nil || string(downloadedDirectoryPayload) != string(directoryPayload) {
		t.Fatalf("downloaded directory file = %q, err=%v; want %q", downloadedDirectoryPayload, err, directoryPayload)
	}
}

func TestScheduledTransferExecutorUsesInheritedGroupAuthProfileUser(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	remoteRoot := t.TempDir()
	listener, signer := startScheduledSFTPTestServerForUser(t, remoteRoot, "profile-user")
	port := listener.Addr().(*net.TCPAddr).Port
	host := model.Host{Kind: model.HostKindSSH, Name: "profile transfer", IP: "127.0.0.1", Port: port, User: "host-user"}
	if err := sshclient.TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust test host: %v", err)
	}

	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "profile-transfer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	profile, err := store.CreateAuthProfile(context.Background(), model.AuthProfile{Name: "profile user", User: "profile-user"})
	if err != nil {
		t.Fatalf("create auth profile: %v", err)
	}
	group, err := store.CreateGroup(context.Background(), model.Group{Name: "Engineering", AuthProfileID: profile.Id})
	if err != nil {
		t.Fatalf("create group with inherited profile: %v", err)
	}
	host.GroupId = group.Id
	host, err = store.CreateHost(context.Background(), host)
	if err != nil {
		t.Fatalf("create profiled host: %v", err)
	}

	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("profile user was applied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := scheduledTransferExecutor{storage: store, sshOptions: sshclient.DefaultConnectOptions()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := executor.Execute(ctx, model.ScheduledTransfer{
		HostId: host.Id, Direction: model.ScheduledTransferUpload,
		LocalPath: source, RemotePath: "profile.txt",
	}); err != nil {
		t.Fatalf("scheduled transfer with auth profile: %v", err)
	}
	if payload, err := os.ReadFile(filepath.Join(remoteRoot, "profile.txt")); err != nil || string(payload) != "profile user was applied\n" {
		t.Fatalf("profiled upload = %q, err=%v", payload, err)
	}
}

func TestScheduledTransferExecutorUsesSCPForSavedSCPHost(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	remoteRoot := t.TempDir()
	remoteIncoming := filepath.Join(remoteRoot, "incoming")
	if err := os.Mkdir(remoteIncoming, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, signer := startScheduledSFTPTestServer(t, remoteRoot)
	port := listener.Addr().(*net.TCPAddr).Port
	host := model.Host{
		Kind: model.HostKindSSH, FileProtocol: model.SSHFileProtocolSCP,
		Name: "local scp", IP: "127.0.0.1", Port: port, User: "tester",
	}
	if err := sshclient.TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust test host: %v", err)
	}
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "scheduled-scp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err = store.CreateHost(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	executor := scheduledTransferExecutor{storage: store, sshOptions: sshclient.DefaultConnectOptions()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	localRoot := t.TempDir()
	source := filepath.Join(localRoot, "upload.bin")
	payload := []byte("scheduled SCP file transfer verification\n")
	if err := os.WriteFile(source, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	remotePath := "incoming/upload.bin"
	if err := executor.Execute(ctx, model.ScheduledTransfer{
		HostId: host.Id, Direction: model.ScheduledTransferUpload,
		LocalPath: source, RemotePath: remotePath,
	}); err != nil {
		t.Fatalf("scheduled SCP upload: %v", err)
	}
	remoteTarget := filepath.Join(remoteIncoming, "upload.bin")
	remotePayload, err := os.ReadFile(remoteTarget)
	if err != nil || string(remotePayload) != string(payload) {
		t.Fatalf("uploaded SCP file = %q, err=%v", remotePayload, err)
	}

	destination := filepath.Join(localRoot, "download.bin")
	if err := executor.Execute(ctx, model.ScheduledTransfer{
		HostId: host.Id, Direction: model.ScheduledTransferDownload,
		LocalPath: destination, RemotePath: remotePath,
	}); err != nil {
		t.Fatalf("scheduled SCP download: %v", err)
	}
	downloaded, err := os.ReadFile(destination)
	if err != nil || string(downloaded) != string(payload) {
		t.Fatalf("downloaded SCP file = %q, err=%v", downloaded, err)
	}

	if err := os.WriteFile(source, []byte("must not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := executor.Execute(ctx, model.ScheduledTransfer{
		HostId: host.Id, Direction: model.ScheduledTransferUpload,
		LocalPath: source, RemotePath: remotePath, Overwrite: false,
	}); err == nil {
		t.Fatal("SCP upload replaced an existing remote file without overwrite permission")
	}
	remotePayload, err = os.ReadFile(remoteTarget)
	if err != nil || string(remotePayload) != string(payload) {
		t.Fatalf("existing remote file changed after rejected upload: %q, err=%v", remotePayload, err)
	}
}

func TestScheduledTransferExecutorTransfersSCPDirectories(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	remoteRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(remoteRoot, "incoming"), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, signer := startScheduledSFTPTestServer(t, remoteRoot)
	port := listener.Addr().(*net.TCPAddr).Port
	host := model.Host{
		Kind: model.HostKindSSH, FileProtocol: model.SSHFileProtocolSCP,
		Name: "local scp directories", IP: "127.0.0.1", Port: port, User: "tester",
	}
	if err := sshclient.TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust test host: %v", err)
	}
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "scheduled-scp-directory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err = store.CreateHost(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	executor := scheduledTransferExecutor{storage: store, sshOptions: sshclient.DefaultConnectOptions()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	localParent := t.TempDir()
	sourceRoot := filepath.Join(localParent, "project")
	if err := os.MkdirAll(filepath.Join(sourceRoot, "nested", "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(sourceRoot, "nested", "payload.txt")
	payload := []byte("recursive SCP directory transfer\n")
	if err := os.WriteFile(payloadPath, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	remotePath := "incoming/project"
	if err := executor.Execute(ctx, model.ScheduledTransfer{
		HostId: host.Id, Direction: model.ScheduledTransferUpload,
		LocalPath: sourceRoot, RemotePath: remotePath,
	}); err != nil {
		t.Fatalf("scheduled SCP directory upload: %v", err)
	}
	remoteFile := filepath.Join(remoteRoot, "incoming", "project", "nested", "payload.txt")
	remotePayload, err := os.ReadFile(remoteFile)
	if err != nil || string(remotePayload) != string(payload) {
		t.Fatalf("uploaded directory file = %q, err=%v", remotePayload, err)
	}
	if _, err := os.Stat(filepath.Join(remoteRoot, "incoming", "project", "nested", "empty")); err != nil {
		t.Fatalf("uploaded empty directory missing: %v", err)
	}
	remoteInfo, err := os.Stat(remoteFile)
	if err != nil || remoteInfo.Mode().Perm() != 0o640 {
		t.Fatalf("uploaded file mode = %v, err=%v; want 0640", remoteInfo, err)
	}
	localInfo, err := os.Stat(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if !remoteInfo.ModTime().Equal(localInfo.ModTime().Truncate(time.Second)) {
		t.Fatalf("uploaded file modification time = %s, want %s", remoteInfo.ModTime(), localInfo.ModTime().Truncate(time.Second))
	}

	destination := filepath.Join(localParent, "downloaded-project")
	if err := executor.Execute(ctx, model.ScheduledTransfer{
		HostId: host.Id, Direction: model.ScheduledTransferDownload,
		LocalPath: destination, RemotePath: remotePath,
	}); err != nil {
		t.Fatalf("scheduled SCP directory download: %v", err)
	}
	downloaded, err := os.ReadFile(filepath.Join(destination, "nested", "payload.txt"))
	if err != nil || string(downloaded) != string(payload) {
		t.Fatalf("downloaded directory file = %q, err=%v", downloaded, err)
	}
	if _, err := os.Stat(filepath.Join(destination, "nested", "empty")); err != nil {
		t.Fatalf("downloaded empty directory missing: %v", err)
	}
	downloadedInfo, err := os.Stat(filepath.Join(destination, "nested", "payload.txt"))
	if err != nil || downloadedInfo.Mode().Perm() != 0o640 {
		t.Fatalf("downloaded file mode = %v, err=%v; want 0640", downloadedInfo, err)
	}
	if !downloadedInfo.ModTime().Equal(remoteInfo.ModTime()) {
		t.Fatalf("downloaded file modification time = %s, want %s", downloadedInfo.ModTime(), remoteInfo.ModTime())
	}
	secretPath := filepath.Join(remoteRoot, "incoming", "outside.txt")
	if err := os.WriteFile(secretPath, []byte("outside directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(remoteRoot, "incoming", "project", "nested", "outside-link")
	if err := os.Symlink(secretPath, linkPath); err == nil {
		if err := executor.Execute(ctx, model.ScheduledTransfer{
			HostId: host.Id, Direction: model.ScheduledTransferDownload,
			LocalPath: filepath.Join(localParent, "symlink-download"), RemotePath: remotePath,
		}); err == nil || !strings.Contains(err.Error(), "符号链接") {
			t.Fatalf("SCP download with a remote symlink returned %v; want explicit rejection", err)
		}
	}

	if err := executor.Execute(ctx, model.ScheduledTransfer{
		HostId: host.Id, Direction: model.ScheduledTransferUpload,
		LocalPath: sourceRoot, RemotePath: remotePath,
	}); err == nil {
		t.Fatal("SCP directory upload replaced an existing target without overwrite permission")
	}
}

func TestScheduledTransferRunnerEndToEndUploadsDuePlan(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	remoteRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(remoteRoot, "incoming"), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, signer := startScheduledSFTPTestServer(t, remoteRoot)
	port := listener.Addr().(*net.TCPAddr).Port
	host := model.Host{Kind: model.HostKindSSH, Name: "scheduled test", IP: "127.0.0.1", Port: port, User: "tester"}
	if err := sshclient.TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust test host: %v", err)
	}

	databasePath := filepath.Join(t.TempDir(), "runner.db")
	store, err := sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	host, err = store.CreateHost(context.Background(), host)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	localPath := filepath.Join(t.TempDir(), "scheduled.txt")
	want := []byte("runner executed scheduled SFTP upload\n")
	if err := os.WriteFile(localPath, want, 0o600); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.SaveScheduledTransfer(context.Background(), model.ScheduledTransfer{
		Name: "due upload", HostId: host.Id, Direction: model.ScheduledTransferUpload,
		LocalPath: localPath, RemotePath: "incoming/scheduled.txt", CronSpec: "* * * * *",
		TimeZone: "UTC", Enabled: true, NextRunAt: time.Now().Add(-time.Minute),
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var cancelOnce sync.Once
	var result scheduledtransfer.Event
	if err := RunScheduledTransfers(ctx, databasePath, model.DefaultConfig(), func(event scheduledtransfer.Event) {
		if event.Phase == "finished" {
			result = event
			cancelOnce.Do(cancel)
		}
	}); err != nil {
		t.Fatalf("run scheduled-transfer service: %v", err)
	}
	if result.Phase != "finished" || result.Status != model.ScheduledTransferSucceeded || result.Err != nil {
		t.Fatalf("scheduled runner result = %#v", result)
	}
	got, err := os.ReadFile(filepath.Join(remoteRoot, "incoming", "scheduled.txt"))
	if err != nil || string(got) != string(want) {
		t.Fatalf("runner uploaded %q, err=%v; want %q", got, err, want)
	}
}

func TestScheduledTransferRunnerEndToEndFTPAndFTPS(t *testing.T) {
	for _, mode := range []string{"off", "explicit", "implicit"} {
		for _, direction := range []model.ScheduledTransferDirection{model.ScheduledTransferUpload, model.ScheduledTransferDownload} {
			name := mode + "/" + string(direction)
			t.Run(name, func(t *testing.T) {
				certificate, roots := newScheduledFTPTestCertificate(t)
				remotePayload := []byte("scheduled FTP/FTPS download\n")
				initialFiles := map[string][]byte{}
				if direction == model.ScheduledTransferDownload {
					initialFiles["/source.txt"] = remotePayload
				}
				server, err := ftpserver.Start(mode, certificate, initialFiles)
				if err != nil {
					t.Fatalf("start FTP test server: %v", err)
				}
				t.Cleanup(func() {
					if err := server.Close(); err != nil {
						t.Error(err)
					}
				})
				_, portText, err := net.SplitHostPort(server.Address)
				if err != nil {
					t.Fatal(err)
				}
				var port int
				if _, err := fmt.Sscan(portText, &port); err != nil {
					t.Fatal(err)
				}

				host := model.Host{
					Kind: model.HostKindFTP, Name: "scheduled " + mode, IP: "127.0.0.1",
					Port: port, User: "anonymous", TLSMode: mode,
				}
				databasePath := filepath.Join(t.TempDir(), "scheduled-ftp.db")
				store, err := sqlite.Open(context.Background(), databasePath)
				if err != nil {
					t.Fatal(err)
				}
				host, err = store.CreateHost(context.Background(), host)
				if err != nil {
					_ = store.Close()
					t.Fatal(err)
				}
				localPath := filepath.Join(t.TempDir(), "scheduled.txt")
				want := []byte("scheduled FTP/FTPS upload\n")
				if direction == model.ScheduledTransferUpload {
					if err := os.WriteFile(localPath, want, 0o600); err != nil {
						_ = store.Close()
						t.Fatal(err)
					}
				}
				remotePath := "/uploaded.txt"
				if direction == model.ScheduledTransferDownload {
					remotePath = "/source.txt"
					want = remotePayload
				}
				plan, err := store.SaveScheduledTransfer(context.Background(), model.ScheduledTransfer{
					Name: "FTP integration", HostId: host.Id, Direction: direction,
					LocalPath: localPath, RemotePath: remotePath, CronSpec: "* * * * *",
					TimeZone: "UTC", Enabled: true, NextRunAt: time.Now().Add(-time.Minute),
				})
				if err != nil {
					_ = store.Close()
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}

				ftpDial := func(ctx context.Context, config ftpclient.Config) (*ftpclient.Client, error) {
					config.ServerName = "localhost"
					config.RootCAs = roots
					config.Timeout = 3 * time.Second
					return ftpclient.Dial(ctx, config)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				var cancelOnce sync.Once
				var result scheduledtransfer.Event
				runErr := runScheduledTransfersWithFTP(ctx, databasePath, model.DefaultConfig(), ftpDial, func(event scheduledtransfer.Event) {
					if event.Phase == "finished" {
						result = event
						cancelOnce.Do(cancel)
					}
				})
				if runErr != nil {
					t.Fatalf("run scheduled %s task: %v", name, runErr)
				}
				if result.Transfer.Id != plan.Id || result.Status != model.ScheduledTransferSucceeded || result.Err != nil {
					t.Fatalf("scheduled %s result = %#v", name, result)
				}
				if direction == model.ScheduledTransferUpload {
					select {
					case uploaded := <-server.Uploads:
						if uploaded.Path != remotePath || string(uploaded.Content) != string(want) {
							t.Fatalf("server received upload path=%q content=%q; want %q %q", uploaded.Path, uploaded.Content, remotePath, want)
						}
					case <-time.After(2 * time.Second):
						t.Fatal("FTP server did not observe the scheduled upload")
					}
				} else if got, err := os.ReadFile(localPath); err != nil || string(got) != string(want) {
					t.Fatalf("scheduled download = %q, err=%v; want %q", got, err, want)
				}
			})
		}
	}
}

func newScheduledFTPTestCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	root, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	return certificate, roots
}

func startScheduledSFTPTestServer(t *testing.T, workingDirectory string) (net.Listener, ssh.Signer) {
	return startScheduledSFTPTestServerForUser(t, workingDirectory, "")
}

func startScheduledSFTPTestServerForUser(t *testing.T, workingDirectory, expectedUser string) (net.Listener, ssh.Signer) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("create test host signer: %v", err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	if expectedUser != "" {
		serverConfig.NoClientAuthCallback = func(connection ssh.ConnMetadata) (*ssh.Permissions, error) {
			if connection.User() != expectedUser {
				return nil, fmt.Errorf("unexpected SSH user %q", connection.User())
			}
			return &ssh.Permissions{}, nil
		}
	}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for test SSH server: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveScheduledSFTPTestConnection(connection, serverConfig, workingDirectory)
		}
	}()
	return listener, signer
}

func serveScheduledSFTPTestConnection(connection net.Conn, config *ssh.ServerConfig, workingDirectory string) {
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
		channel, channelRequests, acceptErr := newChannel.Accept()
		if acceptErr != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for request := range channelRequests {
				if request.Type == "exec" {
					var payload struct{ Command string }
					if err := ssh.Unmarshal(request.Payload, &payload); err != nil || payload.Command == "" {
						_ = request.Reply(false, nil)
						return
					}
					_ = request.Reply(true, nil)
					commandErr := serveScheduledSCPCommand(payload.Command, channel, workingDirectory)
					status := uint32(0)
					if commandErr != nil {
						status = 1
						_, _ = channel.Stderr().Write([]byte(commandErr.Error()))
					}
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
					return
				}
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

func serveScheduledSCPCommand(command string, channel ssh.Channel, root string) error {
	if strings.Contains(command, "printf 'missing\\n'") {
		partial, ok := shellAssignment(command, "partial")
		if !ok {
			return fmt.Errorf("test SCP inspect command omitted partial path")
		}
		localPartial, err := testRemotePath(root, partial)
		if err != nil {
			return err
		}
		info, err := os.Lstat(localPartial)
		if errors.Is(err, os.ErrNotExist) {
			_, err = io.WriteString(channel, "missing\n")
			return err
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			_, err = io.WriteString(channel, "blocked\n")
			return err
		}
		if !strings.Contains(command, "sha256sum") && !strings.Contains(command, "shasum") {
			_, err = fmt.Fprintf(channel, "unavailable %d\n", info.Size())
			return err
		}
		content, err := os.ReadFile(localPartial)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		_, err = fmt.Fprintf(channel, "file %d %s\n", len(content), hex.EncodeToString(digest[:]))
		return err
	}
	if strings.Contains(command, "printf 'directory %s %s\\n'") {
		source, ok := shellAssignment(command, "source")
		if !ok {
			return fmt.Errorf("test SCP directory metadata command omitted source path")
		}
		localSource, err := testRemotePath(root, source)
		if err != nil {
			return err
		}
		info, err := os.Lstat(localSource)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("test SCP directory metadata target is not a directory")
		}
		_, err = fmt.Fprintf(channel, "directory %04o %d.%09d\n", info.Mode().Perm(), info.ModTime().Unix(), info.ModTime().Nanosecond())
		return err
	}
	if strings.Contains(command, "for entry in ./*") {
		directory, ok := shellAssignment(command, "directory")
		if !ok {
			return fmt.Errorf("test SCP listing command omitted directory path")
		}
		localDirectory, err := testRemotePath(root, directory)
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(localDirectory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			localEntry := filepath.Join(localDirectory, entry.Name())
			info, err := os.Lstat(localEntry)
			if err != nil {
				return err
			}
			kind := byte('s')
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				kind = 'l'
			case info.IsDir():
				kind = 'd'
			case info.Mode().IsRegular():
				kind = 'f'
			}
			if _, err := fmt.Fprintf(channel, "%c\x00%04o\x00%d\x00%d.%09d\x00%s\x00", kind, info.Mode().Perm(), info.Size(), info.ModTime().Unix(), info.ModTime().Nanosecond(), entry.Name()); err != nil {
				return err
			}
		}
		return nil
	}
	if strings.Contains(command, `cat >> "$partial"`) {
		partial, ok := shellAssignment(command, "partial")
		if !ok {
			return fmt.Errorf("test SCP upload command omitted partial path")
		}
		localPartial, err := testRemotePath(root, partial)
		if err != nil {
			return err
		}
		payload, err := io.ReadAll(channel)
		if err != nil {
			return err
		}
		file, err := os.OpenFile(localPartial, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(payload)
		closeErr := file.Close()
		return errors.Join(writeErr, closeErr)
	}
	if strings.Contains(command, "expected=") && strings.Contains(command, "chmod ") && strings.Contains(command, "touch -m -t ") {
		partial, ok := shellAssignment(command, "partial")
		if !ok {
			return fmt.Errorf("test SCP commit command omitted partial path")
		}
		target, ok := shellAssignment(command, "target")
		if !ok {
			return fmt.Errorf("test SCP commit command omitted target path")
		}
		localPartial, err := testRemotePath(root, partial)
		if err != nil {
			return err
		}
		localTarget, err := testRemotePath(root, target)
		if err != nil {
			return err
		}
		chmodStart := strings.Index(command, "chmod ")
		chmodFields := strings.Fields(command[chmodStart:])
		if len(chmodFields) < 2 {
			return fmt.Errorf("malformed SCP commit chmod command")
		}
		mode, err := strconv.ParseUint(chmodFields[1], 8, 32)
		if err != nil {
			return err
		}
		if err := os.Chmod(localPartial, os.FileMode(mode)); err != nil {
			return err
		}
		timestamp, ok := firstShellQuotedArgument(command, "touch -m -t ")
		if !ok {
			return fmt.Errorf("test SCP commit command omitted timestamp")
		}
		modified, err := time.ParseInLocation("200601021504.05", timestamp, time.UTC)
		if err != nil {
			return err
		}
		if err := os.Chtimes(localPartial, modified, modified); err != nil {
			return err
		}
		if strings.Contains(command, `ln "$partial" "$target"`) {
			if err := os.Link(localPartial, localTarget); err != nil {
				return err
			}
			return os.Remove(localPartial)
		}
		return os.Rename(localPartial, localTarget)
	}
	if strings.Contains(command, "printf 'file %s %s %s %s\\n'") {
		source, ok := shellAssignment(command, "source")
		if !ok {
			return fmt.Errorf("test SCP metadata command omitted source path")
		}
		localSource, err := testRemotePath(root, source)
		if err != nil {
			return err
		}
		info, err := os.Stat(localSource)
		if err != nil {
			return err
		}
		if info.IsDir() {
			_, err := io.WriteString(channel, "directory\n")
			return err
		}
		content, err := os.ReadFile(localSource)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		_, err = fmt.Fprintf(channel, "file %o %d %d %s\n", info.Mode().Perm(), info.Size(), info.ModTime().Unix(), hex.EncodeToString(digest[:]))
		return err
	}
	if strings.Contains(command, "head -c ") && strings.Contains(command, "sha256sum") && !strings.Contains(command, "tail -c +") {
		source, ok := shellAssignment(command, "source")
		if !ok {
			return fmt.Errorf("test SCP prefix command omitted source path")
		}
		localSource, err := testRemotePath(root, source)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(localSource)
		if err != nil {
			return err
		}
		header := strings.SplitN(command, "head -c ", 2)[1]
		countText := strings.Fields(header)[0]
		count, err := strconv.Atoi(countText)
		if err != nil || count < 0 {
			return fmt.Errorf("invalid test SCP prefix length %q", countText)
		}
		if count > len(content) {
			count = len(content)
		}
		digest := sha256.Sum256(content[:count])
		_, err = fmt.Fprintf(channel, "%s\n", hex.EncodeToString(digest[:]))
		return err
	}
	if strings.Contains(command, "tail -c +") {
		source, ok := shellAssignment(command, "source")
		if !ok {
			return fmt.Errorf("test SCP download command omitted source path")
		}
		localSource, err := testRemotePath(root, source)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(localSource)
		if err != nil {
			return err
		}
		remainingStart := strings.SplitN(command, "tail -c +", 2)[1]
		startText := strings.Fields(remainingStart)[0]
		start, err := strconv.Atoi(startText)
		if err != nil || start < 1 {
			return fmt.Errorf("invalid test SCP download offset %q", startText)
		}
		offset := start - 1
		if offset > len(content) {
			offset = len(content)
		}
		remaining := strings.SplitN(command, "; remaining=", 2)[1]
		remainingText := strings.TrimSuffix(strings.Fields(remaining)[0], ";")
		count, err := strconv.Atoi(remainingText)
		if err != nil || count < 0 {
			return fmt.Errorf("invalid test SCP download length %q", remainingText)
		}
		end := offset + count
		if end > len(content) {
			end = len(content)
		}
		if _, err := channel.Write(content[offset:end]); err != nil {
			return err
		}
		_, err = io.WriteString(channel, "\x00")
		return err
	}
	if strings.Contains(command, "mkdir -p ") && strings.Contains(command, `"$target"`) {
		target, ok := shellAssignment(command, "target")
		if !ok {
			return fmt.Errorf("test SCP directory command omitted target path")
		}
		localTarget, err := testRemotePath(root, target)
		if err != nil {
			return err
		}
		if strings.Contains(command, "mkdir \"$target\"") {
			parent, ok := shellAssignment(command, "parent")
			if !ok {
				return fmt.Errorf("test SCP directory command omitted parent path")
			}
			localParent, err := testRemotePath(root, parent)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(localParent, 0o700); err != nil {
				return err
			}
			return os.Mkdir(localTarget, 0o700)
		}
		return os.MkdirAll(localTarget, 0o700)
	}
	if strings.Contains(command, "chmod ") && strings.Contains(command, "touch -m -t ") {
		parts := strings.Split(command, "; ")
		if len(parts) != 3 {
			return fmt.Errorf("unexpected SCP metadata command: %s", command)
		}
		chmodFields := strings.Fields(parts[1])
		touchFields := strings.Fields(parts[2])
		if len(chmodFields) != 3 || len(touchFields) != 6 {
			return fmt.Errorf("malformed SCP metadata command: %s", command)
		}
		modeValue, err := strconv.ParseUint(chmodFields[1], 8, 32)
		if err != nil {
			return err
		}
		localPath, err := testRemotePath(root, strings.Trim(chmodFields[2], "'"))
		if err != nil {
			return err
		}
		if err := os.Chmod(localPath, os.FileMode(modeValue)); err != nil {
			return err
		}
		modified, err := time.ParseInLocation("200601021504.05", strings.Trim(touchFields[4], "'"), time.UTC)
		if err != nil {
			return err
		}
		return os.Chtimes(localPath, modified, modified)
	}
	if strings.Contains(command, `scp -p -t -- "$temporary"`) {
		temporary, ok := shellAssignment(command, "temporary")
		if !ok {
			return fmt.Errorf("test SCP command omitted temporary path")
		}
		target, ok := shellAssignment(command, "target")
		if !ok {
			return fmt.Errorf("test SCP command omitted target path")
		}
		localTemporary, err := testRemotePath(root, temporary)
		if err != nil {
			return err
		}
		localTarget, err := testRemotePath(root, target)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(channel, "\x00"); err != nil {
			return err
		}
		reader := bufio.NewReader(channel)
		timeRecord, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(timeRecord, "T") {
			return fmt.Errorf("read client SCP time record %q: %v", timeRecord, err)
		}
		timeFields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(timeRecord, "T")))
		if len(timeFields) != 4 {
			return fmt.Errorf("malformed client SCP time record: %q", timeRecord)
		}
		var mtimeSeconds int64
		var mtimeMicroseconds int64
		if _, err := fmt.Sscanf(timeFields[0], "%d", &mtimeSeconds); err != nil {
			return fmt.Errorf("parse client SCP modification seconds: %w", err)
		}
		if _, err := fmt.Sscanf(timeFields[1], "%d", &mtimeMicroseconds); err != nil {
			return fmt.Errorf("parse client SCP time record: %w", err)
		}
		if _, err := io.WriteString(channel, "\x00"); err != nil {
			return err
		}
		header, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		var mode uint32
		var size int64
		var name string
		if _, err := fmt.Sscanf(strings.TrimSpace(header), "C%o %d %s", &mode, &size, &name); err != nil {
			return fmt.Errorf("parse client SCP header: %w", err)
		}
		if _, err := io.WriteString(channel, "\x00"); err != nil {
			return err
		}
		file, err := os.OpenFile(localTemporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(file, reader, size)
		terminator, termErr := reader.ReadByte()
		closeErr := file.Close()
		if copyErr != nil || termErr != nil || terminator != 0 || closeErr != nil {
			_ = os.Remove(localTemporary)
			return errors.Join(copyErr, termErr, closeErr)
		}
		_ = os.Chmod(localTemporary, os.FileMode(mode)&0o777)
		modified := time.Unix(mtimeSeconds, mtimeMicroseconds*1000)
		_ = os.Chtimes(localTemporary, modified, modified)
		if _, err := io.WriteString(channel, "\x00"); err != nil {
			_ = os.Remove(localTemporary)
			return err
		}
		if strings.Contains(command, `ln "$temporary" "$target"`) {
			if err := os.Link(localTemporary, localTarget); err != nil {
				_ = os.Remove(localTemporary)
				return err
			}
			return os.Remove(localTemporary)
		}
		return os.Rename(localTemporary, localTarget)
	}
	if strings.Contains(command, "scp -p -f -r -- ") {
		remotePath, ok := shellAssignment(command, "source")
		if !ok {
			return fmt.Errorf("test SCP command omitted quoted source path")
		}
		localPath, err := testRemotePath(root, remotePath)
		if err != nil {
			return err
		}
		if info, err := os.Lstat(localPath); err != nil {
			return err
		} else if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("SCP 不跟随远程符号链接")
		} else if info.IsDir() {
			err = filepath.WalkDir(localPath, func(current string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.Type()&os.ModeSymlink != 0 {
					return errors.New("SCP 目录下载不会跟随远程符号链接")
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		reader := bufio.NewReader(channel)
		ack, err := reader.ReadByte()
		if err != nil || ack != 0 {
			return fmt.Errorf("read initial SCP ack: %v %w", ack, err)
		}
		info, err := os.Lstat(localPath)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := sendScheduledSCPDirectory(channel, reader, localPath, true); err != nil {
				return err
			}
			return nil
		}
		return sendScheduledSCPFile(channel, reader, localPath, info)
	}
	return fmt.Errorf("unsupported SCP command in test server: %s", command)
}

func sendScheduledSCPDirectory(channel ssh.Channel, reader *bufio.Reader, directory string, root bool) error {
	info, err := os.Stat(directory)
	if err != nil {
		return err
	}
	if err := sendScheduledSCPTime(channel, reader, info.ModTime()); err != nil {
		return err
	}
	name := filepath.Base(directory)
	if root {
		name = filepath.Base(directory)
	}
	if _, err := fmt.Fprintf(channel, "D%04o 0 %s\n", info.Mode().Perm(), name); err != nil {
		return err
	}
	if err := readScheduledSCPAck(reader, "directory header"); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		localPath := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(localPath)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if info.IsDir() {
			if err := sendScheduledSCPDirectory(channel, reader, localPath, false); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := sendScheduledSCPFile(channel, reader, localPath, info); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(channel, "E\n"); err != nil {
		return err
	}
	return readScheduledSCPAck(reader, "directory completion")
}

func sendScheduledSCPFile(channel ssh.Channel, reader *bufio.Reader, localPath string, info os.FileInfo) error {
	if err := sendScheduledSCPTime(channel, reader, info.ModTime()); err != nil {
		return err
	}
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := fmt.Fprintf(channel, "C%04o %d %s\n", info.Mode().Perm(), info.Size(), filepath.Base(localPath)); err != nil {
		return err
	}
	if err := readScheduledSCPAck(reader, "file header"); err != nil {
		return err
	}
	if _, err := io.CopyN(channel, file, info.Size()); err != nil {
		return err
	}
	if _, err := io.WriteString(channel, "\x00"); err != nil {
		return err
	}
	return readScheduledSCPAck(reader, "file completion")
}

func sendScheduledSCPTime(channel ssh.Channel, reader *bufio.Reader, modified time.Time) error {
	seconds := modified.Unix()
	microseconds := modified.Nanosecond() / 1000
	if _, err := fmt.Fprintf(channel, "T%d %d %d %d\n", seconds, microseconds, seconds, microseconds); err != nil {
		return err
	}
	return readScheduledSCPAck(reader, "time record")
}

func readScheduledSCPAck(reader *bufio.Reader, stage string) error {
	ack, err := reader.ReadByte()
	if err != nil || ack != 0 {
		return fmt.Errorf("read SCP %s ack: %v %w", stage, ack, err)
	}
	return nil
}

func shellAssignment(command, name string) (string, bool) {
	marker := name + "='"
	start := strings.Index(command, marker)
	if start < 0 {
		return "", false
	}
	value := command[start+len(marker):]
	end := strings.Index(value, "';")
	if end < 0 {
		return "", false
	}
	return value[:end], true
}

func firstShellQuotedArgument(command, prefix string) (string, bool) {
	start := strings.Index(command, prefix)
	if start < 0 {
		return "", false
	}
	value := command[start+len(prefix):]
	if len(value) < 2 || value[0] != '\'' {
		return "", false
	}
	end := strings.IndexByte(value[1:], '\'')
	if end < 0 {
		return "", false
	}
	return value[1 : end+1], true
}

func testRemotePath(root, remotePath string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(remotePath))
	if filepath.IsAbs(cleaned) {
		cleaned = strings.TrimLeft(cleaned, string(filepath.Separator))
	}
	joined := filepath.Join(root, cleaned)
	relative, err := filepath.Rel(root, joined)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("test SCP path escaped root")
	}
	return joined, nil
}
