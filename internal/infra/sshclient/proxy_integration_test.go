package sshclient

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tui-ssh-workbench/internal/model"
)

func TestDialSSHClientUsesHTTPConnectProxyForInitialHop(t *testing.T) {
	sshListener, _, _ := newProxyJumpTestServer(t)
	targetPort := sshListener.Addr().(*net.TCPAddr).Port
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen HTTP proxy: %v", err)
	}
	t.Cleanup(func() { _ = proxyListener.Close() })
	proxyRequest := make(chan string, 1)
	go func() {
		connection, acceptErr := proxyListener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(connection)
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "CONNECT" || fields[2] != "HTTP/1.1" {
			return
		}
		for {
			header, headerErr := reader.ReadString('\n')
			if headerErr != nil || header == "\r\n" {
				if headerErr != nil {
					return
				}
				break
			}
		}
		target, err := net.DialTimeout("tcp", fields[1], time.Second)
		if err != nil {
			return
		}
		defer target.Close()
		if _, err := io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		proxyRequest <- fields[1]
		copyDone := make(chan struct{})
		go func() {
			_, _ = io.Copy(target, reader)
			_ = target.Close()
			close(copyDone)
		}()
		_, _ = io.Copy(connection, target)
		_ = connection.Close()
		<-copyDone
	}()

	host := model.Host{Kind: model.HostKindSSH, Name: "proxied", IP: "localhost", Port: targetPort, User: "tester"}
	options := DefaultConnectOptions()
	options.Proxy = ProxyOptions{Type: ProxyTypeHTTP, Address: proxyListener.Addr().String()}
	client, err := dialSSHClient(context.Background(), host, nil, "", ssh.InsecureIgnoreHostKey(), nil, options)
	if err != nil {
		t.Fatalf("SSH handshake through HTTP CONNECT proxy: %v", err)
	}
	_ = client.Close()
	select {
	case destination := <-proxyRequest:
		if destination != net.JoinHostPort(host.IP, strconv.Itoa(host.Port)) {
			t.Fatalf("proxy CONNECT target = %q, want %q", destination, net.JoinHostPort(host.IP, strconv.Itoa(host.Port)))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("HTTP proxy did not receive CONNECT request for port %d", targetPort)
	}
}

func TestDialSSHClientUsesSOCKS4ProxyForInitialHop(t *testing.T) {
	sshListener, _, _ := newProxyJumpTestServer(t)
	targetPort := sshListener.Addr().(*net.TCPAddr).Port
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen SOCKS4 proxy: %v", err)
	}
	t.Cleanup(func() { _ = proxyListener.Close() })
	type socks4Request struct {
		destination string
		userID      string
	}
	proxyRequest := make(chan socks4Request, 1)
	go func() {
		connection, acceptErr := proxyListener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
		var request [8]byte
		if _, readErr := io.ReadFull(connection, request[:]); readErr != nil || request[0] != 4 || request[1] != 1 {
			return
		}
		userID := make([]byte, 0, 32)
		for {
			var character [1]byte
			if _, readErr := io.ReadFull(connection, character[:]); readErr != nil {
				return
			}
			if character[0] == 0 {
				break
			}
			userID = append(userID, character[0])
		}
		destination := net.JoinHostPort(net.IP(request[4:8]).String(), strconv.Itoa(int(binary.BigEndian.Uint16(request[2:4]))))
		target, dialErr := net.DialTimeout("tcp", destination, time.Second)
		if dialErr != nil {
			_, _ = connection.Write([]byte{0, 91, 0, 0, 0, 0, 0, 0})
			return
		}
		defer target.Close()
		if _, writeErr := connection.Write([]byte{0, 90, 0, 0, 0, 0, 0, 0}); writeErr != nil {
			return
		}
		proxyRequest <- socks4Request{destination: destination, userID: string(userID)}
		copyDone := make(chan struct{})
		go func() {
			_, _ = io.Copy(target, connection)
			_ = target.Close()
			close(copyDone)
		}()
		_, _ = io.Copy(connection, target)
		_ = connection.Close()
		<-copyDone
	}()

	host := model.Host{Kind: model.HostKindSSH, Name: "proxied", IP: "127.0.0.1", Port: targetPort, User: "tester"}
	options := DefaultConnectOptions()
	options.ConnectTimeout = 2 * time.Second
	options.Proxy = ProxyOptions{Type: ProxyTypeSOCKS4, Address: proxyListener.Addr().String(), Username: "workbench"}
	client, err := dialSSHClient(context.Background(), host, nil, "", ssh.InsecureIgnoreHostKey(), nil, options)
	if err != nil {
		t.Fatalf("SSH handshake through SOCKS4 proxy: %v", err)
	}
	_ = client.Close()
	select {
	case request := <-proxyRequest:
		wantDestination := net.JoinHostPort("127.0.0.1", strconv.Itoa(host.Port))
		if request.destination != wantDestination || request.userID != "workbench" {
			t.Fatalf("SOCKS4 request = %#v; want destination %s and user ID %q", request, wantDestination, "workbench")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("SOCKS4 proxy did not receive CONNECT request for port %d", targetPort)
	}
}
