package sshclient

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDialSSHProxyHTTPConnectSupportsBasicAuthAndBufferedTunnelData(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen HTTP proxy: %v", err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		reader := bufio.NewReader(connection)
		requestLine, readErr := reader.ReadString('\n')
		if readErr != nil {
			serverErr <- readErr
			return
		}
		if requestLine != "CONNECT ssh.example.test:2222 HTTP/1.1\r\n" {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		var authorization string
		for {
			line, lineErr := reader.ReadString('\n')
			if lineErr != nil {
				serverErr <- lineErr
				return
			}
			if line == "\r\n" {
				break
			}
			if strings.HasPrefix(strings.ToLower(line), "proxy-authorization:") {
				authorization = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			}
		}
		wantAuthorization := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-secret"))
		if authorization != wantAuthorization {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		if _, writeErr := io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\nSSH-2.0-test-server\r\n"); writeErr != nil {
			serverErr <- writeErr
			return
		}
		probe := make([]byte, 5)
		if _, readErr = io.ReadFull(reader, probe); readErr != nil {
			serverErr <- readErr
			return
		}
		if string(probe) != "probe" {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		_, writeErr := connection.Write([]byte("reply"))
		serverErr <- writeErr
	}()

	proxy, err := dialSSHProxy(context.Background(), ProxyOptions{
		Type: ProxyTypeHTTP, Address: listener.Addr().String(), Username: "proxy-user", Password: "proxy-secret",
	}, "ssh.example.test:2222", time.Second)
	if err != nil {
		t.Fatalf("dial through HTTP proxy: %v", err)
	}
	defer proxy.Close()
	banner := make([]byte, len("SSH-2.0-test-server\r\n"))
	if _, err := io.ReadFull(proxy, banner); err != nil || string(banner) != "SSH-2.0-test-server\r\n" {
		t.Fatalf("buffered SSH banner = %q, err=%v", banner, err)
	}
	if _, err := proxy.Write([]byte("probe")); err != nil {
		t.Fatalf("write through HTTP proxy tunnel: %v", err)
	}
	reply := make([]byte, 5)
	if _, err := io.ReadFull(proxy, reply); err != nil || string(reply) != "reply" {
		t.Fatalf("HTTP tunnel reply = %q, err=%v", reply, err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("HTTP proxy fixture: %v", err)
	}
}

func TestDialSSHProxyHTTPConnectRejectsProxyResponse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen HTTP proxy: %v", err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil || line == "\r\n" {
				break
			}
		}
		_, _ = io.WriteString(connection, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
	}()

	_, err = dialSSHProxy(context.Background(), ProxyOptions{
		Type: ProxyTypeHTTP, Address: listener.Addr().String(), Username: "user", Password: "secret",
	}, "ssh.example.test:22", time.Second)
	if err == nil || !strings.Contains(err.Error(), "407") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("HTTP proxy failure = %v, want 407 without credentials", err)
	}
}

func TestDialSSHProxySOCKS5UsernamePasswordAndDomainTarget(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen SOCKS5 proxy: %v", err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		var greetingHeader [2]byte
		if _, readErr := io.ReadFull(connection, greetingHeader[:]); readErr != nil {
			serverErr <- readErr
			return
		}
		methods := make([]byte, int(greetingHeader[1]))
		if _, readErr := io.ReadFull(connection, methods); readErr != nil || greetingHeader[0] != 5 || len(methods) != 1 || methods[0] != 2 {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		if _, writeErr := connection.Write([]byte{5, 2}); writeErr != nil {
			serverErr <- writeErr
			return
		}
		var authHeader [2]byte
		if _, readErr := io.ReadFull(connection, authHeader[:]); readErr != nil || authHeader[0] != 1 {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		username := make([]byte, int(authHeader[1]))
		if _, readErr := io.ReadFull(connection, username); readErr != nil {
			serverErr <- readErr
			return
		}
		var passwordLength [1]byte
		if _, readErr := io.ReadFull(connection, passwordLength[:]); readErr != nil {
			serverErr <- readErr
			return
		}
		password := make([]byte, int(passwordLength[0]))
		if _, readErr := io.ReadFull(connection, password); readErr != nil {
			serverErr <- readErr
			return
		}
		if string(username) != "proxy-user" || string(password) != "proxy-secret" {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		if _, writeErr := connection.Write([]byte{1, 0}); writeErr != nil {
			serverErr <- writeErr
			return
		}
		var requestHeader [5]byte
		if _, readErr := io.ReadFull(connection, requestHeader[:]); readErr != nil {
			serverErr <- readErr
			return
		}
		if requestHeader != [5]byte{5, 1, 0, 3, byte(len("ssh.example.test"))} {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		domain := make([]byte, int(requestHeader[4]))
		if _, readErr := io.ReadFull(connection, domain); readErr != nil {
			serverErr <- readErr
			return
		}
		var port [2]byte
		if _, readErr := io.ReadFull(connection, port[:]); readErr != nil {
			serverErr <- readErr
			return
		}
		if string(domain) != "ssh.example.test" || port != [2]byte{0x08, 0xae} {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		if _, writeErr := connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0x10, 0x92, 'S', 'S', 'H'}); writeErr != nil {
			serverErr <- writeErr
			return
		}
		serverErr <- nil
	}()

	proxy, err := dialSSHProxy(context.Background(), ProxyOptions{
		Type: ProxyTypeSOCKS5, Address: listener.Addr().String(), Username: "proxy-user", Password: "proxy-secret",
	}, "ssh.example.test:2222", time.Second)
	if err != nil {
		t.Fatalf("dial through SOCKS5 proxy: %v", err)
	}
	defer proxy.Close()
	banner := make([]byte, 3)
	if _, err := io.ReadFull(proxy, banner); err != nil || string(banner) != "SSH" {
		t.Fatalf("SOCKS5 tunneled bytes = %q, err=%v", banner, err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("SOCKS5 proxy fixture: %v", err)
	}
}

func TestDialSSHProxySOCKS5NoAuthSupportsIPv4Target(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen SOCKS5 proxy: %v", err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		var greeting [3]byte
		if _, readErr := io.ReadFull(connection, greeting[:]); readErr != nil || greeting != [3]byte{5, 1, 0} {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		if _, writeErr := connection.Write([]byte{5, 0}); writeErr != nil {
			serverErr <- writeErr
			return
		}
		var requestHeader [4]byte
		if _, readErr := io.ReadFull(connection, requestHeader[:]); readErr != nil || requestHeader != [4]byte{5, 1, 0, 1} {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		var destination [6]byte
		if _, readErr := io.ReadFull(connection, destination[:]); readErr != nil {
			serverErr <- readErr
			return
		}
		if destination != [6]byte{192, 0, 2, 10, 0, 22} {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		_, writeErr := connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0x04, 0xd2})
		serverErr <- writeErr
	}()

	proxy, err := dialSSHProxy(context.Background(), ProxyOptions{
		Type: ProxyTypeSOCKS5, Address: listener.Addr().String(),
	}, "192.0.2.10:22", time.Second)
	if err != nil {
		t.Fatalf("dial through unauthenticated SOCKS5 proxy: %v", err)
	}
	_ = proxy.Close()
	if err := <-serverErr; err != nil {
		t.Fatalf("SOCKS5 proxy fixture: %v", err)
	}
}

func TestSOCKS5ConnectRequestEncodesIPv6Target(t *testing.T) {
	request, err := socks5ConnectRequest("[2001:db8::10]:22")
	if err != nil {
		t.Fatalf("encode IPv6 target: %v", err)
	}
	if len(request) != 3+1+net.IPv6len+2 || request[3] != 4 || request[len(request)-2] != 0 || request[len(request)-1] != 22 {
		t.Fatalf("SOCKS5 IPv6 request = %v", request)
	}
}

func TestSOCKS4ConnectRequestEncodesIPv4TargetAndUserID(t *testing.T) {
	request, err := socks4ConnectRequest(context.Background(), "192.0.2.15:22", "workbench")
	if err != nil {
		t.Fatalf("encode SOCKS4 target: %v", err)
	}
	want := []byte{4, 1, 0, 22, 192, 0, 2, 15, 'w', 'o', 'r', 'k', 'b', 'e', 'n', 'c', 'h', 0}
	if string(request) != string(want) {
		t.Fatalf("SOCKS4 request = %v, want %v", request, want)
	}
}

func TestSOCKS4ConnectRequestRejectsIPv6AndInvalidUserID(t *testing.T) {
	if _, err := socks4ConnectRequest(context.Background(), "[2001:db8::15]:22", ""); err == nil {
		t.Fatal("SOCKS4 accepted an IPv6 target")
	}
	if _, err := socks4ConnectRequest(context.Background(), "192.0.2.15:22", "bad\x00user"); err == nil {
		t.Fatal("SOCKS4 accepted a user ID containing NUL")
	}
}

func TestEstablishSOCKS4ReportsRejectedTarget(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	serverErr := make(chan error, 1)
	go func() {
		request := make([]byte, 8)
		if _, err := io.ReadFull(server, request); err != nil {
			serverErr <- err
			return
		}
		if request[0] != 4 || request[1] != 1 || net.IP(request[4:8]).String() != "192.0.2.15" {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		for {
			var character [1]byte
			if _, err := io.ReadFull(server, character[:]); err != nil {
				serverErr <- err
				return
			}
			if character[0] == 0 {
				break
			}
		}
		_, err := server.Write([]byte{0, 91, 0, 0, 0, 0, 0, 0})
		serverErr <- err
	}()
	err := establishSOCKS4(context.Background(), client, ProxyOptions{}, "192.0.2.15:22")
	if fixtureErr := <-serverErr; fixtureErr != nil {
		t.Fatalf("SOCKS4 proxy fixture: %v", fixtureErr)
	}
	if err == nil || !strings.Contains(err.Error(), "rejected CONNECT") {
		t.Fatalf("SOCKS4 rejection error = %v", err)
	}
}

func TestProxyOptionsValidationRequiresCredentialsAndAddress(t *testing.T) {
	for _, test := range []struct {
		name    string
		options ProxyOptions
	}{
		{name: "missing address", options: ProxyOptions{Type: ProxyTypeHTTP}},
		{name: "missing password", options: ProxyOptions{Type: ProxyTypeSOCKS5, Address: "127.0.0.1:1080", Username: "user"}},
		{name: "password without user", options: ProxyOptions{Type: ProxyTypeHTTP, Address: "127.0.0.1:3128", Password: "secret"}},
		{name: "SOCKS4 rejects password", options: ProxyOptions{Type: ProxyTypeSOCKS4, Address: "127.0.0.1:1080", PasswordEnv: "PROXY_PASSWORD"}},
		{name: "SOCKS4 user ID too long", options: ProxyOptions{Type: ProxyTypeSOCKS4, Address: "127.0.0.1:1080", Username: strings.Repeat("u", 256)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.options.validate(); err == nil {
				t.Fatal("invalid proxy options unexpectedly passed validation")
			}
		})
	}
	if err := (ProxyOptions{Type: ProxyTypeSOCKS4, Address: "127.0.0.1:1080", Username: "user-id"}).validate(); err != nil {
		t.Fatalf("valid SOCKS4 proxy options failed validation: %v", err)
	}
}

func TestProxyPasswordEnvironmentMustExistAtConnectionTime(t *testing.T) {
	t.Setenv("WORKBENCH_PROXY_PASSWORD_MISSING", "")
	_, err := (ProxyOptions{Type: ProxyTypeSOCKS5, Address: "127.0.0.1:1080", Username: "user", PasswordEnv: "WORKBENCH_PROXY_PASSWORD_MISSING"}).withResolvedPassword()
	if err == nil || !strings.Contains(err.Error(), "WORKBENCH_PROXY_PASSWORD_MISSING") {
		t.Fatalf("missing proxy password result = %v", err)
	}
}
