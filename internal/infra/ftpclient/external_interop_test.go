package ftpclient

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	ftp "github.com/jlaffaye/ftp"
)

func TestFTPClientAgainstPyftpdlib(t *testing.T) {
	portValue := os.Getenv("TUI_FTP_INTEROP_PORT")
	if portValue == "" {
		t.Skip("set TUI_FTP_INTEROP_PORT by running scripts/verify-ftp-interop.sh")
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		t.Fatalf("invalid TUI_FTP_INTEROP_PORT %q", portValue)
	}
	mode := TLSMode(os.Getenv("TUI_FTP_INTEROP_MODE"))
	if mode != TLSOff && mode != TLSExplicit && mode != TLSImplicit {
		t.Fatalf("unsupported TUI_FTP_INTEROP_MODE %q", mode)
	}

	config := Config{
		Address: "127.0.0.1", Port: port, Username: "workbench", Password: "interop-test-password",
		TLSMode: mode, ActiveMode: os.Getenv("TUI_FTP_INTEROP_ACTIVE") == "1", ServerName: "localhost", Timeout: 10 * time.Second,
	}
	if mode != TLSOff {
		certificate, err := os.ReadFile(os.Getenv("TUI_FTP_INTEROP_CERT"))
		if err != nil {
			t.Fatalf("read temporary FTPS certificate: %v", err)
		}
		config.RootCAs = x509.NewCertPool()
		if !config.RootCAs.AppendCertsFromPEM(certificate) {
			t.Fatal("temporary FTPS certificate could not be added to the trust pool")
		}
	}
	client, err := Dial(context.Background(), config)
	if err != nil {
		t.Fatalf("connect to pyftpdlib (%s): %v", mode, err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if cwd, err := client.WorkingDirectory(); err != nil || cwd != "/" {
		t.Fatalf("FTP working directory=%q err=%v; want /", cwd, err)
	}
	rootEntries, err := client.List("/")
	if err != nil || len(rootEntries) != 1 || rootEntries[0].Name != ".interop-hidden" {
		t.Fatalf("initial FTP directory listing=%#v err=%v; want the hidden fixture", rootEntries, err)
	}

	remoteRoot := "/interop workspace"
	if err := client.MakeDirectory(remoteRoot); err != nil {
		t.Fatalf("create FTP directory with a space: %v", err)
	}
	localSource := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(localSource, []byte("FTP interoperability payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	remoteFile := remoteRoot + "/payload.txt"
	if err := client.Upload(context.Background(), localSource, remoteFile, false, nil); err != nil {
		t.Fatalf("upload through pyftpdlib: %v", err)
	}
	entries, err := client.List(remoteRoot)
	if err != nil || len(entries) != 1 || entries[0].Name != "payload.txt" || entries[0].Size != int64(len("FTP interoperability payload")) {
		t.Fatalf("FTP directory listing=%#v err=%v", entries, err)
	}
	if err := client.SetPermissions(remoteFile, 0o600); err != nil {
		t.Fatalf("change FTP permissions with SITE CHMOD: %v", err)
	}
	entries, err = client.List(remoteRoot)
	if err != nil || len(entries) != 1 || !entries[0].ModeKnown || entries[0].Mode.Perm() != 0o600 {
		t.Fatalf("FTP listing after SITE CHMOD=%#v err=%v; want mode 0600", entries, err)
	}

	content, version, err := client.ReadTextFile(remoteFile, 1024)
	if err != nil || string(content) != "FTP interoperability payload" {
		t.Fatalf("read FTP text content=%q err=%v", content, err)
	}
	if _, err := client.SaveTextFile(context.Background(), remoteFile, []byte("edited over FTP"), version, nil); err != nil {
		t.Fatalf("save FTP text file: %v", err)
	}
	localDownload := filepath.Join(t.TempDir(), "download.txt")
	if err := client.Download(context.Background(), remoteFile, localDownload, false, nil); err != nil {
		t.Fatalf("download through pyftpdlib: %v", err)
	}
	if content, err := os.ReadFile(localDownload); err != nil || string(content) != "edited over FTP" {
		t.Fatalf("downloaded FTP content=%q err=%v", content, err)
	}
	matches, truncated, err := client.Search(context.Background(), "/", "payload", 10)
	if err != nil || truncated || len(matches) != 1 || matches[0].Path != remoteFile {
		t.Fatalf("FTP recursive search matches=%#v truncated=%t err=%v", matches, truncated, err)
	}

	remoteUploadResume := "/resumed-upload.txt"
	localResumeSource := filepath.Join(t.TempDir(), "resume-source.txt")
	if err := os.WriteFile(localResumeSource, []byte("prefix-and-tail"), 0o600); err != nil {
		t.Fatal(err)
	}
	localResumePrefix := filepath.Join(t.TempDir(), "resume-prefix.txt")
	if err := os.WriteFile(localResumePrefix, []byte("prefix"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.Upload(context.Background(), localResumePrefix, ftpUploadPartialPath(remoteUploadResume), false, nil); err != nil {
		t.Fatalf("seed verified FTP upload prefix: %v", err)
	}
	if err := client.Upload(context.Background(), localResumeSource, remoteUploadResume, false, nil); err != nil {
		t.Fatalf("resume FTP upload against pyftpdlib: %v", err)
	}
	localResumeDownload := filepath.Join(t.TempDir(), "resume-download.txt")
	if err := client.Upload(context.Background(), localResumeSource, "/resume-download-source.txt", false, nil); err != nil {
		t.Fatalf("seed FTP download source: %v", err)
	}
	var remoteSize int64
	var remoteModTime time.Time
	err = client.do(context.Background(), func(conn *ftp.ServerConn) error {
		entry, err := getFTPEntry(conn, "/resume-download-source.txt")
		if err != nil {
			return err
		}
		var sizeKnown, timeKnown bool
		remoteSize, sizeKnown, remoteModTime, timeKnown = ftpRemoteFileIdentity(conn, "/resume-download-source.txt", entry)
		if !sizeKnown || !timeKnown {
			return fmt.Errorf("pyftpdlib did not provide a verifiable file identity")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("inspect FTP resume source identity: %v", err)
	}
	if remoteSize == 0 || remoteModTime.IsZero() {
		t.Fatalf("pyftpdlib did not provide usable remote size/time metadata: size=%d mtime=%v", remoteSize, remoteModTime)
	}
	partialPath := ftpDownloadPartialPath(localResumeDownload)
	if err := os.WriteFile(partialPath, []byte("prefix"), 0o600); err != nil {
		t.Fatal(err)
	}
	prefixDigest := sha256.Sum256([]byte("prefix"))
	if err := writeFTPDownloadCheckpoint(ftpDownloadCheckpointPath(partialPath), ftpDownloadCheckpoint{
		Endpoint: client.address, RemotePath: "/resume-download-source.txt", Size: remoteSize,
		ModTime: remoteModTime, Offset: int64(len("prefix")), PrefixSHA256: fmt.Sprintf("%x", prefixDigest),
	}); err != nil {
		t.Fatalf("write verified FTP download checkpoint: %v", err)
	}
	if err := client.Download(context.Background(), "/resume-download-source.txt", localResumeDownload, false, nil); err != nil {
		t.Fatalf("resume FTP download against pyftpdlib: %v", err)
	}
	if content, err := os.ReadFile(localResumeDownload); err != nil || string(content) != "prefix-and-tail" {
		t.Fatalf("resumed FTP download content=%q err=%v", content, err)
	}

	localTree := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(localTree, "nested", "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localTree, "nested", "file.txt"), []byte("recursive FTP"), 0o640); err != nil {
		t.Fatal(err)
	}
	remoteTree := remoteRoot + "/tree"
	if err := client.UploadDirectory(context.Background(), localTree, remoteTree, false, nil); err != nil {
		t.Fatalf("upload FTP directory tree: %v", err)
	}
	localTreeCopy := filepath.Join(t.TempDir(), "downloaded-tree")
	if err := client.DownloadDirectory(context.Background(), remoteTree, localTreeCopy, false, nil); err != nil {
		t.Fatalf("download FTP directory tree: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(localTreeCopy, "nested", "file.txt")); err != nil || string(content) != "recursive FTP" {
		t.Fatalf("recursive FTP download content=%q err=%v", content, err)
	}
	if err := client.RemoveTreeContext(context.Background(), remoteRoot); err != nil {
		t.Fatalf("remove FTP integration tree: %v", err)
	}
	if strings.TrimSpace(os.Getenv("TUI_FTP_INTEROP_REST_LOG")) == "" {
		t.Fatal("TUI_FTP_INTEROP_REST_LOG was not set by the interop runner")
	}
	restLog, err := os.ReadFile(os.Getenv("TUI_FTP_INTEROP_REST_LOG"))
	if err != nil {
		t.Fatalf("read FTP server REST trace: %v", err)
	}
	offsets := strings.Fields(string(restLog))
	if len(offsets) != 2 || offsets[0] != "6" || offsets[1] != "6" {
		t.Fatalf("FTP server REST offsets=%v; want upload and download offsets [6 6]", offsets)
	}
}

func TestFTPClientFXPAgainstPyftpdlib(t *testing.T) {
	if os.Getenv("TUI_FTP_FXP_SOURCE_PORT") == "" {
		t.Skip("set FXP interoperability environment variables by running scripts/verify-ftp-interop.sh")
	}
	sourcePort := interopPort(t, "TUI_FTP_FXP_SOURCE_PORT")
	destinationPort := interopPort(t, "TUI_FTP_FXP_DESTINATION_PORT")
	mode := os.Getenv("TUI_FTP_FXP_CASE")
	if mode != "direct" && mode != "fallback" {
		t.Fatalf("unsupported TUI_FTP_FXP_CASE %q", mode)
	}
	config := func(port int) Config {
		return Config{
			Address: "127.0.0.1", Port: port, Username: "workbench", Password: "interop-test-password",
			TLSMode: TLSOff, Timeout: 10 * time.Second,
		}
	}
	source, err := Dial(context.Background(), config(sourcePort))
	if err != nil {
		t.Fatalf("connect to FXP source pyftpdlib: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })
	sourceEntries, err := source.List("/")
	fixtureFound := false
	for _, entry := range sourceEntries {
		if entry.Name == "fxp-source.txt" {
			fixtureFound = true
		}
	}
	if err != nil || !fixtureFound {
		t.Fatalf("FXP source fixture listing=%#v err=%v", sourceEntries, err)
	}
	if err := os.WriteFile(os.Getenv("TUI_FTP_FXP_SOURCE_TRACE"), nil, 0o600); err != nil {
		t.Fatalf("reset FXP source server trace: %v", err)
	}
	destination, err := Dial(context.Background(), config(destinationPort))
	if err != nil {
		t.Fatalf("connect to FXP destination pyftpdlib: %v", err)
	}
	t.Cleanup(func() { _ = destination.Close() })
	if _, err := destination.List("/"); err != nil {
		t.Fatalf("prime FXP destination listing compatibility: %v", err)
	}
	if err := os.WriteFile(os.Getenv("TUI_FTP_FXP_SOURCE_TRACE"), nil, 0o600); err != nil {
		t.Fatalf("reset FXP source server trace: %v", err)
	}
	if err := source.CopyRemoteFileTo(context.Background(), destination, "/fxp-source.txt", "/copied.txt", false, nil); err != nil {
		t.Fatalf("copy between independent pyftpdlib servers (%s): %v", mode, err)
	}
	content, _, err := destination.ReadTextFile("/copied.txt", 1024)
	if err != nil || string(content) != "real FTP server FXP payload" {
		t.Fatalf("copied FXP content=%q err=%v", content, err)
	}
	tracePath := os.Getenv("TUI_FTP_FXP_SOURCE_TRACE")
	trace, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read FXP source server trace: %v", err)
	}
	traceText := string(trace)
	switch mode {
	case "direct":
		if !strings.Contains(traceText, "PORT_ACCEPTED\n") || !strings.Contains(traceText, "RETR\n") {
			t.Fatalf("source server trace does not show an FXP transfer: %q", traceText)
		}
	case "fallback":
		if !strings.Contains(traceText, "PORT_REJECTED\n") || !strings.Contains(traceText, "RETR\n") {
			t.Fatalf("source server trace does not show FXP rejection and fallback: %q", traceText)
		}
		if !strings.Contains(traceText, "EPSV\n") && !strings.Contains(traceText, "PASV\n") {
			t.Fatalf("source server trace does not show passive local-staging fallback: %q", traceText)
		}
	}
}

func interopPort(t *testing.T, variable string) int {
	t.Helper()
	portValue := os.Getenv(variable)
	if portValue == "" {
		t.Fatalf("set %s by running scripts/verify-ftp-interop.sh", variable)
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		t.Fatalf("invalid %s %q", variable, portValue)
	}
	return port
}
