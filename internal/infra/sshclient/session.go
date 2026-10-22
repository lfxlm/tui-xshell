// Package sshclient contains the SSH and PTY adapter used by the workbench.
package sshclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"tui-ssh-workbench/internal/infra/sftpclient"
	"tui-ssh-workbench/internal/model"
)

const connectTimeout = 15 * time.Second
const keepaliveInterval = 30 * time.Second
const keepaliveTimeout = 10 * time.Second
const maxSSHChainLength = 16

// ConnectOptions controls per-hop SSH negotiation deadlines and transport keepalives.
type ConnectOptions struct {
	ConnectTimeout    time.Duration
	KeepaliveInterval time.Duration
	KeepaliveTimeout  time.Duration
	Proxy             ProxyOptions
}

func DefaultConnectOptions() ConnectOptions {
	return ConnectOptions{
		ConnectTimeout:    connectTimeout,
		KeepaliveInterval: keepaliveInterval,
		KeepaliveTimeout:  keepaliveTimeout,
	}
}

func (o ConnectOptions) validate() error {
	if o.ConnectTimeout <= 0 || o.ConnectTimeout > 10*time.Minute {
		return errors.New("SSH connect timeout must be between 0 and 10 minutes")
	}
	if o.KeepaliveInterval < 0 || o.KeepaliveInterval > time.Hour {
		return errors.New("SSH keepalive interval must be between 0 and 1 hour")
	}
	if o.KeepaliveTimeout <= 0 || o.KeepaliveTimeout > 2*time.Minute {
		return errors.New("SSH keepalive timeout must be greater than 0 and at most 2 minutes")
	}
	if err := o.Proxy.validate(); err != nil {
		return err
	}
	return nil
}

var (
	ErrInputQueueFull       = errors.New("SSH input queue is full")
	ErrAuthenticationNeeded = errors.New("需要输入 SSH 登录密码或私钥口令")
	ErrTransportLost        = errors.New("SSH transport lost")
)

// AuthenticationError identifies which SSH hop rejected all offered
// authentication methods. The host data contains no entered credential.
type AuthenticationError struct {
	Host  model.Host
	Cause error
}

func (e *AuthenticationError) Error() string {
	if e == nil {
		return ErrAuthenticationNeeded.Error()
	}
	return fmt.Sprintf("%s：%s", ErrAuthenticationNeeded, e.Host.Name)
}

func (e *AuthenticationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *AuthenticationError) Is(target error) bool {
	return target == ErrAuthenticationNeeded
}

// KeyboardInteractiveHandler collects answers for an SSH keyboard-interactive
// challenge. It is called once per challenge round and may block until the UI
// provides the answers; context cancellation must unblock it.
type KeyboardInteractiveHandler func(ctx context.Context, host model.Host, name, instruction string, questions []string, echos []bool) ([]string, error)

// UnknownHostKeyError asks the UI to get explicit approval before connecting.
type UnknownHostKeyError struct {
	Address     string
	Fingerprint string
	Key         ssh.PublicKey
	Host        model.Host
}

func (e *UnknownHostKeyError) Error() string {
	return "SSH 主机尚未信任"
}

// ChangedHostKeyError carries the old and newly presented fingerprints so an
// interactive client can ask the user to verify and atomically replace trust.
type ChangedHostKeyError struct {
	Address              string
	PreviousFingerprints []string
	Fingerprint          string
	Key                  ssh.PublicKey
	Host                 model.Host
	References           []KnownHostKeyReference
	CanReplace           bool
	ReplaceBlockReason   string
}

func (e *ChangedHostKeyError) Error() string {
	return "SSH 主机指纹已变化，需要确认后更新"
}

// Session owns a single authenticated SSH shell and its PTY streams.
type Session struct {
	client     *ssh.Client
	proxies    []*ssh.Client
	session    *ssh.Session
	stdin      io.WriteCloser
	stdout     io.Reader
	exitResult chan error

	input     chan []byte
	done      chan struct{}
	closeOne  sync.Once
	failureMu sync.Mutex
	failure   error
	forwardMu sync.Mutex
	forwards  map[*LocalForward]struct{}
	x11       *x11Forwarder
	options   ConnectOptions
}

// Connect dials a host, verifies it against the user's known_hosts file, and
// opens an interactive PTY. Passwords are used only for this call.
func Connect(ctx context.Context, host model.Host, password string, width, height int) (*Session, error) {
	return ConnectWithJumps(ctx, host, password, nil, width, height)
}

// ConnectWithJumps opens an interactive shell through the configured SSH jump
// hosts. The host slice supplies the saved hosts referenced by ProxyJumpList.
func ConnectWithJumps(ctx context.Context, host model.Host, password string, hosts []model.Host, width, height int) (*Session, error) {
	return ConnectWithJumpsAndKeyboardInteractive(ctx, host, password, hosts, width, height, nil)
}

// ConnectWithJumpsAndKeyboardInteractive opens an interactive shell and
// supports RFC 4256 prompts from every hop and the destination host.
func ConnectWithJumpsAndKeyboardInteractive(ctx context.Context, host model.Host, password string, hosts []model.Host, width, height int, handler KeyboardInteractiveHandler) (*Session, error) {
	return connect(ctx, host, password, hosts, width, height, true, handler, DefaultConnectOptions())
}

// ConnectWithJumpsAndCredentials opens an SSH shell using temporary secrets
// keyed by HostCredentialKey. The map is read only and secrets are never
// persisted. A missing credential for a hop means that key and agent methods
// will be tried first, then AuthenticationError identifies the hop to prompt.
func ConnectWithJumpsAndCredentials(ctx context.Context, host model.Host, credentials map[string]string, hosts []model.Host, width, height int, handler KeyboardInteractiveHandler) (*Session, error) {
	return ConnectWithJumpsAndCredentialsWithOptions(ctx, host, credentials, hosts, width, height, handler, DefaultConnectOptions())
}

// ConnectWithJumpsAndCredentialsWithOptions opens an SSH shell using the supplied
// timeout and keepalive settings. Credentials remain transient for this call.
func ConnectWithJumpsAndCredentialsWithOptions(ctx context.Context, host model.Host, credentials map[string]string, hosts []model.Host, width, height int, handler KeyboardInteractiveHandler, options ConnectOptions) (*Session, error) {
	return connectWithHostPasswords(ctx, host, credentials, "", hosts, width, height, true, handler, options)
}

// ConnectSFTP establishes an authenticated SSH transport without starting a
// remote shell. The returned session can open SFTP subsystems and owns the
// transport until Close is called.
func ConnectSFTP(ctx context.Context, host model.Host, password string) (*Session, error) {
	return ConnectSFTPWithJumps(ctx, host, password, nil)
}

// ConnectSFTPWithJumps establishes an SFTP transport through configured SSH
// jump hosts without starting a remote shell.
func ConnectSFTPWithJumps(ctx context.Context, host model.Host, password string, hosts []model.Host) (*Session, error) {
	return ConnectSFTPWithJumpsAndKeyboardInteractive(ctx, host, password, hosts, nil)
}

// ConnectSFTPWithJumpsAndKeyboardInteractive opens an SFTP transport and
// supports keyboard-interactive prompts from jump hosts and the destination.
func ConnectSFTPWithJumpsAndKeyboardInteractive(ctx context.Context, host model.Host, password string, hosts []model.Host, handler KeyboardInteractiveHandler) (*Session, error) {
	return connect(ctx, host, password, hosts, 0, 0, false, handler, DefaultConnectOptions())
}

// ConnectSFTPWithJumpsAndCredentials establishes an SFTP transport using
// temporary per-host authentication secrets keyed by HostCredentialKey.
func ConnectSFTPWithJumpsAndCredentials(ctx context.Context, host model.Host, credentials map[string]string, hosts []model.Host, handler KeyboardInteractiveHandler) (*Session, error) {
	return ConnectSFTPWithJumpsAndCredentialsWithOptions(ctx, host, credentials, hosts, handler, DefaultConnectOptions())
}

// ConnectSFTPWithJumpsAndCredentialsWithOptions opens an SFTP transport using
// the supplied timeout and keepalive settings.
func ConnectSFTPWithJumpsAndCredentialsWithOptions(ctx context.Context, host model.Host, credentials map[string]string, hosts []model.Host, handler KeyboardInteractiveHandler, options ConnectOptions) (*Session, error) {
	return ConnectTransportWithJumpsAndCredentialsWithOptions(ctx, host, credentials, hosts, handler, options)
}

// ConnectTransportWithJumpsAndCredentialsWithOptions opens an authenticated
// SSH transport through the configured jump hosts without starting a shell or
// PTY. Callers can open SFTP subsystems or non-interactive exec channels on it.
func ConnectTransportWithJumpsAndCredentialsWithOptions(ctx context.Context, host model.Host, credentials map[string]string, hosts []model.Host, handler KeyboardInteractiveHandler, options ConnectOptions) (*Session, error) {
	return connectWithHostPasswords(ctx, host, credentials, "", hosts, 0, 0, false, handler, options)
}

// ProbeSSHBannerThroughJumps verifies the SSH banner of a target reachable
// through its configured jump hosts. Jump hosts are authenticated using the
// supplied credentials, SSH agent, or configured identity files. The target
// itself is not authenticated, matching the direct banner probe's semantics.
func ProbeSSHBannerThroughJumps(ctx context.Context, target model.Host, credentials map[string]string, hosts []model.Host, options ConnectOptions) bool {
	chain, err := resolveProxyChain(target, hosts)
	if err != nil || len(chain) < 2 {
		return false
	}
	if err := options.validate(); err != nil {
		return false
	}
	proxyOptions, err := options.Proxy.withResolvedPassword()
	if err != nil {
		return false
	}
	options.Proxy = proxyOptions

	knownHostsFile, err := ensureKnownHostsFile()
	if err != nil {
		return false
	}
	knownHostCallback, err := knownhosts.New(knownHostsFile)
	if err != nil {
		return false
	}

	clients := make([]*ssh.Client, 0, len(chain)-1)
	defer func() { closeSSHClients(clients) }()
	for index, jump := range chain[:len(chain)-1] {
		var proxy *ssh.Client
		if index > 0 {
			proxy = clients[index-1]
		}
		password := credentials[HostCredentialKey(jump)]
		client, connectErr := dialSSHClient(ctx, jump, proxy, password, knownHostCallback, nil, options)
		if connectErr != nil {
			return false
		}
		clients = append(clients, client)
	}

	if !validHostAddress(target.IP) {
		return false
	}
	port := target.Port
	if port <= 0 {
		port = 22
	}
	address := net.JoinHostPort(strings.Trim(target.IP, "[]"), strconv.Itoa(port))
	proxy := clients[len(clients)-1]
	dialCtx, cancel := context.WithTimeout(ctx, options.ConnectTimeout)
	stopProxyClose := context.AfterFunc(dialCtx, func() { _ = proxy.Close() })
	connection, err := proxy.Dial("tcp", address)
	stopProxyClose()
	cancel()
	if err != nil {
		return false
	}
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(options.ConnectTimeout))
	reader := bufio.NewReader(io.LimitReader(connection, 2048))
	for range 10 {
		line, readErr := reader.ReadString('\n')
		if strings.HasPrefix(line, "SSH-") {
			return true
		}
		if readErr != nil {
			return false
		}
	}
	return false
}

func connect(ctx context.Context, host model.Host, password string, hosts []model.Host, width, height int, openShell bool, handler KeyboardInteractiveHandler, options ConnectOptions) (*Session, error) {
	return connectWithHostPasswords(ctx, host, nil, password, hosts, width, height, openShell, handler, options)
}

func connectWithHostPasswords(ctx context.Context, host model.Host, hostPasswords map[string]string, fallbackPassword string, hosts []model.Host, width, height int, openShell bool, handler KeyboardInteractiveHandler, options ConnectOptions) (*Session, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	proxyOptions, err := options.Proxy.withResolvedPassword()
	if err != nil {
		return nil, err
	}
	options.Proxy = proxyOptions
	if err := options.validate(); err != nil {
		return nil, err
	}
	if !validHostAddress(host.IP) {
		return nil, errors.New("SSH 地址格式无效")
	}
	if strings.TrimSpace(host.User) == "" {
		return nil, errors.New("SSH 用户名不能为空")
	}
	if host.Port < 1 || host.Port > 65535 {
		return nil, errors.New("SSH 端口无效")
	}
	var localX11Config *x11Config
	if openShell && host.X11Forwarding {
		config, err := discoverX11Config(ctx)
		if err != nil {
			return nil, err
		}
		localX11Config = &config
	}

	knownHostsFile, err := ensureKnownHostsFile()
	if err != nil {
		return nil, fmt.Errorf("准备 SSH 主机指纹文件失败：%w", err)
	}
	knownHostCallback, err := knownhosts.New(knownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("读取 SSH 主机指纹失败：%w", err)
	}

	chain, err := resolveProxyChain(host, hosts)
	if err != nil {
		return nil, err
	}
	credentials := make(map[string]string, len(hostPasswords))
	for key, secret := range hostPasswords {
		credentials[key] = secret
	}
	defer func() {
		for key := range credentials {
			credentials[key] = ""
			delete(credentials, key)
		}
	}()
	clients := make([]*ssh.Client, 0, len(chain))
	for index, current := range chain {
		var proxy *ssh.Client
		if index > 0 {
			proxy = clients[index-1]
		}
		password := fallbackPassword
		if secret, ok := credentials[HostCredentialKey(current)]; ok {
			password = secret
		}
		client, connectErr := dialSSHClient(ctx, current, proxy, password, knownHostCallback, handler, options)
		if connectErr != nil {
			closeSSHClients(clients)
			return nil, connectErr
		}
		clients = append(clients, client)
	}
	client := clients[len(clients)-1]
	proxyClients := append([]*ssh.Client(nil), clients[:len(clients)-1]...)
	if !openShell {
		sessionOptions := options
		sessionOptions.Proxy = ProxyOptions{}
		connected := &Session{client: client, proxies: proxyClients, done: make(chan struct{}), options: sessionOptions}
		connected.startKeepalive()
		return connected, nil
	}

	session, err := client.NewSession()
	if err != nil {
		closeSSHClients(clients)
		return nil, fmt.Errorf("创建 SSH 会话失败：%w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		closeSSHClients(clients)
		return nil, fmt.Errorf("打开 SSH 输入通道失败：%w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		closeSSHClients(clients)
		return nil, fmt.Errorf("打开 SSH 输出通道失败：%w", err)
	}
	width = max(width, 20)
	height = max(height, 5)
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", height, width, modes); err != nil {
		_ = session.Close()
		closeSSHClients(clients)
		return nil, fmt.Errorf("申请远程 PTY 失败：%w", err)
	}
	var x11 *x11Forwarder
	if localX11Config != nil {
		x11, err = startX11Forwarding(client, session, *localX11Config)
		if err != nil {
			_ = session.Close()
			closeSSHClients(clients)
			return nil, err
		}
	}
	if err := session.Shell(); err != nil {
		if x11 != nil {
			x11.Close()
		}
		_ = session.Close()
		closeSSHClients(clients)
		return nil, fmt.Errorf("启动远程 Shell 失败：%w", err)
	}

	sessionOptions := options
	sessionOptions.Proxy = ProxyOptions{}
	connected := &Session{
		client: client, proxies: proxyClients, session: session, stdin: stdin, stdout: stdout,
		input: make(chan []byte, 64), done: make(chan struct{}), exitResult: make(chan error, 1), x11: x11, options: sessionOptions,
	}
	go func() { connected.exitResult <- session.Wait() }()
	go connected.writeLoop()
	connected.startKeepalive()
	return connected, nil
}

// HostCredentialKey returns the key used by per-host credential maps. Saved
// hosts use their stable ID; unsaved hosts use their SSH login identity.
func HostCredentialKey(host model.Host) string {
	if id := strings.TrimSpace(host.Id); id != "" {
		return id
	}
	return strings.TrimSpace(host.User) + "@" + net.JoinHostPort(strings.TrimSpace(host.IP), strconv.Itoa(host.Port))
}

// TrustHost saves a host key only after the user has approved its fingerprint.
func TrustHost(host model.Host, key ssh.PublicKey) error {
	if key == nil {
		return errors.New("SSH 主机指纹无效")
	}
	if !validHostAddress(host.IP) || host.Port < 1 || host.Port > 65535 {
		return errors.New("SSH 主机地址或端口无效")
	}
	knownHostsMutationMu.Lock()
	defer knownHostsMutationMu.Unlock()
	path, err := ensureKnownHostsFile()
	if err != nil {
		return fmt.Errorf("准备 SSH 主机指纹文件失败：%w", err)
	}
	address := net.JoinHostPort(host.IP, strconv.Itoa(host.Port))
	line := knownhosts.Line([]string{knownhosts.Normalize(address)}, key)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开 SSH 主机指纹文件失败：%w", err)
	}
	defer file.Close()
	if _, err := io.WriteString(file, line+"\n"); err != nil {
		return fmt.Errorf("保存 SSH 主机指纹失败：%w", err)
	}
	return nil
}

// Read reads the next bytes emitted by the remote PTY.
func (s *Session) Read(buffer []byte) (int, error) {
	n, err := s.stdout.Read(buffer)
	if !errors.Is(err, io.EOF) {
		return n, err
	}
	return n, s.classifyShellEnd()
}

func (s *Session) classifyShellEnd() error {
	if err := s.transportFailure(); err != nil {
		return err
	}
	var waitErr error
	select {
	case waitErr = <-s.exitResult:
	case <-s.done:
		if err := s.transportFailure(); err != nil {
			return err
		}
		return io.EOF
	}
	var missingStatus *ssh.ExitMissingError
	var probeErr error
	if errors.As(waitErr, &missingStatus) && !s.isClosed() {
		probeErr = sendKeepalive(s.done, s.client, 2*time.Second)
	}
	return classifyShellExit(waitErr, probeErr, s.isClosed())
}

func classifyShellExit(waitErr, probeErr error, closed bool) error {
	if closed {
		return io.EOF
	}
	var exitError *ssh.ExitError
	if waitErr == nil || errors.As(waitErr, &exitError) {
		return io.EOF
	}
	var missingStatus *ssh.ExitMissingError
	if errors.As(waitErr, &missingStatus) && probeErr == nil {
		return io.EOF
	}
	if waitErr != nil {
		return fmt.Errorf("%w: %v", ErrTransportLost, waitErr)
	}
	return fmt.Errorf("%w: %v", ErrTransportLost, probeErr)
}

func (s *Session) isClosed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *Session) setTransportFailure(err error) {
	if err == nil {
		return
	}
	s.failureMu.Lock()
	if s.failure == nil {
		s.failure = err
	}
	s.failureMu.Unlock()
}

func (s *Session) transportFailure() error {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	if s.failure == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrTransportLost, s.failure)
}

// Write enqueues input in order without blocking Bubble Tea's Update method.
func (s *Session) Write(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	copyOfData := append([]byte(nil), data...)
	select {
	case <-s.done:
		return io.ErrClosedPipe
	default:
	}
	select {
	case s.input <- copyOfData:
		return nil
	case <-s.done:
		return io.ErrClosedPipe
	default:
		return ErrInputQueueFull
	}
}

// Resize changes the remote PTY dimensions.
func (s *Session) Resize(width, height int) error {
	return s.session.WindowChange(max(height, 5), max(width, 20))
}

// OpenSFTP opens a separate SFTP subsystem on the authenticated SSH
// transport. Closing the returned client leaves this shell session running.
func (s *Session) OpenSFTP() (*sftpclient.Client, error) {
	select {
	case <-s.done:
		return nil, io.ErrClosedPipe
	default:
	}
	return sftpclient.New(s.client)
}

// OpenCommand creates a separate non-interactive SSH exec channel on the
// authenticated transport. Callers own the returned channel; closing it does
// not close this SSH session. It is intended for binary protocols such as SCP,
// not for commands that need to write into the interactive terminal.
func (s *Session) OpenCommand() (*ssh.Session, error) {
	select {
	case <-s.done:
		return nil, io.ErrClosedPipe
	default:
	}
	return s.client.NewSession()
}

// ForwardLocal starts a local TCP listener and forwards accepted connections
// through this SSH session to remoteAddress. The listener is closed when either
// the returned forward or the owning SSH session is closed.
func (s *Session) ForwardLocal(localAddress, remoteAddress string) (*LocalForward, error) {
	if _, _, err := splitTCPAddress(localAddress, true); err != nil {
		return nil, fmt.Errorf("本地监听地址无效：%w", err)
	}
	if _, _, err := splitTCPAddress(remoteAddress, false); err != nil {
		return nil, fmt.Errorf("远程目标地址无效：%w", err)
	}
	return s.startForward(localAddress, remoteAddress, ForwardLocalTCP)
}

// ForwardSOCKS5 starts an unauthenticated local SOCKS5 proxy. TCP CONNECT
// requests are dialed from the remote SSH server, so remote DNS is supported.
func (s *Session) ForwardSOCKS5(localAddress string) (*LocalForward, error) {
	host, _, err := splitTCPAddress(localAddress, true)
	if err != nil {
		return nil, fmt.Errorf("本地 SOCKS5 监听地址无效：%w", err)
	}
	if strings.ToLower(host) != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("无认证 SOCKS5 代理只能监听本机回环地址")
		}
	}
	return s.startForward(localAddress, "SOCKS5 代理", ForwardSOCKS5)
}

// ForwardRemote opens a TCP listener on the SSH server and forwards each
// accepted connection to localTargetAddress on the machine running this app.
func (s *Session) ForwardRemote(remoteListenAddress, localTargetAddress string) (*LocalForward, error) {
	host, port, err := splitTCPAddress(remoteListenAddress, true)
	if err != nil {
		return nil, fmt.Errorf("远程监听地址无效：%w", err)
	}
	if host == "" {
		host = "127.0.0.1"
		remoteListenAddress = net.JoinHostPort(host, strconv.Itoa(port))
	}
	if _, _, err := splitTCPAddress(localTargetAddress, false); err != nil {
		return nil, fmt.Errorf("本地目标地址无效：%w", err)
	}
	select {
	case <-s.done:
		return nil, io.ErrClosedPipe
	default:
	}
	listener, err := s.client.Listen("tcp", remoteListenAddress)
	if err != nil {
		return nil, fmt.Errorf("创建远程监听失败：%w", err)
	}
	return s.registerForward(listener, localTargetAddress, ForwardRemoteTCP)
}

func (s *Session) startForward(localAddress, remoteAddress string, kind ForwardKind) (*LocalForward, error) {
	select {
	case <-s.done:
		return nil, io.ErrClosedPipe
	default:
	}
	listener, err := net.Listen("tcp", localAddress)
	if err != nil {
		return nil, fmt.Errorf("创建本地监听失败：%w", err)
	}
	return s.registerForward(listener, remoteAddress, kind)
}

func (s *Session) registerForward(listener net.Listener, targetAddress string, kind ForwardKind) (*LocalForward, error) {
	forward := &LocalForward{
		session:       s,
		listener:      listener,
		remoteAddress: targetAddress,
		kind:          kind,
		done:          make(chan struct{}),
		connections:   make(map[net.Conn]struct{}),
	}
	s.forwardMu.Lock()
	select {
	case <-s.done:
		s.forwardMu.Unlock()
		_ = listener.Close()
		return nil, io.ErrClosedPipe
	default:
	}
	if s.forwards == nil {
		s.forwards = make(map[*LocalForward]struct{})
	}
	s.forwards[forward] = struct{}{}
	s.forwardMu.Unlock()
	forward.wg.Add(1)
	go forward.acceptLoop()
	return forward, nil
}

// Close releases the shell and SSH transport. It is safe to call more than once.
func (s *Session) Close() error {
	var closeErr error
	s.closeOne.Do(func() {
		close(s.done)
		s.forwardMu.Lock()
		forwards := make([]*LocalForward, 0, len(s.forwards))
		for forward := range s.forwards {
			forwards = append(forwards, forward)
		}
		s.forwardMu.Unlock()
		for _, forward := range forwards {
			forward.stop()
		}
		if s.x11 != nil {
			s.x11.Close()
		}
		if s.session != nil {
			if err := s.session.Close(); err != nil && !errors.Is(err, io.EOF) {
				closeErr = err
			}
		}
		if err := s.client.Close(); closeErr == nil && err != nil && !errors.Is(err, io.EOF) {
			closeErr = err
		}
		for index := len(s.proxies) - 1; index >= 0; index-- {
			if err := s.proxies[index].Close(); closeErr == nil && err != nil && !errors.Is(err, io.EOF) {
				closeErr = err
			}
		}
		for _, forward := range forwards {
			forward.wg.Wait()
			if closeErr == nil && forward.stopErr != nil {
				closeErr = forward.stopErr
			}
		}
	})
	return closeErr
}

func (s *Session) startKeepalive() {
	if s.options.KeepaliveInterval <= 0 {
		return
	}
	clients := append(append([]*ssh.Client(nil), s.proxies...), s.client)
	if len(clients) == 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(s.options.KeepaliveInterval)
		defer ticker.Stop()
		consecutiveFailures := 0
		var lastFailure error
		for {
			select {
			case <-s.done:
				return
			case <-ticker.C:
			}
			failed := false
			for _, client := range clients {
				err := sendKeepalive(s.done, client, s.options.KeepaliveTimeout)
				if err == nil {
					continue
				}
				if errors.Is(err, context.DeadlineExceeded) {
					s.setTransportFailure(err)
					_ = s.Close()
					return
				}
				select {
				case <-s.done:
					return
				default:
				}
				consecutiveFailures++
				lastFailure = err
				failed = true
				break
			}
			if !failed {
				consecutiveFailures = 0
				lastFailure = nil
			}
			if consecutiveFailures >= 2 {
				s.setTransportFailure(lastFailure)
				_ = s.Close()
				return
			}
		}
	}()
}

func sendKeepalive(sessionDone <-chan struct{}, client *ssh.Client, timeout time.Duration) error {
	result := make(chan error, 1)
	go func() {
		_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		result <- err
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-sessionDone:
		return context.Canceled
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

func (s *Session) writeLoop() {
	for {
		select {
		case <-s.done:
			return
		case data := <-s.input:
			if _, err := s.stdin.Write(data); err != nil {
				_ = s.Close()
				return
			}
		}
	}
}

// IsAuthenticationError reports whether the SSH server rejected every offered
// authentication method, which lets the UI offer password authentication.
func IsAuthenticationError(err error) bool {
	if err == nil {
		return false
	}
	var hostAuth *AuthenticationError
	if errors.As(err, &hostAuth) {
		return true
	}
	if errors.Is(err, ErrAuthenticationNeeded) {
		return true
	}
	var authErr *ssh.ServerAuthError
	if errors.As(err, &authErr) {
		return true
	}
	// The client-side SSH handshake reports exhausted auth methods as a
	// wrapped plain error rather than ServerAuthError.
	return strings.Contains(err.Error(), "ssh: unable to authenticate, attempted methods")
}

func verifyHostKey(callback ssh.HostKeyCallback, address string, host model.Host) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := callback(hostname, remote, key)
		if err == nil {
			return nil
		}
		var revoked *knownhosts.RevokedError
		if errors.As(err, &revoked) {
			return fmt.Errorf("SSH 服务端提供了已撤销的主机指纹，连接已阻止：%w", err)
		}
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			return &UnknownHostKeyError{
				Address: address, Fingerprint: ssh.FingerprintSHA256(key), Key: key, Host: host,
			}
		}
		if errors.As(err, &keyErr) {
			references := make([]KnownHostKeyReference, 0, len(keyErr.Want))
			fingerprints := make([]string, 0, len(keyErr.Want))
			seenLines := make(map[int]struct{}, len(keyErr.Want))
			seenFingerprints := make(map[string]struct{}, len(keyErr.Want))
			for _, expected := range keyErr.Want {
				if expected.Line <= 0 {
					continue
				}
				if _, exists := seenLines[expected.Line]; !exists {
					seenLines[expected.Line] = struct{}{}
					references = append(references, KnownHostKeyReference{
						LineNumber:  expected.Line,
						Fingerprint: ssh.FingerprintSHA256(expected.Key),
					})
				}
				fingerprint := ssh.FingerprintSHA256(expected.Key)
				if _, exists := seenFingerprints[fingerprint]; !exists {
					seenFingerprints[fingerprint] = struct{}{}
					fingerprints = append(fingerprints, fingerprint)
				}
			}
			if len(references) > 0 {
				blockReason := hostKeyReplacementBlockReason(references)
				return &ChangedHostKeyError{
					Address: address, PreviousFingerprints: fingerprints,
					Fingerprint: ssh.FingerprintSHA256(key), Key: key, Host: host,
					References: references, CanReplace: blockReason == "", ReplaceBlockReason: blockReason,
				}
			}
		}
		return fmt.Errorf("SSH 主机指纹已变化或已撤销，连接已阻止：%w", err)
	}
}

func hostKeyReplacementBlockReason(references []KnownHostKeyReference) string {
	entries, err := ListKnownHostEntries()
	if err != nil {
		return "无法安全读取现有主机指纹记录：" + err.Error()
	}
	byLine := make(map[int]KnownHostEntry, len(entries))
	for _, entry := range entries {
		byLine[entry.LineNumber] = entry
	}
	for _, reference := range references {
		entry, ok := byLine[reference.LineNumber]
		if !ok || entry.Fingerprint != reference.Fingerprint {
			return "已保存的指纹记录刚刚发生变化，请取消连接并重新尝试。"
		}
		if entry.Marker != "" {
			return "此主机使用了 " + entry.Marker + " 规则，不能自动替换；请在 SSH 主机指纹管理中手动检查。"
		}
		for _, pattern := range entry.Hosts {
			if strings.ContainsAny(pattern, "*?!") {
				return "此信任记录包含通配符，不能自动替换；请在 SSH 主机指纹管理中手动检查。"
			}
		}
	}
	return ""
}

func resolveProxyChain(target model.Host, hosts []model.Host) ([]model.Host, error) {
	byID := make(map[string]model.Host, len(hosts))
	for _, host := range hosts {
		if host.Id != "" {
			byID[host.Id] = host
		}
	}
	chain := make([]model.Host, 0, len(hosts)+1)
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var appendHost func(model.Host) error
	appendHost = func(host model.Host) error {
		if host.Kind == model.HostKindLocal {
			return fmt.Errorf("跳板机 %q 是本地终端，不能用于 SSH 转发", host.Name)
		}
		if host.Id != "" && visiting[host.Id] {
			return fmt.Errorf("跳板机配置存在循环引用：%s", host.Name)
		}
		if host.Id != "" && visited[host.Id] {
			return nil
		}
		if len(chain)+len(visiting) >= maxSSHChainLength {
			return fmt.Errorf("SSH 跳板链路超过 %d 台主机", maxSSHChainLength)
		}
		if host.Id != "" {
			visiting[host.Id] = true
		}
		for _, jumpID := range host.ProxyJumpList {
			jump, ok := byID[jumpID]
			if !ok {
				return fmt.Errorf("主机 %q 引用了不存在的跳板机 %q", host.Name, jumpID)
			}
			if err := appendHost(jump); err != nil {
				return err
			}
		}
		if host.Id != "" {
			delete(visiting, host.Id)
			visited[host.Id] = true
		}
		chain = append(chain, host)
		return nil
	}
	if err := appendHost(target); err != nil {
		return nil, err
	}
	return chain, nil
}

func dialSSHClient(ctx context.Context, host model.Host, proxy *ssh.Client, password string, knownHostCallback ssh.HostKeyCallback, handler KeyboardInteractiveHandler, options ConnectOptions) (*ssh.Client, error) {
	if !validHostAddress(host.IP) || host.Port < 1 || host.Port > 65535 || strings.TrimSpace(host.User) == "" {
		return nil, fmt.Errorf("SSH 主机 %q 的地址、端口或用户名无效", host.Name)
	}
	address := net.JoinHostPort(host.IP, strconv.Itoa(host.Port))
	var (
		conn net.Conn
		err  error
	)
	if proxy == nil {
		if options.Proxy.Type == "" || options.Proxy.Type == "none" {
			conn, err = (&net.Dialer{Timeout: options.ConnectTimeout}).DialContext(ctx, "tcp", address)
		} else {
			conn, err = dialSSHProxy(ctx, options.Proxy, address, options.ConnectTimeout)
		}
	} else {
		dialCtx, cancel := context.WithTimeout(ctx, options.ConnectTimeout)
		stopProxyClose := context.AfterFunc(dialCtx, func() { _ = proxy.Close() })
		conn, err = proxy.Dial("tcp", address)
		stopProxyClose()
		cancel()
	}
	if err != nil {
		return nil, fmt.Errorf("连接 %s (%s) 失败：%w", host.Name, address, err)
	}
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	_ = conn.SetDeadline(time.Now().Add(options.ConnectTimeout))
	interactiveHandler := keyboardInteractiveDeadlineHandler(conn, handler, options.ConnectTimeout)
	authMethods, closeAuth, err := authenticationMethods(ctx, host, password, interactiveHandler)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("读取 SSH 身份密钥失败：%w", err)
	}
	defer closeAuth()
	clientConfig := &ssh.ClientConfig{
		User:            strings.TrimSpace(host.User),
		Auth:            authMethods,
		HostKeyCallback: verifyHostKey(knownHostCallback, address, host),
		Timeout:         options.ConnectTimeout,
	}
	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, clientConfig)
	if err != nil {
		_ = conn.Close()
		if IsAuthenticationError(err) || (len(authMethods) == 0 && strings.Contains(err.Error(), "unable to authenticate")) {
			return nil, &AuthenticationError{Host: host, Cause: err}
		}
		return nil, fmt.Errorf("连接 SSH 主机 %q 失败：%w", host.Name, err)
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(clientConn, channels, requests), nil
}

func keyboardInteractiveDeadlineHandler(conn net.Conn, handler KeyboardInteractiveHandler, timeout time.Duration) KeyboardInteractiveHandler {
	if handler == nil {
		return nil
	}
	return func(ctx context.Context, host model.Host, name, instruction string, questions []string, echos []bool) ([]string, error) {
		// The network handshake deadline must not expire while a person is
		// reading and answering an MFA prompt. Restore it before SSH resumes.
		if err := conn.SetDeadline(time.Time{}); err != nil {
			return nil, err
		}
		answers, challengeErr := handler(ctx, host, name, instruction, questions, echos)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
			return nil, err
		}
		return answers, challengeErr
	}
}

func closeSSHClients(clients []*ssh.Client) {
	for index := len(clients) - 1; index >= 0; index-- {
		_ = clients[index].Close()
	}
}

func ensureKnownHostsFile() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("获取用户配置目录失败：%w", err)
	}
	dir := filepath.Join(configDir, "tui-ssh-workbench")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建配置目录失败：%w", err)
	}
	path := filepath.Join(dir, "known_hosts")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("SSH known_hosts 必须是普通文件")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("检查 SSH known_hosts 失败：%w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", fmt.Errorf("创建文件失败：%w", err)
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("设置文件权限失败：%w", err)
	}
	return path, nil
}

func validHostAddress(address string) bool {
	if strings.TrimSpace(address) == "" || strings.TrimSpace(address) != address {
		return false
	}
	// known_hosts treats these characters as glob separators. The host form
	// stores one literal DNS name or IP, never a host pattern.
	return !strings.ContainsAny(address, ",*?\\[]/\t\r\n ")
}

func authenticationMethods(ctx context.Context, host model.Host, password string, handler KeyboardInteractiveHandler) ([]ssh.AuthMethod, func(), error) {
	methods := make([]ssh.AuthMethod, 0, 5)
	var agentConnections []net.Conn

	if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
		if connection, err := net.DialTimeout("unix", socket, 2*time.Second); err == nil {
			agentClient := agent.NewClient(connection)
			methods = append(methods, ssh.PublicKeysCallback(agentClient.Signers))
			agentConnections = append(agentConnections, connection)
		}
	}
	if host.IdentityFile != "" {
		signer, err := identityFileSigner(host.IdentityFile, password)
		if err != nil {
			for _, connection := range agentConnections {
				_ = connection.Close()
			}
			return nil, func() {}, err
		}
		if signer != nil {
			methods = append(methods, ssh.PublicKeys(signer))
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			signer, err := identityFileSigner(filepath.Join(home, ".ssh", name), password)
			if err == nil && signer != nil {
				methods = append(methods, ssh.PublicKeys(signer))
			}
		}
	}
	if handler != nil {
		methods = append(methods, ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return handler(ctx, host, name, instruction, append([]string(nil), questions...), append([]bool(nil), echos...))
		}))
	}
	if password != "" {
		methods = append(methods, ssh.Password(password))
	}
	cleanup := func() {
		for _, connection := range agentConnections {
			_ = connection.Close()
		}
	}
	return methods, cleanup, nil
}

const maxIdentityFileBytes = 16 << 20

func identityFileSigner(identityPath, passphrase string) (ssh.Signer, error) {
	identityPath = strings.TrimSpace(identityPath)
	if identityPath == "" {
		return nil, nil
	}
	if identityPath == "~" || strings.HasPrefix(identityPath, "~/") || strings.HasPrefix(identityPath, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory: %w", err)
		}
		if identityPath == "~" {
			identityPath = home
		} else {
			identityPath = filepath.Join(home, identityPath[2:])
		}
	} else {
		absolutePath, err := filepath.Abs(identityPath)
		if err != nil {
			return nil, fmt.Errorf("resolve path %q: %w", identityPath, err)
		}
		identityPath = absolutePath
	}

	file, err := os.Open(identityPath)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", identityPath, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect %q: %w", identityPath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", identityPath)
	}
	if info.Size() > maxIdentityFileBytes {
		return nil, fmt.Errorf("%q exceeds the %d-byte private key limit", identityPath, maxIdentityFileBytes)
	}
	keyBytes, err := io.ReadAll(io.LimitReader(file, maxIdentityFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", identityPath, err)
	}
	defer clear(keyBytes)
	if len(keyBytes) > maxIdentityFileBytes {
		return nil, fmt.Errorf("%q exceeds the %d-byte private key limit", identityPath, maxIdentityFileBytes)
	}

	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err == nil {
		return attachOpenSSHUserCertificate(identityPath, signer)
	}
	var passphraseError *ssh.PassphraseMissingError
	if !errors.As(err, &passphraseError) || passphrase == "" {
		if errors.As(err, &passphraseError) {
			return nil, nil
		}
		return nil, fmt.Errorf("parse %q: %w", identityPath, err)
	}
	signer, err = ssh.ParsePrivateKeyWithPassphrase(keyBytes, []byte(passphrase))
	if err != nil {
		// A failed passphrase is treated like any other rejected auth method;
		// the normal SSH auth prompt can then request a password or key passphrase.
		return nil, nil
	}
	return attachOpenSSHUserCertificate(identityPath, signer)
}

func attachOpenSSHUserCertificate(identityPath string, signer ssh.Signer) (ssh.Signer, error) {
	certificatePath := identityPath + "-cert.pub"
	file, err := os.Open(certificatePath)
	if errors.Is(err, os.ErrNotExist) {
		return signer, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open OpenSSH user certificate %q: %w", certificatePath, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect OpenSSH user certificate %q: %w", certificatePath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("OpenSSH user certificate %q is not a regular file", certificatePath)
	}
	if info.Size() > maxIdentityFileBytes {
		return nil, fmt.Errorf("OpenSSH user certificate %q exceeds the %d-byte limit", certificatePath, maxIdentityFileBytes)
	}
	certificateBytes, err := io.ReadAll(io.LimitReader(file, maxIdentityFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read OpenSSH user certificate %q: %w", certificatePath, err)
	}
	if len(certificateBytes) > maxIdentityFileBytes {
		return nil, fmt.Errorf("OpenSSH user certificate %q exceeds the %d-byte limit", certificatePath, maxIdentityFileBytes)
	}
	publicKey, _, options, rest, err := ssh.ParseAuthorizedKey(certificateBytes)
	if err != nil {
		return nil, fmt.Errorf("parse OpenSSH user certificate %q: %w", certificatePath, err)
	}
	if len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("OpenSSH user certificate %q must contain exactly one authorized-key entry without options", certificatePath)
	}
	certificate, ok := publicKey.(*ssh.Certificate)
	if !ok || certificate.CertType != ssh.UserCert {
		return nil, fmt.Errorf("OpenSSH certificate %q is not a user certificate", certificatePath)
	}
	certificateSigner, err := ssh.NewCertSigner(certificate, signer)
	if err != nil {
		return nil, fmt.Errorf("OpenSSH user certificate %q does not match private key %q: %w", certificatePath, identityPath, err)
	}
	return certificateSigner, nil
}
