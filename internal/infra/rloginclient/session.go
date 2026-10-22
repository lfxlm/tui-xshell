// Package rloginclient implements the BSD Rlogin terminal protocol (RFC 1282).
package rloginclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout      = 15 * time.Second
	maxHandshakeMessage = 4096
)

// Session owns a raw Rlogin TCP stream. Rlogin provides no encryption.
type Session struct {
	conn net.Conn
	read *io.PipeReader
	pipe *io.PipeWriter

	writeMu sync.Mutex
	close   sync.Once
	width   int
	height  int
}

// CurrentUsername returns the local account name used in the Rlogin handshake.
func CurrentUsername() (string, error) {
	if current, err := user.Current(); err == nil && strings.TrimSpace(current.Username) != "" {
		return current.Username, nil
	}
	for _, name := range []string{"USER", "USERNAME"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value, nil
		}
	}
	return "", errors.New("无法读取本机登录用户名，Rlogin 握手需要客户端用户名")
}

// Dial connects to a Rlogin server, sends the four null-terminated handshake
// fields, reads the server's status byte, and starts forwarding the raw stream.
func Dial(ctx context.Context, address, localUser, remoteUser, terminalType string, timeout time.Duration, width, height int) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	localUser = strings.TrimSpace(localUser)
	if localUser == "" {
		var err error
		localUser, err = CurrentUsername()
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(address) == "" {
		return nil, errors.New("Rlogin 地址不能为空")
	}
	if width < 1 || height < 1 || width > 65535 || height > 65535 {
		return nil, errors.New("Rlogin 终端尺寸必须在 1 到 65535 之间")
	}
	if terminalType == "" {
		terminalType = "xterm-256color/9600"
	}
	for label, value := range map[string]string{
		"客户端用户名": localUser,
		"服务器用户名": remoteUser,
		"终端类型":   terminalType,
	} {
		if strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("Rlogin %s不能包含 NUL 字符", label)
		}
		if len(value) > 255 {
			return nil, fmt.Errorf("Rlogin %s不能超过 255 字节", label)
		}
	}

	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("连接 Rlogin 服务器失败：%w", err)
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		stopCancel()
		_ = conn.Close()
		return nil, fmt.Errorf("设置 Rlogin 握手超时失败：%w", err)
	}
	handshake := []byte{0}
	for _, field := range []string{localUser, remoteUser, terminalType} {
		handshake = append(handshake, field...)
		handshake = append(handshake, 0)
	}
	if err := writeFull(conn, handshake); err != nil {
		stopCancel()
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("发送 Rlogin 握手失败：%w", err)
	}
	var response [1]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		stopCancel()
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("读取 Rlogin 握手响应失败：%w", err)
	}
	if response[0] != 0 {
		message, _ := readRefusalMessage(conn)
		stopCancel()
		_ = conn.Close()
		if message == "" {
			message = "服务器拒绝了连接"
		}
		return nil, fmt.Errorf("Rlogin 登录失败：%s", message)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		stopCancel()
		_ = conn.Close()
		return nil, fmt.Errorf("完成 Rlogin 握手失败：%w", err)
	}
	stopCancel()
	reader, writer := io.Pipe()
	session := &Session{conn: conn, read: reader, pipe: writer, width: width, height: height}
	if rloginUrgentDataSupported() {
		if err := configureRloginUrgentData(conn); err != nil {
			_ = session.Close()
			return nil, fmt.Errorf("启用 Rlogin 紧急数据处理失败：%w", err)
		}
	}
	if err := session.writeWindowSize(width, height); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("发送 Rlogin 终端尺寸失败：%w", err)
	}
	go session.readLoop()
	return session, nil
}

func readRefusalMessage(conn net.Conn) (string, error) {
	message := make([]byte, 0, 128)
	buffer := []byte{0}
	for len(message) < maxHandshakeMessage {
		if _, err := io.ReadFull(conn, buffer); err != nil {
			return strings.TrimSpace(string(message)), err
		}
		if buffer[0] == '\n' || buffer[0] == 0 {
			break
		}
		message = append(message, buffer[0])
	}
	return strings.TrimSpace(string(message)), nil
}

func (session *Session) Read(buffer []byte) (int, error) { return session.read.Read(buffer) }

// Write forwards input bytes unchanged to the remote terminal.
func (session *Session) Write(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	return writeFull(session.conn, data)
}

// Resize sends RFC 1282's 12-byte in-band window-size message. Character rows
// and columns are reported; pixel dimensions are zero because a TUI has no
// portable mapping from terminal cells to physical pixels.
func (session *Session) Resize(width, height int) error {
	if width < 1 || height < 1 || width > 65535 || height > 65535 {
		return errors.New("Rlogin 终端尺寸必须在 1 到 65535 之间")
	}
	return session.writeWindowSize(width, height)
}

func (session *Session) writeWindowSize(width, height int) error {
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	if err := writeFull(session.conn, windowSizeMessage(width, height)); err != nil {
		return err
	}
	session.width, session.height = width, height
	return nil
}

func windowSizeMessage(width, height int) []byte {
	return []byte{
		0xff, 0xff, 's', 's',
		byte(height >> 8), byte(height),
		byte(width >> 8), byte(width),
		0, 0, 0, 0,
	}
}

func (session *Session) resendWindowSize() error {
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	return writeFull(session.conn, windowSizeMessage(session.width, session.height))
}

func (session *Session) Close() error {
	var err error
	session.close.Do(func() {
		err = session.conn.Close()
		_ = session.pipe.CloseWithError(io.ErrClosedPipe)
		_ = session.read.Close()
	})
	return err
}

func (session *Session) readPlainLoop() {
	defer func() { _ = session.pipe.Close() }()
	buffer := make([]byte, 4096)
	for {
		count, err := session.conn.Read(buffer)
		if count > 0 {
			if _, writeErr := session.pipe.Write(buffer[:count]); writeErr != nil {
				return
			}
		}
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				_ = session.pipe.Close()
			} else {
				_ = session.pipe.CloseWithError(err)
			}
			return
		}
	}
}

func (session *Session) readLoop() {
	if rloginUrgentDataSupported() {
		session.readUrgentLoop()
		return
	}
	session.readPlainLoop()
}

func (session *Session) readUrgentLoop() {
	defer func() { _ = session.pipe.Close() }()
	buffer := make([]byte, 4096)
	pending := make([]byte, 0, len(buffer))
	lastReadAt := time.Time{}
	const flushDelay = 20 * time.Millisecond
	const maxPending = 64 << 10
	flush := func() bool {
		if len(pending) == 0 {
			return true
		}
		if _, err := session.pipe.Write(pending); err != nil {
			return false
		}
		pending = pending[:0]
		return true
	}
	for {
		wait := 100 * time.Millisecond
		if len(pending) > 0 {
			remaining := flushDelay - time.Since(lastReadAt)
			if remaining <= 0 {
				if !flush() {
					return
				}
				continue
			}
			wait = min(wait, remaining)
		}
		count, control, hasControl, err := readRloginUrgentChunk(session.conn, buffer, wait)
		if count > 0 {
			pending = append(pending, buffer[:count]...)
			lastReadAt = time.Now()
		}
		if hasControl {
			switch control {
			case 0x02:
				pending = pending[:0]
			case 0x80:
				if !flush() {
					return
				}
				if err == nil {
					err = session.resendWindowSize()
				}
			default:
				// The urgent control byte is never terminal output. Cooked/raw
				// flow-control handling is unnecessary for the TUI's raw input path.
				if !flush() {
					return
				}
			}
		}
		if len(pending) >= maxPending || (len(pending) > 0 && time.Since(lastReadAt) >= flushDelay) {
			if !flush() {
				return
			}
		}
		if err != nil {
			if !flush() {
				return
			}
			if !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
				_ = session.pipe.CloseWithError(err)
			}
			return
		}
	}
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if written > 0 {
			data = data[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// Address formats a host and port for a Rlogin TCP connection.
func Address(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) }
