package sshclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tui-ssh-workbench/internal/model"
)

func TestConnectSFTPWithJumpsForwardsThroughMultiHopChain(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	workingDirectory := t.TempDir()
	targetListener, targetSigner, _ := newSFTPTestServer(t, workingDirectory)
	targetPort := targetListener.Addr().(*net.TCPAddr).Port
	firstListener, firstSigner, firstForwarded := newProxyJumpTestServer(t)
	firstPort := firstListener.Addr().(*net.TCPAddr).Port
	secondListener, secondSigner, secondForwarded := newProxyJumpTestServer(t)
	secondPort := secondListener.Addr().(*net.TCPAddr).Port

	firstJump := model.Host{Id: "jump-a", Kind: model.HostKindSSH, Name: "gateway-a", IP: "127.0.0.1", Port: firstPort, User: "tester"}
	secondJump := model.Host{
		Id: "jump-b", Kind: model.HostKindSSH, Name: "gateway-b", IP: "127.0.0.1", Port: secondPort,
		User: "tester", ProxyJumpList: []string{firstJump.Id},
	}
	target := model.Host{
		Id: "target", Kind: model.HostKindSSH, Name: "application", IP: "127.0.0.1", Port: targetPort,
		User: "tester", ProxyJumpList: []string{secondJump.Id},
	}
	ctx := context.Background()
	connect := func() (*Session, error) {
		return ConnectSFTPWithJumps(ctx, target, "", []model.Host{firstJump, secondJump, target})
	}
	_, err := connect()
	var unknown *UnknownHostKeyError
	if !errors.As(err, &unknown) || unknown.Host.Id != firstJump.Id {
		t.Fatalf("first unknown key should identify jump host %q, got err=%v unknown=%#v", firstJump.Id, err, unknown)
	}
	if err := TrustHost(unknown.Host, firstSigner.PublicKey()); err != nil {
		t.Fatalf("trust first jump host: %v", err)
	}
	_, err = connect()
	if !errors.As(err, &unknown) || unknown.Host.Id != secondJump.Id {
		t.Fatalf("next unknown key should identify jump host %q, got err=%v unknown=%#v", secondJump.Id, err, unknown)
	}
	if err := TrustHost(unknown.Host, secondSigner.PublicKey()); err != nil {
		t.Fatalf("trust second jump host: %v", err)
	}
	_, err = connect()
	if !errors.As(err, &unknown) || unknown.Host.Id != target.Id {
		t.Fatalf("next unknown key should identify target host %q, got err=%v unknown=%#v", target.Id, err, unknown)
	}
	if err := TrustHost(unknown.Host, targetSigner.PublicKey()); err != nil {
		t.Fatalf("trust target host: %v", err)
	}

	session, err := connect()
	if err != nil {
		t.Fatalf("connect to target through jump host: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	client, err := session.OpenSFTP()
	if err != nil {
		t.Fatalf("open SFTP through jump host: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	remotePath, err := client.WorkingDirectory()
	if err != nil {
		t.Fatalf("read SFTP working directory through jump host: %v", err)
	}
	if _, err := client.List(remotePath); err != nil {
		t.Fatalf("list target directory through jump host: %v", err)
	}
	waitForwardedDestination(t, firstForwarded, net.JoinHostPort(secondJump.IP, strconv.Itoa(secondJump.Port)))
	waitForwardedDestination(t, secondForwarded, net.JoinHostPort(target.IP, strconv.Itoa(target.Port)))
}

func waitForwardedDestination(t *testing.T, forwarded <-chan string, expected string) {
	t.Helper()
	select {
	case destination := <-forwarded:
		if destination != expected {
			t.Fatalf("jump forwarded to %q, want %q", destination, expected)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("jump host did not forward to %q", expected)
	}
}

func newProxyJumpTestServer(t *testing.T) (net.Listener, ssh.Signer, <-chan string) {
	return newProxyJumpTestServerConfigured(t, func(config *ssh.ServerConfig) {
		config.NoClientAuth = true
	})
}

func newProxyJumpTestServerConfigured(t *testing.T, configure func(*ssh.ServerConfig)) (net.Listener, ssh.Signer, <-chan string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate jump host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("create jump host signer: %v", err)
	}
	config := &ssh.ServerConfig{}
	configure(config)
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for jump host: %v", err)
	}
	forwarded := make(chan string, 4)
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveProxyJumpConnection(connection, config, forwarded)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener, signer, forwarded
}

func serveProxyJumpConnection(connection net.Conn, config *ssh.ServerConfig, forwarded chan<- string) {
	_, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		_ = connection.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "direct-tcpip" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only TCP forwarding is enabled in this test server")
			continue
		}
		var payload struct {
			Host       string
			Port       uint32
			OriginHost string
			OriginPort uint32
		}
		if err := ssh.Unmarshal(newChannel.ExtraData(), &payload); err != nil {
			_ = newChannel.Reject(ssh.ConnectionFailed, "invalid forwarding payload")
			continue
		}
		destination := net.JoinHostPort(payload.Host, strconv.Itoa(int(payload.Port)))
		target, err := net.DialTimeout("tcp", destination, 2*time.Second)
		if err != nil {
			_ = newChannel.Reject(ssh.ConnectionFailed, "cannot reach target")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			_ = target.Close()
			continue
		}
		go ssh.DiscardRequests(channelRequests)
		select {
		case forwarded <- destination:
		default:
		}
		go relaySSHChannel(channel, target)
	}
	_ = connection.Close()
}

func relaySSHChannel(channel ssh.Channel, target net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(channel, target)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(target, channel)
		done <- struct{}{}
	}()
	<-done
	_ = channel.Close()
	_ = target.Close()
}
