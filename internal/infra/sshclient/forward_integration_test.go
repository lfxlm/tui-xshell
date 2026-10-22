package sshclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tui-ssh-workbench/internal/model"
)

func TestLocalForwardRelaysDataAndClosesWithSession(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "tui-ssh-workbench"), 0o700); err != nil {
		t.Fatal(err)
	}

	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for echo server: %v", err)
	}
	t.Cleanup(func() { _ = echoListener.Close() })
	go func() {
		for {
			connection, acceptErr := echoListener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = io.Copy(connection, connection)
			}()
		}
	}()

	listener, signer, _ := newProxyJumpTestServer(t)
	host := model.Host{
		Id: "forward-target", Kind: model.HostKindSSH, Name: "forward-target",
		IP: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "tester",
	}
	_, err = ConnectSFTP(context.Background(), host, "")
	var unknown *UnknownHostKeyError
	if !errors.As(err, &unknown) {
		t.Fatalf("first connection should request host trust, got %v", err)
	}
	if err := TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust SSH host: %v", err)
	}
	session, err := ConnectSFTP(context.Background(), host, "")
	if err != nil {
		t.Fatalf("connect to SSH host: %v", err)
	}
	if err := sendKeepalive(session.done, session.client, time.Second); err != nil {
		_ = session.Close()
		t.Fatalf("SSH keepalive request failed on a live connection: %v", err)
	}

	forward, err := session.ForwardLocal("127.0.0.1:0", echoListener.Addr().String())
	if err != nil {
		_ = session.Close()
		t.Fatalf("start local forward: %v", err)
	}
	local, err := net.DialTimeout("tcp", forward.LocalAddress(), time.Second)
	if err != nil {
		_ = session.Close()
		t.Fatalf("connect to local listener %s: %v", forward.LocalAddress(), err)
	}
	_ = local.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(local, "hello over SSH"); err != nil {
		_ = local.Close()
		_ = session.Close()
		t.Fatalf("write through forward: %v", err)
	}
	response := make([]byte, len("hello over SSH"))
	if _, err := io.ReadFull(local, response); err != nil {
		_ = local.Close()
		_ = session.Close()
		t.Fatalf("read through forward: %v", err)
	}
	if string(response) != "hello over SSH" {
		t.Fatalf("forwarded response = %q, want %q", response, "hello over SSH")
	}
	var channel ForwardChannelInfo
	channelDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(channelDeadline) {
		channels := forward.Channels()
		if len(channels) == 1 && channels[0].Target == echoListener.Addr().String() &&
			channels[0].BytesToTarget >= uint64(len("hello over SSH")) &&
			channels[0].BytesFromTarget >= uint64(len("hello over SSH")) {
			channel = channels[0]
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if channel.ID == 0 {
		t.Fatalf("active forwarding channel was not tracked with byte counts: %#v", forward.Channels())
	}
	if err := forward.CloseChannel(channel.ID); err != nil {
		t.Fatalf("close one forwarded channel: %v", err)
	}
	if _, err := local.Read(make([]byte, 1)); err == nil {
		t.Fatal("client connection remained open after its forwarded channel was closed")
	}
	if !forward.Active() {
		t.Fatal("closing one forwarded channel stopped the forwarding listener")
	}
	_ = local.Close()
	second, err := net.DialTimeout("tcp", forward.LocalAddress(), time.Second)
	if err != nil {
		t.Fatalf("listener did not accept a new connection after channel closure: %v", err)
	}
	_ = second.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(second, "listener still active"); err != nil {
		t.Fatalf("write through listener after closing one channel: %v", err)
	}
	secondResponse := make([]byte, len("listener still active"))
	if _, err := io.ReadFull(second, secondResponse); err != nil || string(secondResponse) != "listener still active" {
		t.Fatalf("read after closing one channel = %q, %v", secondResponse, err)
	}
	_ = second.Close()

	if err := session.Close(); err != nil {
		t.Fatalf("close SSH session: %v", err)
	}
	if _, err := net.DialTimeout("tcp", forward.LocalAddress(), 100*time.Millisecond); err == nil {
		t.Fatal("local forward listener remained open after its SSH session closed")
	}
}

func TestSOCKS5ForwardRelaysDomainConnectAndClosesWithSession(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "tui-ssh-workbench"), 0o700); err != nil {
		t.Fatal(err)
	}

	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for echo server: %v", err)
	}
	t.Cleanup(func() { _ = echoListener.Close() })
	go func() {
		for {
			connection, acceptErr := echoListener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = io.Copy(connection, connection)
			}()
		}
	}()

	listener, signer, forwarded := newProxyJumpTestServer(t)
	host := model.Host{
		Id: "socks-target", Kind: model.HostKindSSH, Name: "socks-target",
		IP: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "tester",
	}
	_, err = ConnectSFTP(context.Background(), host, "")
	var unknown *UnknownHostKeyError
	if !errors.As(err, &unknown) {
		t.Fatalf("first connection should request host trust, got %v", err)
	}
	if err := TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust SSH host: %v", err)
	}
	session, err := ConnectSFTP(context.Background(), host, "")
	if err != nil {
		t.Fatalf("connect to SSH host: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	forward, err := session.ForwardSOCKS5("127.0.0.1:0")
	if err != nil {
		_ = session.Close()
		t.Fatalf("start SOCKS5 proxy: %v", err)
	}

	proxy, err := net.DialTimeout("tcp", forward.LocalAddress(), time.Second)
	if err != nil {
		_ = session.Close()
		t.Fatalf("connect to SOCKS5 listener: %v", err)
	}
	defer proxy.Close()
	_ = proxy.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := proxy.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("send SOCKS5 methods: %v", err)
	}
	var methodReply [2]byte
	if _, err := io.ReadFull(proxy, methodReply[:]); err != nil || methodReply != [2]byte{5, 0} {
		t.Fatalf("SOCKS5 method reply = %v, %v; want [5 0]", methodReply, err)
	}

	target := echoListener.Addr().(*net.TCPAddr)
	targetHost := "127.0.0.1"
	request := []byte{5, 1, 0, 3, byte(len(targetHost))}
	request = append(request, targetHost...)
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], uint16(target.Port))
	request = append(request, port[:]...)
	if _, err := proxy.Write(request); err != nil {
		t.Fatalf("send SOCKS5 connect request: %v", err)
	}
	var connectReply [10]byte
	if _, err := io.ReadFull(proxy, connectReply[:]); err != nil || connectReply[1] != 0 {
		t.Fatalf("SOCKS5 connect reply = %v, %v; want success", connectReply, err)
	}
	if _, err := io.WriteString(proxy, "through SOCKS over SSH"); err != nil {
		t.Fatalf("write through SOCKS proxy: %v", err)
	}
	response := make([]byte, len("through SOCKS over SSH"))
	if _, err := io.ReadFull(proxy, response); err != nil || string(response) != "through SOCKS over SSH" {
		t.Fatalf("SOCKS response = %q, %v", response, err)
	}
	_ = proxy.Close()
	waitForwardedDestination(t, forwarded, net.JoinHostPort(targetHost, strconv.Itoa(target.Port)))

	if err := session.Close(); err != nil {
		t.Fatalf("close SSH session: %v", err)
	}
	if forward.Active() {
		t.Fatal("SOCKS5 forward remained active after its SSH session closed")
	}
	if _, err := net.DialTimeout("tcp", forward.LocalAddress(), 100*time.Millisecond); err == nil {
		t.Fatal("SOCKS5 listener remained open after its SSH session closed")
	}
}

func TestRemoteForwardRelaysToLocalTargetAndCanStop(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "tui-ssh-workbench"), 0o700); err != nil {
		t.Fatal(err)
	}
	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for local echo service: %v", err)
	}
	t.Cleanup(func() { _ = echoListener.Close() })
	go func() {
		for {
			connection, acceptErr := echoListener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = io.Copy(connection, connection)
			}()
		}
	}()

	listener, signer := newRemoteForwardTestServer(t)
	host := model.Host{
		Id: "remote-forward-target", Kind: model.HostKindSSH, Name: "remote-forward-target",
		IP: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "tester",
	}
	_, err = ConnectSFTP(context.Background(), host, "")
	var unknown *UnknownHostKeyError
	if !errors.As(err, &unknown) {
		t.Fatalf("first connection should request host trust, got %v", err)
	}
	if err := TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust SSH host: %v", err)
	}
	session, err := ConnectSFTP(context.Background(), host, "")
	if err != nil {
		t.Fatalf("connect to SSH host: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	forward, err := session.ForwardRemote("127.0.0.1:0", echoListener.Addr().String())
	if err != nil {
		t.Fatalf("start remote port forward: %v", err)
	}
	if forward.Kind() != ForwardRemoteTCP || forward.RemoteAddress() != echoListener.Addr().String() {
		t.Fatalf("remote forward metadata is wrong: kind=%v target=%s", forward.Kind(), forward.RemoteAddress())
	}

	connection, err := net.DialTimeout("tcp", forward.ListenAddress(), time.Second)
	if err != nil {
		t.Fatalf("connect to remote listener %s: %v", forward.ListenAddress(), err)
	}
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(connection, "back through SSH"); err != nil {
		t.Fatalf("write through remote forward: %v", err)
	}
	response := make([]byte, len("back through SSH"))
	if _, err := io.ReadFull(connection, response); err != nil || string(response) != "back through SSH" {
		t.Fatalf("remote forward response = %q, %v", response, err)
	}
	_ = connection.Close()
	if err := forward.Close(); err != nil {
		t.Fatalf("stop remote forward: %v", err)
	}
	if forward.Active() {
		t.Fatal("remote forward remained active after Close")
	}
	if _, err := net.DialTimeout("tcp", forward.ListenAddress(), 100*time.Millisecond); err == nil {
		t.Fatal("remote listener remained open after the forward was stopped")
	}
}

func newRemoteForwardTestServer(t *testing.T) (net.Listener, ssh.Signer) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate remote-forward host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("create remote-forward host signer: %v", err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for remote-forward SSH server: %v", err)
	}
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveRemoteForwardConnection(connection, config)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener, signer
}

func serveRemoteForwardConnection(connection net.Conn, config *ssh.ServerConfig) {
	server, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		_ = connection.Close()
		return
	}
	go func() {
		for channel := range channels {
			_ = channel.Reject(ssh.UnknownChannelType, "unexpected client channel")
		}
	}()
	var forwardMu sync.Mutex
	forwardListeners := make(map[string]net.Listener)
	defer func() {
		forwardMu.Lock()
		for _, forwardListener := range forwardListeners {
			_ = forwardListener.Close()
		}
		forwardMu.Unlock()
		_ = server.Close()
	}()
	for request := range requests {
		switch request.Type {
		case "tcpip-forward":
			var payload struct {
				Address string
				Port    uint32
			}
			if err := ssh.Unmarshal(request.Payload, &payload); err != nil || payload.Port > 65535 {
				_ = request.Reply(false, nil)
				continue
			}
			bindAddress := payload.Address
			if bindAddress == "" {
				bindAddress = "127.0.0.1"
			}
			remoteListener, listenErr := net.Listen("tcp", net.JoinHostPort(bindAddress, strconv.Itoa(int(payload.Port))))
			if listenErr != nil {
				_ = request.Reply(false, nil)
				continue
			}
			remoteTCP := remoteListener.Addr().(*net.TCPAddr)
			forwardKey := net.JoinHostPort(payload.Address, strconv.Itoa(remoteTCP.Port))
			forwardMu.Lock()
			forwardListeners[forwardKey] = remoteListener
			forwardMu.Unlock()
			var response []byte
			if payload.Port == 0 {
				response = ssh.Marshal(struct{ Port uint32 }{Port: uint32(remoteTCP.Port)})
			}
			if err := request.Reply(true, response); err != nil {
				_ = remoteListener.Close()
			}
			go serveForwardedTCPConnections(remoteListener, server, remoteTCP)
		case "cancel-tcpip-forward":
			var payload struct {
				Address string
				Port    uint32
			}
			if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
				_ = request.Reply(false, nil)
				continue
			}
			key := net.JoinHostPort(payload.Address, strconv.Itoa(int(payload.Port)))
			forwardMu.Lock()
			remoteListener := forwardListeners[key]
			delete(forwardListeners, key)
			forwardMu.Unlock()
			if remoteListener == nil {
				_ = request.Reply(false, nil)
				continue
			}
			_ = remoteListener.Close()
			_ = request.Reply(true, nil)
		default:
			_ = request.Reply(false, nil)
		}
	}
}

func serveForwardedTCPConnections(listener net.Listener, server *ssh.ServerConn, listenAddress *net.TCPAddr) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer connection.Close()
			origin, ok := connection.RemoteAddr().(*net.TCPAddr)
			if !ok {
				return
			}
			payload := struct {
				Address       string
				Port          uint32
				OriginAddress string
				OriginPort    uint32
			}{
				Address: listenAddress.IP.String(), Port: uint32(listenAddress.Port),
				OriginAddress: origin.IP.String(), OriginPort: uint32(origin.Port),
			}
			channel, requests, err := server.OpenChannel("forwarded-tcpip", ssh.Marshal(payload))
			if err != nil {
				return
			}
			go ssh.DiscardRequests(requests)
			relaySSHChannel(channel, connection)
		}()
	}
}
