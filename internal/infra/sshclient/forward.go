package sshclient

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type ForwardKind uint8

const (
	ForwardLocalTCP ForwardKind = iota
	ForwardSOCKS5
	ForwardRemoteTCP
)

// LocalForward owns a local TCP listener whose connections are carried by an
// authenticated SSH transport.
type LocalForward struct {
	session       *Session
	listener      net.Listener
	remoteAddress string
	kind          ForwardKind

	done         chan struct{}
	stopOne      sync.Once
	wg           sync.WaitGroup
	connectionMu sync.Mutex
	connections  map[net.Conn]struct{}
	channels     map[uint64]*forwardChannel
	nextChannel  uint64
	errMu        sync.Mutex
	err          error
	stopErr      error
}

// ForwardChannelInfo describes one currently established connection carried
// by a forwarding rule. Byte counts are from the connecting client's point of
// view: BytesToTarget flows toward the configured target.
type ForwardChannelInfo struct {
	ID              uint64
	Target          string
	StartedAt       time.Time
	BytesToTarget   uint64
	BytesFromTarget uint64
}

type forwardChannel struct {
	info       ForwardChannelInfo
	clientConn net.Conn
	targetConn net.Conn
	toTarget   atomic.Uint64
	fromTarget atomic.Uint64
	closeOnce  sync.Once
	closeErr   error
}

func (channel *forwardChannel) snapshot() ForwardChannelInfo {
	info := channel.info
	info.BytesToTarget = channel.toTarget.Load()
	info.BytesFromTarget = channel.fromTarget.Load()
	return info
}

// ListenAddress returns the actual listener address. For remote forwards this
// is the address opened on the SSH server.
func (f *LocalForward) ListenAddress() string { return f.listener.Addr().String() }

// LocalAddress is kept as an alias for callers that only manage local listeners.
func (f *LocalForward) LocalAddress() string { return f.ListenAddress() }

// RemoteAddress returns the destination reached from the SSH server.
func (f *LocalForward) RemoteAddress() string { return f.remoteAddress }

func (f *LocalForward) Kind() ForwardKind { return f.kind }

// Active reports whether the listener has not been closed.
func (f *LocalForward) Active() bool {
	select {
	case <-f.done:
		return false
	default:
		return true
	}
}

// Err reports an unexpected accept or forwarding error, if one occurred.
func (f *LocalForward) Err() error {
	f.errMu.Lock()
	defer f.errMu.Unlock()
	return f.err
}

// Channels returns a stable snapshot of the active forwarded connections.
func (f *LocalForward) Channels() []ForwardChannelInfo {
	f.connectionMu.Lock()
	defer f.connectionMu.Unlock()
	channels := make([]ForwardChannelInfo, 0, len(f.channels))
	for _, channel := range f.channels {
		channels = append(channels, channel.snapshot())
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].ID < channels[j].ID })
	return channels
}

// CloseChannel closes one active forwarded connection without stopping its
// listener or any other channels.
func (f *LocalForward) CloseChannel(id uint64) error {
	f.connectionMu.Lock()
	channel := f.channels[id]
	f.connectionMu.Unlock()
	if channel == nil {
		return errors.New("SSH 转发通道已结束")
	}
	channel.closeOnce.Do(func() {
		clientErr := channel.clientConn.Close()
		targetErr := channel.targetConn.Close()
		if errors.Is(clientErr, net.ErrClosed) {
			clientErr = nil
		}
		if errors.Is(targetErr, net.ErrClosed) {
			targetErr = nil
		}
		channel.closeErr = errors.Join(clientErr, targetErr)
	})
	return channel.closeErr
}

// Close stops accepting connections, closes active streams, and waits for
// forwarding goroutines to exit. It is safe to call more than once.
func (f *LocalForward) Close() error {
	f.stop()
	f.wg.Wait()
	return f.stopErr
}

func (f *LocalForward) stop() {
	f.stopOne.Do(func() {
		close(f.done)
		if err := f.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			f.stopErr = err
		}
		f.connectionMu.Lock()
		for connection := range f.connections {
			_ = connection.Close()
		}
		f.connectionMu.Unlock()
		f.session.forwardMu.Lock()
		delete(f.session.forwards, f)
		f.session.forwardMu.Unlock()
	})
}

func (f *LocalForward) acceptLoop() {
	defer f.wg.Done()
	for {
		local, err := f.listener.Accept()
		if err != nil {
			select {
			case <-f.done:
				return
			default:
			}
			f.setError(fmt.Errorf("接受本地转发连接失败：%w", err))
			return
		}
		if !f.track(local) {
			_ = local.Close()
			return
		}
		f.wg.Add(1)
		switch f.kind {
		case ForwardSOCKS5:
			go f.forwardSOCKS5Connection(local)
		case ForwardRemoteTCP:
			go f.forwardRemoteConnection(local)
		default:
			go f.forwardConnection(local)
		}
	}
}

func (f *LocalForward) forwardSOCKS5Connection(local net.Conn) {
	defer f.wg.Done()
	defer f.untrack(local)
	defer local.Close()
	target, err := negotiateSOCKS5(local)
	if err != nil {
		return
	}
	remote, err := f.session.client.Dial("tcp", target)
	if err != nil {
		_ = writeSOCKS5Reply(local, 5)
		select {
		case <-f.done:
		default:
			f.setError(fmt.Errorf("连接 SOCKS 目标 %s 失败：%w", target, err))
		}
		return
	}
	if !f.track(remote) {
		_ = remote.Close()
		return
	}
	defer f.untrack(remote)
	defer remote.Close()
	channel, ok := f.beginChannel(local, remote, target)
	if !ok {
		return
	}
	defer f.finishChannel(channel)
	if err := writeSOCKS5Reply(local, 0); err != nil {
		return
	}
	finished := make(chan struct{}, 2)
	go func() {
		copyForwardChannel(channel, local, remote, &channel.toTarget, finished)
	}()
	go func() {
		copyForwardChannel(channel, remote, local, &channel.fromTarget, finished)
	}()
	<-finished
	<-finished
}

func negotiateSOCKS5(connection net.Conn) (string, error) {
	var greeting [2]byte
	if _, err := io.ReadFull(connection, greeting[:]); err != nil {
		return "", err
	}
	if greeting[0] != 5 || greeting[1] == 0 {
		return "", errors.New("SOCKS5 握手版本或认证方法无效")
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(connection, methods); err != nil {
		return "", err
	}
	noAuth := false
	for _, method := range methods {
		if method == 0 {
			noAuth = true
			break
		}
	}
	if !noAuth {
		_, _ = connection.Write([]byte{5, 0xff})
		return "", errors.New("SOCKS5 客户端未提供无认证方法")
	}
	if _, err := connection.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	var request [4]byte
	if _, err := io.ReadFull(connection, request[:]); err != nil {
		return "", err
	}
	if request[0] != 5 || request[2] != 0 {
		_ = writeSOCKS5Reply(connection, 1)
		return "", errors.New("SOCKS5 请求格式无效")
	}
	if request[1] != 1 {
		_ = writeSOCKS5Reply(connection, 7)
		return "", errors.New("SOCKS5 仅支持 TCP CONNECT")
	}
	var host string
	switch request[3] {
	case 1:
		address := make([]byte, 4)
		if _, err := io.ReadFull(connection, address); err != nil {
			return "", err
		}
		host = net.IP(address).String()
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(connection, size[:]); err != nil {
			return "", err
		}
		if size[0] == 0 {
			_ = writeSOCKS5Reply(connection, 8)
			return "", errors.New("SOCKS5 域名不能为空")
		}
		address := make([]byte, int(size[0]))
		if _, err := io.ReadFull(connection, address); err != nil {
			return "", err
		}
		host = string(address)
	case 4:
		address := make([]byte, 16)
		if _, err := io.ReadFull(connection, address); err != nil {
			return "", err
		}
		host = net.IP(address).String()
	default:
		_ = writeSOCKS5Reply(connection, 8)
		return "", errors.New("SOCKS5 地址类型不受支持")
	}
	var portBytes [2]byte
	if _, err := io.ReadFull(connection, portBytes[:]); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBytes[:])
	if port == 0 || host == "" {
		_ = writeSOCKS5Reply(connection, 1)
		return "", errors.New("SOCKS5 目标地址无效")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func writeSOCKS5Reply(connection net.Conn, code byte) error {
	_, err := connection.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}

func (f *LocalForward) forwardConnection(local net.Conn) {
	defer f.wg.Done()
	defer f.untrack(local)
	defer local.Close()

	remote, err := f.session.client.Dial("tcp", f.remoteAddress)
	if err != nil {
		select {
		case <-f.done:
		default:
			f.setError(fmt.Errorf("连接远程目标 %s 失败：%w", f.remoteAddress, err))
		}
		return
	}
	if !f.track(remote) {
		_ = remote.Close()
		return
	}
	defer f.untrack(remote)
	defer remote.Close()
	channel, ok := f.beginChannel(local, remote, f.remoteAddress)
	if !ok {
		return
	}
	defer f.finishChannel(channel)

	finished := make(chan struct{}, 2)
	go func() {
		copyForwardChannel(channel, local, remote, &channel.toTarget, finished)
	}()
	go func() {
		copyForwardChannel(channel, remote, local, &channel.fromTarget, finished)
	}()
	<-finished
	<-finished
}

func (f *LocalForward) forwardRemoteConnection(remote net.Conn) {
	defer f.wg.Done()
	defer f.untrack(remote)
	defer remote.Close()
	local, err := net.DialTimeout("tcp", f.remoteAddress, connectTimeout)
	if err != nil {
		select {
		case <-f.done:
		default:
			f.setError(fmt.Errorf("连接本地目标 %s 失败：%w", f.remoteAddress, err))
		}
		return
	}
	if !f.track(local) {
		_ = local.Close()
		return
	}
	defer f.untrack(local)
	defer local.Close()
	channel, ok := f.beginChannel(remote, local, f.remoteAddress)
	if !ok {
		return
	}
	defer f.finishChannel(channel)
	finished := make(chan struct{}, 2)
	go func() {
		copyForwardChannel(channel, remote, local, &channel.toTarget, finished)
	}()
	go func() {
		copyForwardChannel(channel, local, remote, &channel.fromTarget, finished)
	}()
	<-finished
	<-finished
}

func (f *LocalForward) beginChannel(clientConn, targetConn net.Conn, target string) (*forwardChannel, bool) {
	f.connectionMu.Lock()
	defer f.connectionMu.Unlock()
	select {
	case <-f.done:
		return nil, false
	default:
	}
	f.nextChannel++
	channel := &forwardChannel{
		info:       ForwardChannelInfo{ID: f.nextChannel, Target: target, StartedAt: time.Now()},
		clientConn: clientConn, targetConn: targetConn,
	}
	if f.channels == nil {
		f.channels = make(map[uint64]*forwardChannel)
	}
	f.channels[channel.info.ID] = channel
	return channel, true
}

func (f *LocalForward) finishChannel(channel *forwardChannel) {
	f.connectionMu.Lock()
	delete(f.channels, channel.info.ID)
	f.connectionMu.Unlock()
}

type forwardByteWriter struct {
	connection net.Conn
	counter    *atomic.Uint64
}

func (writer forwardByteWriter) Write(buffer []byte) (int, error) {
	n, err := writer.connection.Write(buffer)
	if n > 0 {
		writer.counter.Add(uint64(n))
	}
	return n, err
}

func copyForwardChannel(_ *forwardChannel, source, destination net.Conn, counter *atomic.Uint64, finished chan<- struct{}) {
	_, _ = io.Copy(forwardByteWriter{connection: destination, counter: counter}, source)
	closeWrite(destination)
	finished <- struct{}{}
}

func (f *LocalForward) track(connection net.Conn) bool {
	f.connectionMu.Lock()
	defer f.connectionMu.Unlock()
	select {
	case <-f.done:
		return false
	default:
	}
	f.connections[connection] = struct{}{}
	return true
}

func (f *LocalForward) untrack(connection net.Conn) {
	f.connectionMu.Lock()
	delete(f.connections, connection)
	f.connectionMu.Unlock()
}

func (f *LocalForward) setError(err error) {
	f.errMu.Lock()
	if f.err == nil {
		f.err = err
	}
	f.errMu.Unlock()
}

func closeWrite(connection net.Conn) {
	if halfCloser, ok := connection.(interface{ CloseWrite() error }); ok {
		_ = halfCloser.CloseWrite()
	}
}

func splitTCPAddress(address string, allowZeroPort bool) (string, int, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, err
	}
	if host == "" && !allowZeroPort {
		return "", 0, errors.New("主机不能为空")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 || (!allowZeroPort && port == 0) {
		return "", 0, errors.New("端口必须在 1 到 65535 之间")
	}
	return host, port, nil
}
