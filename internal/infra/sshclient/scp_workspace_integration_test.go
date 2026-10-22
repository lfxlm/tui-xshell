package sshclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tui-ssh-workbench/internal/infra/scpclient"
	"tui-ssh-workbench/internal/model"
)

func TestSCPWorkspaceOverAuthenticatedSSHExecChannels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell and OpenSSH scp binary")
	}
	if _, err := exec.LookPath("scp"); err != nil {
		t.Skip("OpenSSH scp binary is not installed")
	}
	if _, err := exec.LookPath("stat"); err != nil {
		t.Skip("POSIX stat command is not installed")
	}
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	remoteRoot := t.TempDir()
	physicalRemoteRoot, err := filepath.EvalSymlinks(remoteRoot)
	if err != nil {
		t.Fatalf("resolve test remote directory: %v", err)
	}
	listener, signer := startSCPWorkspaceServer(t, remoteRoot)
	address := listener.Addr().(*net.TCPAddr)
	host := model.Host{Kind: model.HostKindSSH, FileProtocol: model.SSHFileProtocolSCP, Name: "scp-test", IP: "127.0.0.1", Port: address.Port, User: "tester"}
	if err := TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust test SSH host: %v", err)
	}

	session, err := ConnectSFTP(context.Background(), host, "")
	if err != nil {
		t.Fatalf("connect SCP workspace SSH transport: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	client, err := scpclient.New(func() (scpclient.Command, error) { return session.OpenCommand() })
	if err != nil {
		t.Fatalf("create SCP workspace client: %v", err)
	}

	workingDirectory, err := client.WorkingDirectory()
	if err != nil || workingDirectory != physicalRemoteRoot {
		t.Fatalf("working directory=%q err=%v, want %q", workingDirectory, err, physicalRemoteRoot)
	}
	remoteDirectory := filepath.Join(remoteRoot, "workspace 'one")
	if err := client.MakeDirectory(remoteDirectory); err != nil {
		t.Fatalf("make remote workspace directory: %v", err)
	}
	localSource := filepath.Join(t.TempDir(), "local payload.txt")
	if err := os.WriteFile(localSource, []byte("SCP workspace integration"), 0o640); err != nil {
		t.Fatal(err)
	}
	remoteFile := filepath.Join(remoteDirectory, "uploaded.txt")
	if err := client.Upload(context.Background(), localSource, remoteFile, false, nil); err != nil {
		t.Fatalf("upload through remote SCP client: %v", err)
	}
	localSourceInfo, err := os.Stat(localSource)
	if err != nil {
		t.Fatalf("stat local SCP source: %v", err)
	}
	remoteFileInfo, err := os.Stat(remoteFile)
	if err != nil || !remoteFileInfo.ModTime().Equal(localSourceInfo.ModTime()) {
		t.Fatalf("SCP upload modification time=%v err=%v; want source time %v", remoteFileInfo, err, localSourceInfo.ModTime())
	}
	entries, err := client.List(remoteDirectory)
	if err != nil || len(entries) != 1 || entries[0].Name != "uploaded.txt" || entries[0].Mode.Perm() != 0o640 {
		t.Fatalf("remote listing=%#v err=%v", entries, err)
	}
	content, version, err := client.ReadTextFile(remoteFile, 1024)
	if err != nil || string(content) != "SCP workspace integration" {
		t.Fatalf("read remote text=%q err=%v", content, err)
	}
	updated, err := client.SaveTextFile(context.Background(), remoteFile, []byte("saved through SCP"), version, nil)
	if err != nil || updated.Size != int64(len("saved through SCP")) {
		t.Fatalf("save remote text version=%#v err=%v", updated, err)
	}
	localDownload := filepath.Join(t.TempDir(), "downloaded.txt")
	if err := client.Download(context.Background(), remoteFile, localDownload, false, nil); err != nil {
		t.Fatalf("download through remote SCP client: %v", err)
	}
	downloaded, err := os.ReadFile(localDownload)
	if err != nil || string(downloaded) != "saved through SCP" {
		t.Fatalf("downloaded content=%q err=%v", downloaded, err)
	}
	if err := client.Rename(remoteFile, filepath.Join(remoteDirectory, "renamed.txt")); err != nil {
		t.Fatalf("rename remote file: %v", err)
	}
	if err := client.SetPermissions(filepath.Join(remoteDirectory, "renamed.txt"), 0o600); err != nil {
		t.Fatalf("change remote mode: %v", err)
	}
	if err := client.RemoveTree(remoteDirectory); err != nil {
		t.Fatalf("remove remote workspace directory: %v", err)
	}
	if exists, err := client.Exists(remoteDirectory); err != nil || exists {
		t.Fatalf("remote directory after removal exists=%t err=%v", exists, err)
	}

	localTree := filepath.Join(t.TempDir(), "source-tree")
	if err := os.MkdirAll(filepath.Join(localTree, "nested", "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localTree, "nested", "tree.txt"), []byte("recursive SCP"), 0o640); err != nil {
		t.Fatal(err)
	}
	remoteTree := filepath.Join(physicalRemoteRoot, "source-tree")
	if err := client.UploadDirectory(context.Background(), localTree, remoteTree, false, nil); err != nil {
		t.Fatalf("upload remote directory: %v", err)
	}
	localTreeCopy := filepath.Join(t.TempDir(), "download-tree")
	if err := client.DownloadDirectory(context.Background(), remoteTree, localTreeCopy, false, nil); err != nil {
		t.Fatalf("download remote directory: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(localTreeCopy, "nested", "tree.txt")); err != nil || string(content) != "recursive SCP" {
		t.Fatalf("downloaded tree file=%q err=%v", content, err)
	}
	remoteTreeCopy := filepath.Join(physicalRemoteRoot, "remote-copy")
	if err := client.CopyRemoteDirectoryTo(context.Background(), client, remoteTree, remoteTreeCopy, false, nil); err != nil {
		t.Fatalf("copy remote directory through local staging: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(remoteTreeCopy, "nested", "tree.txt")); err != nil || string(content) != "recursive SCP" {
		t.Fatalf("copied remote tree file=%q err=%v", content, err)
	}
}

func TestSCPWorkspaceAgainstOpenSSH(t *testing.T) {
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
		t.Skip("the SCP workspace adapter targets POSIX OpenSSH servers")
	}

	privateKey, err := os.ReadFile(hostKeyPath)
	if err != nil {
		t.Fatalf("read temporary OpenSSH host key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		t.Fatalf("parse temporary OpenSSH host key: %v", err)
	}
	host := model.Host{Kind: model.HostKindSSH, FileProtocol: model.SSHFileProtocolSCP, Name: "openssh-scp-test", IP: "127.0.0.1", Port: port, User: user}
	if err := TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust temporary OpenSSH host: %v", err)
	}

	session, err := ConnectSFTP(context.Background(), host, "")
	if err != nil {
		t.Fatalf("connect to temporary OpenSSH server: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	client, err := scpclient.New(func() (scpclient.Command, error) { return session.OpenCommand() })
	if err != nil {
		t.Fatalf("create SCP workspace client: %v", err)
	}
	workingDirectory, err := client.WorkingDirectory()
	if err != nil {
		t.Fatalf("read OpenSSH working directory: %v", err)
	}
	remoteRoot := path.Join(workingDirectory, fmt.Sprintf(".tui-ssh-scp-integration-%d", time.Now().UnixNano()))
	if err := client.MakeDirectory(remoteRoot); err != nil {
		t.Fatalf("create OpenSSH test directory: %v", err)
	}
	t.Cleanup(func() { _ = client.RemoveTree(remoteRoot) })

	localSource := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(localSource, []byte("OpenSSH SCP workspace"), 0o640); err != nil {
		t.Fatal(err)
	}
	remoteFile := path.Join(remoteRoot, "payload.txt")
	if err := client.Upload(context.Background(), localSource, remoteFile, false, nil); err != nil {
		t.Fatalf("upload through OpenSSH: %v", err)
	}

	resumePayload := make([]byte, 256*1024+19)
	for index := range resumePayload {
		resumePayload[index] = byte((index*37 + index/11) % 251)
	}
	resumeSource := filepath.Join(t.TempDir(), "resume-payload.bin")
	if err := os.WriteFile(resumeSource, resumePayload, 0o600); err != nil {
		t.Fatalf("write SCP resume source: %v", err)
	}
	resumeRoot := path.Join(remoteRoot, "resume")
	if err := client.MakeDirectory(resumeRoot); err != nil {
		t.Fatalf("create SCP resume test directory: %v", err)
	}
	resumeTarget := path.Join(resumeRoot, "resume-payload.bin")
	resumeContext, cancelResume := context.WithCancel(context.Background())
	interruptedErr := client.Upload(resumeContext, resumeSource, resumeTarget, false, func(current, _ int64) {
		if current >= 128*1024 {
			cancelResume()
		}
	})
	cancelResume()
	if !errors.Is(interruptedErr, context.Canceled) {
		t.Fatalf("interrupted OpenSSH SCP upload error=%v, want context.Canceled", interruptedErr)
	}
	resumeEntries, err := client.List(resumeRoot)
	if err != nil {
		t.Fatalf("list OpenSSH SCP resume partial: %v", err)
	}
	var resumeOffset int64 = -1
	for _, entry := range resumeEntries {
		if strings.HasPrefix(entry.Name, ".tui-scp-") && strings.HasSuffix(entry.Name, "-00.part") {
			resumeOffset = entry.Size
			break
		}
	}
	if resumeOffset < 64*1024 || resumeOffset >= int64(len(resumePayload)) {
		t.Fatalf("OpenSSH SCP partial size=%d; want a non-empty incomplete partial", resumeOffset)
	}
	var resumedFrom int64 = -1
	if err := client.Upload(context.Background(), resumeSource, resumeTarget, false, func(current, _ int64) {
		if resumedFrom < 0 {
			resumedFrom = current
		}
	}); err != nil {
		t.Fatalf("resume OpenSSH SCP upload: %v", err)
	}
	if resumedFrom != resumeOffset {
		t.Fatalf("OpenSSH SCP resumed at %d bytes, want %d", resumedFrom, resumeOffset)
	}
	resumeDownload := filepath.Join(t.TempDir(), "resume-downloaded.bin")
	if err := client.Download(context.Background(), resumeTarget, resumeDownload, false, nil); err != nil {
		t.Fatalf("verify resumed OpenSSH SCP upload: %v", err)
	}
	resumedContent, err := os.ReadFile(resumeDownload)
	if err != nil || !bytes.Equal(resumedContent, resumePayload) {
		t.Fatalf("resumed OpenSSH SCP content mismatch: read error %v", err)
	}

	downloadingSource := path.Join(resumeRoot, "download-resume.bin")
	if err := os.WriteFile(filepath.Join(remoteRoot, "resume", "download-resume.bin"), resumePayload, 0o640); err != nil {
		t.Fatalf("write OpenSSH SCP download-resume source: %v", err)
	}
	downloadingDirectory := t.TempDir()
	downloadingTarget := filepath.Join(downloadingDirectory, "download-resume.bin")
	interruptDownloadContext, cancelDownload := context.WithCancel(context.Background())
	interruptedDownloadErr := client.Download(interruptDownloadContext, downloadingSource, downloadingTarget, false, func(current, _ int64) {
		if current >= 128*1024 {
			cancelDownload()
		}
	})
	cancelDownload()
	if !errors.Is(interruptedDownloadErr, context.Canceled) {
		t.Fatalf("interrupted OpenSSH SCP download error=%v, want context.Canceled", interruptedDownloadErr)
	}
	partialEntries, err := os.ReadDir(downloadingDirectory)
	if err != nil {
		t.Fatalf("list OpenSSH SCP download partial: %v", err)
	}
	var downloadOffset int64 = -1
	for _, entry := range partialEntries {
		if strings.HasPrefix(entry.Name(), ".tui-scp-download-") && strings.HasSuffix(entry.Name(), "-00.part") {
			partialInfo, statErr := entry.Info()
			if statErr != nil {
				t.Fatalf("stat OpenSSH SCP download partial: %v", statErr)
			}
			downloadOffset = partialInfo.Size()
			break
		}
	}
	if downloadOffset < 64*1024 || downloadOffset >= int64(len(resumePayload)) {
		t.Fatalf("OpenSSH SCP download partial size=%d; want a non-empty incomplete partial", downloadOffset)
	}
	var resumedDownloadFrom int64 = -1
	if err := client.Download(context.Background(), downloadingSource, downloadingTarget, false, func(current, _ int64) {
		if resumedDownloadFrom < 0 {
			resumedDownloadFrom = current
		}
	}); err != nil {
		t.Fatalf("resume OpenSSH SCP download: %v", err)
	}
	if resumedDownloadFrom != downloadOffset {
		t.Fatalf("OpenSSH SCP download resumed at %d bytes, want %d", resumedDownloadFrom, downloadOffset)
	}
	resumedDownloadContent, err := os.ReadFile(downloadingTarget)
	if err != nil || !bytes.Equal(resumedDownloadContent, resumePayload) {
		t.Fatalf("resumed OpenSSH SCP download content mismatch: read error %v", err)
	}

	entries, err := client.List(remoteRoot)
	if err != nil || len(entries) != 2 || entries[0].Name != "payload.txt" || entries[0].Mode.Perm() != 0o640 || entries[1].Name != "resume" || !entries[1].IsDir {
		t.Fatalf("OpenSSH directory listing=%#v err=%v", entries, err)
	}
	content, version, err := client.ReadTextFile(remoteFile, 1024)
	if err != nil || string(content) != "OpenSSH SCP workspace" {
		t.Fatalf("read through OpenSSH: content=%q err=%v", content, err)
	}
	if _, err := client.SaveTextFile(context.Background(), remoteFile, []byte("saved via OpenSSH"), version, nil); err != nil {
		t.Fatalf("save through OpenSSH: %v", err)
	}
	localDownload := filepath.Join(t.TempDir(), "downloaded.txt")
	if err := client.Download(context.Background(), remoteFile, localDownload, false, nil); err != nil {
		t.Fatalf("download through OpenSSH: %v", err)
	}
	if content, err := os.ReadFile(localDownload); err != nil || string(content) != "saved via OpenSSH" {
		t.Fatalf("downloaded content=%q err=%v", content, err)
	}

	localTree := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(localTree, "nested", "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	recursivePayload := make([]byte, 512*1024+31)
	for index := range recursivePayload {
		recursivePayload[index] = byte((index*19 + index/7) % 253)
	}
	if err := os.WriteFile(filepath.Join(localTree, "nested", "file.txt"), recursivePayload, 0o640); err != nil {
		t.Fatal(err)
	}
	remoteTree := path.Join(remoteRoot, "tree")
	if err := client.UploadDirectory(context.Background(), localTree, remoteTree, false, nil); err != nil {
		t.Fatalf("upload recursive directory through OpenSSH: %v", err)
	}
	localTreeCopy := filepath.Join(t.TempDir(), "downloaded-tree")
	interruptDirectoryContext, cancelDirectoryDownload := context.WithCancel(context.Background())
	interruptedDirectoryErr := client.DownloadDirectory(interruptDirectoryContext, remoteTree, localTreeCopy, false, func(current, _ int64) {
		if current >= 128*1024 {
			cancelDirectoryDownload()
		}
	})
	cancelDirectoryDownload()
	if !errors.Is(interruptedDirectoryErr, context.Canceled) {
		t.Fatalf("interrupted recursive OpenSSH SCP download error=%v, want context.Canceled", interruptedDirectoryErr)
	}
	partialEntries, err = os.ReadDir(filepath.Join(localTreeCopy, "nested"))
	if err != nil {
		t.Fatalf("list recursive OpenSSH SCP partial: %v", err)
	}
	var recursiveOffset int64 = -1
	for _, entry := range partialEntries {
		if strings.HasPrefix(entry.Name(), ".tui-scp-download-") && strings.HasSuffix(entry.Name(), "-00.part") {
			partialInfo, statErr := entry.Info()
			if statErr != nil {
				t.Fatalf("stat recursive OpenSSH SCP partial: %v", statErr)
			}
			recursiveOffset = partialInfo.Size()
			break
		}
	}
	if recursiveOffset < 64*1024 || recursiveOffset >= int64(len(recursivePayload)) {
		t.Fatalf("recursive OpenSSH SCP partial size=%d; want a non-empty incomplete partial", recursiveOffset)
	}
	var recursiveResumedFrom int64 = -1
	recursiveProgressEvents := 0
	if err := client.DownloadDirectory(context.Background(), remoteTree, localTreeCopy, false, func(current, _ int64) {
		recursiveProgressEvents++
		if recursiveProgressEvents == 2 {
			recursiveResumedFrom = current
		}
	}); err != nil {
		t.Fatalf("resume recursive directory through OpenSSH: %v", err)
	}
	if recursiveResumedFrom != recursiveOffset {
		t.Fatalf("recursive OpenSSH SCP resumed at %d bytes, want %d", recursiveResumedFrom, recursiveOffset)
	}
	if content, err := os.ReadFile(filepath.Join(localTreeCopy, "nested", "file.txt")); err != nil || !bytes.Equal(content, recursivePayload) {
		t.Fatalf("recursive resumed download content mismatch: read error %v", err)
	}
}

func startSCPWorkspaceServer(t *testing.T, workingDirectory string) (net.Listener, ssh.Signer) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate SSH host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("create SSH host signer: %v", err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for SCP workspace SSH test server: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveSCPWorkspaceConnection(connection, serverConfig, workingDirectory)
		}
	}()
	return listener, signer
}

func serveSCPWorkspaceConnection(connection net.Conn, config *ssh.ServerConfig, workingDirectory string) {
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
		go serveSCPWorkspaceChannel(channel, channelRequests, workingDirectory)
	}
	_ = connection.Close()
}

func serveSCPWorkspaceChannel(channel ssh.Channel, requests <-chan *ssh.Request, workingDirectory string) {
	defer channel.Close()
	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
			_ = request.Reply(false, nil)
			return
		}
		if err := request.Reply(true, nil); err != nil {
			return
		}
		command := exec.Command("/bin/sh", "-c", payload.Command)
		command.Dir = workingDirectory
		stdin, err := command.StdinPipe()
		if err != nil {
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: 1}))
			return
		}
		stdout, err := command.StdoutPipe()
		if err != nil {
			_ = stdin.Close()
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: 1}))
			return
		}
		stderr, err := command.StderrPipe()
		if err != nil {
			_ = stdin.Close()
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: 1}))
			return
		}
		if err := command.Start(); err != nil {
			_ = stdin.Close()
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: 1}))
			return
		}
		stdinDone := make(chan struct{})
		go func() {
			_, _ = io.Copy(stdin, channel)
			_ = stdin.Close()
			close(stdinDone)
		}()
		stdoutDone := make(chan struct{})
		go func() {
			_, _ = io.Copy(channel, stdout)
			_ = channel.CloseWrite()
			close(stdoutDone)
		}()
		stderrDone := make(chan struct{})
		go func() {
			_, _ = io.Copy(channel.Stderr(), stderr)
			close(stderrDone)
		}()
		<-stdoutDone
		<-stderrDone
		<-stdinDone
		waitErr := command.Wait()
		status := uint32(0)
		if waitErr != nil {
			var exitError *exec.ExitError
			if errors.As(waitErr, &exitError) {
				status = uint32(exitError.ExitCode())
			} else {
				status = 1
			}
		}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: status}))
		return
	}
}
