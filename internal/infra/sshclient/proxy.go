package sshclient

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ProxyTypeHTTP   = "http"
	ProxyTypeSOCKS4 = "socks4"
	ProxyTypeSOCKS5 = "socks5"
)

// ProxyOptions configures an outbound proxy used for the initial network hop
// of an SSH connection. Password is transient and is never persisted.
type ProxyOptions struct {
	Type        string
	Address     string
	Username    string
	Password    string
	PasswordEnv string
}

func (options ProxyOptions) validate() error {
	typeName := strings.ToLower(strings.TrimSpace(options.Type))
	if typeName == "" || typeName == "none" {
		if options.Address != "" || options.Username != "" || options.Password != "" || options.PasswordEnv != "" {
			return errors.New("SSH proxy fields require type http, socks4 or socks5")
		}
		return nil
	}
	if typeName != ProxyTypeHTTP && typeName != ProxyTypeSOCKS4 && typeName != ProxyTypeSOCKS5 {
		return fmt.Errorf("unsupported SSH proxy type %q", options.Type)
	}
	host, portText, err := net.SplitHostPort(strings.TrimSpace(options.Address))
	if err != nil || strings.TrimSpace(host) == "" || strings.ContainsAny(host, " \t\r\n/@?#") {
		return errors.New("SSH proxy address must be host:port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("SSH proxy port must be between 1 and 65535")
	}
	if strings.ContainsAny(options.Username, "\r\n\x00") || strings.ContainsAny(options.Password, "\r\n\x00") {
		return errors.New("SSH proxy credentials contain invalid characters")
	}
	if typeName == ProxyTypeHTTP && strings.Contains(options.Username, ":") {
		return errors.New("HTTP proxy usernames cannot contain a colon")
	}
	if typeName == ProxyTypeSOCKS4 {
		if options.Password != "" || options.PasswordEnv != "" {
			return errors.New("SOCKS4 does not support password authentication")
		}
		if len(options.Username) > 255 {
			return errors.New("SOCKS4 user ID must be at most 255 bytes")
		}
	} else {
		if options.Username == "" && (options.Password != "" || options.PasswordEnv != "") {
			return errors.New("SSH proxy password requires a username")
		}
		if options.Username != "" && options.Password == "" && options.PasswordEnv == "" {
			return errors.New("SSH proxy authentication requires both username and password")
		}
		if options.Password != "" && options.PasswordEnv != "" {
			return errors.New("SSH proxy password must come from either an explicit value or password_env")
		}
		if typeName == ProxyTypeSOCKS5 && (len(options.Username) > 255 || len(options.Password) > 255) {
			return errors.New("SOCKS5 username and password must each be at most 255 bytes")
		}
		if typeName == ProxyTypeHTTP && (len(options.Username) > 4096 || len(options.Password) > 4096) {
			return errors.New("HTTP proxy username and password must each be at most 4096 bytes")
		}
	}
	return nil
}

func (options ProxyOptions) withResolvedPassword() (ProxyOptions, error) {
	if options.PasswordEnv == "" {
		return options, nil
	}
	password, ok := os.LookupEnv(options.PasswordEnv)
	if !ok || password == "" {
		return ProxyOptions{}, fmt.Errorf("SSH proxy password environment variable %q is not set or empty", options.PasswordEnv)
	}
	options.Password = password
	options.PasswordEnv = ""
	return options, nil
}

func dialSSHProxy(ctx context.Context, proxy ProxyOptions, target string, timeout time.Duration) (net.Conn, error) {
	proxyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dialer := &net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(proxyCtx, "tcp", proxy.Address)
	if err != nil {
		return nil, fmt.Errorf("dial SSH proxy: %w", err)
	}
	stopClose := context.AfterFunc(proxyCtx, func() { _ = connection.Close() })
	defer stopClose()
	_ = connection.SetDeadline(time.Now().Add(timeout))

	var tunneled net.Conn
	switch strings.ToLower(strings.TrimSpace(proxy.Type)) {
	case ProxyTypeHTTP:
		tunneled, err = establishHTTPConnect(connection, proxy, target)
	case ProxyTypeSOCKS5:
		err = establishSOCKS5(connection, proxy, target)
		if err == nil {
			tunneled = connection
		}
	case ProxyTypeSOCKS4:
		err = establishSOCKS4(proxyCtx, connection, proxy, target)
		if err == nil {
			tunneled = connection
		}
	default:
		err = fmt.Errorf("unsupported SSH proxy type %q", proxy.Type)
	}
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	if err := tunneled.SetDeadline(time.Time{}); err != nil {
		_ = tunneled.Close()
		return nil, fmt.Errorf("clear SSH proxy deadline: %w", err)
	}
	return tunneled, nil
}

func establishHTTPConnect(connection net.Conn, proxy ProxyOptions, target string) (net.Conn, error) {
	var request strings.Builder
	fmt.Fprintf(&request, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\n", target, target)
	if proxy.Username != "" {
		token := base64.StdEncoding.EncodeToString([]byte(proxy.Username + ":" + proxy.Password))
		fmt.Fprintf(&request, "Proxy-Authorization: Basic %s\r\n", token)
	}
	request.WriteString("\r\n")
	if err := writeProxyFull(connection, []byte(request.String())); err != nil {
		return nil, fmt.Errorf("write HTTP CONNECT request: %w", err)
	}
	reader := bufio.NewReader(connection)
	response, err := httpReadResponse(reader)
	if err != nil {
		return nil, fmt.Errorf("read HTTP CONNECT response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP CONNECT proxy rejected tunnel with status %d", response.StatusCode)
	}
	return &bufferedProxyConn{Conn: connection, reader: reader}, nil
}

func httpReadResponse(reader *bufio.Reader) (*http.Response, error) {
	return http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
}

type bufferedProxyConn struct {
	net.Conn
	reader *bufio.Reader
}

func (connection *bufferedProxyConn) Read(data []byte) (int, error) {
	return connection.reader.Read(data)
}

func establishSOCKS5(connection net.Conn, proxy ProxyOptions, target string) error {
	methods := []byte{0}
	if proxy.Username != "" {
		methods = []byte{2}
	}
	greeting := []byte{5, byte(len(methods))}
	greeting = append(greeting, methods...)
	if err := writeProxyFull(connection, greeting); err != nil {
		return fmt.Errorf("write SOCKS5 greeting: %w", err)
	}
	var methodReply [2]byte
	if _, err := io.ReadFull(connection, methodReply[:]); err != nil {
		return fmt.Errorf("read SOCKS5 method selection: %w", err)
	}
	if methodReply[0] != 5 || methodReply[1] == 0xff {
		return errors.New("SOCKS5 proxy has no acceptable authentication method")
	}
	if proxy.Username == "" && methodReply[1] != 0 {
		return errors.New("SOCKS5 proxy requires authentication but no credentials are configured")
	}
	if proxy.Username != "" && methodReply[1] != 2 {
		return errors.New("SOCKS5 proxy did not select username/password authentication")
	}
	if methodReply[1] == 2 {
		if err := authenticateSOCKS5(connection, proxy.Username, proxy.Password); err != nil {
			return err
		}
	}
	request, err := socks5ConnectRequest(target)
	if err != nil {
		return err
	}
	if err := writeProxyFull(connection, request); err != nil {
		return fmt.Errorf("write SOCKS5 CONNECT request: %w", err)
	}
	var header [4]byte
	if _, err := io.ReadFull(connection, header[:]); err != nil {
		return fmt.Errorf("read SOCKS5 CONNECT response: %w", err)
	}
	if header[0] != 5 || header[2] != 0 {
		return errors.New("SOCKS5 proxy returned a malformed CONNECT response")
	}
	if header[1] != 0 {
		return fmt.Errorf("SOCKS5 proxy rejected CONNECT request with code %d", header[1])
	}
	var addressLength int
	switch header[3] {
	case 1:
		addressLength = net.IPv4len
	case 4:
		addressLength = net.IPv6len
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(connection, length[:]); err != nil {
			return fmt.Errorf("read SOCKS5 bound domain length: %w", err)
		}
		addressLength = int(length[0])
	default:
		return errors.New("SOCKS5 proxy returned an unsupported address type")
	}
	if _, err := io.CopyN(io.Discard, connection, int64(addressLength+2)); err != nil {
		return fmt.Errorf("read SOCKS5 bound address: %w", err)
	}
	return nil
}

func establishSOCKS4(ctx context.Context, connection net.Conn, proxy ProxyOptions, target string) error {
	request, err := socks4ConnectRequest(ctx, target, proxy.Username)
	if err != nil {
		return err
	}
	if err := writeProxyFull(connection, request); err != nil {
		return fmt.Errorf("write SOCKS4 CONNECT request: %w", err)
	}
	var response [8]byte
	if _, err := io.ReadFull(connection, response[:]); err != nil {
		return fmt.Errorf("read SOCKS4 CONNECT response: %w", err)
	}
	if response[0] != 0 {
		return errors.New("SOCKS4 proxy returned a malformed CONNECT response")
	}
	switch response[1] {
	case 90:
		return nil
	case 91:
		return errors.New("SOCKS4 proxy rejected CONNECT request")
	case 92:
		return errors.New("SOCKS4 proxy could not reach identd on the client")
	case 93:
		return errors.New("SOCKS4 proxy rejected the user ID")
	default:
		return fmt.Errorf("SOCKS4 proxy returned unknown CONNECT status %d", response[1])
	}
}

func socks4ConnectRequest(ctx context.Context, target, userID string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("invalid SSH proxy target %q: %w", target, err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("invalid SSH proxy target port %q", portText)
	}
	if strings.ContainsAny(userID, "\x00\r\n") || len(userID) > 255 {
		return nil, errors.New("SOCKS4 user ID must be at most 255 bytes and contain no NUL or line break")
	}

	var ipv4 net.IP
	if ip := net.ParseIP(host); ip != nil {
		ipv4 = ip.To4()
		if ipv4 == nil {
			return nil, errors.New("SOCKS4 only supports IPv4 targets")
		}
	} else {
		addresses, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		if err != nil {
			return nil, fmt.Errorf("resolve SOCKS4 target locally: %w", err)
		}
		if len(addresses) == 0 || addresses[0].To4() == nil {
			return nil, errors.New("SOCKS4 target did not resolve to an IPv4 address")
		}
		ipv4 = addresses[0].To4()
	}

	request := []byte{4, 1, byte(port >> 8), byte(port)}
	request = append(request, ipv4...)
	request = append(request, []byte(userID)...)
	request = append(request, 0)
	return request, nil
}

func authenticateSOCKS5(connection net.Conn, username, password string) error {
	userBytes, passwordBytes := []byte(username), []byte(password)
	if len(userBytes) == 0 || len(userBytes) > 255 || len(passwordBytes) > 255 {
		return errors.New("SOCKS5 username/password length is invalid")
	}
	request := []byte{1, byte(len(userBytes))}
	request = append(request, userBytes...)
	request = append(request, byte(len(passwordBytes)))
	request = append(request, passwordBytes...)
	if err := writeProxyFull(connection, request); err != nil {
		return fmt.Errorf("write SOCKS5 authentication: %w", err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(connection, reply[:]); err != nil {
		return fmt.Errorf("read SOCKS5 authentication response: %w", err)
	}
	if reply[0] != 1 || reply[1] != 0 {
		return errors.New("SOCKS5 username/password authentication failed")
	}
	return nil
}

func socks5ConnectRequest(target string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("invalid SSH proxy target %q: %w", target, err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("invalid SSH proxy target port %q", portText)
	}
	request := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			request = append(request, 1)
			request = append(request, ipv4...)
		} else {
			request = append(request, 4)
			request = append(request, ip.To16()...)
		}
	} else {
		domain := []byte(host)
		if len(domain) == 0 || len(domain) > 255 {
			return nil, errors.New("SOCKS5 target domain must contain 1 to 255 bytes")
		}
		request = append(request, 3, byte(len(domain)))
		request = append(request, domain...)
	}
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], uint16(port))
	request = append(request, portBytes[:]...)
	return request, nil
}

func writeProxyFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
