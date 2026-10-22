package app

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/model"
)

func TestRunFileTransferUsesSavedHostAndReportsProgress(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	remoteRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(remoteRoot, "incoming"), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, signer := startScheduledSFTPTestServer(t, remoteRoot)
	port := listener.Addr().(*net.TCPAddr).Port
	host := model.Host{Kind: model.HostKindSSH, Name: "build", IP: "127.0.0.1", Port: port, User: "tester"}
	if err := sshclient.TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust test host: %v", err)
	}
	databasePath := filepath.Join(t.TempDir(), "workbench.db")
	store, err := sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	host, err = store.CreateHost(context.Background(), host)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	localDirectory := t.TempDir()
	source := filepath.Join(localDirectory, "payload.bin")
	want := []byte("Xftp one-shot CLI transfer\n")
	if err := os.WriteFile(source, want, 0o600); err != nil {
		t.Fatal(err)
	}
	var progressUpdates int
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := RunFileTransfer(ctx, databasePath, model.DefaultConfig(), "BUILD", model.ScheduledTransferUpload,
		source, "incoming/payload.bin", false, func(current, total int64) {
			progressUpdates++
			if current > total {
				t.Errorf("upload progress %d exceeds total %d", current, total)
			}
		}); err != nil {
		t.Fatalf("upload through saved host name: %v", err)
	}
	if progressUpdates == 0 {
		t.Fatal("file transfer did not report progress")
	}
	uploaded, err := os.ReadFile(filepath.Join(remoteRoot, "incoming", "payload.bin"))
	if err != nil || string(uploaded) != string(want) {
		t.Fatalf("uploaded file = %q, err=%v; want %q", uploaded, err, want)
	}

	downloadedPath := filepath.Join(localDirectory, "downloaded.bin")
	if err := RunFileTransfer(ctx, databasePath, model.DefaultConfig(), host.Id, model.ScheduledTransferDownload,
		downloadedPath, "incoming/payload.bin", false, nil); err != nil {
		t.Fatalf("download through saved host ID: %v", err)
	}
	downloaded, err := os.ReadFile(downloadedPath)
	if err != nil || string(downloaded) != string(want) {
		t.Fatalf("downloaded file = %q, err=%v; want %q", downloaded, err, want)
	}
}

func TestRunFileTransferRejectsAmbiguousHostName(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "ambiguous.db")
	store, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"192.0.2.20", "192.0.2.21"} {
		if _, err := store.CreateHost(ctx, model.Host{
			Kind: model.HostKindSSH, Name: "duplicate", IP: address, Port: 22, User: "tester",
		}); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	err = RunFileTransfer(ctx, databasePath, model.DefaultConfig(), "duplicate", model.ScheduledTransferUpload,
		filepath.Join(t.TempDir(), "source"), "/tmp/destination", false, nil)
	if err == nil || !strings.Contains(err.Error(), "不唯一") {
		t.Fatalf("ambiguous host selection error = %v; want explicit ambiguity", err)
	}
}
