package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"tui-ssh-workbench/internal/infra/xymodem"
	"tui-ssh-workbench/internal/infra/zmodem"
)

const maxQueuedZMODEMBytes = 16 << 20

type zmodemTransferMode uint8

const (
	zmodemReceive zmodemTransferMode = iota
	zmodemSend
)

type zmodemTransferPhase uint8

const (
	zmodemChoose zmodemTransferPhase = iota
	zmodemPrompt
	zmodemRunning
	zmodemCanceling
)

type terminalTransferProtocol uint8

const (
	terminalTransferZMODEM terminalTransferProtocol = iota
	terminalTransferXMODEM
	terminalTransferYMODEM
)

type zmodemTransferState struct {
	requestID uint64
	tabID     string
	paneID    string
	session   terminalSession
	protocol  terminalTransferProtocol
	mode      zmodemTransferMode
	phase     zmodemTransferPhase
	selection int
	input     *zmodemInput
	path      string
	cursor    int
	overwrite bool
	bytes     int64
	total     int64
	fileName  string
	progress  chan zmodem.Progress
	cancel    context.CancelFunc
	errorText string
}

type zmodemTransferFinishedMsg struct {
	requestID uint64
	files     []zmodem.ReceivedFile
	trailing  []byte
	err       error
}

type zmodemTransferProgressMsg struct {
	requestID uint64
	progress  zmodem.Progress
	closed    bool
}

var (
	errZMODEMInputClosed = errors.New("zmodem input is closed")
	errZMODEMInputLimit  = errors.New("zmodem input buffer exceeded its limit")
)

type zmodemInput struct {
	mu       sync.Mutex
	data     []byte
	wake     chan struct{}
	closed   bool
	closeErr error
}

func newZMODEMInput() *zmodemInput {
	return &zmodemInput{wake: make(chan struct{}, 1)}
}

func (input *zmodemInput) Append(data []byte) error {
	if input == nil || len(data) == 0 {
		return nil
	}
	input.mu.Lock()
	if input.closed {
		input.mu.Unlock()
		return errZMODEMInputClosed
	}
	if len(input.data)+len(data) > maxQueuedZMODEMBytes {
		input.mu.Unlock()
		return errZMODEMInputLimit
	}
	input.data = append(input.data, data...)
	input.mu.Unlock()
	input.signal()
	return nil
}

func (input *zmodemInput) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	for {
		input.mu.Lock()
		if len(input.data) > 0 {
			n := copy(data, input.data)
			input.data = append(input.data[:0], input.data[n:]...)
			input.mu.Unlock()
			return n, nil
		}
		if input.closed {
			err := input.closeErr
			input.mu.Unlock()
			if err == nil {
				return 0, io.EOF
			}
			return 0, err
		}
		input.mu.Unlock()
		<-input.wake
	}
}

func (input *zmodemInput) Close(err error) {
	if input == nil {
		return
	}
	input.mu.Lock()
	if !input.closed {
		input.closed = true
		input.closeErr = err
	}
	input.mu.Unlock()
	input.signal()
}

func (input *zmodemInput) Drain() []byte {
	if input == nil {
		return nil
	}
	input.mu.Lock()
	defer input.mu.Unlock()
	data := append([]byte(nil), input.data...)
	input.data = nil
	return data
}

func (input *zmodemInput) signal() {
	select {
	case input.wake <- struct{}{}:
	default:
	}
}

type zmodemSessionWriter struct{ session terminalSession }

func (writer zmodemSessionWriter) Write(data []byte) (int, error) {
	if writer.session == nil {
		return 0, errors.New("terminal session is closed")
	}
	if err := writer.session.Write(data); err != nil {
		return 0, err
	}
	return len(data), nil
}

type terminalTransferStream struct {
	reader io.Reader
	writer io.Writer
}

func (stream terminalTransferStream) Read(data []byte) (int, error) { return stream.reader.Read(data) }
func (stream terminalTransferStream) Write(data []byte) (int, error) {
	return stream.writer.Write(data)
}

type zmodemMarker struct {
	bytes []byte
	mode  zmodemTransferMode
}

var zmodemStartMarkers = []zmodemMarker{
	{bytes: []byte("**\x18B00"), mode: zmodemReceive}, // sender invitation: ZRQINIT
	{bytes: []byte("*\x18A\x00"), mode: zmodemReceive},
	{bytes: []byte("**\x18B01"), mode: zmodemSend}, // remote receiver: ZRINIT
	{bytes: []byte("*\x18A\x01"), mode: zmodemSend},
}

// detectZMODEMStart keeps only a possible marker prefix between read chunks.
// It returns ordinary terminal bytes separately so binary frames never enter
// the VT emulator or output log.
func detectZMODEMStart(pending, data []byte) (terminalBytes, protocolBytes, nextPending []byte, mode zmodemTransferMode, found bool) {
	combined := append(append([]byte(nil), pending...), data...)
	for index := 0; index < len(combined); index++ {
		for _, marker := range zmodemStartMarkers {
			if bytes.HasPrefix(combined[index:], marker.bytes) {
				return append([]byte(nil), combined[:index]...), append([]byte(nil), combined[index:]...), nil, marker.mode, true
			}
		}
	}
	keep := 0
	for _, marker := range zmodemStartMarkers {
		maxPrefix := min(len(marker.bytes)-1, len(combined))
		for prefix := maxPrefix; prefix > keep; prefix-- {
			if bytes.Equal(combined[len(combined)-prefix:], marker.bytes[:prefix]) {
				keep = prefix
				break
			}
		}
	}
	cut := len(combined) - keep
	return append([]byte(nil), combined[:cut]...), nil, append([]byte(nil), combined[cut:]...), 0, false
}

func (m *modelState) openZMODEMPrompt(pane *terminalPane, mode zmodemTransferMode, firstFrame []byte) bool {
	if pane == nil || pane.session == nil || m.zmodemTransfer != nil {
		return false
	}
	tab := m.tabForPane(pane)
	if tab == nil {
		return false
	}
	path := ""
	if mode == zmodemReceive {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			path = "."
		} else {
			path = filepath.Join(home, "Downloads")
		}
	}
	m.nextZMODEMRequestID++
	input := newZMODEMInput()
	if err := input.Append(firstFrame); err != nil {
		return false
	}
	m.zmodemTransfer = &zmodemTransferState{
		requestID: m.nextZMODEMRequestID,
		tabID:     tab.id,
		paneID:    pane.id,
		session:   pane.session,
		protocol:  terminalTransferZMODEM,
		mode:      mode,
		phase:     zmodemPrompt,
		input:     input,
		path:      path,
		cursor:    len([]rune(path)),
	}
	m.statusText = "检测到远程 ZMODEM " + zmodemModeName(mode) + " 请求，请确认本地路径后继续"
	m.statusErr = false
	return true
}

func (m *modelState) openXYMODEMPrompt() {
	pane := m.activeTerminalPane()
	if pane == nil || pane.session == nil || m.zmodemTransfer != nil {
		return
	}
	tab := m.tabForPane(pane)
	if tab == nil {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	path := filepath.Join(home, "Downloads", "xmodem.bin")
	m.nextZMODEMRequestID++
	m.zmodemTransfer = &zmodemTransferState{
		requestID: m.nextZMODEMRequestID,
		tabID:     tab.id,
		paneID:    pane.id,
		session:   pane.session,
		protocol:  terminalTransferXMODEM,
		mode:      zmodemReceive,
		phase:     zmodemChoose,
		input:     newZMODEMInput(),
		path:      path,
		cursor:    len([]rune(path)),
	}
	m.statusText = "请选择 XMODEM/YMODEM 模式；远端程序需先处于传输等待状态"
	m.statusErr = false
}

func (state *zmodemTransferState) chooseXYMODEM(selection int) {
	if state == nil || state.phase != zmodemChoose || selection < 0 || selection > 3 {
		return
	}
	state.selection = selection
	if selection < 2 {
		state.protocol = terminalTransferXMODEM
	} else {
		state.protocol = terminalTransferYMODEM
	}
	if selection%2 == 0 {
		state.mode = zmodemReceive
	} else {
		state.mode = zmodemSend
	}
	state.path = ""
	if state.mode == zmodemReceive {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		if state.protocol == terminalTransferXMODEM {
			state.path = filepath.Join(home, "Downloads", "xmodem.bin")
		} else {
			state.path = filepath.Join(home, "Downloads")
		}
	}
	state.cursor = len([]rune(state.path))
	state.phase = zmodemPrompt
	state.errorText = ""
}

func terminalTransferProtocolName(protocol terminalTransferProtocol) string {
	switch protocol {
	case terminalTransferXMODEM:
		return "XMODEM"
	case terminalTransferYMODEM:
		return "YMODEM"
	default:
		return "ZMODEM"
	}
}

func zmodemModeName(mode zmodemTransferMode) string {
	if mode == zmodemReceive {
		return "接收"
	}
	return "发送"
}

func (m *modelState) startZMODEMTransfer() tea.Cmd {
	state := m.zmodemTransfer
	if state == nil || state.phase != zmodemPrompt {
		return nil
	}
	pane := m.paneForID(state.paneID)
	if pane == nil || pane.session == nil || pane.session != state.session {
		state.errorText = "目标终端已断开，请重新连接后再试"
		return nil
	}
	path := ""
	var sendPaths []string
	if state.mode == zmodemReceive && state.protocol == terminalTransferXMODEM {
		path = expandASCIIFilePath(strings.TrimSpace(state.path))
		if path == "" {
			state.errorText = "请输入保存文件路径"
			return nil
		}
		parent, err := os.Stat(filepath.Dir(path))
		if err != nil || !parent.IsDir() {
			state.errorText = "保存文件的父目录不存在或不可访问"
			return nil
		}
		if !state.overwrite {
			if _, err := os.Lstat(path); err == nil {
				state.errorText = "目标文件已存在，请切换覆盖选项或更换路径"
				return nil
			} else if !errors.Is(err, os.ErrNotExist) {
				state.errorText = "无法检查目标文件：" + err.Error()
				return nil
			}
		}
	} else if state.mode == zmodemReceive {
		path = expandASCIIFilePath(strings.TrimSpace(state.path))
		if path == "" {
			state.errorText = "请输入保存目录"
			return nil
		}
		info, err := os.Stat(path)
		if err != nil {
			state.errorText = "无法访问保存目录：" + err.Error()
			return nil
		} else if !info.IsDir() {
			state.errorText = "保存路径必须是一个已存在的目录"
			return nil
		}
	} else {
		var err error
		sendPaths, err = zmodemSendPaths(state.path)
		if err != nil {
			state.errorText = err.Error()
			return nil
		}
		if state.protocol == terminalTransferXMODEM && len(sendPaths) != 1 {
			state.errorText = "XMODEM 每次只能发送一个文件"
			return nil
		}
		state.path = strings.Join(sendPaths, "\n")
	}
	if state.mode == zmodemReceive {
		state.path = path
	}
	state.phase = zmodemRunning
	state.errorText = ""
	ctx, cancel := context.WithCancel(context.Background())
	state.cancel = cancel
	state.progress = make(chan zmodem.Progress, 4)
	requestID := state.requestID
	input := state.input
	session := state.session
	mode := state.mode
	protocol := state.protocol
	overwrite := state.overwrite
	progress := state.progress
	return tea.Batch(runZMODEMTransferCmd(ctx, input, session, protocol, mode, path, sendPaths, overwrite, requestID, progress), readZMODEMProgressCmd(requestID, progress))
}

func runZMODEMTransferCmd(ctx context.Context, input *zmodemInput, session terminalSession, protocol terminalTransferProtocol, mode zmodemTransferMode, path string, sendPaths []string, overwrite bool, requestID uint64, progress chan zmodem.Progress) tea.Cmd {
	return func() tea.Msg {
		wire := zmodem.NewWire(input, zmodemSessionWriter{session: session})
		var files []zmodem.ReceivedFile
		var err error
		report := func(update zmodem.Progress) {
			select {
			case progress <- update:
			default:
				select {
				case <-progress:
				default:
				}
				select {
				case progress <- update:
				default:
				}
			}
		}
		if protocol == terminalTransferZMODEM {
			if mode == zmodemReceive {
				files, err = zmodem.Receive(ctx, wire, zmodem.ReceiveOptions{Directory: path, Overwrite: overwrite}, report)
			} else {
				err = zmodem.Send(ctx, wire, sendPaths, report)
			}
		} else {
			xyProtocol := xymodem.XMODEM
			if protocol == terminalTransferYMODEM {
				xyProtocol = xymodem.YMODEM
			}
			xyReport := func(update xymodem.Progress) {
				report(zmodem.Progress{Name: update.Name, Bytes: update.Bytes, Total: update.Total, Direction: update.Direction})
			}
			if mode == zmodemReceive {
				options := xymodem.ReceiveOptions{Protocol: xyProtocol, Overwrite: overwrite}
				if xyProtocol == xymodem.XMODEM {
					options.Path = path
				} else {
					options.Directory = path
				}
				stream := terminalTransferStream{reader: input, writer: zmodemSessionWriter{session: session}}
				received, receiveErr := xymodem.Receive(ctx, stream, options, xyReport)
				err = receiveErr
				for _, file := range received {
					files = append(files, zmodem.ReceivedFile{Name: file.Name, Path: file.Path, Size: file.Size})
				}
			} else {
				stream := terminalTransferStream{reader: input, writer: zmodemSessionWriter{session: session}}
				err = xymodem.Send(ctx, stream, xyProtocol, sendPaths, xyReport)
			}
		}
		input.Close(io.EOF)
		trailing := wire.DrainBuffered()
		trailing = append(trailing, input.Drain()...)
		close(progress)
		return zmodemTransferFinishedMsg{requestID: requestID, files: files, trailing: trailing, err: err}
	}
}

func readZMODEMProgressCmd(requestID uint64, progress <-chan zmodem.Progress) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-progress
		return zmodemTransferProgressMsg{requestID: requestID, progress: update, closed: !ok}
	}
}

func (m *modelState) cancelZMODEMTransfer() {
	state := m.zmodemTransfer
	if state == nil {
		return
	}
	if state.phase == zmodemPrompt || state.phase == zmodemChoose {
		if state.protocol == terminalTransferZMODEM && state.session != nil {
			_ = state.session.Write(bytes.Repeat([]byte{0x18}, 8))
		}
		state.input.Close(io.EOF)
		m.zmodemTransfer = nil
		m.statusText = "已取消 " + terminalTransferProtocolName(state.protocol) + " 操作"
		m.statusErr = false
		return
	}
	if state.phase == zmodemRunning {
		state.phase = zmodemCanceling
		if state.cancel != nil {
			state.cancel()
		}
		if state.protocol != terminalTransferZMODEM && state.session != nil {
			_ = state.session.Write(bytes.Repeat([]byte{0x18}, 3))
		}
		state.input.Close(context.Canceled)
	}
}

func (m *modelState) handleZMODEMFinished(msg zmodemTransferFinishedMsg) {
	state := m.zmodemTransfer
	if state == nil || state.requestID != msg.requestID {
		return
	}
	if state.cancel != nil {
		state.cancel()
	}
	m.zmodemTransfer = nil
	if len(msg.trailing) > 0 {
		if pane := m.paneForID(state.paneID); pane != nil && pane.session == state.session {
			m.renderTerminalBytes(pane, msg.trailing, false)
		}
	}
	if errors.Is(msg.err, zmodem.ErrCancelled) || errors.Is(msg.err, context.Canceled) {
		m.statusText = "已取消 " + terminalTransferProtocolName(state.protocol) + " 传输"
		m.statusErr = false
		return
	}
	if msg.err != nil {
		m.statusText = terminalTransferProtocolName(state.protocol) + " 传输失败：" + msg.err.Error()
		m.statusErr = true
		return
	}
	protocolName := terminalTransferProtocolName(state.protocol)
	if state.mode == zmodemReceive {
		m.statusText = fmt.Sprintf("%s 接收完成：%d 个文件", protocolName, len(msg.files))
	} else {
		m.statusText = protocolName + " 文件发送完成"
	}
	m.statusErr = false
}

func (m *modelState) renderTerminalBytes(pane *terminalPane, data []byte, atEOF bool) tea.Cmd {
	if pane == nil || pane.terminal == nil || (len(data) == 0 && !atEOF) {
		return nil
	}
	displayData, decodeErr := decodeTerminalOutput(pane, data, atEOF)
	return m.renderTerminalDecodedBytes(pane, data, displayData, decodeErr)
}

func (m *modelState) renderTerminalDecodedBytes(pane *terminalPane, rawData, displayData []byte, decodeErr error) tea.Cmd {
	if pane == nil || pane.terminal == nil {
		return nil
	}
	var logCloseCmd tea.Cmd
	if pane.outputLogger != nil && len(rawData) > 0 {
		if err := pane.outputLogger.Write(rawData); err != nil {
			pane.statusText = "终端日志写入失败，记录已停止：" + err.Error()
			pane.statusErr = true
			logCloseCmd = m.stopTerminalOutputLog(pane)
		}
	}
	previousScrollback := pane.terminal.ScrollbackLen()
	pane.updateTerminalModes(rawData)
	if decodeErr != nil {
		pane.statusText = "终端输出解码失败：" + decodeErr.Error()
		pane.statusErr = true
	}
	if len(displayData) > 0 {
		_, _ = pane.terminal.Write(displayData)
	}
	m.keepTerminalCursorVisible(pane)
	observeTerminalCommandEcho(pane, displayData)
	if pane.search != nil && len(displayData) > 0 {
		pane.search.dirty = true
	}
	if pane.scrollbackViewing {
		currentScrollback := pane.terminal.ScrollbackLen()
		_, bodyHeight := m.terminalViewportSize(pane)
		pane.scrollbackOffset = clamp(pane.scrollbackOffset+currentScrollback-previousScrollback, 0, terminalScrollbackMax(pane, bodyHeight))
		if pane.terminal.IsAltScreen() || currentScrollback == 0 {
			pane.scrollbackViewing = false
			pane.scrollbackOffset = 0
		}
	}
	return logCloseCmd
}

func (m *modelState) updateZMODEMProgress(msg zmodemTransferProgressMsg) tea.Cmd {
	state := m.zmodemTransfer
	if state == nil || state.requestID != msg.requestID || msg.closed {
		return nil
	}
	state.fileName = msg.progress.Name
	state.bytes = msg.progress.Bytes
	state.total = msg.progress.Total
	return readZMODEMProgressCmd(msg.requestID, state.progress)
}

func (m *modelState) updateZMODEMKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.zmodemTransfer
	if state == nil {
		return m, nil
	}
	key := msg.String()
	if state.phase == zmodemRunning || state.phase == zmodemCanceling {
		if key == "esc" && state.phase == zmodemRunning {
			m.cancelZMODEMTransfer()
		}
		return m, nil
	}
	if key == "esc" {
		m.cancelZMODEMTransfer()
		return m, nil
	}
	if state.phase == zmodemChoose {
		switch key {
		case "1", "2", "3", "4":
			state.chooseXYMODEM(int(key[0] - '1'))
		case "enter":
			state.chooseXYMODEM(state.selection)
		}
		return m, nil
	}
	if key == "ctrl+o" && state.mode == zmodemReceive {
		state.overwrite = !state.overwrite
		state.errorText = ""
		return m, nil
	}
	if key == "enter" {
		return m, m.startZMODEMTransfer()
	}
	if key == "ctrl+j" && state.mode == zmodemSend {
		m.insertZMODEMPath("\n")
		return m, nil
	}
	if key == "left" {
		state.cursor = max(0, state.cursor-1)
		return m, nil
	}
	if key == "right" {
		state.cursor = min(len([]rune(state.path)), state.cursor+1)
		return m, nil
	}
	if key == "home" || key == "ctrl+a" {
		state.cursor = 0
		return m, nil
	}
	if key == "end" || key == "ctrl+e" {
		state.cursor = len([]rune(state.path))
		return m, nil
	}
	if key == "backspace" || key == "ctrl+h" {
		m.editZMODEMPath(0)
		return m, nil
	}
	if key == "delete" || key == "ctrl+d" {
		m.editZMODEMPath(1)
		return m, nil
	}
	if msg.Key().Text != "" {
		m.insertZMODEMPath(msg.Key().Text)
	}
	return m, nil
}

func (m *modelState) appendZMODEMPaste(text string) {
	state := m.zmodemTransfer
	if state == nil || state.phase != zmodemPrompt {
		return
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if state.mode == zmodemSend {
		text = strings.ReplaceAll(text, "\r", "\n")
	} else {
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r", ""), "\n", "")
	}
	m.insertZMODEMPath(text)
}

func zmodemSendPaths(value string) ([]string, error) {
	var paths []string
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		path := expandASCIIFilePath(line)
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("无法读取发送文件 %q：%w", line, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("目前只支持发送普通文件：%q", line)
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil, errors.New("请输入至少一个本地文件路径")
	}
	return paths, nil
}

func (m *modelState) insertZMODEMPath(text string) {
	state := m.zmodemTransfer
	if state == nil || state.phase != zmodemPrompt || text == "" {
		return
	}
	path := []rune(state.path)
	insert := []rune(text)
	if len(path)+len(insert) > 4096 {
		return
	}
	cursor := clamp(state.cursor, 0, len(path))
	path = append(path[:cursor], append(insert, path[cursor:]...)...)
	state.path = string(path)
	state.cursor = cursor + len(insert)
	state.errorText = ""
}

func (m *modelState) editZMODEMPath(direction int) {
	state := m.zmodemTransfer
	if state == nil || state.phase != zmodemPrompt {
		return
	}
	path := []rune(state.path)
	cursor := clamp(state.cursor, 0, len(path))
	if direction == 0 && cursor > 0 {
		path = append(path[:cursor-1], path[cursor:]...)
		state.cursor--
	} else if direction == 1 && cursor < len(path) {
		path = append(path[:cursor], path[cursor+1:]...)
	}
	state.path = string(path)
	state.errorText = ""
}

func (m modelState) zmodemTransferLayout() (content string, x, y, width, height int) {
	state := m.zmodemTransfer
	if state == nil {
		return "", 0, 0, 0, 0
	}
	innerWidth := min(76, max(32, m.width-8))
	title := terminalTransferProtocolName(state.protocol) + " 文件传输"
	if state.phase == zmodemChoose {
		title = "XMODEM / YMODEM 文件传输"
	}
	rows := []string{
		lipgloss.NewStyle().Foreground(palette.accent).Bold(true).Render(title),
	}
	if state.phase == zmodemChoose {
		rows = append(rows,
			mutedStyle.Render("先在远端启动对应命令并等待传输；终端没有通用的 X/YMODEM 自动识别帧。"),
			fitText("1  XMODEM 接收（远端 lsx -X <文件名>，单文件）", innerWidth),
			fitText("2  XMODEM 发送（远端 lrx -X -y <文件名>，单文件）", innerWidth),
			fitText("3  YMODEM 接收（远端 lsb -g <文件...>，批量）", innerWidth),
			fitText("4  YMODEM 发送（远端 lrb -g -y，批量）", innerWidth),
			mutedStyle.Render("按 1–4 选择 · Esc 取消"),
		)
	} else if state.phase == zmodemPrompt {
		label := "本地文件"
		if state.mode == zmodemReceive {
			label = "保存目录"
			if state.protocol == terminalTransferXMODEM {
				label = "保存文件"
			}
		}
		value := []rune(state.path)
		cursor := clamp(state.cursor, 0, len(value))
		pathText := strings.ReplaceAll(string(value[:cursor]), "\n", " ↵ ") + "▏" + strings.ReplaceAll(string(value[cursor:]), "\n", " ↵ ")
		promptHint := "检测到远端终端的 ZMODEM 请求，请检查路径后确认。"
		if state.protocol != terminalTransferZMODEM {
			promptHint = "确认后立即开始；XMODEM 末块有填充，YMODEM 保留文件长度。"
		}
		if state.mode == zmodemSend {
			promptHint = "可粘贴多行路径（每行一个文件）；Ctrl+J 换行，Enter 开始发送。"
			if state.protocol == terminalTransferXMODEM {
				promptHint = "XMODEM 单文件发送，末块会补 0x1a；可粘贴路径后按 Enter。"
			}
		}
		rows = append(rows,
			mutedStyle.Render(promptHint),
			fitText(label+"："+pathText, innerWidth),
		)
		if state.mode == zmodemReceive {
			policy := "已存在文件：跳过"
			if state.protocol != terminalTransferZMODEM {
				policy = "已存在文件：拒绝接收"
			}
			if state.overwrite {
				policy = "已存在文件：覆盖"
			}
			rows = append(rows, fitText(policy+"（Ctrl+O 切换）", innerWidth))
		}
		if state.errorText != "" {
			rows = append(rows, warningStyle.Render(fitText(sanitizeRemoteText(state.errorText), innerWidth)))
		}
		rows = append(rows, mutedStyle.Render("Enter 开始 · Esc 取消"))
	} else {
		percentage := 0
		if state.total > 0 {
			percentage = int(state.bytes * 100 / state.total)
		}
		rows = append(rows,
			fitText("文件："+sanitizeRemoteText(state.fileName), innerWidth),
			fitText(fmt.Sprintf("进度：%s / %s（%d%%）", formatByteCount(state.bytes), formatByteCount(state.total), percentage), innerWidth),
			asciiSendProgressBar(percentage, innerWidth),
		)
		if state.phase == zmodemCanceling {
			rows = append(rows, warningStyle.Render("正在取消传输…"))
		} else {
			rows = append(rows, mutedStyle.Render("Esc 取消传输"))
		}
	}
	for index := range rows {
		rows[index] = fitLine(rows[index], innerWidth)
	}
	content = lipgloss.NewStyle().Width(innerWidth).Border(lipgloss.RoundedBorder()).BorderForeground(palette.accent).Background(palette.canvas).Padding(0, 1).Render(strings.Join(rows, "\n"))
	width = min(lipgloss.Width(content), m.width)
	height = min(lipgloss.Height(content), m.height)
	x = max(0, (m.width-width)/2)
	y = max(0, (m.height-height)/2)
	return content, x, y, width, height
}
