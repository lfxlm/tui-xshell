package ftpclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"tui-ssh-workbench/internal/infra/remotefs"
)

func TestDialRejectsInvalidTLSModeBeforeNetwork(t *testing.T) {
	_, err := Dial(context.Background(), Config{Address: "127.0.0.1", Port: 1, TLSMode: "starttls"})
	if err == nil {
		t.Fatal("Dial() succeeded with an unsupported TLS mode")
	}
}

func TestValidateRawCommand(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		command string
		want    string
		wantErr bool
	}{
		{name: "trims whitespace", command: "  SYST  ", want: "SYST"},
		{name: "allows site extension", command: "SITE CHMOD 640 /tmp/file", want: "SITE CHMOD 640 /tmp/file"},
		{name: "rejects empty", command: "  ", wantErr: true},
		{name: "rejects command injection", command: "SYST\r\nDELE /important", wantErr: true},
		{name: "rejects control characters", command: "SYST\x1b", wantErr: true},
		{name: "rejects oversized input", command: strings.Repeat("A", 513), wantErr: true},
		{name: "rejects managed login command", command: "PASS secret", wantErr: true},
		{name: "rejects managed data command", command: "RETR /file", wantErr: true},
		{name: "rejects managed connection options", command: "OPTS UTF8 OFF", wantErr: true},
		{name: "rejects managed mount command", command: "SMNT /", wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ValidateRawCommand(testCase.command)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("ValidateRawCommand(%q) error = %v, wantErr %v", testCase.command, err, testCase.wantErr)
			}
			if !testCase.wantErr && got != testCase.want {
				t.Fatalf("ValidateRawCommand(%q) = %q, want %q", testCase.command, got, testCase.want)
			}
		})
	}
}

func TestRawCommandReturnsServerReply(t *testing.T) {
	certificate, _ := newTestCertificate(t)
	address, _, stop := startFTPTestServer(t, TLSOff, certificate)
	t.Cleanup(stop)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var portNumber int
	if _, err := fmt.Sscan(port, &portNumber); err != nil {
		t.Fatal(err)
	}
	client, err := Dial(context.Background(), Config{Address: "127.0.0.1", Port: portNumber, TLSMode: TLSOff, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Dial() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	code, response, err := client.RawCommand(context.Background(), "SYST")
	if err != nil || code != 215 || response != "UNIX Type: L8" {
		t.Fatalf("RawCommand(SYST) = %d, %q, %v; want 215, UNIX Type: L8", code, response, err)
	}
	if _, _, err := client.RawCommand(context.Background(), "SYST\r\nDELE /important"); err == nil {
		t.Fatal("RawCommand() accepted a command injection attempt")
	}
}

func TestDialAndListProtectFTPSControlAndDataConnections(t *testing.T) {
	certificate, roots := newTestCertificate(t)
	for _, activeMode := range []bool{false, true} {
		for _, mode := range []TLSMode{TLSOff, TLSExplicit, TLSImplicit} {
			t.Run(fmt.Sprintf("%s/active=%t", mode, activeMode), func(t *testing.T) {
				address, _, stop := startFTPTestServer(t, mode, certificate, map[string][]byte{
					"/hello.txt": []byte("hello"), "/.hidden": []byte("secret"),
				})
				t.Cleanup(stop)
				_, port, err := net.SplitHostPort(address)
				if err != nil {
					t.Fatal(err)
				}
				var portNumber int
				if _, err := fmt.Sscan(port, &portNumber); err != nil {
					t.Fatal(err)
				}
				client, err := Dial(context.Background(), Config{
					Address: "127.0.0.1", Port: portNumber, Username: "tester", Password: "secret",
					TLSMode: mode, ActiveMode: activeMode, ServerName: "localhost", RootCAs: roots, Timeout: 3 * time.Second,
				})
				if err != nil {
					t.Fatalf("Dial() failed: %v", err)
				}
				t.Cleanup(func() { _ = client.Close() })

				cwd, err := client.WorkingDirectory()
				if err != nil || cwd != "/" {
					t.Fatalf("WorkingDirectory() = %q, %v; want /", cwd, err)
				}
				entries, err := client.List("/")
				if err != nil {
					t.Fatalf("List() failed: %v", err)
				}
				if len(entries) != 2 {
					t.Fatalf("List() = %#v; want visible and hidden files", entries)
				}
				listed := make(map[string]remotefs.Entry, len(entries))
				for _, entry := range entries {
					listed[entry.Name] = entry
				}
				if visible := listed["hello.txt"]; visible.Size != 5 || visible.IsDir || !visible.ModeKnown || visible.Mode.Perm() != 0o644 {
					t.Fatalf("visible entry = %#v; want 5-byte file with parsed rw-r--r-- permissions", visible)
				}
				if hidden := listed[".hidden"]; hidden.Size != 6 || hidden.IsDir || !hidden.ModeKnown || hidden.Mode.Perm() != 0o644 {
					t.Fatalf("hidden entry = %#v; want 6-byte file with parsed rw-r--r-- permissions", hidden)
				}

				directory := t.TempDir()
				downloaded := filepath.Join(directory, "downloaded.txt")
				if err := client.Download(context.Background(), "/hello.txt", downloaded, false, nil); err != nil {
					t.Fatalf("Download() failed: %v", err)
				}
				if content, err := os.ReadFile(downloaded); err != nil || string(content) != "hello" {
					t.Fatalf("downloaded file = %q, err=%v; want hello", content, err)
				}

				uploadSource := filepath.Join(directory, "upload.txt")
				if err := os.WriteFile(uploadSource, []byte("world"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := client.Upload(context.Background(), uploadSource, "/uploaded.txt", false, nil); err != nil {
					t.Fatalf("Upload() failed: %v", err)
				}
				uploaded := filepath.Join(directory, "uploaded.txt")
				if err := client.Download(context.Background(), "/uploaded.txt", uploaded, false, nil); err != nil {
					t.Fatalf("verify uploaded file: %v", err)
				}
				if content, err := os.ReadFile(uploaded); err != nil || string(content) != "world" {
					t.Fatalf("uploaded file content = %q, err=%v; want world", content, err)
				}

				emptySource := filepath.Join(directory, "empty.txt")
				if err := os.WriteFile(emptySource, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := client.Upload(context.Background(), emptySource, "/empty.txt", false, nil); err != nil {
					t.Fatalf("Upload() of an empty file failed: %v", err)
				}
				emptyTarget := filepath.Join(directory, "empty-download.txt")
				if err := client.Download(context.Background(), "/empty.txt", emptyTarget, false, nil); err != nil {
					t.Fatalf("Download() of an empty file failed: %v", err)
				}
				if content, err := os.ReadFile(emptyTarget); err != nil || len(content) != 0 {
					t.Fatalf("empty download = %q, err=%v; want an empty file", content, err)
				}
			})
		}
	}
}

func TestSetPermissionsUsesSiteChmodAndListsUpdatedMode(t *testing.T) {
	certificate, _ := newTestCertificate(t)
	address, _, stop := startFTPTestServer(t, TLSOff, certificate, map[string][]byte{
		"/report with spaces.txt": []byte("payload"),
	})
	t.Cleanup(stop)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var portNumber int
	if _, err := fmt.Sscan(port, &portNumber); err != nil {
		t.Fatal(err)
	}
	client, err := Dial(context.Background(), Config{Address: "127.0.0.1", Port: portNumber, TLSMode: TLSOff, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Dial() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	remotePath := "/report with spaces.txt"
	if err := client.SetPermissions(remotePath, 0o600); err != nil {
		t.Fatalf("SetPermissions() failed: %v", err)
	}
	entries, err := client.List("/")
	if err != nil {
		t.Fatalf("List() after SITE CHMOD failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "report with spaces.txt" || !entries[0].ModeKnown || entries[0].Mode.Perm() != 0o600 {
		t.Fatalf("FTP listing after SITE CHMOD = %#v, want report.txt mode 0600", entries)
	}
	if err := client.SetPermissions("/safe\r\nDELE /important", 0o600); err == nil || !strings.Contains(err.Error(), "command contains CR or LF") {
		t.Fatalf("SetPermissions() command injection error = %v, want CR/LF rejection", err)
	}
}

func TestSetPermissionsReportsUnsupportedServerExtension(t *testing.T) {
	certificate, _ := newTestCertificate(t)
	address, _, stop := startFTPTestServerWithOptions(t, TLSOff, certificate, ftpTestServerOptions{rejectChmod: true})
	t.Cleanup(stop)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var portNumber int
	if _, err := fmt.Sscan(port, &portNumber); err != nil {
		t.Fatal(err)
	}
	client, err := Dial(context.Background(), Config{Address: "127.0.0.1", Port: portNumber, TLSMode: TLSOff, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Dial() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetPermissions("/hello.txt", 0o600); err == nil || !strings.Contains(err.Error(), "可能不支持权限修改") {
		t.Fatalf("unsupported SITE CHMOD error = %v, want a useful server capability message", err)
	}
}

func TestActiveModeFallsBackToPORTWhenEPRTIsRejected(t *testing.T) {
	certificate, _ := newTestCertificate(t)
	address, _, stop := startFTPTestServerWithOptions(t, TLSOff, certificate, ftpTestServerOptions{rejectEPRT: true})
	t.Cleanup(stop)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var portNumber int
	if _, err := fmt.Sscan(port, &portNumber); err != nil {
		t.Fatal(err)
	}
	client, err := Dial(context.Background(), Config{
		Address: "127.0.0.1", Port: portNumber, Username: "tester", Password: "secret",
		TLSMode: TLSOff, ActiveMode: true, Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial() in active mode failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	entries, err := client.List("/")
	if err != nil || len(entries) != 1 || entries[0].Name != "hello.txt" {
		t.Fatalf("active FTP LIST after EPRT fallback = %#v, %v; want hello.txt", entries, err)
	}
}

func TestActiveModeUploadCancellationClosesCallbackListener(t *testing.T) {
	certificate, _ := newTestCertificate(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	address, _, stop := startFTPTestServerWithOptions(t, TLSOff, certificate, ftpTestServerOptions{
		activeDataStarted: started, activeDataRelease: release,
	})
	t.Cleanup(stop)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var portNumber int
	if _, err := fmt.Sscan(port, &portNumber); err != nil {
		t.Fatal(err)
	}
	client, err := Dial(context.Background(), Config{
		Address: "127.0.0.1", Port: portNumber, Username: "tester", Password: "secret",
		TLSMode: TLSOff, ActiveMode: true, Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial() in active mode failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	local := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(local, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- client.Upload(ctx, local, "/upload.txt", false, nil) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("FTP test server did not reach active data callback")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled active upload error = %v, want context cancellation", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancelling active upload did not close its callback listener promptly")
	}
}

func TestFTPResumesOnlyVerifiedUploadAndDownloadPrefixes(t *testing.T) {
	certificate, roots := newTestCertificate(t)
	remoteTime := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	for _, mode := range []TLSMode{TLSOff, TLSExplicit, TLSImplicit} {
		t.Run(string(mode), func(t *testing.T) {
			partialRemotePath := ftpUploadPartialPath("/uploaded.txt")
			address, restOffsets, stop := startFTPTestServer(t, mode, certificate, map[string][]byte{
				"/hello.txt":      []byte("hello"),
				partialRemotePath: []byte("hello"),
			})
			t.Cleanup(stop)
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				t.Fatal(err)
			}
			var portNumber int
			if _, err := fmt.Sscan(port, &portNumber); err != nil {
				t.Fatal(err)
			}
			client, err := Dial(context.Background(), Config{
				Address: "127.0.0.1", Port: portNumber, Username: "tester", Password: "secret",
				TLSMode: mode, ServerName: "localhost", RootCAs: roots, Timeout: 3 * time.Second,
			})
			if err != nil {
				t.Fatalf("Dial() failed: %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })

			directory := t.TempDir()
			downloadTarget := filepath.Join(directory, "resumed.txt")
			downloadPartial := ftpDownloadPartialPath(downloadTarget)
			if err := os.WriteFile(downloadPartial, []byte("he"), 0o600); err != nil {
				t.Fatal(err)
			}
			prefixDigest := sha256.Sum256([]byte("he"))
			if err := writeFTPDownloadCheckpoint(ftpDownloadCheckpointPath(downloadPartial), ftpDownloadCheckpoint{
				Endpoint: client.address, RemotePath: "/hello.txt", Size: 5, ModTime: remoteTime,
				Offset: 2, PrefixSHA256: fmt.Sprintf("%x", prefixDigest),
			}); err != nil {
				t.Fatalf("create download checkpoint: %v", err)
			}
			if err := client.Download(context.Background(), "/hello.txt", downloadTarget, false, nil); err != nil {
				t.Fatalf("resumed Download() failed: %v", err)
			}
			if content, err := os.ReadFile(downloadTarget); err != nil || string(content) != "hello" {
				t.Fatalf("resumed download = %q, err=%v; want hello", content, err)
			}
			if got := waitFTPResumeOffset(t, restOffsets); got != 2 {
				t.Fatalf("download REST offset = %d, want 2", got)
			}

			uploadSource := filepath.Join(directory, "upload.txt")
			if err := os.WriteFile(uploadSource, []byte("hello world"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := client.Upload(context.Background(), uploadSource, "/uploaded.txt", false, nil); err != nil {
				t.Fatalf("resumed Upload() failed: %v", err)
			}
			if got := waitFTPResumeOffset(t, restOffsets); got != 5 {
				t.Fatalf("upload REST offset = %d, want 5", got)
			}
			uploaded := filepath.Join(directory, "uploaded.txt")
			if err := client.Download(context.Background(), "/uploaded.txt", uploaded, false, nil); err != nil {
				t.Fatalf("verify resumed upload: %v", err)
			}
			if content, err := os.ReadFile(uploaded); err != nil || string(content) != "hello world" {
				t.Fatalf("resumed upload content = %q, err=%v; want hello world", content, err)
			}
		})
	}
}

func waitFTPResumeOffset(t *testing.T, offsets <-chan uint64) uint64 {
	t.Helper()
	select {
	case offset := <-offsets:
		return offset
	case <-time.After(2 * time.Second):
		t.Fatal("FTP server did not receive REST for a resumed transfer")
		return 0
	}
}

func TestFTPMismatchedResumeDataRestartsAndPreservesUntrackedLocalFiles(t *testing.T) {
	certificate, roots := newTestCertificate(t)
	address, restOffsets, stop := startFTPTestServer(t, TLSOff, certificate, map[string][]byte{
		"/hello.txt":                          []byte("hello"),
		ftpUploadPartialPath("/uploaded.txt"): []byte("wrong"),
	})
	t.Cleanup(stop)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var portNumber int
	if _, err := fmt.Sscan(port, &portNumber); err != nil {
		t.Fatal(err)
	}
	client, err := Dial(context.Background(), Config{
		Address: "127.0.0.1", Port: portNumber, Username: "tester", Password: "secret",
		TLSMode: TLSOff, ServerName: "localhost", RootCAs: roots, Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	directory := t.TempDir()
	uploadSource := filepath.Join(directory, "upload.txt")
	if err := os.WriteFile(uploadSource, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.Upload(context.Background(), uploadSource, "/uploaded.txt", false, nil); err != nil {
		t.Fatalf("Upload() with a mismatched remote prefix failed: %v", err)
	}
	select {
	case offset := <-restOffsets:
		t.Fatalf("mismatched remote prefix was resumed at byte %d", offset)
	default:
	}

	target := filepath.Join(directory, "protected.txt")
	partial := ftpDownloadPartialPath(target)
	if err := os.WriteFile(partial, []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.Download(context.Background(), "/hello.txt", target, false, nil); err == nil {
		t.Fatal("Download() overwrote a local temporary-looking file without a workbench checkpoint")
	}
	if content, err := os.ReadFile(partial); err != nil || string(content) != "user data" {
		t.Fatalf("untracked local file changed: %q, err=%v", content, err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("download target unexpectedly exists: %v", err)
	}
}

func TestFTPDirectoryDownloadReportsFileProgress(t *testing.T) {
	certificate, roots := newTestCertificate(t)
	address, _, stop := startFTPTestServer(t, TLSOff, certificate, map[string][]byte{
		"/tree/first.txt":         []byte("abc"),
		"/tree/nested/second.txt": []byte("wxyz"),
	})
	t.Cleanup(stop)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var portNumber int
	if _, err := fmt.Sscan(port, &portNumber); err != nil {
		t.Fatal(err)
	}
	client, err := Dial(context.Background(), Config{
		Address: "127.0.0.1", Port: portNumber, Username: "tester", Password: "secret",
		TLSMode: TLSOff, ServerName: "localhost", RootCAs: roots, Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	var reports []remotefs.TransferProgress
	ctx := remotefs.WithTransferProgressReporter(context.Background(), func(progress remotefs.TransferProgress) {
		reports = append(reports, progress)
	})
	target := filepath.Join(t.TempDir(), "tree")
	if err := client.DownloadDirectory(ctx, "/tree", target, false, nil); err != nil {
		t.Fatalf("DownloadDirectory() failed: %v", err)
	}
	assertFTPDirectoryProgressReports(t, reports, 7)
}

func TestFTPUploadDirectoryReportsFileProgress(t *testing.T) {
	certificate, roots := newTestCertificate(t)
	address, _, stop := startFTPTestServer(t, TLSOff, certificate)
	t.Cleanup(stop)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var portNumber int
	if _, err := fmt.Sscan(port, &portNumber); err != nil {
		t.Fatal(err)
	}
	client, err := Dial(context.Background(), Config{
		Address: "127.0.0.1", Port: portNumber, Username: "tester", Password: "secret",
		TLSMode: TLSOff, ServerName: "localhost", RootCAs: roots, Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
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
	var reports []remotefs.TransferProgress
	ctx := remotefs.WithTransferProgressReporter(context.Background(), func(progress remotefs.TransferProgress) {
		reports = append(reports, progress)
	})
	if err := client.UploadDirectory(ctx, localRoot, "/tree", false, nil); err != nil {
		t.Fatalf("UploadDirectory() failed: %v", err)
	}
	assertFTPDirectoryProgressReports(t, reports, 7)
}

func assertFTPDirectoryProgressReports(t *testing.T, reports []remotefs.TransferProgress, totalBytes int64) {
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

func newTestCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
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

type ftpTestServerOptions struct {
	rejectEPRT        bool
	rejectChmod       bool
	activeDataStarted chan struct{}
	activeDataRelease <-chan struct{}
}

func startFTPTestServer(t *testing.T, mode TLSMode, certificate tls.Certificate, initialFiles ...map[string][]byte) (string, <-chan uint64, func()) {
	return startFTPTestServerWithOptions(t, mode, certificate, ftpTestServerOptions{}, initialFiles...)
}

func startFTPTestServerWithOptions(t *testing.T, mode TLSMode, certificate tls.Certificate, options ftpTestServerOptions, initialFiles ...map[string][]byte) (string, <-chan uint64, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	finished := make(chan struct{})
	files := map[string][]byte{"/hello.txt": []byte("hello")}
	if len(initialFiles) > 0 {
		files = make(map[string][]byte, len(initialFiles[0]))
		for name, content := range initialFiles[0] {
			files[name] = append([]byte(nil), content...)
		}
	}
	directories := map[string]bool{"/": true}
	// The fixture's LIST responses report Unix rwx permissions, so keep
	// explicit modes for the same paths and let SITE CHMOD update them.
	permissions := map[string]os.FileMode{}
	for remotePath := range files {
		permissions[remotePath] = 0o644
	}
	for remotePath := range files {
		for directory := path.Dir(remotePath); directory != "/" && directory != "."; directory = path.Dir(directory) {
			directories[directory] = true
			permissions[directory] = 0o755
		}
	}
	permissions["/"] = 0o755
	restOffsets := make(chan uint64, 16)
	go func() {
		defer close(finished)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		secure := mode == TLSImplicit
		if secure {
			conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
			if err := conn.(*tls.Conn).Handshake(); err != nil {
				return
			}
		}
		control := textproto.NewConn(conn)
		if _, err := fmt.Fprint(conn, "220 test FTP ready\r\n"); err != nil {
			return
		}
		var dataListener net.Listener
		var activeAddress string
		var renameSource string
		var restOffset uint64
		defer func() {
			if dataListener != nil {
				_ = dataListener.Close()
			}
		}()
		openDataConnection := func() (net.Conn, error) {
			if activeAddress != "" {
				address := activeAddress
				activeAddress = ""
				if options.activeDataStarted != nil {
					select {
					case options.activeDataStarted <- struct{}{}:
					default:
					}
				}
				if options.activeDataRelease != nil {
					<-options.activeDataRelease
				}
				return net.DialTimeout("tcp", address, 3*time.Second)
			}
			if dataListener == nil {
				return nil, errors.New("no FTP data endpoint configured")
			}
			data, acceptErr := dataListener.Accept()
			_ = dataListener.Close()
			dataListener = nil
			return data, acceptErr
		}
		for {
			line, err := control.ReadLine()
			if err != nil {
				return
			}
			command, argument, _ := strings.Cut(line, " ")
			switch strings.ToUpper(command) {
			case "AUTH":
				if mode != TLSExplicit || !strings.EqualFold(argument, "TLS") {
					_, _ = fmt.Fprint(conn, "502 unsupported\r\n")
					continue
				}
				_, _ = fmt.Fprint(conn, "234 begin TLS\r\n")
				conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
				if err := conn.(*tls.Conn).Handshake(); err != nil {
					return
				}
				control = textproto.NewConn(conn)
				secure = true
			case "USER":
				_, _ = fmt.Fprint(conn, "331 password required\r\n")
			case "PASS":
				_, _ = fmt.Fprint(conn, "230 logged in\r\n")
			case "FEAT":
				_, _ = fmt.Fprint(conn, "211-Features:\r\n MDTM\r\n211 End\r\n")
			case "TYPE", "PBSZ", "PROT", "OPTS":
				_, _ = fmt.Fprint(conn, "200 ok\r\n")
			case "MDTM":
				_, _ = fmt.Fprint(conn, "213 20261020120000\r\n")
			case "REST":
				restOffset, err = strconv.ParseUint(argument, 10, 64)
				if err != nil {
					_, _ = fmt.Fprint(conn, "501 invalid offset\r\n")
					continue
				}
				select {
				case restOffsets <- restOffset:
				default:
				}
				_, _ = fmt.Fprint(conn, "350 restart position accepted\r\n")
			case "PWD":
				_, _ = fmt.Fprint(conn, "257 \"/\" is current directory\r\n")
			case "SYST":
				_, _ = fmt.Fprint(conn, "215 UNIX Type: L8\r\n")
			case "SIZE":
				content, found := files[argument]
				if found {
					_, _ = fmt.Fprintf(conn, "213 %d\r\n", len(content))
				} else {
					_, _ = fmt.Fprint(conn, "550 file not found\r\n")
				}
			case "RNFR":
				renameSource = argument
				_, _ = fmt.Fprint(conn, "350 ready for destination\r\n")
			case "RNTO":
				content, found := files[renameSource]
				if !found {
					_, _ = fmt.Fprint(conn, "550 source not found\r\n")
					continue
				}
				files[argument] = content
				delete(files, renameSource)
				renameSource = ""
				_, _ = fmt.Fprint(conn, "250 renamed\r\n")
			case "DELE":
				delete(files, argument)
				_, _ = fmt.Fprint(conn, "250 deleted\r\n")
			case "MKD":
				directories[path.Clean(argument)] = true
				permissions[path.Clean(argument)] = 0o755
				_, _ = fmt.Fprintf(conn, "257 %q created\r\n", path.Clean(argument))
			case "SITE":
				verb, args, found := strings.Cut(argument, " ")
				modeText, remotePath, hasPath := strings.Cut(args, " ")
				mode, parseErr := strconv.ParseUint(modeText, 8, 12)
				if options.rejectChmod {
					_, _ = fmt.Fprint(conn, "502 SITE CHMOD unsupported\r\n")
				} else if !found || !strings.EqualFold(verb, "CHMOD") || !hasPath || parseErr != nil {
					_, _ = fmt.Fprint(conn, "501 invalid SITE CHMOD\r\n")
				} else {
					permissions[remotePath] = os.FileMode(mode & 0o777)
					_, _ = fmt.Fprint(conn, "200 SITE CHMOD successful\r\n")
				}
			case "EPSV":
				if dataListener != nil {
					_ = dataListener.Close()
				}
				dataListener, err = net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					_, _ = fmt.Fprint(conn, "425 no data listener\r\n")
					continue
				}
				port := dataListener.Addr().(*net.TCPAddr).Port
				_, _ = fmt.Fprintf(conn, "229 Entering Extended Passive Mode (|||%d|)\r\n", port)
			case "EPRT":
				if options.rejectEPRT {
					_, _ = fmt.Fprint(conn, "502 EPRT unsupported\r\n")
					continue
				}
				parts := strings.Split(argument, "|")
				if len(parts) != 5 || (parts[1] != "1" && parts[1] != "2") {
					_, _ = fmt.Fprint(conn, "501 invalid EPRT\r\n")
					continue
				}
				if _, err := strconv.Atoi(parts[3]); err != nil {
					_, _ = fmt.Fprint(conn, "501 invalid EPRT port\r\n")
					continue
				}
				activeAddress = net.JoinHostPort(parts[2], parts[3])
				_, _ = fmt.Fprint(conn, "200 EPRT accepted\r\n")
			case "PORT":
				parts := strings.Split(argument, ",")
				if len(parts) != 6 {
					_, _ = fmt.Fprint(conn, "501 invalid PORT\r\n")
					continue
				}
				values := make([]int, len(parts))
				valid := true
				for index, part := range parts {
					values[index], err = strconv.Atoi(part)
					if err != nil || values[index] < 0 || values[index] > 255 {
						valid = false
						break
					}
				}
				if !valid {
					_, _ = fmt.Fprint(conn, "501 invalid PORT\r\n")
					continue
				}
				activeAddress = net.JoinHostPort(fmt.Sprintf("%d.%d.%d.%d", values[0], values[1], values[2], values[3]), strconv.Itoa(values[4]*256+values[5]))
				_, _ = fmt.Fprint(conn, "200 PORT accepted\r\n")
			case "LIST":
				if dataListener == nil && activeAddress == "" {
					_, _ = fmt.Fprint(conn, "425 no passive listener\r\n")
					continue
				}
				_, _ = fmt.Fprint(conn, "150 opening data\r\n")
				data, acceptErr := openDataConnection()
				if acceptErr != nil {
					return
				}
				if secure {
					tlsData := tls.Server(data, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
					if err := tlsData.Handshake(); err != nil {
						_ = data.Close()
						return
					}
					data = tlsData
				}
				listArgument := strings.TrimSpace(strings.TrimPrefix(argument, "-a"))
				listDirectory := path.Clean(listArgument)
				if listDirectory == "." || listDirectory == "" {
					listDirectory = "/"
				}
				type listedEntry struct {
					name string
					size int
					dir  bool
					mode os.FileMode
				}
				entries := make([]listedEntry, 0)
				for directory := range directories {
					if directory != listDirectory && path.Dir(directory) == listDirectory {
						entries = append(entries, listedEntry{name: path.Base(directory), dir: true, mode: permissions[directory]})
					}
				}
				for remotePath, content := range files {
					if path.Dir(remotePath) == listDirectory {
						entries = append(entries, listedEntry{name: path.Base(remotePath), size: len(content), mode: permissions[remotePath]})
					}
				}
				sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
				var writeErr error
				for _, entry := range entries {
					mode := entry.mode
					if entry.dir {
						mode |= os.ModeDir
					}
					if _, writeErr = fmt.Fprintf(data, "%s 1 tester users %d Jan 02 15:04 %s\r\n", mode.String(), entry.size, entry.name); writeErr != nil {
						break
					}
				}
				_ = data.Close()
				if writeErr != nil {
					return
				}
				_, _ = fmt.Fprint(conn, "226 transfer complete\r\n")
			case "RETR", "STOR":
				if dataListener == nil && activeAddress == "" {
					_, _ = fmt.Fprint(conn, "425 no passive listener\r\n")
					continue
				}
				_, _ = fmt.Fprint(conn, "150 opening data\r\n")
				data, acceptErr := openDataConnection()
				if acceptErr != nil {
					return
				}
				if secure {
					tlsData := tls.Server(data, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
					if err := tlsData.Handshake(); err != nil {
						_ = data.Close()
						return
					}
					data = tlsData
				}
				if strings.EqualFold(command, "RETR") {
					content := files[argument]
					if restOffset > uint64(len(content)) {
						_ = data.Close()
						_, _ = fmt.Fprint(conn, "551 restart position outside file\r\n")
						continue
					}
					_, _ = data.Write(content[restOffset:])
				} else {
					content, readErr := io.ReadAll(data)
					if readErr != nil {
						_ = data.Close()
						return
					}
					prefix := files[argument]
					if restOffset > uint64(len(prefix)) {
						_ = data.Close()
						_, _ = fmt.Fprint(conn, "551 restart position outside file\r\n")
						continue
					}
					prefix = prefix[:restOffset]
					files[argument] = append(prefix, content...)
					if _, exists := permissions[argument]; !exists {
						permissions[argument] = 0o644
					}
				}
				restOffset = 0
				_ = data.Close()
				_, _ = fmt.Fprint(conn, "226 transfer complete\r\n")
			case "QUIT":
				_, _ = fmt.Fprint(conn, "221 goodbye\r\n")
				return
			default:
				_, _ = fmt.Fprint(conn, "502 unsupported\r\n")
			}
		}
	}()
	stop := func() {
		_ = listener.Close()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("test FTP server did not stop")
		}
	}
	return address, restOffsets, stop
}

func TestSafeRemoteName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "config.yaml", want: true},
		{name: "带 空格.txt", want: true},
		{name: "", want: false},
		{name: ".", want: false},
		{name: "..", want: false},
		{name: "../outside", want: false},
		{name: `..\\outside`, want: false},
		{name: "C:outside.txt", want: false},
		{name: "bad\x00name", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := safeRemoteName(test.name); got != test.want {
				t.Fatalf("safeRemoteName(%q) = %v, want %v", test.name, got, test.want)
			}
		})
	}
}

func TestProgressReaderReportsBytesAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var progress [][2]int64
	reader := &progressReader{
		ctx: ctx, reader: io.LimitReader(zeroReader{}, 5), total: 5,
		progress: func(current, total int64) { progress = append(progress, [2]int64{current, total}) },
	}
	buffer := make([]byte, 2)
	for range 3 {
		_, err := reader.Read(buffer)
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("Read() error = %v", err)
		}
	}
	cancel()
	if _, err := reader.Read(buffer); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() after cancellation error = %v, want context.Canceled", err)
	}
	if len(progress) == 0 || progress[len(progress)-1] != [2]int64{5, 5} {
		t.Fatalf("progress = %v, want final [5 5]", progress)
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}

func TestTrackedDialerCloseAll(t *testing.T) {
	left, right := net.Pipe()
	tracker := newTrackedDialer(time.Second, context.Background())
	tracked := &trackedConn{Conn: left, owner: tracker}
	tracker.conns[tracked] = struct{}{}
	tracker.CloseAll()

	if _, err := right.Write([]byte("x")); err == nil {
		t.Fatal("peer write unexpectedly succeeded after CloseAll")
	}
	if _, err := tracker.Dial("tcp", "127.0.0.1:1"); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Dial() after CloseAll error = %v, want net.ErrClosed", err)
	}
	_ = right.Close()
}

func TestEnsureSafeLocalDirectoryRejectsTraversalAndSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := ensureSafeLocalDirectory(root, filepath.Join(root, "nested", "child")); err != nil {
		t.Fatalf("ensureSafeLocalDirectory() for nested path: %v", err)
	}
	if err := ensureSafeLocalDirectory(root, filepath.Join(root, "..", filepath.Base(outside), "escape")); err == nil {
		t.Fatal("ensureSafeLocalDirectory() accepted a path outside the root")
	}
	if err := ensureSafeLocalDirectory(root, filepath.Join(root, "linked", "escape")); err == nil {
		t.Fatal("ensureSafeLocalDirectory() accepted a symlink component")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside directory changed, entries=%v err=%v", entries, err)
	}
}

func TestReplaceLocalFileOverwritePreservesOldFileOnFailure(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "existing.txt")
	temporary := filepath.Join(directory, "replacement.txt")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceLocalFile(temporary, destination, true); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new" {
		t.Fatalf("destination = %q, want new", content)
	}
	if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file still exists: %v", err)
	}
}
