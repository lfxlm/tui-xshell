package sshclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"tui-ssh-workbench/internal/model"
)

func TestConnectSFTPOpensSubsystemWithoutStartingRemoteShell(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	workingDirectory := t.TempDir()
	listener, signer, serverRequests := newSFTPTestServer(t, workingDirectory)
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	host := model.Host{Kind: model.HostKindSSH, Name: "test", IP: "127.0.0.1", Port: port, User: "tester"}
	if err := TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust test SSH host: %v", err)
	}

	session, err := ConnectSFTP(context.Background(), host, "")
	if err != nil {
		t.Fatalf("connect SFTP transport: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	client, err := session.OpenSFTP()
	if err != nil {
		t.Fatalf("open SFTP subsystem: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	remotePath, err := client.WorkingDirectory()
	if err != nil {
		t.Fatalf("read SFTP working directory: %v", err)
	}
	if _, err := client.List(remotePath); err != nil {
		t.Fatalf("list SFTP working directory: %v", err)
	}

	// Opening the SFTP subsystem requires a session channel, but must not send
	// the terminal-only PTY or shell requests.
	select {
	case request := <-serverRequests:
		t.Fatalf("unexpected remote terminal request %q", request)
	default:
	}
}

func newSFTPTestServer(t *testing.T, workingDirectory string) (net.Listener, ssh.Signer, <-chan string) {
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
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for test SSH server: %v", err)
	}
	requests := make(chan string, 8)
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveSFTPTestConnection(connection, serverConfig, workingDirectory, requests)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener, signer, requests
}

func serveSFTPTestConnection(connection net.Conn, config *ssh.ServerConfig, workingDirectory string, specialRequests chan<- string) {
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
				if request.Type == "shell" || request.Type == "pty-req" {
					select {
					case specialRequests <- request.Type:
					default:
					}
					_ = request.Reply(false, nil)
					continue
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
