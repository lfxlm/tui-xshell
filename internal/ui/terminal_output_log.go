package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/infra/terminalog"
	"tui-ssh-workbench/internal/model"
)

type terminalOutputLogClosedMsg struct {
	paneID string
	logger *terminalog.Logger
	path   string
	err    error
}

type terminalOutputLogPollMsg struct {
	paneID string
	logger *terminalog.Logger
}

type terminalLogsLoadedMsg struct {
	generation uint64
	entries    []terminalog.Entry
	err        error
}

type terminalLogDeletedMsg struct {
	generation uint64
	name       string
	err        error
}

type terminalLogPreviewLoadedMsg struct {
	generation uint64
	entry      terminalog.Entry
	encoding   string
	terminal   *vt.SafeEmulator
	err        error
}

type terminalLogViewerState struct {
	generation             uint64
	entries                []terminalog.Entry
	selected               int
	listTop                int
	loading                bool
	filtering              bool
	query                  string
	errText                string
	loadingPreviewPath     string
	loadingPreviewEncoding string
	encoding               string
	preview                *vt.SafeEmulator
	previewEntry           *terminalog.Entry
	previewOffset          int
	deleteTarget           *terminalog.Entry
	deleting               bool
	noticeText             string
}

func (m *modelState) toggleActiveTerminalOutputLog() tea.Cmd {
	pane := m.activeTerminalPane()
	if pane == nil {
		m.statusText = "没有可记录的终端 pane"
		m.statusErr = true
		return nil
	}
	if pane.outputLogClosing {
		m.statusText = "终端日志正在关闭，请稍候"
		m.statusErr = false
		return nil
	}
	if pane.outputLogger != nil {
		return m.stopTerminalOutputLog(pane)
	}
	if pane.session == nil {
		pane.statusText = "终端未连接，无法开始记录"
		pane.statusErr = true
		return nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		pane.statusText = "获取用户配置目录失败：" + err.Error()
		pane.statusErr = true
		return nil
	}
	logger, err := terminalog.NewWithOptions(
		filepath.Join(configDir, "tui-ssh-workbench", "logs"), pane.host.Name,
		terminalLogOptions(m.config),
	)
	if err != nil {
		pane.statusText = "创建终端日志失败：" + err.Error()
		pane.statusErr = true
		return nil
	}
	if m.outputLoggers == nil {
		m.outputLoggers = make(map[*terminalog.Logger]struct{})
	}
	m.outputLoggers[logger] = struct{}{}
	pane.outputLogger = logger
	if len(m.config.Logging.RedactPatterns) > 0 {
		pane.statusText = fmt.Sprintf("正在记录终端输出；落盘前应用 %d 条脱敏规则", len(m.config.Logging.RedactPatterns))
	} else {
		pane.statusText = "正在记录终端输出；未配置脱敏规则，日志可能包含敏感信息"
	}
	pane.statusErr = false
	return terminalOutputLogPollCmd(pane.id, logger)
}

func terminalLogOptions(config model.Config) terminalog.Options {
	options := terminalog.DefaultOptions()
	if config.Logging.MaxTotalBytes >= 0 {
		options.MaxTotalBytes = config.Logging.MaxTotalBytes
	}
	if config.Logging.MaxAgeDays >= 0 {
		options.MaxAge = time.Duration(config.Logging.MaxAgeDays) * 24 * time.Hour
	}
	if config.Logging.SegmentBytes > 0 {
		options.MaxBytes = config.Logging.SegmentBytes
	}
	if config.Logging.RetainedSegments > 0 {
		options.RetainedFiles = config.Logging.RetainedSegments
	}
	options.RedactPatterns = append([]string(nil), config.Logging.RedactPatterns...)
	return options
}

func (m *modelState) openTerminalLogViewer() tea.Cmd {
	directory := m.terminalLogDirectory()
	if directory == "" {
		m.statusText = "获取用户配置目录失败"
		m.statusErr = true
		return nil
	}
	m.terminalLogsGeneration++
	m.terminalLogViewer = &terminalLogViewerState{
		generation: m.terminalLogsGeneration,
		loading:    true,
		encoding:   model.TerminalEncodingUTF8,
	}
	return terminalLogsListCmd(directory, m.terminalLogViewer.generation)
}

func (m modelState) terminalLogDirectory() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(configDir, "tui-ssh-workbench", "logs")
}

func terminalLogsListCmd(directory string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		entries, err := terminalog.List(directory)
		return terminalLogsLoadedMsg{generation: generation, entries: entries, err: err}
	}
}

func terminalLogDeleteCmd(directory, name string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		return terminalLogDeletedMsg{generation: generation, name: name, err: terminalog.Remove(directory, name)}
	}
}

func (m *modelState) handleTerminalLogsLoaded(msg terminalLogsLoadedMsg) {
	viewer := m.terminalLogViewer
	if viewer == nil || viewer.generation != msg.generation {
		return
	}
	viewer.loading = false
	viewer.errText = ""
	if msg.err != nil {
		viewer.errText = "读取日志列表失败：" + safeLogDisplayText(msg.err.Error())
		return
	}
	viewer.entries = msg.entries
	viewer.selected = clamp(viewer.selected, 0, max(0, len(viewer.visibleEntries())-1))
	viewer.listTop = keepVisible(viewer.selected, viewer.listTop, max(1, m.height-5))
}

func (m *modelState) handleTerminalLogDeleted(msg terminalLogDeletedMsg) {
	viewer := m.terminalLogViewer
	if viewer == nil || viewer.generation != msg.generation {
		return
	}
	viewer.deleting = false
	viewer.deleteTarget = nil
	if msg.err != nil {
		viewer.errText = "删除日志失败：" + safeLogDisplayText(msg.err.Error())
		return
	}
	for index := range viewer.entries {
		if viewer.entries[index].Name == msg.name {
			viewer.entries = append(viewer.entries[:index], viewer.entries[index+1:]...)
			break
		}
	}
	viewer.selected = clamp(viewer.selected, 0, max(0, len(viewer.visibleEntries())-1))
	viewer.listTop = keepVisible(viewer.selected, viewer.listTop, max(1, m.height-5))
	viewer.errText = ""
	viewer.noticeText = "已删除日志：" + safeLogDisplayText(msg.name)
}

func (m *modelState) handleTerminalLogPreviewLoaded(msg terminalLogPreviewLoadedMsg) {
	viewer := m.terminalLogViewer
	if viewer == nil || viewer.generation != msg.generation || viewer.loadingPreviewPath != msg.entry.Path || viewer.loadingPreviewEncoding != msg.encoding {
		if msg.terminal != nil {
			_ = msg.terminal.Close()
		}
		return
	}
	viewer.loadingPreviewPath = ""
	viewer.loadingPreviewEncoding = ""
	if msg.err != nil {
		viewer.errText = "读取日志失败：" + safeLogDisplayText(msg.err.Error())
		return
	}
	viewer.errText = ""
	msg.terminal.Resize(max(20, m.width), max(5, m.height-3))
	viewer.preview = msg.terminal
	viewer.encoding = normalizedTerminalEncoding(msg.encoding)
	entry := msg.entry
	viewer.previewEntry = &entry
	viewer.previewOffset = 0
}

func terminalLogPreviewCmd(entry terminalog.Entry, width, height int, generation uint64, requestedEncoding ...string) tea.Cmd {
	encoding := model.TerminalEncodingUTF8
	if len(requestedEncoding) > 0 {
		encoding = normalizedTerminalEncoding(requestedEncoding[0])
	}
	return func() tea.Msg {
		terminal, err := loadTerminalLogWithEncoding(entry.Path, width, height, encoding)
		return terminalLogPreviewLoadedMsg{generation: generation, entry: entry, encoding: encoding, terminal: terminal, err: err}
	}
}

func loadTerminalLog(path string, width, height int) (*vt.SafeEmulator, error) {
	return loadTerminalLogWithEncoding(path, width, height, model.TerminalEncodingUTF8)
}

func loadTerminalLogWithEncoding(path string, width, height int, encoding string) (*vt.SafeEmulator, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect log file: %w", err)
	}
	if !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("log file must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	defer file.Close()
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened log file: %w", err)
	}
	if !os.SameFile(pathInfo, fileInfo) {
		return nil, errors.New("log file changed while opening")
	}
	if fileInfo.Size() > terminalog.MaxLogBytes {
		return nil, fmt.Errorf("log file exceeds the %d MiB preview limit", terminalog.MaxLogBytes>>20)
	}
	terminal := vt.NewSafeEmulator(max(20, width), max(5, height))
	drainTerminalResponses(terminal)
	terminal.SetScrollbackSize(5000)
	decoder := &terminalPane{terminalEncoding: normalizedTerminalEncoding(encoding)}
	buffer := make([]byte, 32*1024)
	for {
		read, readErr := file.Read(buffer)
		if read > 0 {
			decoded, err := decodeTerminalOutput(decoder, buffer[:read], false)
			if err != nil {
				_ = terminal.Close()
				return nil, fmt.Errorf("decode terminal log as %s: %w", decoder.terminalEncoding, err)
			}
			if _, err := terminal.Write(decoded); err != nil {
				_ = terminal.Close()
				return nil, fmt.Errorf("parse terminal log: %w", err)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				tail, err := decodeTerminalOutput(decoder, nil, true)
				if err != nil {
					_ = terminal.Close()
					return nil, fmt.Errorf("finish decoding terminal log as %s: %w", decoder.terminalEncoding, err)
				}
				if _, err := terminal.Write(tail); err != nil {
					_ = terminal.Close()
					return nil, fmt.Errorf("parse terminal log: %w", err)
				}
				return terminal, nil
			}
			_ = terminal.Close()
			return nil, fmt.Errorf("read log file: %w", readErr)
		}
	}
}

func (viewer *terminalLogViewerState) visibleEntries() []terminalog.Entry {
	if viewer == nil {
		return nil
	}
	if strings.TrimSpace(viewer.query) == "" {
		return viewer.entries
	}
	query := strings.ToLower(strings.TrimSpace(viewer.query))
	entries := make([]terminalog.Entry, 0, len(viewer.entries))
	for _, entry := range viewer.entries {
		if strings.Contains(strings.ToLower(entry.Name), query) {
			entries = append(entries, entry)
		}
	}
	return entries
}

func (m *modelState) updateTerminalLogViewerKey(msg tea.KeyPressMsg) tea.Cmd {
	viewer := m.terminalLogViewer
	if viewer == nil {
		return nil
	}
	key := msg.Key()
	if viewer.deleteTarget != nil {
		if viewer.deleting {
			return nil
		}
		if key.Code == tea.KeyEscape || strings.EqualFold(key.Text, "n") {
			viewer.deleteTarget = nil
			return nil
		}
		if key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "y") {
			target := *viewer.deleteTarget
			if m.terminalLogRecordingActive(target.Path) {
				viewer.deleteTarget = nil
				viewer.errText = "此日志仍属于正在记录的终端，停止记录后才能删除"
				return nil
			}
			viewer.deleting = true
			viewer.errText = ""
			return terminalLogDeleteCmd(m.terminalLogDirectory(), target.Name, viewer.generation)
		}
		return nil
	}
	if viewer.preview != nil {
		if key.Mod&tea.ModCtrl != 0 && (key.Code == 'u' || key.Code == 'U') && viewer.previewEntry != nil {
			entry := *viewer.previewEntry
			viewer.encoding = nextTerminalEncoding(viewer.encoding)
			closeTerminalLogPreview(viewer)
			viewer.loadingPreviewPath = entry.Path
			viewer.loadingPreviewEncoding = viewer.encoding
			viewer.errText = ""
			return terminalLogPreviewCmd(entry, max(20, m.width), max(5, m.height-3), viewer.generation, viewer.encoding)
		}
		switch key.Code {
		case tea.KeyEscape:
			_ = viewer.preview.Close()
			viewer.preview = nil
			viewer.previewEntry = nil
			viewer.previewOffset = 0
		case tea.KeyUp:
			viewer.previewOffset = min(viewer.previewOffset+1, viewer.preview.ScrollbackLen())
		case tea.KeyDown:
			viewer.previewOffset = max(viewer.previewOffset-1, 0)
		case tea.KeyPgUp:
			viewer.previewOffset = min(viewer.previewOffset+max(1, m.height-5), viewer.preview.ScrollbackLen())
		case tea.KeyPgDown:
			viewer.previewOffset = max(viewer.previewOffset-max(1, m.height-5), 0)
		case tea.KeyHome:
			viewer.previewOffset = viewer.preview.ScrollbackLen()
		case tea.KeyEnd:
			viewer.previewOffset = 0
		}
		return nil
	}
	if viewer.filtering {
		if key.Mod&tea.ModCtrl != 0 && (key.Code == 'c' || key.Code == 'C') {
			m.terminalLogViewer = nil
			return nil
		}
		switch key.Code {
		case tea.KeyEscape:
			viewer.filtering = false
			viewer.query = ""
		case tea.KeyEnter:
			viewer.filtering = false
		case tea.KeyBackspace, tea.KeyDelete:
			runes := []rune(viewer.query)
			if len(runes) > 0 {
				viewer.query = string(runes[:len(runes)-1])
			}
		default:
			if key.Text != "" && len([]rune(viewer.query))+len([]rune(key.Text)) <= 128 {
				viewer.query += key.Text
			}
		}
		viewer.selected = 0
		viewer.listTop = 0
		return nil
	}
	entries := viewer.visibleEntries()
	if key.Mod&tea.ModCtrl != 0 && (key.Code == 'c' || key.Code == 'C') {
		closeTerminalLogPreview(viewer)
		m.terminalLogViewer = nil
		return nil
	}
	switch key.Code {
	case tea.KeyEscape:
		closeTerminalLogPreview(viewer)
		m.terminalLogViewer = nil
	case tea.KeyUp:
		viewer.selected = clamp(viewer.selected-1, 0, max(0, len(entries)-1))
	case tea.KeyDown:
		viewer.selected = clamp(viewer.selected+1, 0, max(0, len(entries)-1))
	case tea.KeyEnter:
		if !viewer.loading && viewer.loadingPreviewPath == "" && len(entries) > 0 {
			entry := entries[clamp(viewer.selected, 0, len(entries)-1)]
			viewer.errText = ""
			viewer.loadingPreviewPath = entry.Path
			if viewer.encoding == "" {
				viewer.encoding = model.TerminalEncodingUTF8
			}
			viewer.loadingPreviewEncoding = viewer.encoding
			return terminalLogPreviewCmd(entry, max(20, m.width), max(5, m.height-3), viewer.generation, viewer.encoding)
		}
	case 'd':
		if !viewer.loading && viewer.loadingPreviewPath == "" && len(entries) > 0 {
			entry := entries[clamp(viewer.selected, 0, len(entries)-1)]
			viewer.deleteTarget = &entry
			viewer.noticeText = ""
			viewer.errText = ""
		}
	}
	if key.Text == "/" {
		viewer.filtering = true
		viewer.query = ""
	} else if strings.EqualFold(key.Text, "r") {
		viewer.loading = true
		return terminalLogsListCmd(m.terminalLogDirectory(), viewer.generation)
	}
	viewer.selected = clamp(viewer.selected, 0, max(0, len(viewer.visibleEntries())-1))
	viewer.listTop = keepVisible(viewer.selected, viewer.listTop, max(1, m.height-5))
	return nil
}

func closeTerminalLogPreview(viewer *terminalLogViewerState) {
	if viewer != nil && viewer.preview != nil {
		_ = viewer.preview.Close()
		viewer.preview = nil
		viewer.previewEntry = nil
	}
}

func (m *modelState) handleTerminalLogViewerPaste(text string) {
	viewer := m.terminalLogViewer
	if viewer == nil || !viewer.filtering {
		return
	}
	if len([]rune(viewer.query))+len([]rune(text)) <= 128 {
		viewer.query += text
		viewer.selected = 0
		viewer.listTop = 0
	}
}

func (m modelState) terminalLogViewerView() string {
	viewer := m.terminalLogViewer
	if viewer == nil {
		return ""
	}
	if viewer.deleteTarget != nil {
		lines := []string{
			"确定删除日志文件「" + safeLogDisplayText(viewer.deleteTarget.Name) + "」吗？",
			"日志文件可能包含终端输出中的敏感信息。",
			"",
		}
		if viewer.deleting {
			lines = append(lines, accentStyle.Render("◌  正在删除…"))
		} else {
			lines = append(lines, helpKeyStyle.Render("Enter / y ")+"确认   "+helpKeyStyle.Render("Esc / n ")+"取消")
		}
		return m.modalView("删除终端日志", lines, 72)
	}
	if viewer.preview != nil && viewer.previewEntry != nil {
		entry := viewer.previewEntry
		title := fmt.Sprintf("终端日志 · %s · %s · %s", safeLogDisplayText(entry.Name), formatLogSize(entry.Size), entry.Modified.Local().Format("2006-01-02 15:04:05"))
		bodyHeight := max(1, m.height-3)
		previewPane := &terminalPane{terminal: viewer.preview, scrollbackViewing: true, scrollbackOffset: viewer.previewOffset}
		rows := renderTerminalScrollback(previewPane, bodyHeight)
		for index := range rows {
			rows[index] = fitLine(rows[index], m.width)
		}
		footer := fmt.Sprintf("编码：%s · Ctrl+U 切换编码 · ↑/↓ 滚动 · PgUp/PgDn 翻页 · Home/End 定位 · Esc 返回日志列表", viewer.encoding)
		return strings.Join([]string{fitLine(titleStyle.Render(title), m.width), strings.Join(rows, "\n"), fitLine(mutedStyle.Render(footer), m.width)}, "\n")
	}
	entries := viewer.visibleEntries()
	listHeight := max(1, m.height-5)
	listTop := keepVisible(viewer.selected, viewer.listTop, listHeight)
	lines := []string{fitLine(titleStyle.Render("终端输出日志 · 原始内容可能含敏感信息"), m.width)}
	filterLabel := "按 / 搜索主机名或文件名"
	if viewer.filtering {
		filterLabel = "筛选：" + viewer.query + "▌"
	} else if viewer.query != "" {
		filterLabel = "筛选：" + viewer.query + " · 按 / 修改 · Esc 关闭筛选"
	}
	lines = append(lines, fitLine(mutedStyle.Render(filterLabel), m.width))
	if viewer.loading {
		lines = append(lines, fitLine(mutedStyle.Render("正在读取日志目录…"), m.width))
	} else if viewer.errText != "" {
		lines = append(lines, fitLine(statusErrorStyle.Render(viewer.errText), m.width))
	} else if len(entries) == 0 {
		lines = append(lines, fitLine(mutedStyle.Render("没有找到日志；在 SSH pane 按 "+m.workspaceLeaderChord("toggle_recording")+" 开始记录"), m.width))
	} else {
		for row := 0; row < listHeight; row++ {
			index := listTop + row
			if index >= len(entries) {
				lines = append(lines, "")
				continue
			}
			entry := entries[index]
			label := fmt.Sprintf("%-*s  %8s  %s", max(12, m.width-34), safeLogDisplayText(entry.Name), formatLogSize(entry.Size), entry.Modified.Local().Format("2006-01-02 15:04"))
			if index == viewer.selected {
				lines = append(lines, fitLine(selectedStyle.Render("› "+label), m.width))
			} else {
				lines = append(lines, fitLine("  "+label, m.width))
			}
		}
	}
	if viewer.loadingPreviewPath != "" {
		lines = append(lines, fitLine(mutedStyle.Render("正在解析日志中的终端控制序列…"), m.width))
	} else {
		footer := "↑/↓ 选择 · Enter 预览 · d 删除 · / 筛选 · R 刷新 · Esc 关闭"
		if viewer.noticeText != "" {
			footer = viewer.noticeText + " · " + footer
		}
		lines = append(lines, fitLine(mutedStyle.Render(footer), m.width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m modelState) terminalLogRecordingActive(path string) bool {
	for logger := range m.outputLoggers {
		if sameTerminalLogRecording(logger.Path(), path) {
			return true
		}
	}
	return false
}

func sameTerminalLogRecording(left, right string) bool {
	return terminalLogRecordingBase(left) == terminalLogRecordingBase(right)
}

func terminalLogRecordingBase(path string) string {
	directory, name := filepath.Split(filepath.Clean(path))
	if filepath.Ext(name) != ".log" {
		return filepath.Join(directory, name)
	}
	stem := strings.TrimSuffix(name, ".log")
	separator := strings.LastIndexByte(stem, '.')
	if separator >= 0 && len(stem)-separator == 4 {
		rotated := true
		for _, digit := range stem[separator+1:] {
			if digit < '0' || digit > '9' {
				rotated = false
				break
			}
		}
		if rotated {
			stem = stem[:separator]
		}
	}
	return filepath.Join(directory, stem+".log")
}

func formatLogSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	if size < 1<<20 {
		return fmt.Sprintf("%.1f KiB", float64(size)/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))
}

func safeLogDisplayText(value string) string {
	value = strings.ToValidUTF8(value, "�")
	var result strings.Builder
	for _, char := range value {
		if unicode.IsControl(char) {
			result.WriteRune('�')
			continue
		}
		result.WriteRune(char)
	}
	return result.String()
}

func terminalOutputLogPollCmd(paneID string, logger *terminalog.Logger) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return terminalOutputLogPollMsg{paneID: paneID, logger: logger}
	})
}

func (m *modelState) handleTerminalOutputLogPoll(msg terminalOutputLogPollMsg) tea.Cmd {
	pane := m.paneByID(msg.paneID)
	if pane == nil || pane.outputLogger != msg.logger {
		return nil
	}
	if err := msg.logger.Err(); err != nil {
		pane.statusText = "终端日志停止记录：" + err.Error()
		pane.statusErr = true
		return m.stopTerminalOutputLog(pane)
	}
	if err := msg.logger.MaintenanceErr(); err != nil {
		pane.statusText = "终端日志保留清理失败：" + safeLogDisplayText(err.Error())
		pane.statusErr = true
	}
	return terminalOutputLogPollCmd(msg.paneID, msg.logger)
}

func (m *modelState) stopTerminalOutputLog(pane *terminalPane) tea.Cmd {
	if pane == nil || pane.outputLogger == nil {
		return nil
	}
	logger := pane.outputLogger
	pane.outputLogger = nil
	pane.outputLogClosing = true
	paneID := pane.id
	path := logger.Path()
	return func() tea.Msg {
		return terminalOutputLogClosedMsg{paneID: paneID, logger: logger, path: path, err: logger.Close()}
	}
}

func (m *modelState) handleTerminalOutputLogClosed(msg terminalOutputLogClosedMsg) {
	delete(m.outputLoggers, msg.logger)
	pane := m.paneByID(msg.paneID)
	if pane == nil {
		return
	}
	pane.outputLogClosing = false
	if msg.err != nil {
		pane.statusText = "终端日志关闭失败：" + msg.err.Error()
		pane.statusErr = true
		return
	}
	if pane.session != nil && !pane.statusErr {
		pane.statusText = "终端日志已保存：" + msg.path
		pane.statusErr = false
	}
}

func (m *modelState) paneByID(paneID string) *terminalPane {
	for _, tab := range m.tabs {
		if pane := findPane(tab.root, paneID); pane != nil {
			return pane
		}
	}
	return nil
}

func (m *modelState) stopAllTerminalOutputLogs() error {
	var firstErr error
	for logger := range m.outputLoggers {
		if err := logger.Close(); firstErr == nil && err != nil {
			firstErr = err
		}
		delete(m.outputLoggers, logger)
	}
	return firstErr
}
