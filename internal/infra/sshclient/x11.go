package sshclient

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	x11AuthProtocol = "MIT-MAGIC-COOKIE-1"
	maxX11AuthField = 4096
	maxX11Channels  = 32
)

type x11Display struct {
	network string
	address string
	screen  uint32
}

type x11Config struct {
	display    x11Display
	realCookie []byte
	fakeCookie []byte
}

type x11Request struct {
	SingleConnection bool
	AuthProtocol     string
	AuthCookie       string
	Screen           uint32
}

type x11Forwarder struct {
	config   x11Config
	channels <-chan ssh.NewChannel
	ctx      context.Context
	cancel   context.CancelFunc

	mu      sync.Mutex
	active  map[ssh.Channel]struct{}
	stopped bool
	wg      sync.WaitGroup
}

func discoverX11Config(ctx context.Context) (x11Config, error) {
	displayName := strings.TrimSpace(os.Getenv("DISPLAY"))
	if displayName == "" {
		return x11Config{}, errors.New("已启用 X11 转发，但本机 DISPLAY 环境变量为空")
	}
	display, err := parseX11Display(displayName)
	if err != nil {
		return x11Config{}, fmt.Errorf("解析本机 DISPLAY 失败：%w", err)
	}
	xauthCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(xauthCtx, "xauth", "list", displayName).Output()
	if err != nil {
		return x11Config{}, fmt.Errorf("读取 X11 授权失败（需要本机安装并配置 xauth）：%w", err)
	}
	realCookie, err := parseX11AuthCookie(string(output))
	if err != nil {
		return x11Config{}, err
	}
	fakeCookie := make([]byte, len(realCookie))
	if _, err := rand.Read(fakeCookie); err != nil {
		return x11Config{}, fmt.Errorf("生成 X11 临时授权失败：%w", err)
	}
	return x11Config{display: display, realCookie: realCookie, fakeCookie: fakeCookie}, nil
}

func parseX11Display(value string) (x11Display, error) {
	colon := strings.LastIndexByte(value, ':')
	if colon < 0 || colon == len(value)-1 {
		return x11Display{}, errors.New("DISPLAY 格式应为 :display[.screen] 或 host:display[.screen]")
	}
	host, suffix := value[:colon], value[colon+1:]
	parts := strings.Split(suffix, ".")
	if len(parts) > 2 {
		return x11Display{}, errors.New("DISPLAY screen 格式无效")
	}
	displayNumber, err := strconv.Atoi(parts[0])
	if err != nil || displayNumber < 0 || displayNumber > 59535 {
		return x11Display{}, errors.New("DISPLAY 编号无效")
	}
	screen := uint64(0)
	if len(parts) == 2 {
		screen, err = strconv.ParseUint(parts[1], 10, 32)
		if err != nil {
			return x11Display{}, errors.New("DISPLAY screen 编号无效")
		}
	}
	if strings.HasPrefix(host, "tcp/") {
		host = strings.TrimPrefix(host, "tcp/")
	}
	if host == "" || host == "unix" || strings.HasPrefix(host, "/") || strings.HasSuffix(host, "/unix") {
		address := ""
		switch {
		case strings.HasPrefix(host, "/"):
			address = host
		case strings.HasSuffix(host, "/unix"):
			address = filepath.Join(strings.TrimSuffix(host, "/unix"), ".X11-unix", "X"+strconv.Itoa(displayNumber))
		default:
			address = filepath.Join("/tmp/.X11-unix", "X"+strconv.Itoa(displayNumber))
		}
		return x11Display{network: "unix", address: address, screen: uint32(screen)}, nil
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return x11Display{}, errors.New("为保护本机 X11 授权，只允许连接 Unix socket 或本机回环地址")
	}
	return x11Display{
		network: "tcp", address: net.JoinHostPort(host, strconv.Itoa(6000+displayNumber)), screen: uint32(screen),
	}, nil
}

func parseX11AuthCookie(output string) ([]byte, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		for index := 0; index+1 < len(fields); index++ {
			if fields[index] != x11AuthProtocol {
				continue
			}
			cookie, err := hex.DecodeString(fields[index+1])
			if err == nil && len(cookie) > 0 && len(cookie) <= maxX11AuthField {
				return cookie, nil
			}
		}
	}
	return nil, errors.New("本机 xauth 没有返回有效的 MIT-MAGIC-COOKIE-1 授权")
}

func startX11Forwarding(client *ssh.Client, session *ssh.Session, config x11Config) (*x11Forwarder, error) {
	channels := client.HandleChannelOpen("x11")
	request := x11Request{
		AuthProtocol: x11AuthProtocol,
		AuthCookie:   hex.EncodeToString(config.fakeCookie),
		Screen:       config.display.screen,
	}
	accepted, err := session.SendRequest("x11-req", true, ssh.Marshal(request))
	if err != nil {
		return nil, fmt.Errorf("请求 SSH X11 转发失败：%w", err)
	}
	if !accepted {
		return nil, errors.New("SSH 服务器拒绝 X11 转发请求")
	}
	ctx, cancel := context.WithCancel(context.Background())
	forwarder := &x11Forwarder{
		config: config, channels: channels, ctx: ctx, cancel: cancel,
		active: make(map[ssh.Channel]struct{}),
	}
	forwarder.wg.Add(1)
	go forwarder.acceptLoop()
	return forwarder, nil
}

func (forwarder *x11Forwarder) acceptLoop() {
	defer forwarder.wg.Done()
	for {
		select {
		case <-forwarder.ctx.Done():
			return
		case incoming, ok := <-forwarder.channels:
			if !ok {
				return
			}
			forwarder.mu.Lock()
			full := len(forwarder.active) >= maxX11Channels || forwarder.stopped
			forwarder.mu.Unlock()
			if full {
				_ = incoming.Reject(ssh.ResourceShortage, "too many X11 connections")
				continue
			}
			channel, requests, err := incoming.Accept()
			if err != nil {
				continue
			}
			go ssh.DiscardRequests(requests)
			if !forwarder.track(channel) {
				_ = channel.Close()
				continue
			}
			forwarder.wg.Add(1)
			go forwarder.handleChannel(channel)
		}
	}
}

func (forwarder *x11Forwarder) track(channel ssh.Channel) bool {
	forwarder.mu.Lock()
	defer forwarder.mu.Unlock()
	if forwarder.stopped || len(forwarder.active) >= maxX11Channels {
		return false
	}
	forwarder.active[channel] = struct{}{}
	return true
}

func (forwarder *x11Forwarder) untrack(channel ssh.Channel) {
	forwarder.mu.Lock()
	delete(forwarder.active, channel)
	forwarder.mu.Unlock()
}

func (forwarder *x11Forwarder) handleChannel(channel ssh.Channel) {
	defer forwarder.wg.Done()
	defer forwarder.untrack(channel)
	defer channel.Close()
	local, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(forwarder.ctx, forwarder.config.display.network, forwarder.config.display.address)
	if err != nil {
		return
	}
	defer local.Close()
	if err := rewriteX11Setup(channel, local, forwarder.config.fakeCookie, forwarder.config.realCookie); err != nil {
		return
	}
	finished := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(local, channel)
		if halfCloser, ok := local.(interface{ CloseWrite() error }); ok {
			_ = halfCloser.CloseWrite()
		}
		finished <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(channel, local)
		_ = channel.CloseWrite()
		finished <- struct{}{}
	}()
	<-finished
	_ = channel.Close()
	_ = local.Close()
	<-finished
}

func (forwarder *x11Forwarder) Close() {
	forwarder.mu.Lock()
	if !forwarder.stopped {
		forwarder.stopped = true
		forwarder.cancel()
		for channel := range forwarder.active {
			_ = channel.Close()
		}
	}
	forwarder.mu.Unlock()
	forwarder.wg.Wait()
}

func rewriteX11Setup(reader io.Reader, writer io.Writer, fakeCookie, realCookie []byte) error {
	header := make([]byte, 12)
	if _, err := io.ReadFull(reader, header); err != nil {
		return fmt.Errorf("读取 X11 握手失败：%w", err)
	}
	var order binary.ByteOrder
	switch header[0] {
	case 'l':
		order = binary.LittleEndian
	case 'B':
		order = binary.BigEndian
	default:
		return errors.New("X11 握手的字节序标记无效")
	}
	nameLength := int(order.Uint16(header[6:8]))
	dataLength := int(order.Uint16(header[8:10]))
	if nameLength < 1 || nameLength > maxX11AuthField || dataLength < 1 || dataLength > maxX11AuthField {
		return errors.New("X11 授权字段长度无效")
	}
	authName := make([]byte, paddedX11Length(nameLength))
	if _, err := io.ReadFull(reader, authName); err != nil {
		return fmt.Errorf("读取 X11 授权协议失败：%w", err)
	}
	if string(authName[:nameLength]) != x11AuthProtocol {
		return errors.New("远程 X11 客户端使用了不支持的授权协议")
	}
	authData := make([]byte, paddedX11Length(dataLength))
	if _, err := io.ReadFull(reader, authData); err != nil {
		return fmt.Errorf("读取 X11 授权数据失败：%w", err)
	}
	if len(fakeCookie) != dataLength || subtle.ConstantTimeCompare(authData[:dataLength], fakeCookie) != 1 {
		return errors.New("远程 X11 临时授权不匹配")
	}
	order.PutUint16(header[8:10], uint16(len(realCookie)))
	if err := writeX11Bytes(writer, header); err != nil {
		return err
	}
	if err := writeX11Bytes(writer, authName); err != nil {
		return err
	}
	realData := make([]byte, paddedX11Length(len(realCookie)))
	copy(realData, realCookie)
	if err := writeX11Bytes(writer, realData); err != nil {
		return err
	}
	return nil
}

func writeX11Bytes(writer io.Writer, data []byte) error {
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

func paddedX11Length(length int) int { return (length + 3) &^ 3 }
