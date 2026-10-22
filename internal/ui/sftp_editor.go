package ui

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"tui-ssh-workbench/internal/infra/sftpclient"
)

func (m *modelState) openSFTPTextEditor(mode sftpEditorMode) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy {
		return nil
	}
	side := sftpRemoteSide
	if state.standalone {
		side = state.activeSide
	}
	remote := sftpSideIsRemote(state, side)
	client := sftpClientForSide(state, side)
	if remote && client == nil {
		state.status, state.statusErr = "远程文件连接不可用", true
		return nil
	}
	entry := m.selectedSFTPEntry(side)
	if entry == nil || entry.IsDir || entry.IsSymlink || !entry.Mode.IsRegular() {
		state.status, state.statusErr = "请选择普通文件进行预览或编辑", true
		return nil
	}
	filePath := m.sftpEntryPath(side, entry.Name)
	hostName := "本地"
	if side == sftpRemoteSide {
		hostName = state.host.Name
	} else if remote {
		hostName = state.peerHost.Name
	}
	state.requestID++
	requestID, generation := state.requestID, state.generation
	state.busy = true
	state.status = "正在读取文件…"
	state.statusErr = false
	return func() tea.Msg {
		var content []byte
		var version sftpclient.FileVersion
		var err error
		if remote {
			content, version, err = client.ReadTextFile(filePath, sftpclient.MaxEditableFileSize)
		} else {
			content, version, err = readLocalTextFile(filePath, sftpclient.MaxEditableFileSize)
		}
		rawContent := append([]byte(nil), content...)
		encodingName := ""
		bom := false
		if err == nil {
			var decoded string
			decoded, encodingName, err = decodeSFTPText(content)
			if err == nil {
				bom = hasSFTPTextBOM(content, encodingName)
				content = []byte(decoded)
			}
		}
		return sftpEditorLoadedMsg{
			requestID: requestID, generation: generation, side: side,
			hostName: hostName, path: filePath,
			mode: mode, content: content, rawContent: rawContent,
			encoding: encodingName, bom: bom, version: version, err: err,
		}
	}
}

func (m *modelState) handleSFTPTextLoaded(msg sftpEditorLoadedMsg) tea.Cmd {
	state := m.sftp
	if state == nil || state.generation != msg.generation || state.requestID != msg.requestID {
		return nil
	}
	openExternal := state.externalEditorOnLoad
	state.externalEditorOnLoad = false
	state.busy = false
	if msg.err != nil {
		state.status, state.statusErr = "无法打开文件："+msg.err.Error(), true
		return nil
	}
	content := string(msg.content)
	lines, lineEndings := splitSFTPText(content)
	encodingName := msg.encoding
	if encodingName == "" {
		encodingName = "UTF-8"
	}
	rawContent := msg.rawContent
	if rawContent == nil {
		rawContent = msg.content
	}
	state.editor = &sftpEditorState{
		mode: msg.mode, side: msg.side, hostName: msg.hostName,
		path: msg.path, name: remoteBase(msg.path),
		encoding: encodingName, originalEncoding: encodingName,
		bom: msg.bom, originalBOM: msg.bom,
		rawContent: append([]byte(nil), rawContent...),
		version:    msg.version, original: content, lines: lines,
		lineEndings: lineEndings, newline: preferredSFTPNewline(lineEndings),
	}
	refreshSFTPTextHighlight(state.editor)
	if msg.mode == sftpEditMode {
		state.status, state.statusErr = "编辑模式：Ctrl+S 保存，Esc 返回", false
	} else {
		state.status, state.statusErr = "预览模式：↑↓/PgUp/PgDn 滚动，Esc 返回", false
	}
	if openExternal && msg.mode == sftpEditMode {
		state.externalEditorOnly = true
		command := m.openSFTPExternalEditor(true)
		if command == nil {
			state.externalEditorOnly = false
			state.editor = nil
		}
		return command
	}
	return nil
}

func remoteBase(filePath string) string {
	index := strings.LastIndex(filePath, "/")
	return filePath[index+1:]
}

func splitSFTPText(content string) ([]string, []string) {
	lines := make([]string, 0, strings.Count(content, "\n")+1)
	endings := make([]string, 0, cap(lines)-1)
	start := 0
	for index := 0; index < len(content); {
		switch content[index] {
		case '\r':
			ending := "\r"
			step := 1
			if index+1 < len(content) && content[index+1] == '\n' {
				ending, step = "\r\n", 2
			}
			lines = append(lines, content[start:index])
			endings = append(endings, ending)
			index += step
			start = index
		case '\n':
			lines = append(lines, content[start:index])
			endings = append(endings, "\n")
			index++
			start = index
		default:
			index++
		}
	}
	lines = append(lines, content[start:])
	return lines, endings
}

func preferredSFTPNewline(endings []string) string {
	counts := make(map[string]int)
	preferred, largest := "\n", 0
	for _, ending := range endings {
		counts[ending]++
		if counts[ending] > largest {
			preferred, largest = ending, counts[ending]
		}
	}
	return preferred
}

func serializeSFTPText(lines, endings []string) string {
	var content strings.Builder
	for index, line := range lines {
		content.WriteString(line)
		if index < len(lines)-1 {
			ending := "\n"
			if index < len(endings) && endings[index] != "" {
				ending = endings[index]
			}
			content.WriteString(ending)
		}
	}
	return content.String()
}

func (m *modelState) updateSFTPTextEditor(key tea.Key) (tea.Model, tea.Cmd) {
	state := m.sftp
	editor := state.editor
	if editor == nil {
		return m, nil
	}
	if editor.leaderPending {
		editor.leaderPending = false
		if key.Code == tea.KeyEscape {
			state.status, state.statusErr = "已取消外部直接编辑", false
			return m, nil
		}
		if workspaceKeyName(key) == m.config.WithDefaults().Workspace.Bindings["external_edit"] {
			return m, m.openSFTPExternalEditor(true)
		}
		state.status, state.statusErr = m.workspaceLeaderChord("external_edit")+" 直接编辑并自动保存；Esc 取消", false
		return m, nil
	}
	if key.Mod&tea.ModCtrl != 0 {
		switch strings.ToLower(string(key.Code)) {
		case "u":
			if err := cycleSFTPTextEncoding(editor); err != nil {
				state.status, state.statusErr = "切换文件编码失败："+err.Error(), true
			} else {
				state.status = "文件编码已切换为 " + editor.encoding
				state.statusErr = false
			}
			return m, nil
		case "f":
			state.prompt = &sftpPrompt{action: sftpPromptEditorFind, title: "查找文本（不区分大小写）"}
			return m, nil
		case "r":
			if editor.mode == sftpEditMode {
				state.prompt = &sftpPrompt{action: sftpPromptEditorReplaceFind, title: "查找并替换：输入要查找的文本"}
			}
			return m, nil
		case "n":
			if editor.searchQuery != "" {
				if findNextSFTPTextMatch(editor, editor.searchQuery, true, m.height) {
					state.status = fmt.Sprintf("查找：第 %d 行", editor.row+1)
					state.statusErr = false
				} else {
					state.status = "没有找到更多匹配：" + editor.searchQuery
					state.statusErr = true
				}
			}
			return m, nil
		}
	}
	if key.Mod&tea.ModCtrl != 0 && (key.Code == 's' || key.Text == "s") && editor.mode == sftpEditMode {
		return m, m.saveSFTPTextEditor()
	}
	if workspaceLeaderMatches(key, m.config.WithDefaults().Workspace) && editor.mode == sftpEditMode {
		editor.leaderPending = true
		state.status, state.statusErr = m.workspaceLeaderKeyLabel()+" 前缀已按下："+displayWorkspaceKey(m.config.WithDefaults().Workspace.Bindings["external_edit"])+" 外部直接编辑并自动保存 · Esc 取消", false
		return m, nil
	}
	if key.Mod&tea.ModCtrl != 0 && (key.Code == 'e' || key.Text == "e") && editor.mode == sftpEditMode {
		return m, m.openSFTPExternalEditor(false)
	}
	if key.Code == tea.KeyEscape || key.Text == "q" {
		if editor.mode == sftpPreviewMode || !editor.dirty {
			state.editor = nil
			state.status = "已返回远程文件列表"
			state.statusErr = false
			return m, nil
		}
		state.prompt = &sftpPrompt{
			action: sftpPromptDiscard,
			title:  "有未保存的修改：按 s 保存并返回，d 放弃，Esc 继续编辑",
		}
		return m, nil
	}
	if editor.mode == sftpPreviewMode {
		visible := max(1, m.height-6)
		switch key.Code {
		case tea.KeyUp:
			editor.topLine = max(0, editor.topLine-1)
		case tea.KeyDown:
			editor.topLine = min(max(0, len(editor.lines)-visible), editor.topLine+1)
		case tea.KeyPgUp:
			editor.topLine = max(0, editor.topLine-visible)
		case tea.KeyPgDown:
			editor.topLine = min(max(0, len(editor.lines)-visible), editor.topLine+visible)
		case tea.KeyHome:
			editor.topLine = 0
		case tea.KeyEnd:
			editor.topLine = max(0, len(editor.lines)-visible)
		}
		return m, nil
	}
	contentMayChange := key.Code == tea.KeyEnter || key.Code == tea.KeyBackspace || key.Code == tea.KeyDelete ||
		(key.Text != "" && key.Mod&tea.ModCtrl == 0 && key.Mod&tea.ModAlt == 0 && key.Code != tea.KeyEnter)
	previousContent := ""
	if contentMayChange {
		previousContent = serializeSFTPText(editor.lines, editor.lineEndings)
	}
	switch key.Code {
	case tea.KeyLeft:
		if editor.column > 0 {
			editor.column--
		} else if editor.row > 0 {
			editor.row--
			editor.column = len([]rune(editor.lines[editor.row]))
		}
	case tea.KeyRight:
		lineLength := len([]rune(editor.lines[editor.row]))
		if editor.column < lineLength {
			editor.column++
		} else if editor.row < len(editor.lines)-1 {
			editor.row++
			editor.column = 0
		}
	case tea.KeyUp:
		if editor.row > 0 {
			editor.row--
			editor.column = min(editor.column, len([]rune(editor.lines[editor.row])))
		}
	case tea.KeyDown:
		if editor.row < len(editor.lines)-1 {
			editor.row++
			editor.column = min(editor.column, len([]rune(editor.lines[editor.row])))
		}
	case tea.KeyHome:
		editor.column = 0
	case tea.KeyEnd:
		editor.column = len([]rune(editor.lines[editor.row]))
	case tea.KeyEnter:
		line := []rune(editor.lines[editor.row])
		before, after := string(line[:editor.column]), string(line[editor.column:])
		editor.lines[editor.row] = before
		editor.lines = append(editor.lines, "")
		copy(editor.lines[editor.row+2:], editor.lines[editor.row+1:])
		editor.lines[editor.row+1] = after
		if editor.row < len(editor.lineEndings) {
			editor.lineEndings = append(editor.lineEndings, "")
			copy(editor.lineEndings[editor.row+1:], editor.lineEndings[editor.row:])
			editor.lineEndings[editor.row] = editor.newline
		} else {
			editor.lineEndings = append(editor.lineEndings, editor.newline)
		}
		editor.row++
		editor.column = 0
	case tea.KeyBackspace:
		if editor.column > 0 {
			line := []rune(editor.lines[editor.row])
			editor.lines[editor.row] = string(append(line[:editor.column-1], line[editor.column:]...))
			editor.column--
		} else if editor.row > 0 {
			previousLength := len([]rune(editor.lines[editor.row-1]))
			editor.lines[editor.row-1] += editor.lines[editor.row]
			editor.lines = append(editor.lines[:editor.row], editor.lines[editor.row+1:]...)
			editor.lineEndings = append(editor.lineEndings[:editor.row-1], editor.lineEndings[editor.row:]...)
			editor.row--
			editor.column = previousLength
		}
	case tea.KeyDelete:
		line := []rune(editor.lines[editor.row])
		if editor.column < len(line) {
			editor.lines[editor.row] = string(append(line[:editor.column], line[editor.column+1:]...))
		} else if editor.row < len(editor.lines)-1 {
			editor.lines[editor.row] += editor.lines[editor.row+1]
			editor.lines = append(editor.lines[:editor.row+1], editor.lines[editor.row+2:]...)
			editor.lineEndings = append(editor.lineEndings[:editor.row], editor.lineEndings[editor.row+1:]...)
		}
	}
	if key.Text != "" && key.Mod&tea.ModCtrl == 0 && key.Mod&tea.ModAlt == 0 && key.Code != tea.KeyEnter {
		insertSFTPText(editor, key.Text)
	}
	if contentMayChange {
		currentContent := serializeSFTPText(editor.lines, editor.lineEndings)
		editor.dirty = currentContent != editor.original
		if currentContent != previousContent {
			refreshSFTPTextHighlight(editor)
		}
	}
	keepSFTPTextCursorVisible(editor, m.height)
	return m, nil
}

func (m *modelState) updateSFTPTextEditorPrompt(key tea.Key) (tea.Model, tea.Cmd) {
	state := m.sftp
	if state == nil || state.editor == nil || state.prompt == nil {
		return m, nil
	}
	prompt, editor := state.prompt, state.editor
	if key.Code == tea.KeyEscape {
		state.prompt = nil
		state.status, state.statusErr = "已取消查找或替换", false
		return m, nil
	}
	if prompt.action == sftpPromptEditorReplaceValue && key.Mod&tea.ModCtrl != 0 && key.Code == tea.KeyEnter {
		state.prompt = nil
		count := replaceAllSFTPText(editor, editor.replaceQuery, prompt.value)
		if count == 0 {
			state.status, state.statusErr = "没有找到要替换的文本", true
		} else {
			keepSFTPTextCursorVisible(editor, m.height)
			state.status = fmt.Sprintf("已全部替换 %d 处", count)
			state.statusErr = false
		}
		return m, nil
	}
	switch key.Code {
	case tea.KeyEnter:
		switch prompt.action {
		case sftpPromptEditorFind:
			query := prompt.value
			state.prompt = nil
			if !findNextSFTPTextMatch(editor, query, false, m.height) {
				state.status, state.statusErr = "没有找到："+query, true
			} else {
				state.status = fmt.Sprintf("找到：第 %d 行，第 %d 列", editor.row+1, editor.column+1)
				state.statusErr = false
			}
		case sftpPromptEditorReplaceFind:
			if prompt.value == "" {
				state.status, state.statusErr = "请输入要查找的文本", true
				return m, nil
			}
			editor.replaceQuery = prompt.value
			prompt.action = sftpPromptEditorReplaceValue
			prompt.title = "替换为（Enter 替换当前匹配，Ctrl+Enter 全部替换）"
			prompt.value = ""
		case sftpPromptEditorReplaceValue:
			state.prompt = nil
			if replaceNextSFTPText(editor, editor.replaceQuery, prompt.value) {
				keepSFTPTextCursorVisible(editor, m.height)
				state.status, state.statusErr = "已替换 1 处", false
			} else {
				state.status, state.statusErr = "没有找到要替换的文本", true
			}
		}
		return m, nil
	case tea.KeyBackspace:
		value := []rune(prompt.value)
		if len(value) > 0 {
			prompt.value = string(value[:len(value)-1])
		}
		return m, nil
	}
	if key.Text != "" && key.Mod&tea.ModCtrl == 0 && key.Mod&tea.ModAlt == 0 && len([]rune(prompt.value))+len([]rune(key.Text)) <= 1024 {
		prompt.value += key.Text
	}
	return m, nil
}

func findNextSFTPTextMatch(editor *sftpEditorState, query string, advance bool, height int) bool {
	if editor == nil || query == "" || len(editor.lines) == 0 {
		return false
	}
	editor.searchQuery = query
	row := clamp(editor.row, 0, len(editor.lines)-1)
	column := clamp(editor.column, 0, len([]rune(editor.lines[row])))
	if advance {
		column = min(column+1, len([]rune(editor.lines[row])))
	}
	if matchRow, matchColumn, ok := findSFTPTextMatch(editor.lines, row, column, query); ok {
		editor.row, editor.column = matchRow, matchColumn
		keepSFTPTextCursorVisible(editor, height)
		return true
	}
	return false
}

func findSFTPTextMatch(lines []string, startRow, startColumn int, query string) (int, int, bool) {
	needle := []rune(query)
	if len(needle) == 0 || len(lines) == 0 {
		return 0, 0, false
	}
	startRow = clamp(startRow, 0, len(lines)-1)
	for row := startRow; row < len(lines); row++ {
		line := []rune(lines[row])
		start := 0
		if row == startRow {
			start = clamp(startColumn, 0, len(line))
		}
		if column, ok := findSFTPTextMatchInLine(line, needle, start, len(line)); ok {
			return row, column, true
		}
	}
	for row := 0; row <= startRow; row++ {
		line := []rune(lines[row])
		end := len(line)
		if row == startRow {
			end = clamp(startColumn, 0, len(line))
		}
		if column, ok := findSFTPTextMatchInLine(line, needle, 0, end); ok {
			return row, column, true
		}
	}
	return 0, 0, false
}

func findSFTPTextMatchInLine(line, needle []rune, start, end int) (int, bool) {
	start, end = clamp(start, 0, len(line)), clamp(end, 0, len(line))
	for column := start; column+len(needle) <= end; column++ {
		if strings.EqualFold(string(line[column:column+len(needle)]), string(needle)) {
			return column, true
		}
	}
	return 0, false
}

func replaceNextSFTPText(editor *sftpEditorState, query, replacement string) bool {
	if editor == nil || query == "" {
		return false
	}
	row, column, ok := findSFTPTextMatch(editor.lines, editor.row, editor.column, query)
	if !ok {
		return false
	}
	line := []rune(editor.lines[row])
	needleLength := len([]rune(query))
	updated := make([]rune, 0, len(line)-needleLength+len([]rune(replacement)))
	updated = append(updated, line[:column]...)
	updated = append(updated, []rune(replacement)...)
	updated = append(updated, line[column+needleLength:]...)
	editor.lines[row] = string(updated)
	editor.row, editor.column = row, column+len([]rune(replacement))
	editor.searchQuery = query
	markSFTPTextEditorDirty(editor)
	return true
}

func replaceAllSFTPText(editor *sftpEditorState, query, replacement string) int {
	if editor == nil || query == "" {
		return 0
	}
	needle, insert := []rune(query), []rune(replacement)
	count, firstRow, firstColumn := 0, -1, 0
	for row, text := range editor.lines {
		line := []rune(text)
		var updated []rune
		lineCount := 0
		for column := 0; column < len(line); {
			if column+len(needle) <= len(line) && strings.EqualFold(string(line[column:column+len(needle)]), string(needle)) {
				if firstRow < 0 {
					firstRow, firstColumn = row, len(updated)
				}
				updated = append(updated, insert...)
				column += len(needle)
				count++
				lineCount++
				continue
			}
			updated = append(updated, line[column])
			column++
		}
		if lineCount > 0 {
			editor.lines[row] = string(updated)
		}
	}
	if count > 0 {
		editor.row = firstRow
		editor.column = min(firstColumn+len(insert), len([]rune(editor.lines[firstRow])))
		editor.searchQuery = query
		markSFTPTextEditorDirty(editor)
	}
	return count
}

func markSFTPTextEditorDirty(editor *sftpEditorState) {
	if editor != nil {
		editor.dirty = serializeSFTPText(editor.lines, editor.lineEndings) != editor.original
		refreshSFTPTextHighlight(editor)
	}
}

func insertSFTPText(editor *sftpEditorState, text string) {
	if editor == nil || editor.mode != sftpEditMode || !utf8.ValidString(text) {
		return
	}
	line := []rune(editor.lines[editor.row])
	insert := []rune(text)
	updated := make([]rune, 0, len(line)+len(insert))
	updated = append(updated, line[:editor.column]...)
	updated = append(updated, insert...)
	updated = append(updated, line[editor.column:]...)
	editor.lines[editor.row] = string(updated)
	editor.column += len(insert)
}

func keepSFTPTextCursorVisible(editor *sftpEditorState, height int) {
	visible := max(1, height-6)
	if editor.row < editor.topLine {
		editor.topLine = editor.row
	} else if editor.row >= editor.topLine+visible {
		editor.topLine = editor.row - visible + 1
	}
}

func (m *modelState) saveSFTPTextEditor() tea.Cmd {
	state := m.sftp
	if state == nil || state.editor == nil || state.editor.mode != sftpEditMode || state.busy {
		return nil
	}
	editor := state.editor
	content := serializeSFTPText(editor.lines, editor.lineEndings)
	encodingName := editor.encoding
	if encodingName == "" {
		encodingName = "UTF-8"
	}
	originalEncoding := editor.originalEncoding
	if originalEncoding == "" {
		originalEncoding = encodingName
	}
	bom := editor.bom
	if content == editor.original && encodingName == originalEncoding && bom == editor.originalBOM {
		if editor.closeAfterSave {
			state.editor = nil
			state.externalEditorOnly = false
		}
		state.status, state.statusErr = "内容没有变化", false
		return nil
	}
	encodedContent, err := encodeSFTPTextWithBOM(content, encodingName, bom)
	if err != nil {
		state.status, state.statusErr = "编码失败："+err.Error(), true
		return nil
	}
	state.requestID++
	requestID, generation := state.requestID, state.generation
	remote := sftpSideIsRemote(state, editor.side)
	client := sftpClientForSide(state, editor.side)
	if remote && client == nil {
		state.status, state.statusErr = "保存失败：远程文件连接不可用", true
		return nil
	}
	filePath, expected := editor.path, editor.version
	state.busy = true
	state.status, state.statusErr = "正在保存文件…", false
	return func() tea.Msg {
		var version sftpclient.FileVersion
		var err error
		if remote {
			version, err = client.SaveTextFile(context.Background(), filePath, encodedContent, expected, nil)
		} else {
			version, err = saveLocalTextFile(context.Background(), filePath, encodedContent, expected)
		}
		return sftpEditorSavedMsg{requestID: requestID, generation: generation, version: version, content: encodedContent, bom: bom, err: err}
	}
}

func (m *modelState) handleSFTPTextSaved(msg sftpEditorSavedMsg) {
	state := m.sftp
	if state == nil || state.generation != msg.generation || state.requestID != msg.requestID || state.editor == nil {
		return
	}
	state.busy = false
	if msg.err != nil {
		if errors.Is(msg.err, sftpclient.ErrRemoteFileChanged) || errors.Is(msg.err, errLocalFileChanged) {
			state.status = "保存冲突：文件已改变，当前修改仍保留"
		} else {
			state.status = "保存失败：" + msg.err.Error()
		}
		state.statusErr = true
		if state.externalEditorOnly {
			// Keep failed external edits recoverable in the in-app editor.
			state.externalEditorOnly = false
		}
		return
	}
	editor := state.editor
	editor.version = msg.version
	if editor.encoding == "" {
		editor.encoding = "UTF-8"
	}
	editor.bom = msg.bom
	editor.originalBOM = msg.bom
	editor.original = serializeSFTPText(editor.lines, editor.lineEndings)
	editor.originalEncoding = editor.encoding
	if msg.content != nil || editor.original == "" {
		editor.rawContent = append(editor.rawContent[:0], msg.content...)
	}
	editor.dirty = false
	state.status, state.statusErr = "文件已保存", false
	if editor.closeAfterSave {
		state.editor = nil
		state.externalEditorOnly = false
	}
}

func renderSFTPTextEditor(editor *sftpEditorState, width, height int, status string, statusErr bool, externalEditChord, leaderLabel, externalEditKey string) string {
	lines := make([]string, height)
	mode := "预览"
	if editor.mode == sftpEditMode {
		mode = "编辑"
		if editor.dirty {
			mode += " *"
		}
	}
	title := mode + "  " + editor.name
	if editor.hostName != "" {
		title = mode + " · " + editor.hostName + "  " + editor.name
	}
	lines[0] = fitLine(brandStyle.Render(sanitizeRemoteText(title)), width)
	lines[1] = fitLine(mutedStyle.Render(sanitizeRemoteText(editor.path)), width)
	lines[2] = separatorStyle.Render(strings.Repeat("─", width))
	encodingName := editor.encoding
	if encodingName == "" {
		encodingName = "UTF-8"
	}
	lines[3] = fitLine(mutedStyle.Render(fmt.Sprintf("%s · %d 行", encodingName, len(editor.lines))), width)
	visible := max(1, height-6)
	for row := 0; row < visible && 4+row < height-2; row++ {
		lineIndex := editor.topLine + row
		if lineIndex >= len(editor.lines) {
			break
		}
		text := strings.ReplaceAll(editor.lines[lineIndex], "\r", "")
		text = strings.ReplaceAll(text, "\t", "    ")
		text = sanitizeRemoteText(text)
		if lineIndex < len(editor.highlightedLines) {
			text = editor.highlightedLines[lineIndex]
		}
		if (editor.mode == sftpEditMode || editor.searchQuery != "") && lineIndex == editor.row {
			lines[4+row] = selectedStyle.Width(width).Render(fitText(text, width))
		} else {
			lines[4+row] = fitLine(fitText(text, width), width)
		}
	}
	if statusErr {
		lines[height-2] = fitLine(statusErrorStyle.Render(sanitizeRemoteText(status)), width)
	} else {
		lines[height-2] = fitLine(secondaryStyle.Render(sanitizeRemoteText(status)), width)
	}
	help := "Ctrl+U 切换编码  ↑↓/PgUp/PgDn 滚动  Esc 返回"
	if editor.searchQuery != "" {
		help = "Ctrl+U 切换编码  Ctrl+F 查找  Ctrl+N 下一个  ↑↓/PgUp/PgDn 滚动  Esc 返回"
	}
	if editor.mode == sftpEditMode {
		help = "方向键编辑  Enter 换行  Ctrl+U 切换编码  Ctrl+F 查找  Ctrl+N 下一个  Ctrl+R 替换  Ctrl+E 外部编辑  " + externalEditChord + " 直接编辑并自动保存  Ctrl+S 保存  Esc 返回"
		if editor.leaderPending {
			help = leaderLabel + " 前缀已按下：" + externalEditKey + " 外部直接编辑并自动保存  Esc 取消"
		}
	}
	lines[height-1] = fitLine(mutedStyle.Render(help), width)
	return strings.Join(lines, "\n")
}

func sftpTextEditorCursor(editor *sftpEditorState, width, height int) *tea.Cursor {
	if editor == nil || editor.mode != sftpEditMode {
		return nil
	}
	if editor.row < editor.topLine || editor.row >= editor.topLine+max(1, height-6) {
		return nil
	}
	line := []rune(editor.lines[editor.row])
	prefix := string(line[:min(editor.column, len(line))])
	x := clamp(lipgloss.Width(prefix), 0, max(0, width-1))
	y := clamp(4+editor.row-editor.topLine, 0, max(0, height-1))
	return tea.NewCursor(x, y)
}

var errLocalFileChanged = errors.New("本地文件已发生变化")

func readLocalTextFile(filePath string, maxSize int64) ([]byte, sftpclient.FileVersion, error) {
	pathInfo, err := os.Lstat(filePath)
	if err != nil {
		return nil, sftpclient.FileVersion{}, fmt.Errorf("读取本地文件信息失败：%w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return nil, sftpclient.FileVersion{}, errors.New("仅支持预览普通文件")
	}
	if maxSize > 0 && pathInfo.Size() > maxSize {
		return nil, sftpclient.FileVersion{}, fmt.Errorf("文件大小超过限制（最大 %s）", formatByteCount(maxSize))
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, sftpclient.FileVersion{}, fmt.Errorf("打开本地文件失败：%w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, sftpclient.FileVersion{}, fmt.Errorf("读取已打开文件信息失败：%w", err)
	}
	if !os.SameFile(pathInfo, openedInfo) || !openedInfo.Mode().IsRegular() {
		return nil, sftpclient.FileVersion{}, errLocalFileChanged
	}
	reader := io.Reader(file)
	if maxSize > 0 {
		reader = io.LimitReader(file, maxSize+1)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, sftpclient.FileVersion{}, fmt.Errorf("读取本地文件失败：%w", err)
	}
	if maxSize > 0 && int64(len(content)) > maxSize {
		return nil, sftpclient.FileVersion{}, fmt.Errorf("文件大小超过限制（最大 %s）", formatByteCount(maxSize))
	}
	return content, sftpclient.FileVersion{
		Size: int64(len(content)), ModTime: openedInfo.ModTime(), Mode: openedInfo.Mode(),
		Digest: sha256.Sum256(content),
	}, nil
}

func saveLocalTextFile(ctx context.Context, filePath string, content []byte, expected sftpclient.FileVersion) (sftpclient.FileVersion, error) {
	if int64(len(content)) > sftpclient.MaxEditableFileSize {
		return sftpclient.FileVersion{}, fmt.Errorf("文件大小超过限制（最大 %s）", formatByteCount(sftpclient.MaxEditableFileSize))
	}
	if _, current, err := readLocalTextFile(filePath, sftpclient.MaxEditableFileSize); err != nil {
		return sftpclient.FileVersion{}, err
	} else if current.Digest != expected.Digest {
		return sftpclient.FileVersion{}, errLocalFileChanged
	}
	if err := ctx.Err(); err != nil {
		return sftpclient.FileVersion{}, err
	}
	directory, base := filepath.Dir(filePath), filepath.Base(filePath)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return sftpclient.FileVersion{}, errors.New("本地文件名无效")
	}
	temporary, err := os.CreateTemp(directory, "."+base+".edit-*")
	if err != nil {
		return sftpclient.FileVersion{}, fmt.Errorf("创建本地临时文件失败：%w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if written, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return sftpclient.FileVersion{}, fmt.Errorf("写入本地临时文件失败：%w", err)
	} else if written != len(content) {
		_ = temporary.Close()
		return sftpclient.FileVersion{}, io.ErrShortWrite
	}
	if err := temporary.Chmod(expected.Mode.Perm()); err != nil {
		_ = temporary.Close()
		return sftpclient.FileVersion{}, fmt.Errorf("设置本地文件权限失败：%w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return sftpclient.FileVersion{}, fmt.Errorf("同步本地文件失败：%w", err)
	}
	if err := temporary.Close(); err != nil {
		return sftpclient.FileVersion{}, fmt.Errorf("关闭本地临时文件失败：%w", err)
	}
	if _, current, err := readLocalTextFile(filePath, sftpclient.MaxEditableFileSize); err != nil {
		return sftpclient.FileVersion{}, err
	} else if current.Digest != expected.Digest {
		return sftpclient.FileVersion{}, errLocalFileChanged
	}
	if err := ctx.Err(); err != nil {
		return sftpclient.FileVersion{}, err
	}
	if err := os.Rename(temporaryPath, filePath); err != nil {
		return sftpclient.FileVersion{}, fmt.Errorf("提交本地文件失败：%w", err)
	}
	committed = true
	_, version, err := readLocalTextFile(filePath, sftpclient.MaxEditableFileSize)
	if err != nil {
		return sftpclient.FileVersion{}, fmt.Errorf("文件已保存，但读取新版本失败：%w", err)
	}
	return version, nil
}
