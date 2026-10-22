package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	maxASCIIFileSendBytes = 8 << 20
	maxPromptOutputBytes  = 64 << 10
)

var (
	asciiFileSourceEncodings   = []string{"自动检测", "UTF-8", "UTF-8 BOM", "GB18030", "GBK", "Big5", "Shift_JIS", "Windows-1252"}
	asciiFileTerminalEncodings = []string{"UTF-8", "GB18030", "GBK", "Big5", "Shift_JIS", "Windows-1252"}
)

type asciiFileSendPhase uint8

const (
	asciiFileSendEdit asciiFileSendPhase = iota
	asciiFileSendLoading
	asciiFileSendReview
	asciiFileSending
	asciiFileSendCanceling
)

type asciiFileSendOptions struct {
	characterDelay   time.Duration
	lineDelay        time.Duration
	prompt           string
	maxWait          time.Duration
	terminalEncoding string
}

type asciiFileSendState struct {
	requestID uint64
	tabID     string
	paneID    string
	session   terminalSession
	phase     asciiFileSendPhase

	path               string
	sourceEncoding     string
	terminalEncoding   string
	characterDelayText string
	lineDelayText      string
	prompt             string
	maxWaitText        string
	field              int
	cursor             int
	content            []byte
	preview            []byte
	output             []byte
	sent               int64
	total              int64
	errorText          string
	cancel             context.CancelFunc
	progress           chan asciiSendProgress
	promptFeed         *terminalPromptFeed
}

type asciiFileSendLoadedMsg struct {
	requestID uint64
	path      string
	content   []byte
	preview   []byte
	output    []byte
	err       error
}

type asciiSendProgress struct{ sent int64 }

type terminalPromptFeed struct {
	mu   sync.Mutex
	data []byte
	wake chan struct{}
}

func newTerminalPromptFeed() *terminalPromptFeed {
	return &terminalPromptFeed{wake: make(chan struct{}, 1)}
}

func (feed *terminalPromptFeed) Append(data []byte) {
	if feed == nil || len(data) == 0 {
		return
	}
	feed.mu.Lock()
	feed.data = append(feed.data, data...)
	if len(feed.data) > maxPromptOutputBytes {
		feed.data = append([]byte(nil), feed.data[len(feed.data)-maxPromptOutputBytes:]...)
	}
	feed.mu.Unlock()
	select {
	case feed.wake <- struct{}{}:
	default:
	}
}

func (feed *terminalPromptFeed) Reset() {
	if feed == nil {
		return
	}
	feed.mu.Lock()
	feed.data = nil
	feed.mu.Unlock()
	select {
	case <-feed.wake:
	default:
	}
}

func (feed *terminalPromptFeed) Wait(ctx context.Context, prompt string, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	seen := ""
	for {
		feed.mu.Lock()
		data := append([]byte(nil), feed.data...)
		feed.data = nil
		feed.mu.Unlock()
		if len(data) > 0 {
			seen += ansi.Strip(string(data))
			if len(seen) > maxPromptOutputBytes {
				seen = seen[len(seen)-maxPromptOutputBytes:]
			}
			if strings.Contains(seen, prompt) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("等待提示符 %q 超时", prompt)
		case <-feed.wake:
		}
	}
}

func (m *modelState) openASCIIFileSend() {
	tab := m.activeTab()
	pane := m.activeTerminalPane()
	if tab == nil || pane == nil || pane.session == nil {
		m.statusText = "当前 pane 没有已连接的终端，无法发送文本文件"
		m.statusErr = true
		return
	}
	if m.asciiFileSend != nil {
		return
	}
	m.nextASCIIFileSendID++
	m.asciiFileSend = &asciiFileSendState{
		requestID: m.nextASCIIFileSendID, tabID: tab.id, paneID: pane.id, session: pane.session,
		phase: asciiFileSendEdit, sourceEncoding: "自动检测", terminalEncoding: "UTF-8",
		characterDelayText: "0", lineDelayText: "0", maxWaitText: "30",
	}
}

func (m *modelState) loadASCIIFileSend() tea.Cmd {
	state := m.asciiFileSend
	if state == nil {
		return nil
	}
	pane := m.paneForID(state.paneID)
	if state.session == nil || pane == nil || pane.session == nil || pane.session != state.session {
		state.errorText = "目标终端已断开，请重新连接后再试"
		return nil
	}
	if strings.TrimSpace(state.path) == "" {
		state.errorText = "请输入要发送的本地文件路径"
		return nil
	}
	if _, err := parseASCIIFileSendOptions(state); err != nil {
		state.errorText = err.Error()
		return nil
	}
	path := expandASCIIFilePath(state.path)
	sourceEncoding, terminalEncoding := state.sourceEncoding, state.terminalEncoding
	state.phase = asciiFileSendLoading
	state.errorText = ""
	requestID := state.requestID
	return func() tea.Msg {
		content, err := readASCIIFileSend(path)
		var preview, output []byte
		if err == nil {
			preview, output, err = prepareASCIIFileSendContent(content, sourceEncoding, terminalEncoding)
		}
		return asciiFileSendLoadedMsg{requestID: requestID, path: path, content: content, preview: preview, output: output, err: err}
	}
}

func parseASCIIFileSendOptions(state *asciiFileSendState) (asciiFileSendOptions, error) {
	characterDelay, err := parseASCIIFileSendNumber("字符间隔", state.characterDelayText, 0, 1000)
	if err != nil {
		return asciiFileSendOptions{}, err
	}
	lineDelay, err := parseASCIIFileSendNumber("行间隔", state.lineDelayText, 0, 60000)
	if err != nil {
		return asciiFileSendOptions{}, err
	}
	maxWait, err := parseASCIIFileSendNumber("最大等待时间", state.maxWaitText, 0, 300)
	if err != nil {
		return asciiFileSendOptions{}, err
	}
	if strings.TrimSpace(state.prompt) != "" && maxWait == 0 {
		return asciiFileSendOptions{}, errors.New("填写提示符后，最大等待时间必须在 1 到 300 秒之间")
	}
	sourceEncoding := state.sourceEncoding
	if sourceEncoding == "" {
		sourceEncoding = "自动检测"
	}
	if !containsString(asciiFileSourceEncodings, sourceEncoding) {
		return asciiFileSendOptions{}, errors.New("不支持的源文件编码")
	}
	terminalEncoding := state.terminalEncoding
	if terminalEncoding == "" {
		terminalEncoding = "UTF-8"
	}
	if !containsString(asciiFileTerminalEncodings, terminalEncoding) {
		return asciiFileSendOptions{}, errors.New("不支持的终端编码")
	}
	prompt := state.prompt
	if prompt != "" {
		encodedPrompt, err := encodeSFTPText(prompt, terminalEncoding)
		if err != nil {
			return asciiFileSendOptions{}, fmt.Errorf("提示符无法转换为 %s：%w", terminalEncoding, err)
		}
		prompt = string(encodedPrompt)
	}
	return asciiFileSendOptions{
		characterDelay: time.Duration(characterDelay) * time.Millisecond,
		lineDelay:      time.Duration(lineDelay) * time.Millisecond,
		prompt:         prompt, maxWait: time.Duration(maxWait) * time.Second,
		terminalEncoding: terminalEncoding,
	}, nil
}

func prepareASCIIFileSendContent(content []byte, sourceEncoding, terminalEncoding string) (preview, output []byte, err error) {
	var decoded string
	if sourceEncoding == "" || sourceEncoding == "自动检测" {
		decoded, _, err = decodeSFTPText(content)
	} else {
		decoded, err = decodeSFTPTextWithEncoding(content, sourceEncoding)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("读取源文件编码失败：%w", err)
	}
	if terminalEncoding == "" {
		terminalEncoding = "UTF-8"
	}
	preview = normalizeASCIIFileLineEndings([]byte(decoded))
	output, err = encodeSFTPText(string(preview), terminalEncoding)
	if err != nil {
		return nil, nil, fmt.Errorf("无法转换为终端编码 %s：%w", terminalEncoding, err)
	}
	return preview, output, nil
}

func parseASCIIFileSendNumber(name, text string, minValue, maxValue int) (int, error) {
	value := 0
	if strings.TrimSpace(text) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil {
			return 0, fmt.Errorf("%s必须是数字", name)
		}
		value = parsed
	}
	if value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s必须在 %d 到 %d 之间", name, minValue, maxValue)
	}
	return value, nil
}

func expandASCIIFilePath(path string) string {
	path = os.ExpandEnv(strings.TrimSpace(path))
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
			}
		}
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

func readASCIIFileSend(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开文件失败：%w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("读取文件信息失败：%w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("只能发送普通文件")
	}
	if info.Size() > maxASCIIFileSendBytes {
		return nil, fmt.Errorf("文件超过 %s 限制；请改用 SFTP/SCP", formatByteCount(maxASCIIFileSendBytes))
	}
	content, err := io.ReadAll(io.LimitReader(file, maxASCIIFileSendBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取文件失败：%w", err)
	}
	if len(content) > maxASCIIFileSendBytes {
		return nil, fmt.Errorf("文件超过 %s 限制；请改用 SFTP/SCP", formatByteCount(maxASCIIFileSendBytes))
	}
	if len(content) == 0 {
		return nil, errors.New("文件为空，没有内容可发送")
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return nil, errors.New("文件包含 NUL 字节，疑似二进制文件；请改用 SFTP/SCP")
	}
	return content, nil
}

func (m *modelState) handleASCIIFileSendLoaded(msg asciiFileSendLoadedMsg) {
	state := m.asciiFileSend
	if state == nil || state.requestID != msg.requestID || state.phase != asciiFileSendLoading {
		return
	}
	if msg.err != nil {
		state.phase = asciiFileSendEdit
		state.errorText = msg.err.Error()
		return
	}
	state.path = msg.path
	state.content = msg.content
	state.preview = msg.preview
	state.output = msg.output
	state.total = int64(len(state.output))
	state.phase = asciiFileSendReview
	state.errorText = ""
}

func normalizeASCIIFileLineEndings(content []byte) []byte {
	output := make([]byte, 0, len(content))
	for index := 0; index < len(content); index++ {
		switch content[index] {
		case '\r':
			output = append(output, '\r')
			if index+1 < len(content) && content[index+1] == '\n' {
				index++
			}
		case '\n':
			output = append(output, '\r')
		default:
			output = append(output, content[index])
		}
	}
	return output
}

func (m *modelState) startASCIIFileSend() tea.Cmd {
	state := m.asciiFileSend
	if state == nil || state.phase != asciiFileSendReview {
		return nil
	}
	pane := m.paneForID(state.paneID)
	if pane == nil || pane.session == nil || pane.session != state.session {
		state.phase = asciiFileSendEdit
		state.errorText = "目标终端已断开或已变化，请重新选择文件并确认"
		return nil
	}
	options, err := parseASCIIFileSendOptions(state)
	if err != nil {
		state.phase = asciiFileSendEdit
		state.errorText = err.Error()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	state.cancel = cancel
	state.promptFeed = newTerminalPromptFeed()
	state.progress = make(chan asciiSendProgress, 1)
	state.phase = asciiFileSending
	state.sent = 0
	state.errorText = ""
	requestID := state.requestID
	data := append([]byte(nil), state.output...)
	session, feed, progress := state.session, state.promptFeed, state.progress
	sendCmd := func() tea.Msg {
		sent, sendErr := sendASCIIFileContent(ctx, session, data, options, feed, progress)
		close(progress)
		return asciiFileSendFinishedMsg{requestID: requestID, sent: sent, err: sendErr}
	}
	return tea.Batch(sendCmd, readASCIIFileSendProgressCmd(requestID, progress))
}

func sendASCIIFileContent(ctx context.Context, session terminalSession, data []byte, options asciiFileSendOptions, feed *terminalPromptFeed, progress chan asciiSendProgress) (int64, error) {
	lines := splitASCIIFileSendLines(data)
	var sent, lastProgress int64
	for lineIndex, line := range lines {
		if err := ctx.Err(); err != nil {
			return sent, err
		}
		if options.prompt != "" && lineIndex < len(lines)-1 {
			feed.Reset()
		}
		if options.characterDelay == 0 {
			for offset := 0; offset < len(line); {
				if err := ctx.Err(); err != nil {
					return sent, err
				}
				end := min(len(line), offset+(32<<10))
				if err := session.Write(line[offset:end]); err != nil {
					return sent, fmt.Errorf("写入终端失败：%w", err)
				}
				sent += int64(end - offset)
				offset = end
				publishASCIIFileSendProgress(progress, sent)
				lastProgress = sent
			}
		} else {
			units := splitASCIIFileSendUnitsWithEncoding(line, options.terminalEncoding)
			for unitIndex, unit := range units {
				if err := ctx.Err(); err != nil {
					return sent, err
				}
				if err := session.Write(unit); err != nil {
					return sent, fmt.Errorf("写入终端失败：%w", err)
				}
				sent += int64(len(unit))
				if sent-lastProgress >= 64 || unitIndex == len(units)-1 {
					publishASCIIFileSendProgress(progress, sent)
					lastProgress = sent
				}
				if unitIndex < len(units)-1 {
					if err := waitASCIIFileSend(ctx, options.characterDelay); err != nil {
						return sent, err
					}
				}
			}
		}
		if lineIndex < len(lines)-1 {
			if options.prompt != "" {
				if err := feed.Wait(ctx, options.prompt, options.maxWait); err != nil {
					return sent, err
				}
			}
			if options.lineDelay > 0 {
				if err := waitASCIIFileSend(ctx, options.lineDelay); err != nil {
					return sent, err
				}
			}
		}
	}
	publishASCIIFileSendProgress(progress, sent)
	return sent, nil
}

func splitASCIIFileSendLines(content []byte) [][]byte {
	lines := make([][]byte, 0, bytes.Count(content, []byte{'\n'})+bytes.Count(content, []byte{'\r'})+1)
	start := 0
	for index := 0; index < len(content); {
		if content[index] != '\r' && content[index] != '\n' {
			index++
			continue
		}
		line := append([]byte(nil), content[start:index]...)
		line = append(line, '\r')
		lines = append(lines, line)
		if content[index] == '\r' && index+1 < len(content) && content[index+1] == '\n' {
			index++
		}
		index++
		start = index
	}
	if start < len(content) {
		lines = append(lines, append([]byte(nil), content[start:]...))
	}
	return lines
}

func splitASCIIFileSendUnits(line []byte) [][]byte {
	return splitASCIIFileSendUnitsWithEncoding(line, "UTF-8")
}

func splitASCIIFileSendUnitsWithEncoding(line []byte, encodingName string) [][]byte {
	units := make([][]byte, 0, utf8.RuneCount(line))
	for len(line) > 0 {
		size := 1
		switch encodingName {
		case "UTF-8", "":
			_, size = utf8.DecodeRune(line)
		case "GBK":
			if line[0] >= 0x81 && line[0] <= 0xFE && len(line) >= 2 {
				size = 2
			}
		case "GB18030":
			if line[0] >= 0x81 && line[0] <= 0xFE && len(line) >= 2 {
				if line[1] >= 0x30 && line[1] <= 0x39 && len(line) >= 4 {
					size = 4
				} else {
					size = 2
				}
			}
		case "Big5":
			if line[0] >= 0x81 && line[0] <= 0xFE && len(line) >= 2 {
				size = 2
			}
		case "Shift_JIS":
			if (line[0] >= 0x81 && line[0] <= 0x9F || line[0] >= 0xE0 && line[0] <= 0xFC) && len(line) >= 2 {
				size = 2
			}
		}
		if size < 1 || size > len(line) {
			size = 1
		}
		units = append(units, line[:size])
		line = line[size:]
	}
	return units
}

func waitASCIIFileSend(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func publishASCIIFileSendProgress(progress chan asciiSendProgress, sent int64) {
	select {
	case progress <- asciiSendProgress{sent: sent}:
	default:
		select {
		case <-progress:
		default:
		}
		select {
		case progress <- asciiSendProgress{sent: sent}:
		default:
		}
	}
}

func readASCIIFileSendProgressCmd(requestID uint64, progress <-chan asciiSendProgress) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-progress
		return asciiFileSendProgressMsg{requestID: requestID, sent: update.sent, closed: !ok}
	}
}

func (m *modelState) handleASCIIFileSendFinished(msg asciiFileSendFinishedMsg) {
	state := m.asciiFileSend
	if state == nil || state.requestID != msg.requestID {
		return
	}
	if state.cancel != nil {
		state.cancel()
	}
	pane := m.paneForID(state.paneID)
	m.asciiFileSend = nil
	if pane == nil {
		return
	}
	if errors.Is(msg.err, context.Canceled) {
		pane.statusText = fmt.Sprintf("文本文件发送已取消（%s/%s）", formatByteCount(msg.sent), formatByteCount(state.total))
		pane.statusErr = false
	} else if msg.err != nil {
		pane.statusText = fmt.Sprintf("文本文件发送中断（%s/%s）：%v", formatByteCount(msg.sent), formatByteCount(state.total), msg.err)
		pane.statusErr = true
	} else {
		pane.statusText = "文本文件发送完成：" + sanitizeRemoteText(filepath.Base(state.path))
		pane.statusErr = false
	}
}

func (m *modelState) paneForID(id string) *terminalPane {
	for _, tab := range m.tabs {
		if pane := findPane(tab.root, id); pane != nil {
			return pane
		}
	}
	return nil
}

func (m *modelState) updateASCIIFileSendKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.asciiFileSend
	if state == nil {
		return m, nil
	}
	key := msg.Key()
	if state.phase == asciiFileSending || state.phase == asciiFileSendCanceling {
		if key.Code == tea.KeyEscape || (key.Mod&tea.ModCtrl != 0 && key.Code == 'c') {
			if state.phase == asciiFileSending && state.cancel != nil {
				state.phase = asciiFileSendCanceling
				state.cancel()
			}
		}
		return m, nil
	}
	if state.phase == asciiFileSendLoading {
		if key.Code == tea.KeyEscape {
			m.asciiFileSend = nil
		}
		return m, nil
	}
	if state.phase == asciiFileSendReview {
		switch key.Code {
		case tea.KeyEscape:
			state.phase = asciiFileSendEdit
			state.errorText = ""
			state.field = 0
			state.cursor = len([]rune(state.path))
		case tea.KeyEnter:
			return m, m.startASCIIFileSend()
		}
		return m, nil
	}
	if key.Code == tea.KeyEscape {
		m.asciiFileSend = nil
		return m, nil
	}
	if (key.Code == tea.KeyTab && key.Mod&tea.ModShift == 0) || key.Code == tea.KeyDown || (key.Code == tea.KeyRight && key.Mod&tea.ModCtrl != 0) {
		state.field = (state.field + 1) % 7
		state.cursor = len([]rune(state.asciiFileSendFieldValue(state.field)))
		state.errorText = ""
		return m, nil
	}
	if (key.Code == tea.KeyTab && key.Mod&tea.ModShift != 0) || key.Code == tea.KeyUp || (key.Code == tea.KeyLeft && key.Mod&tea.ModCtrl != 0) {
		state.field = (state.field + 6) % 7
		state.cursor = len([]rune(state.asciiFileSendFieldValue(state.field)))
		state.errorText = ""
		return m, nil
	}
	if key.Code == tea.KeyEnter {
		if state.field < 6 {
			state.field++
			state.cursor = len([]rune(state.asciiFileSendFieldValue(state.field)))
			state.errorText = ""
			return m, nil
		}
		return m, m.loadASCIIFileSend()
	}
	if state.field == 1 || state.field == 2 {
		delta := 0
		if key.Code == tea.KeyLeft {
			delta = -1
		} else if key.Code == tea.KeyRight {
			delta = 1
		}
		if delta != 0 {
			state.cycleEncoding(state.field, delta)
			state.errorText = ""
		}
		return m, nil
	}

	value := []rune(state.asciiFileSendFieldValue(state.field))
	switch key.Code {
	case tea.KeyLeft:
		state.cursor = max(0, state.cursor-1)
	case tea.KeyRight:
		state.cursor = min(len(value), state.cursor+1)
	case tea.KeyHome:
		state.cursor = 0
	case tea.KeyEnd:
		state.cursor = len(value)
	case tea.KeyBackspace:
		if state.cursor > 0 {
			value = append(value[:state.cursor-1], value[state.cursor:]...)
			state.cursor--
			state.setFieldValue(state.field, string(value))
		}
	case tea.KeyDelete:
		if state.cursor < len(value) {
			value = append(value[:state.cursor], value[state.cursor+1:]...)
			state.setFieldValue(state.field, string(value))
		}
	default:
		if key.Mod == 0 && key.Text != "" {
			m.insertASCIIFileSendText(key.Text)
		}
	}
	state.errorText = ""
	return m, nil
}

func (m *modelState) appendASCIIFileSendPaste(text string) {
	if m.asciiFileSend == nil || m.asciiFileSend.phase != asciiFileSendEdit {
		return
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r", ""), "\n", "")
	m.insertASCIIFileSendText(text)
}

func (m *modelState) insertASCIIFileSendText(text string) {
	state := m.asciiFileSend
	if state == nil || state.phase != asciiFileSendEdit || text == "" {
		return
	}
	if state.field == 1 || state.field == 2 {
		return
	}
	value := []rune(state.asciiFileSendFieldValue(state.field))
	limit := 4096
	if state.field == 5 {
		limit = 256
	} else if state.field > 0 {
		limit = 6
	}
	insert := []rune(text)
	if len(value)+len(insert) > limit {
		insert = insert[:max(0, limit-len(value))]
	}
	value = append(value[:state.cursor], append(insert, value[state.cursor:]...)...)
	state.cursor += len(insert)
	state.setFieldValue(state.field, string(value))
}

func (state *asciiFileSendState) asciiFileSendFieldValue(field int) string {
	switch field {
	case 0:
		return state.path
	case 1:
		return state.sourceEncoding
	case 2:
		return state.terminalEncoding
	case 3:
		return state.characterDelayText
	case 4:
		return state.lineDelayText
	case 5:
		return state.prompt
	case 6:
		return state.maxWaitText
	default:
		return ""
	}
}

func (state *asciiFileSendState) setFieldValue(field int, value string) {
	switch field {
	case 0:
		state.path = value
	case 1:
		state.sourceEncoding = value
	case 2:
		state.terminalEncoding = value
	case 3:
		state.characterDelayText = value
	case 4:
		state.lineDelayText = value
	case 5:
		state.prompt = value
	case 6:
		state.maxWaitText = value
	}
}

func (state *asciiFileSendState) cycleEncoding(field, delta int) {
	choices := asciiFileSourceEncodings
	if field == 2 {
		choices = asciiFileTerminalEncodings
	} else if field != 1 {
		return
	}
	current := 0
	for index, choice := range choices {
		if choice == state.asciiFileSendFieldValue(field) {
			current = index
			break
		}
	}
	index := (current + delta + len(choices)) % len(choices)
	state.setFieldValue(field, choices[index])
	state.cursor = len([]rune(choices[index]))
}

func (m modelState) asciiFileSendFieldValue(field int) string {
	if m.asciiFileSend == nil {
		return ""
	}
	return m.asciiFileSend.asciiFileSendFieldValue(field)
}

func (m *modelState) asciiFileSendLayout() (content string, x, y, width, height int) {
	state := m.asciiFileSend
	if state == nil || m.width < 1 || m.height < 1 {
		return "", 0, 0, 0, 0
	}
	panelWidth := max(16, min(m.width-4, 76))
	innerWidth := max(8, panelWidth-4)
	rows := []string{
		brandStyle.Render("◆  发送文本文件到当前 pane"),
		mutedStyle.Render("按终端输入发送，换行统一转换为回车；最多 8 MiB"),
		separatorStyle.Render(strings.Repeat("─", innerWidth)),
	}
	switch state.phase {
	case asciiFileSendEdit, asciiFileSendLoading:
		rows = append(rows,
			m.asciiFileSendInputRow(0, "本地路径", state.path, innerWidth),
			m.asciiFileSendInputRow(1, "源文件编码", state.sourceEncoding, innerWidth),
			m.asciiFileSendInputRow(2, "终端编码", state.terminalEncoding, innerWidth),
			m.asciiFileSendInputRow(3, "字符间隔 ms", state.characterDelayText, innerWidth),
			m.asciiFileSendInputRow(4, "行间隔 ms", state.lineDelayText, innerWidth),
			m.asciiFileSendInputRow(5, "等待提示符（可留空）", state.prompt, innerWidth),
			m.asciiFileSendInputRow(6, "提示符最大等待秒", state.maxWaitText, innerWidth),
			separatorStyle.Render(strings.Repeat("─", innerWidth)),
		)
		if state.errorText != "" {
			rows = append(rows, warningStyle.Render(fitText(sanitizeRemoteText(state.errorText), innerWidth)))
		} else if state.phase == asciiFileSendLoading {
			rows = append(rows, mutedStyle.Render("正在读取并检查文件…"))
		} else {
			rows = append(rows, mutedStyle.Render("Tab 切换 · 编码项 ←/→ 选择 · Enter 下一项/预览 · Esc 取消"))
		}
	case asciiFileSendReview:
		rows = append(rows,
			fitText("文件："+sanitizeRemoteText(filepath.Base(state.path)), innerWidth),
			fitText(fmt.Sprintf("大小：%s · 发送：%s · 目标：%s", formatByteCount(int64(len(state.content))), formatByteCount(state.total), sanitizeRemoteText(m.asciiFileSendHostName())), innerWidth),
			fitText(fmt.Sprintf("编码：%s → %s · 字符间隔 %s ms · 行间隔 %s ms", state.sourceEncoding, state.terminalEncoding, state.characterDelayText, state.lineDelayText), innerWidth),
		)
		if strings.TrimSpace(state.prompt) != "" {
			rows = append(rows, fitText(fmt.Sprintf("每行等待提示符 %q，最多 %s 秒", state.prompt, state.maxWaitText), innerWidth))
		}
		rows = append(rows, separatorStyle.Render(strings.Repeat("─", innerWidth)), mutedStyle.Render("预览（控制字符已隐藏）："))
		rows = append(rows, asciiFileSendPreview(state.preview, innerWidth)...)
		rows = append(rows,
			separatorStyle.Render(strings.Repeat("─", innerWidth)),
			warningStyle.Render("确认后会向远端终端发送内容，包含的命令可能立即执行。"),
			mutedStyle.Render("Enter 开始发送 · Esc 返回修改"),
		)
	default:
		percentage := 0
		if state.total > 0 {
			percentage = int(state.sent * 100 / state.total)
		}
		rows = append(rows,
			fitText("文件："+sanitizeRemoteText(filepath.Base(state.path)), innerWidth),
			fitText(fmt.Sprintf("进度：%s / %s（%d%%）", formatByteCount(state.sent), formatByteCount(state.total), percentage), innerWidth),
			asciiSendProgressBar(percentage, innerWidth),
		)
		if state.phase == asciiFileSendCanceling {
			rows = append(rows, warningStyle.Render("正在取消发送…"))
		} else {
			rows = append(rows, mutedStyle.Render("Esc 取消发送；等待提示符时也可取消"))
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

func (m modelState) asciiFileSendInputRow(field int, label, value string, width int) string {
	state := m.asciiFileSend
	marker := "  "
	style := lipgloss.NewStyle().Foreground(palette.secondary)
	if state != nil && state.field == field && state.phase == asciiFileSendEdit {
		marker = "› "
		style = lipgloss.NewStyle().Foreground(palette.accent).Bold(true)
		if field == 1 || field == 2 {
			value = "‹ " + value + " ›"
		} else {
			valueRunes := []rune(value)
			cursor := clamp(state.cursor, 0, len(valueRunes))
			value = string(valueRunes[:cursor]) + "▏" + string(valueRunes[cursor:])
		}
	}
	value = sanitizeRemoteText(value)
	return fitText(style.Render(marker+label+"  "+value), width)
}

func (m modelState) asciiFileSendHostName() string {
	if m.asciiFileSend == nil {
		return ""
	}
	if pane := m.paneForID(m.asciiFileSend.paneID); pane != nil {
		return pane.host.Name
	}
	return "已断开"
}

func asciiFileSendPreview(content []byte, width int) []string {
	text := strings.ToValidUTF8(string(content), "�")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	preview := make([]string, 0, min(4, len(lines)))
	for _, line := range lines {
		var safe strings.Builder
		for _, character := range line {
			if character < 0x20 || character == 0x7f {
				if character == '\t' {
					safe.WriteString("  ")
				} else {
					safe.WriteRune('�')
				}
			} else {
				safe.WriteRune(character)
			}
		}
		preview = append(preview, fitText(safe.String(), width))
		if len(preview) >= 4 {
			break
		}
	}
	if len(lines) > len(preview) {
		preview = append(preview, mutedStyle.Render("…"))
	}
	return preview
}

func asciiSendProgressBar(percentage, width int) string {
	barWidth := max(1, width-4)
	filled := clamp(percentage*barWidth/100, 0, barWidth)
	return selectedAccent.Render(strings.Repeat("█", filled)) + mutedStyle.Render(strings.Repeat("░", barWidth-filled))
}
