package ui

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/model"
)

func TestSavedPortForwardRestoresOverSSHAndClosesWithSession(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)

	sshListener, signer := startPortForwardSSHServer(t)
	echoListener := startPortForwardEchoServer(t)
	host := model.Host{
		Id: "forward-integration-host", Kind: model.HostKindSSH, Name: "forward-test",
		IP: "127.0.0.1", Port: sshListener.Addr().(*net.TCPAddr).Port, User: "tester",
	}
	if err := sshclient.TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust SSH test server: %v", err)
	}

	ctx := context.Background()
	store, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateHost(ctx, host); err != nil {
		t.Fatalf("save SSH host: %v", err)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local forward port: %v", err)
	}
	listenAddress := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("release local forward port: %v", err)
	}
	rule, err := store.SavePortForwardRule(ctx, model.PortForwardRule{
		HostId: host.Id, Kind: model.PortForwardLocalTCP,
		ListenAddress: listenAddress, RemoteAddress: echoListener.Addr().String(),
	})
	if err != nil {
		t.Fatalf("save local forwarding rule: %v", err)
	}

	session, err := sshclient.ConnectSFTPWithJumpsAndCredentials(ctx, host, nil, []model.Host{host}, nil)
	if err != nil {
		t.Fatalf("connect to local SSH server: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	pane := &terminalPane{id: "forward-pane", host: host, session: session}
	state := modelState{
		catalog: store,
		tabs:    []*workspaceTab{{id: "forward-tab", root: &paneNode{pane: pane}}},
	}
	command := state.restorePortForwardRules(pane.id, host.Id, session)
	if command == nil {
		t.Fatal("no automatic restore command was scheduled")
	}
	rawMessage := command()
	message, ok := rawMessage.(portForwardRulesRestoredMsg)
	if !ok {
		t.Fatalf("restore command returned %T, want portForwardRulesRestoredMsg", rawMessage)
	}
	if message.err != nil || len(message.results) != 1 || message.results[0].err != nil {
		t.Fatalf("restore result for rule %q = %#v", rule.Id, message)
	}
	updated, _ := state.Update(message)
	state = unwrapModel(t, updated)
	if len(pane.forwards) != 1 || !pane.forwards[0].Active() {
		t.Fatalf("forward listener was not attached to the pane: %#v", pane.forwards)
	}

	connection, err := net.DialTimeout("tcp", pane.forwards[0].LocalAddress(), time.Second)
	if err != nil {
		t.Fatalf("connect to restored local listener: %v", err)
	}
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(connection, "restored through ssh"); err != nil {
		_ = connection.Close()
		t.Fatalf("write through restored listener: %v", err)
	}
	response := make([]byte, len("restored through ssh"))
	if _, err := io.ReadFull(connection, response); err != nil {
		_ = connection.Close()
		t.Fatalf("read through restored listener: %v", err)
	}
	_ = connection.Close()
	if string(response) != "restored through ssh" {
		t.Fatalf("restored forwarding response = %q", response)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close SSH session: %v", err)
	}
	if pane.forwards[0].Active() {
		t.Fatal("restored listener remained active after its SSH session closed")
	}
	if connection, err := net.DialTimeout("tcp", pane.forwards[0].LocalAddress(), 100*time.Millisecond); err == nil {
		_ = connection.Close()
		t.Fatal("local listener still accepted connections after SSH session close")
	}
}

func startPortForwardEchoServer(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for echo target: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = io.Copy(connection, connection)
			}()
		}
	}()
	return listener
}

func startPortForwardSSHServer(t *testing.T) (net.Listener, ssh.Signer) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate SSH test key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("create SSH test signer: %v", err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for SSH test server: %v", err)
	}
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go servePortForwardSSHConnection(connection, config)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener, signer
}

func servePortForwardSSHConnection(connection net.Conn, config *ssh.ServerConfig) {
	_, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		_ = connection.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "direct-tcpip" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only TCP forwarding is enabled in the test server")
			continue
		}
		var destination struct {
			Host       string
			Port       uint32
			OriginHost string
			OriginPort uint32
		}
		if err := ssh.Unmarshal(newChannel.ExtraData(), &destination); err != nil {
			_ = newChannel.Reject(ssh.ConnectionFailed, "invalid direct-tcpip payload")
			continue
		}
		remote, err := net.Dial("tcp", net.JoinHostPort(destination.Host, strconv.Itoa(int(destination.Port))))
		if err != nil {
			_ = newChannel.Reject(ssh.ConnectionFailed, "cannot reach forwarding target")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			_ = remote.Close()
			continue
		}
		go ssh.DiscardRequests(requests)
		go relayPortForwardChannel(channel, remote)
	}
	_ = connection.Close()
}

func relayPortForwardChannel(channel ssh.Channel, remote net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(remote, channel)
		if closer, ok := remote.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(channel, remote)
		_ = channel.CloseWrite()
		done <- struct{}{}
	}()
	<-done
	<-done
	_ = channel.Close()
	_ = remote.Close()
}
