package sshclient

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tui-ssh-workbench/internal/model"
)

func TestSFTPWorkspaceAgainstOpenSSH(t *testing.T) {
	runSFTPWorkspaceAgainstOpenSSH(t, "")
}

func TestSFTPWorkspaceWithOpenSSHUserCertificate(t *testing.T) {
	identityPath := os.Getenv("TUI_SCP_OPENSSH_CERT_IDENTITY")
	if identityPath == "" {
		t.Skip("set TUI_SCP_OPENSSH_CERT_IDENTITY by running scripts/verify-openssh-scp.sh")
	}
	runSFTPWorkspaceAgainstOpenSSH(t, identityPath)
}

func runSFTPWorkspaceAgainstOpenSSH(t *testing.T, identityPath string) {
	portValue := os.Getenv("TUI_SCP_OPENSSH_PORT")
	if portValue == "" {
		t.Skip("set TUI_SCP_OPENSSH_PORT by running scripts/verify-openssh-scp.sh")
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		t.Fatalf("invalid TUI_SCP_OPENSSH_PORT %q", portValue)
	}
	user := os.Getenv("TUI_SCP_OPENSSH_USER")
	hostKeyPath := os.Getenv("TUI_SCP_OPENSSH_HOST_KEY")
	if user == "" || hostKeyPath == "" {
		t.Fatal("TUI_SCP_OPENSSH_USER and TUI_SCP_OPENSSH_HOST_KEY are required")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the OpenSSH SFTP interoperability script targets POSIX sshd")
	}

	privateKey, err := os.ReadFile(hostKeyPath)
	if err != nil {
		t.Fatalf("read temporary OpenSSH host key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		t.Fatalf("parse temporary OpenSSH host key: %v", err)
	}
	host := model.Host{
		Kind: model.HostKindSSH, FileProtocol: model.SSHFileProtocolSFTP,
		Name: "openssh-sftp-test", IP: "127.0.0.1", Port: port, User: user,
		IdentityFile: identityPath,
	}
	if err := TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust temporary OpenSSH host: %v", err)
	}

	session, err := ConnectSFTP(context.Background(), host, "")
	if err != nil {
		t.Fatalf("connect to temporary OpenSSH server: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	client, err := session.OpenSFTP()
	if err != nil {
		t.Fatalf("open OpenSSH SFTP subsystem: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	workingDirectory, err := client.WorkingDirectory()
	if err != nil {
		t.Fatalf("read OpenSSH SFTP working directory: %v", err)
	}
	remoteRoot := path.Join(workingDirectory, ".tui-ssh-sftp-integration-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := client.MakeDirectory(remoteRoot); err != nil {
		t.Fatalf("create OpenSSH SFTP test directory: %v", err)
	}
	t.Cleanup(func() { _ = client.RemoveTree(remoteRoot) })

	localSource := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(localSource, []byte("OpenSSH SFTP workspace"), 0o640); err != nil {
		t.Fatal(err)
	}
	remoteFile := path.Join(remoteRoot, "payload.txt")
	if err := client.Upload(context.Background(), localSource, remoteFile, false, nil); err != nil {
		t.Fatalf("upload through OpenSSH SFTP: %v", err)
	}
	entries, err := client.List(remoteRoot)
	if err != nil || len(entries) != 1 || entries[0].Name != "payload.txt" || entries[0].Mode.Perm() != 0o640 {
		t.Fatalf("OpenSSH SFTP listing=%#v err=%v", entries, err)
	}
	content, version, err := client.ReadTextFile(remoteFile, 1024)
	if err != nil || string(content) != "OpenSSH SFTP workspace" {
		t.Fatalf("read through OpenSSH SFTP: content=%q err=%v", content, err)
	}
	if _, err := client.SaveTextFile(context.Background(), remoteFile, []byte("saved via OpenSSH SFTP"), version, nil); err != nil {
		t.Fatalf("save through OpenSSH SFTP: %v", err)
	}
	if err := client.SetPermissions(remoteFile, 0o600); err != nil {
		t.Fatalf("change OpenSSH SFTP permissions: %v", err)
	}
	localDownload := filepath.Join(t.TempDir(), "downloaded.txt")
	if err := client.Download(context.Background(), remoteFile, localDownload, false, nil); err != nil {
		t.Fatalf("download through OpenSSH SFTP: %v", err)
	}
	if content, err := os.ReadFile(localDownload); err != nil || string(content) != "saved via OpenSSH SFTP" {
		t.Fatalf("downloaded SFTP content=%q err=%v", content, err)
	}

	localTree := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(localTree, "nested", "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localTree, "nested", "file.txt"), []byte("recursive SFTP transfer"), 0o640); err != nil {
		t.Fatal(err)
	}
	remoteTree := path.Join(remoteRoot, "tree")
	if err := client.UploadDirectory(context.Background(), localTree, remoteTree, false, nil); err != nil {
		t.Fatalf("upload recursive directory through OpenSSH SFTP: %v", err)
	}
	localTreeCopy := filepath.Join(t.TempDir(), "downloaded-tree")
	if err := client.DownloadDirectory(context.Background(), remoteTree, localTreeCopy, false, nil); err != nil {
		t.Fatalf("download recursive directory through OpenSSH SFTP: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(localTreeCopy, "nested", "file.txt")); err != nil || string(content) != "recursive SFTP transfer" {
		t.Fatalf("recursive SFTP download content=%q err=%v", content, err)
	}
}
