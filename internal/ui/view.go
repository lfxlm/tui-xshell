package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"tui-ssh-workbench/internal/model"
)

func (m modelState) view() string {
	if m.screenLock != nil {
		return m.screenLockView()
	}
	if m.appConfirmation != nil {
		return m.appConfirmationView()
	}
	if m.batchEdit != nil {
		return m.batchEditView()
	}
	if m.tabGroupManager != nil {
		return m.workspaceTabGroupManagerView()
	}
	if m.workspaceHelp {
		return m.workspaceHelpView()
	}
	if m.appearancePicker != nil {
		return m.appearancePickerView()
	}
	if m.commandHistoryView != nil {
		return m.terminalCommandHistoryView()
	}
	if m.terminalLogViewer != nil {
		return m.terminalLogViewerView()
	}
	if m.keyboardAuthPrompt != nil {
		return m.keyboardAuthView()
	}
	if m.credentialPrompt != nil {
		return m.credentialPromptView()
	}
	if m.quickConnect != nil {
		return m.quickConnectView()
	}
	if m.trustPrompt != nil {
		return m.hostKeyView()
	}
	if m.hostKeyChangePrompt != nil {
		return m.changedHostKeyView()
	}
	if m.hostPicker != nil {
		return m.hostPickerView()
	}
	if m.shortcutBarPicker != nil {
		return m.shortcutBarPickerView()
	}
	if m.workspaceLayouts != nil {
		return m.workspaceLayoutManagerView()
	}
	if m.pluginPalette != nil {
		return m.pluginPaletteView()
	}
	if m.portForwardManager != nil {
		return m.portForwardManagerView()
	}
	if m.sftpFilters != nil {
		return m.sftpFilterManagerView()
	}
	if m.sftpBookmarks != nil {
		return m.sftpBookmarkManagerView()
	}
	if m.scheduledTransfers != nil {
		return m.scheduledTransferManagerView()
	}
	if m.composePane != nil {
		return m.composePaneView()
	}
	if m.quickCommands != nil {
		return m.quickCommandView()
	}
	if m.sshKeyGeneration != nil {
		return m.sshKeyGenerationView()
	}
	if m.sshKeyManager != nil {
		return m.sshKeyManagerView()
	}
	if m.sshHostKeyManager != nil {
		return m.sshHostKeyManagerView()
	}
	if m.authProfileManager != nil {
		return m.authProfileManagerView()
	}
	if m.passwordPrompt {
		return m.passwordView()
	}
	if m.ftpPasswordPrompt {
		return m.ftpPasswordView()
	}
	if m.trustingHost {
		if m.replacingHostKey {
			return m.connectionProgressView("正在更新 SSH 主机指纹…", "已确认替换；正在安全更新本机信任记录并重连。")
		}
		return m.connectionProgressView("正在保存主机指纹…", "确认后会把指纹写入本机 known_hosts。")
	}
	if m.connectingHost != nil {
		if m.connectingHost.Kind == model.HostKindLocal {
			return m.connectionProgressView("正在启动 "+m.connectingHost.Name+"…", "正在启动本地登录 Shell 和伪终端。")
		}
		if m.connectingHost.Kind == model.HostKindFTP {
			return m.connectionProgressView("正在连接 "+m.connectingHost.Name+"…", "正在建立 FTP/FTPS 文件连接。")
		}
		if m.connectingHost.Kind == model.HostKindTelnet {
			return m.connectionProgressView("正在连接 "+m.connectingHost.Name+"…", "正在建立未加密的 "+terminalProtocolName(*m.connectingHost)+" 终端连接。")
		}
		if m.connectingHost.Kind == model.HostKindSerial {
			return m.connectionProgressView("正在打开 "+m.connectingHost.Name+"…", "正在打开串口设备 "+m.connectingHost.IP+"。")
		}
		return m.connectionProgressView("正在连接 "+m.connectingHost.Name+"…", "正在验证主机并建立交互式 SSH 终端。")
	}
	if m.workspaceVisible && len(m.tabs) > 0 {
		return m.renderWorkspace()
	}
	if m.sftp != nil && len(m.tabs) == 0 {
		return m.sftpView()
	}
	if m.deleteConfirm != nil {
		return m.catalogDeleteConfirmView()
	}
	if m.serialPortPicker != nil {
		return m.serialPortPickerView()
	}
	if m.form != nil {
		return m.formView()
	}
	if m.width < 54 {
		return m.smallTerminalView()
	}
	if m.height < 14 {
		return "\n  终端高度不足，请至少调整到 14 行。"
	}
	if m.showHelp {
		return m.helpView()
	}

	innerWidth := m.width - 4
	bodyHeight := m.height - 8
	header := m.headerView(innerWidth)
	footer := m.footerView(innerWidth)
	body := m.bodyView(innerWidth, bodyHeight)
	return lipgloss.JoinVertical(lipgloss.Left,
		m.workspaceTabsView(m.width),
		indentLines(header, 2),
		indentLines(separatorStyle.Render(strings.Repeat("─", innerWidth)), 2),
		indentLines(body, 2),
		indentLines(separatorStyle.Render(strings.Repeat("─", innerWidth)), 2),
		indentLines(footer, 2),
	)
}

func (m modelState) ftpPasswordView() string {
	host := "FTP/FTPS 主机"
	if m.ftpPendingHost != nil {
		host = m.ftpPendingHost.Name
	}
	mask := strings.Repeat("•", min(16, len([]rune(m.ftpPasswordValue))))
	lines := []string{
		brandStyle.Render("◆  FTP / FTPS 身份验证"),
		mutedStyle.Render("连接：" + host),
		"请输入密码。密码仅保存在当前进程内存中，用于本次文件面板会话。",
		"",
		"密码  " + selectedStyle.Render(mask+"▏"),
	}
	if m.ftpPasswordError != "" {
		lines = append(lines, "", warningStyle.Render("! "+m.ftpPasswordError))
	}
	lines = append(lines, "", mutedStyle.Render("Enter 连接   ·   Esc 取消"))
	return m.modalView("FTP / FTPS 连接", lines, 76)
}

func indentLines(content string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(content, "\n")
	for index := range lines {
		lines[index] = prefix + lines[index]
	}
	return strings.Join(lines, "\n")
}

func (m modelState) headerView(width int) string {
	left := brandStyle.Render("◆") + "  " + titleStyle.Render("SSH 终端工作台") +
		"  " + mutedStyle.Render("本地工作区")
	rightText := fmt.Sprintf("%02d 主机   ·   %02d 分组/栏", len(m.hosts), len(m.groups)+len(m.shortcutBars))
	if len(m.tabs) > 0 {
		rightText += fmt.Sprintf("   ·   %02d 会话", len(m.tabs))
	}
	right := secondaryStyle.Render(rightText)
	line := alignSides(left, right, width)
	subtitle := mutedStyle.Render("管理主机与远程连接")
	return lipgloss.JoinVertical(lipgloss.Left,
		fitLine(line, width),
		fitLine(subtitle, width),
	)
}

func (m modelState) bodyView(width, height int) string {
	if width < 66 {
		if m.activePane == groupPane {
			return m.renderGroupPane(width, height)
		}
		return m.renderHostPane(width, height)
	}

	groupWidth := 22
	if width >= 90 {
		groupWidth = 28
	}
	if width >= 116 {
		detailWidth := 29
		mainWidth := width - groupWidth - detailWidth - 2
		groups := m.renderGroupPane(groupWidth, height)
		main := m.renderHostPane(mainWidth, height)
		details := m.renderDetailPane(detailWidth, height)
		separatorRow := m.hostSeparatorRow()
		return joinFixedColumns(height,
			fixedColumn{content: groups, width: groupWidth},
			fixedColumn{content: m.divider(height, separatorRow, "├"), width: 1},
			fixedColumn{content: main, width: mainWidth},
			fixedColumn{content: m.divider(height, separatorRow, "┤"), width: 1},
			fixedColumn{content: details, width: detailWidth},
		)
	}

	mainWidth := width - groupWidth - 1
	return joinFixedColumns(height,
		fixedColumn{content: m.renderGroupPane(groupWidth, height), width: groupWidth},
		fixedColumn{content: m.divider(height, m.hostSeparatorRow(), "├"), width: 1},
		fixedColumn{content: m.renderHostPane(mainWidth, height), width: mainWidth},
	)
}

type fixedColumn struct {
	content string
	width   int
}

// joinFixedColumns keeps pane boundaries in the same terminal column on every
// row, including rows containing ANSI styling or wide CJK characters.
func joinFixedColumns(height int, columns ...fixedColumn) string {
	rowsByColumn := make([][]string, len(columns))
	for index, column := range columns {
		rowsByColumn[index] = strings.Split(column.content, "\n")
	}

	var output strings.Builder
	for row := range height {
		for index, column := range columns {
			line := ""
			if row < len(rowsByColumn[index]) {
				line = rowsByColumn[index][row]
			}
			output.WriteString(fitLine(line, column.width))
		}
		if row < height-1 {
			output.WriteByte('\n')
		}
	}
	return output.String()
}

func (m modelState) renderGroupPane(width, height int) string {
	contentWidth := max(1, width-2) // reserve a two-cell gutter before the pane divider
	entries := m.groupEntries()
	lines := make([]string, 0, height)
	groupHeading := sectionStyle.Render("分组与连接栏")
	groupSubtitle := mutedStyle.Render("管理分组和快捷集合")
	if m.activePane == groupPane {
		groupHeading += focusBadgeStyle.Render(" ◆")
		groupSubtitle = focusBadgeStyle.Render("◆ 当前焦点：分组")
	}
	lines = append(lines,
		alignSides(groupHeading, mutedStyle.Render(fmt.Sprintf("%02d", len(m.groups)+len(m.shortcutBars))), contentWidth),
		groupSubtitle,
		"",
	)
	rows := max(1, height-len(lines))
	top := clamp(m.groupTop, 0, max(0, len(entries)-rows))
	bottom := min(len(entries), top+rows)
	for index := top; index < bottom; index++ {
		entry := entries[index]
		prefix := "   "
		if entry.isAll {
			prefix = "◇  "
		} else if entry.isFavoriteView {
			prefix = "★  "
		} else if entry.isRecentView {
			prefix = "◷  "
		} else if entry.isShortcutBar {
			prefix = "▣  "
		} else {
			var treePrefix strings.Builder
			for _, ancestorHasNext := range entry.ancestorHasNext {
				if ancestorHasNext {
					treePrefix.WriteString("│ ")
				} else {
					treePrefix.WriteString("  ")
				}
			}
			branch := "├─ "
			if entry.lastSibling {
				branch = "└─ "
			}
			prefix = treePrefix.String() + branch
		}
		// Reserve the same two-cell marker gutter for every row. Without it,
		// selecting a top-level group shifts its tree branch relative to siblings.
		labelWidth := max(1, contentWidth-7)
		label := fitText(prefix+entry.name, labelWidth)
		padding := strings.Repeat(" ", max(0, labelWidth-lipgloss.Width(label)))
		rest := label + padding + fmt.Sprintf(" %3d", entry.hostCount)
		line := "  " + rest
		selected := index == m.groupIndex
		style := mutedStyle
		if selected {
			if m.activePane == groupPane {
				style = selectedStyle
				if contentWidth <= 2 {
					line = selectedAccent.Render(fitText("› ", contentWidth))
				} else {
					line = selectedAccent.Render("› ") + selectedStyle.Width(contentWidth-2).Render(fitText(rest, contentWidth-2))
				}
			} else {
				style = selectedSubtle
				line = selectedSubtle.Width(contentWidth).Render(fitText("› "+rest, contentWidth))
			}
		}
		lines = append(lines, style.Width(contentWidth).Render(fitText(line, contentWidth)))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for index := range lines {
		lines[index] = fitLine(lines[index], contentWidth) + "  "
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}

func (m modelState) renderHostPane(width, height int) string {
	contentWidth := max(1, width-4) // leave two cells beside each vertical divider
	entries := m.visibleHosts()
	group := m.selectedGroup()
	heading := "主机列表"
	if group.isFavoriteView {
		heading = "收藏"
	} else if group.isRecentView {
		heading = "最近连接"
	} else if group.isAll {
		heading = "全部主机"
	} else if group.isShortcutBar {
		heading = "连接栏 · " + group.name
	} else {
		heading = strings.ToUpper(group.name)
	}
	countLabel := mutedStyle.Render(fmt.Sprintf("%02d", len(entries)))
	heading = fitText(heading, max(1, contentWidth-lipgloss.Width(countLabel)-2))
	headingLabel := sectionStyle.Render(heading)
	if m.activePane == hostPane {
		headingLabel += focusBadgeStyle.Render(" ◆")
	}
	lines := []string{fitLine(headingLabel+"  "+countLabel, contentWidth)}
	if len(m.hosts) > 0 {
		lines = append(lines, m.hostFilterLine(contentWidth))
	} else {
		lines = append(lines, mutedStyle.Render("暂无已保存的连接"))
	}
	lines = append(lines, separatorStyle.Render(strings.Repeat("·", width)), "")
	if m.loading {
		lines = append(lines, "", accentStyle.Render("◌  正在读取本地数据…"))
	} else if len(entries) == 0 {
		lines = append(lines, "", accentStyle.Render("◇"), "", titleStyle.Render("暂无主机"))
		if m.query != "" {
			lines = append(lines, secondaryStyle.Render("没有符合筛选条件的主机。"))
		} else if group.isFavoriteView {
			lines = append(lines, secondaryStyle.Render("按 v 将选中的主机加入收藏。"))
		} else if group.isRecentView {
			lines = append(lines, secondaryStyle.Render("成功连接过的主机会显示在这里。"))
		} else if group.isShortcutBar {
			lines = append(lines,
				secondaryStyle.Render("此连接栏还没有主机。"),
				mutedStyle.Render("切换到主机列表，按 m 选择要加入的连接。"),
			)
		} else if !group.isAll {
			lines = append(lines, secondaryStyle.Render("此分组下还没有主机。"))
		} else {
			lines = append(lines,
				secondaryStyle.Render("保存的主机连接会显示在这里。"),
				mutedStyle.Render("按 n 新增主机，按 g 新建分组。"),
			)
		}
	} else {
		nameWidth := hostTableNameWidth(contentWidth)
		connectionWidth := max(1, contentWidth-hostTablePrefixWidth-2-nameWidth)
		lines[3] = hostTableHeader(contentWidth, nameWidth, connectionWidth)
		rows := max(1, height-len(lines))
		top := clamp(m.hostTop, 0, max(0, len(entries)-rows))
		bottom := min(len(entries), top+rows)
		shortcuts, shortcutBar := m.currentShortcutNumbers()
		for index := top; index < bottom; index++ {
			host := entries[index]
			selected := index == m.hostIndex
			lines = append(lines, hostTableRow(
				m.hostWithAuthProfile(host), selected, shortcuts[host.Id], host.Id == m.defaultHostID,
				contentWidth, shortcutBar, m.batchSelectedHostIDs[host.Id], m.activePane == hostPane,
				m.sshProbeStates[host.Id], nameWidth, connectionWidth,
			))
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	lines = lines[:min(len(lines), height)]
	for index := range lines {
		// Keep the dotted rule full-width so its junctions meet the dividers.
		if index == 2 {
			continue
		}
		lines[index] = "  " + fitLine(lines[index], contentWidth) + "  "
	}
	return strings.Join(lines, "\n")
}

func hostCardLines(host model.Host, selected bool, shortcut int, isDefault bool, width int, shortcutBarMember ...bool) (string, string) {
	inShortcutBar := len(shortcutBarMember) > 0 && shortcutBarMember[0]
	return hostCardLinesWithState(host, selected, shortcut, isDefault, width, inShortcutBar, false)
}

func hostCardLinesWithState(host model.Host, selected bool, shortcut int, isDefault bool, width int, inShortcutBar, marked bool) (string, string) {
	return hostCardLinesWithFocus(host, selected, shortcut, isDefault, width, inShortcutBar, marked, true)
}

func hostCardLinesWithFocus(host model.Host, selected bool, shortcut int, isDefault bool, width int, inShortcutBar, marked, focused bool, sshStatus ...sshProbeStatus) (string, string) {
	leading := "  "
	if marked {
		leading = "☑ "
	}
	if selected {
		leading = "› "
		if marked {
			leading = "›☑"
		}
	}
	markerLabel := "◇"
	if inShortcutBar && shortcut > 0 {
		markerLabel = fmt.Sprintf("%d▣", shortcut)
	} else if host.Favorite {
		markerLabel = "★"
		if shortcut > 0 {
			markerLabel = fmt.Sprintf("%d★", shortcut)
		}
	}
	statusMarker := ""
	if host.Kind == model.HostKindSSH {
		status := sshProbeUnknown
		if len(sshStatus) > 0 {
			status = sshStatus[0]
		}
		statusMarker = sshProbeIndicator(status) + " "
	}
	marker := leading + markerLabel + "  "
	name := titleStyle.Render(host.Name)
	if isDefault {
		name += accentStyle.Render(" ◆默认")
	}
	meta := hostConnectionSummary(host)
	first := fitText(statusMarker+marker+name, width)
	second := fitText("     "+meta, width)
	if selected {
		if !focused {
			selectedName := secondaryStyle.Render(host.Name)
			if isDefault {
				selectedName += mutedStyle.Render(" ◆默认")
			}
			first = fitText(statusMarker+selectedSubtle.Render(marker)+selectedName, width)
			second = fitText(selectedSubtle.Render("     "+meta), width)
			return lipgloss.NewStyle().Background(palette.selected).Width(width).Render(first), selectedSubtle.Width(width).Render(second)
		}
		selectedName := selectedStyle.Render(host.Name)
		if isDefault {
			selectedName += accentStyle.Render(" ◆默认")
		}
		first = fitText(statusMarker+selectedAccent.Render(marker)+selectedName, width)
		second = fitText("     "+meta, width)
		return lipgloss.NewStyle().Background(palette.selected).Width(width).Render(first), selectedSubtle.Width(width).Render(second)
	}
	return fitLine(first, width), mutedStyle.Width(width).Render(second)
}

const hostTableStatusWidth = 4
const hostTableSelectionWidth = 2
const hostTableIconWidth = 4
const hostTablePrefixWidth = hostTableStatusWidth + hostTableSelectionWidth + hostTableIconWidth

func hostTableNameWidth(width int) int {
	return min(24, max(10, (width-hostTablePrefixWidth-2)/2))
}

func hostTableHeader(width, nameWidth, connectionWidth int) string {
	columns := padHostTableColumn("状态", hostTableStatusWidth) +
		padHostTableColumn("", hostTableSelectionWidth) +
		padHostTableColumn("", hostTableIconWidth) +
		padHostTableColumn("主机名称", nameWidth) + "  " +
		padHostTableColumn("连接信息", connectionWidth)
	return fitLine(mutedStyle.Render(columns), width)
}

func hostTableRow(host model.Host, selected bool, shortcut int, isDefault bool, width int, inShortcutBar, marked, focused bool, sshStatus sshProbeStatus, nameWidth, connectionWidth int) string {
	statusColumn := strings.Repeat(" ", hostTableStatusWidth)
	if host.Kind == model.HostKindSSH {
		statusColumn = padHostTableColumn(sshProbeIndicator(sshStatus)+"   ", hostTableStatusWidth)
	}

	selection := "  "
	if marked {
		selection = "☑ "
	}
	if selected {
		selection = "› "
		if marked {
			selection = "›☑"
		}
	}
	selection = padHostTableColumn(selection, hostTableSelectionWidth)

	icon := "◇"
	if inShortcutBar && shortcut > 0 {
		icon = fmt.Sprintf("%d▣", shortcut)
	} else if host.Favorite {
		icon = "★"
		if shortcut > 0 {
			icon = fmt.Sprintf("%d★", shortcut)
		}
	}
	if selected && focused {
		icon = selectedAccent.Render(icon)
	} else if selected {
		icon = selectedSubtle.Render(icon)
	}
	icon = padHostTableColumn(icon+" ", hostTableIconWidth)

	name := sanitizeRemoteText(host.Name)
	if isDefault {
		name += " ◆默认"
	}
	name = fitText(name, nameWidth)
	nameStyle := titleStyle
	connectionStyle := mutedStyle
	if selected {
		if focused {
			nameStyle = selectedStyle
			connectionStyle = selectedSubtle
		} else {
			nameStyle = secondaryStyle
			connectionStyle = selectedSubtle
		}
	}
	nameColumn := padHostTableColumn(nameStyle.Render(name), nameWidth)
	connection := padHostTableColumn(connectionStyle.Render(sanitizeRemoteText(hostConnectionSummary(host))), connectionWidth)
	row := statusColumn + selection + icon + nameColumn + "  " + connection
	if selected {
		return lipgloss.NewStyle().Background(palette.selected).Width(width).Render(fitText(row, width))
	}
	return fitLine(row, width)
}

func padHostTableColumn(value string, width int) string {
	value = fitText(value, width)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func (m modelState) batchEditView() string {
	state := m.batchEdit
	if state == nil {
		return ""
	}
	fields := []struct {
		label   string
		value   string
		enabled bool
	}{{
		label: "所属分组", value: batchEditGroupLabel(m, state.groupID), enabled: state.groupEnabled,
	}, {
		label: "收藏状态", value: batchEditFavoriteLabel(state.favoriteMode), enabled: state.favoriteMode != 0,
	}, {
		label: "备注", value: batchEditTextValue(state.description, state.descriptionOn), enabled: state.descriptionOn,
	}, {
		label: "用户名", value: batchEditTextValue(state.user, state.userOn), enabled: state.userOn,
	}, {
		label: "端口", value: batchEditTextValue(state.port, state.portOn), enabled: state.portOn,
	}, {
		label: "SSH 私钥文件", value: batchEditTextValue(state.identityFile, state.identityFileOn), enabled: state.identityFileOn,
	}, {
		label: "名称模板", value: batchEditTextValue(state.nameTemplate, state.nameTemplateOn), enabled: state.nameTemplateOn,
	}}
	lines := []string{
		mutedStyle.Render(batchEditCountLabel(state) + " · 仅更新启用的字段，其它连接配置保持原样。"),
		"",
	}
	for index, field := range fields {
		mark := "[ ]"
		if field.enabled {
			mark = "[✓]"
		}
		value := field.value
		if field.enabled && state.field == batchEditNameTemplate && state.nameTemplate == "" {
			value = "(输入模板)"
		}
		if state.field == batchEditField(index) && state.field >= batchEditDescription && state.field <= batchEditNameTemplate && field.enabled {
			value += "▏"
		}
		line := fmt.Sprintf("%s %-10s %s", mark, field.label, value)
		if state.field == batchEditField(index) {
			lines = append(lines, selectedStyle.Render("› "+line))
		} else {
			lines = append(lines, "  "+line)
		}
	}
	if state.nameTemplateOn {
		lines = append(lines, mutedStyle.Render("名称模板占位符：{name}=原名称，{index}=所选列表顺序（从 1 开始）。"))
	}
	if state.nameTemplateOn && state.nameTemplate != "" {
		if names, err := renderBatchRenameNames(state.hostIDs, state.originalNames, state.nameTemplate); err == nil {
			lines = append(lines, "", mutedStyle.Render("改名预览："))
			for _, id := range state.hostIDs[:min(3, len(state.hostIDs))] {
				lines = append(lines, fitLine(fmt.Sprintf("  %s → %s", state.originalNames[id], names[id]), 76))
			}
			if len(state.hostIDs) > 3 {
				lines = append(lines, mutedStyle.Render(fmt.Sprintf("  另有 %d 台…", len(state.hostIDs)-3)))
			}
		}
	}
	if state.errorText != "" {
		lines = append(lines, "", warningStyle.Render(state.errorText))
	}
	if state.busy {
		lines = append(lines, "", accentStyle.Render("◌  正在事务式更新并刷新主机列表…"))
	} else {
		lines = append(lines, "", mutedStyle.Render("↑ / ↓ 选择字段 · Ctrl+T 启用文本字段 · ← / → 选择分组/收藏 · Enter 保存 · Esc 取消"))
	}
	return m.modalView("批量编辑主机", lines, 82)
}

func batchEditTextValue(value string, enabled bool) string {
	if enabled {
		if value == "" {
			return "(空值；保存后清空字段)"
		}
		return value
	}
	return mutedStyle.Render("(保持原值)")
}

func (m modelState) shortcutBarPickerView() string {
	picker := m.shortcutBarPicker
	if picker == nil {
		return ""
	}
	lines := []string{
		mutedStyle.Render("Space / Enter 切换成员状态；主机可加入多个连接栏。"),
		"",
	}
	if len(m.shortcutBars) == 0 {
		lines = append(lines, warningStyle.Render("没有可选连接栏，请按 b 新建。"))
	}
	for index, bar := range m.shortcutBars {
		member := false
		for _, hostID := range bar.HostIDs {
			if hostID == picker.hostID {
				member = true
				break
			}
		}
		mark := "[ ] "
		if member {
			mark = "[✓] "
		}
		line := mark + bar.Name + mutedStyle.Render(fmt.Sprintf("  %d 台", len(bar.HostIDs)))
		if index == picker.index {
			lines = append(lines, selectedStyle.Render("› "+line))
		} else {
			lines = append(lines, "  "+line)
		}
	}
	if picker.errorText != "" {
		lines = append(lines, "", warningStyle.Render(picker.errorText))
	}
	if picker.busy {
		lines = append(lines, "", accentStyle.Render("◌  正在保存连接栏成员…"))
	} else {
		lines = append(lines, "", mutedStyle.Render("↑ / ↓ 移动  ·  Enter / Space 切换  ·  Esc 返回"))
	}
	return m.modalView("选择连接栏", lines, 70)
}

func hostKindLabel(kind model.HostKind) string {
	if kind == model.HostKindLocal {
		return "本地终端"
	}
	if kind == model.HostKindFTP {
		return "FTP / FTPS"
	}
	if kind == model.HostKindTelnet {
		return "Telnet（明文）"
	}
	if kind == model.HostKindSerial {
		return "串口终端"
	}
	return "SSH"
}

func hostDisplayKind(host model.Host) string {
	if host.Kind == model.HostKindTelnet && host.TerminalProtocol == model.TerminalProtocolRlogin {
		return "Rlogin（明文）"
	}
	return hostKindLabel(host.Kind)
}

func hostConnectionSummary(host model.Host) string {
	if host.Kind == model.HostKindLocal {
		return "本地 Shell"
	}
	if host.Kind == model.HostKindSerial {
		baudRate, dataBits := host.SerialBaudRate, host.SerialDataBits
		if baudRate == 0 {
			baudRate = 9600
		}
		if dataBits == 0 {
			dataBits = 8
		}
		return fmt.Sprintf("%s · %d-%d-%s-%s", host.IP, baudRate, dataBits,
			serialParityShort(host.SerialParity), serialStopBitsDisplay(host.SerialStopBits)) + " · " + serialFlowControlLabel(host.SerialFlowControl)
	}
	address := host.IP
	if host.User != "" {
		address = host.User + "@" + address
	}
	if host.Port > 0 {
		address += fmt.Sprintf(" · %d", host.Port)
	}
	if host.Kind == model.HostKindFTP {
		address += " · " + ftpTLSModeLabel(host.TLSMode)
	} else if host.Kind == model.HostKindTelnet {
		address += " · " + terminalProtocolName(host) + " 明文"
	}
	return address
}

func serialParityShort(parity string) string {
	switch parity {
	case model.SerialParityEven:
		return "E"
	case model.SerialParityOdd:
		return "O"
	case model.SerialParityMark:
		return "M"
	case model.SerialParitySpace:
		return "S"
	default:
		return "N"
	}
}

func serialStopBitsDisplay(stopBits string) string {
	if stopBits == "" {
		return model.SerialStopBitsOne
	}
	return stopBits
}

func (m modelState) hostPickerView() string {
	picker := m.hostPicker
	if picker == nil {
		return ""
	}
	rows := m.hostPickerRows()
	direction := "左右分屏"
	if picker.target.kind == sftpPeerTarget {
		direction = "为" + sftpSideLabel(picker.target.sftpSide) + "添加远程标签"
	} else if picker.target.axis == splitHorizontal {
		direction = "上下分屏"
	}
	lines := []string{
		mutedStyle.Render(direction + " · ↑ / ↓ 或 j / k 选择，Enter 连接，Esc 取消"),
		"",
	}
	if len(picker.hosts) == 0 {
		lines = append(lines, warningStyle.Render("没有可连接的主机。"))
	} else {
		rowWidth := min(68, max(1, min(76, m.width-10)-2))
		nameWidth, kindWidth := 1, 1
		for _, candidate := range picker.hosts {
			host := m.hostWithAuthProfile(candidate)
			nameWidth = max(nameWidth, lipgloss.Width(sanitizeRemoteText(host.Name)))
			kindWidth = max(kindWidth, lipgloss.Width(hostDisplayKind(host)))
		}
		labelWidth := max(2, rowWidth-4-16) // leave two gaps and room for a useful connection summary
		nameWidth = min(min(nameWidth, 24), max(1, labelWidth*2/3))
		kindWidth = min(min(kindWidth, 16), max(1, labelWidth-nameWidth))
		connectionWidth := max(1, rowWidth-nameWidth-kindWidth-4)
		start := clamp(picker.top, 0, max(0, len(picker.hosts)-rows))
		end := min(len(picker.hosts), start+rows)
		for index := start; index < end; index++ {
			host := m.hostWithAuthProfile(picker.hosts[index])
			name := padHostTableColumn(sanitizeRemoteText(host.Name), nameWidth)
			kind := padHostTableColumn(hostDisplayKind(host), kindWidth)
			connection := fitText(sanitizeRemoteText(hostConnectionSummary(host)), connectionWidth)
			line := name + "  " + kind + "  " + connection
			if index == picker.index {
				lines = append(lines, selectedStyle.Render("› "+line))
			} else {
				lines = append(lines, "  "+line)
			}
		}
		lines = append(lines, "", mutedStyle.Render(fmt.Sprintf("%d / %d", picker.index+1, len(picker.hosts))))
	}
	title := "选择分屏连接目标 · " + direction
	if picker.target.kind == sftpPeerTarget {
		title = "选择" + sftpSideLabel(picker.target.sftpSide) + "远程标签主机"
	}
	return m.modalView(title, lines, 82)
}

func (m modelState) renderDetailPane(width, height int) string {
	contentWidth := max(1, width-2) // add a two-cell gutter after the left divider
	lines := []string{sectionStyle.Render("主机详情"), mutedStyle.Render("已保存的连接配置"), ""}
	host := m.selectedHost()
	if host == nil {
		lines = append(lines,
			mutedStyle.Render("选择主机以查看"),
			mutedStyle.Render("对应的连接信息。"),
		)
	} else {
		displayHost := m.hostWithAuthProfile(*host)
		lines = append(lines,
			mutedStyle.Render("名称"),
			titleStyle.Render(fitText(host.Name, contentWidth)),
			"",
			mutedStyle.Render("类型"),
			secondaryStyle.Render(hostDisplayKind(*host)),
		)
		if host.Kind == model.HostKindLocal {
			lines = append(lines,
				"",
				mutedStyle.Render("Shell"),
				secondaryStyle.Render("启动当前用户的登录 Shell"),
			)
		} else if host.Kind == model.HostKindSerial {
			baudRate, dataBits := host.SerialBaudRate, host.SerialDataBits
			if baudRate == 0 {
				baudRate = 9600
			}
			if dataBits == 0 {
				dataBits = 8
			}
			lines = append(lines,
				"", mutedStyle.Render("设备路径"), secondaryStyle.Render(fitText(host.IP, contentWidth)),
				"", mutedStyle.Render("波特率"), secondaryStyle.Render(fmt.Sprintf("%d", baudRate)),
				"", mutedStyle.Render("数据格式"), secondaryStyle.Render(fmt.Sprintf("%d-%s-%s", dataBits, serialParityShort(host.SerialParity), serialStopBitsDisplay(host.SerialStopBits))),
				"", mutedStyle.Render("流控制"), secondaryStyle.Render(serialFlowControlLabel(host.SerialFlowControl)),
			)
		} else {
			usernameLabel := "用户名"
			if host.Kind == model.HostKindTelnet {
				usernameLabel = "登录名提示"
				if host.TerminalProtocol == model.TerminalProtocolRlogin {
					usernameLabel = "远程用户名"
				}
			}
			lines = append(lines,
				"",
				mutedStyle.Render("地址"),
				secondaryStyle.Render(fitText(host.IP, contentWidth)),
				"",
				mutedStyle.Render("端口"),
				secondaryStyle.Render(fmt.Sprintf("%d", host.Port)),
				"",
				mutedStyle.Render(usernameLabel),
				secondaryStyle.Render(valueOrDash(displayHost.User)),
			)
			if host.Kind == model.HostKindTelnet {
				lines = append(lines, "", warningStyle.Render(terminalProtocolName(*host)+" 不加密账号和终端内容"))
			}
			if host.Kind == model.HostKindFTP {
				lines = append(lines,
					"", mutedStyle.Render("安全模式"), secondaryStyle.Render(ftpTLSModeLabel(host.TLSMode)),
					"", mutedStyle.Render("数据连接"), secondaryStyle.Render(ftpDataModeLabel(host.FTPActiveMode)),
					"", mutedStyle.Render("凭据"), secondaryStyle.Render(credentialLabel(displayHost.CredentialRef)),
				)
			}
			if host.Kind == model.HostKindSSH && len(host.ProxyJumpList) > 0 {
				jumpNames := make([]string, 0, len(host.ProxyJumpList))
				for _, jumpID := range host.ProxyJumpList {
					if jumpHost := m.hostByID(jumpID); jumpHost != nil {
						jumpNames = append(jumpNames, jumpHost.Name)
					} else {
						jumpNames = append(jumpNames, "(主机不存在)")
					}
				}
				lines = append(lines, "", mutedStyle.Render("跳板机"), secondaryStyle.Render(fitText(strings.Join(jumpNames, " → "), contentWidth)))
			}
			if host.Kind == model.HostKindSSH {
				if profile := m.authProfileByID(m.effectiveAuthProfileID(*host)); profile != nil {
					label := "认证配置"
					if host.AuthProfileID == "" {
						label = "认证配置（分组继承）"
					}
					lines = append(lines, "", mutedStyle.Render(label), secondaryStyle.Render(profile.Name))
				}
				x11Status := "关闭"
				if host.X11Forwarding {
					x11Status = "开启"
				}
				lines = append(lines, "", mutedStyle.Render("X11 转发"), secondaryStyle.Render(x11Status))
			}
		}
		lines = append(lines,
			"",
			mutedStyle.Render("所属分组"),
			secondaryStyle.Render(fitText(m.groupPath(host.GroupId), contentWidth)),
		)
		if host.Kind == model.HostKindSSH {
			lines = append(lines, "", mutedStyle.Render("凭据"), secondaryStyle.Render(credentialLabel(displayHost.CredentialRef)))
		}
	}
	for index := range lines {
		lines[index] = "  " + fitLine(lines[index], contentWidth)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}

func (m modelState) hostFilterLine(width int) string {
	base := mutedStyle.Render("已保存的连接  ·  / 筛选")
	if m.searching {
		base = accentStyle.Render("/ ") + secondaryStyle.Render(m.query+"▏")
	} else if m.query != "" {
		base = accentStyle.Render("筛选  ") + secondaryStyle.Render(m.query) + mutedStyle.Render("  ·  Esc 清空")
	}
	sshCount := 0
	for _, host := range m.hosts {
		if host.Kind == model.HostKindSSH {
			sshCount++
		}
	}
	if sshCount > 0 {
		legend := mutedStyle.Render("SSH  ") + sshProbeIndicator(sshProbeOnline) + mutedStyle.Render("  在线   ") +
			sshProbeIndicator(sshProbeChecking) + mutedStyle.Render("  探测中   ") + sshProbeIndicator(sshProbeOffline) + mutedStyle.Render("  无法连接")
		if lipgloss.Width(base)+lipgloss.Width(legend)+2 <= width {
			return fitLine(base+"  "+legend, width)
		}
	}
	return fitLine(base, width)
}

func (m modelState) footerView(width int) string {
	status := m.statusText
	if status == "" {
		status = "本地连接配置"
	}
	statusStyle := mutedStyle
	if m.statusErr {
		statusStyle = statusErrorStyle
	}
	if !m.statusErr && !m.loading && (status == "" || strings.HasPrefix(status, "本地目录已就绪")) {
		status = "↑↓ 移动"
	}
	keys := ""
	canManageCredential := false
	if host := m.selectedHost(); m.activePane == hostPane && host != nil {
		canManageCredential = host.Kind == model.HostKindSSH || host.Kind == model.HostKindFTP
	}
	compactKeys := width < 70 || m.statusErr || m.loading ||
		(m.statusText != "" && !strings.HasPrefix(m.statusText, "本地目录已就绪"))
	if m.searching {
		keys = helpKeyStyle.Render("Enter ") + mutedStyle.Render("应用   ") +
			helpKeyStyle.Render("Esc ") + mutedStyle.Render("清空   ") +
			helpKeyStyle.Render("⌫ ") + mutedStyle.Render("删除")
	} else if compactKeys {
		keys = helpKeyStyle.Render("g ") + mutedStyle.Render("分组  ") +
			helpKeyStyle.Render("b ") + mutedStyle.Render("连接栏  ") +
			helpKeyStyle.Render("n ") + mutedStyle.Render("主机  ")
		if m.activePane == hostPane {
			keys += helpKeyStyle.Render("Space ") + mutedStyle.Render("选择  ")
			if m.batchSelectedCount() > 0 {
				keys += helpKeyStyle.Render("B ") + mutedStyle.Render("批量编辑  ")
			}
		}
		if m.hasCatalogSelection() {
			keys += helpKeyStyle.Render("e ") + mutedStyle.Render("编辑  ") +
				helpKeyStyle.Render("d ") + mutedStyle.Render("删除  ")
		}
		if canManageCredential && width >= 48 {
			keys += helpKeyStyle.Render("c ") + mutedStyle.Render("凭据  ")
		}
		if width >= 64 {
			keys += helpKeyStyle.Render("Ctrl+K ") + mutedStyle.Render("密钥  ")
		}
		if width >= 78 {
			keys += helpKeyStyle.Render("Ctrl+P ") + mutedStyle.Render("认证配置  ")
		}
		keys += helpKeyStyle.Render("l ") + mutedStyle.Render("布局  ")
		if m.lastWorkspaceData != "" && width >= 62 {
			keys += helpKeyStyle.Render("R ") + mutedStyle.Render("恢复上次  ")
		}
		keys += helpKeyStyle.Render("a ") + mutedStyle.Render("快速连接  ")
		if width >= 48 {
			keys += helpKeyStyle.Render("T ") + mutedStyle.Render("配色  ")
		}
		if width >= 76 {
			keys += helpKeyStyle.Render("S ") + mutedStyle.Render("底部提示  ")
		}
		if len(m.hosts) > 0 && width >= 60 {
			keys += helpKeyStyle.Render("Enter ") + mutedStyle.Render("连接  ")
		}
		if m.canOpenStandaloneSFTP() && width >= 54 {
			keys += helpKeyStyle.Render("f ") + mutedStyle.Render("SFTP  ")
		}
		if width >= 70 {
			keys += helpKeyStyle.Render("t ") + mutedStyle.Render("计划传输  ")
		}
		if len(m.hosts) > 0 && width >= 56 {
			keys += helpKeyStyle.Render("/ ") + mutedStyle.Render("筛选  ")
		}
		if m.activePane == hostPane {
			if len(m.shortcutBars) > 0 && width >= 54 {
				keys += helpKeyStyle.Render("m ") + mutedStyle.Render("连接栏成员  ")
			}
			shortcuts, isBar := m.currentShortcutNumbers()
			if len(shortcuts) > 0 && width >= 70 {
				label := "收藏连接"
				if isBar {
					label = "栏内连接"
				}
				keys += helpKeyStyle.Render("1–9 ") + mutedStyle.Render(label+"  ")
			}
			if host := m.selectedHost(); host != nil && (isBar || host.Favorite) {
				keys += helpKeyStyle.Render("[ / ] ") + mutedStyle.Render("调整顺序  ")
			}
		}
		if len(m.tabs) > 0 {
			keys += helpKeyStyle.Render("Ctrl+O ") + mutedStyle.Render("会话  ")
		}
		keys += helpKeyStyle.Render("r ") + mutedStyle.Render("刷新  ") +
			helpKeyStyle.Render("? ") + mutedStyle.Render("帮助  ") +
			helpKeyStyle.Render("q ") + mutedStyle.Render("退出")
	} else {
		keys = helpKeyStyle.Render("g ") + mutedStyle.Render("分组   ") +
			helpKeyStyle.Render("b ") + mutedStyle.Render("连接栏   ") +
			helpKeyStyle.Render("n ") + mutedStyle.Render("主机   ")
		if m.activePane == hostPane {
			keys += helpKeyStyle.Render("Space ") + mutedStyle.Render("选择主机   ")
			if m.batchSelectedCount() > 0 {
				keys += helpKeyStyle.Render("B ") + mutedStyle.Render(fmt.Sprintf("批量编辑 %d 台   ", m.batchSelectedCount()))
			}
		}
		if m.hasCatalogSelection() {
			keys += helpKeyStyle.Render("e ") + mutedStyle.Render("编辑   ") +
				helpKeyStyle.Render("d ") + mutedStyle.Render("删除   ")
		}
		if canManageCredential {
			keys += helpKeyStyle.Render("c ") + mutedStyle.Render("凭据   ")
		}
		keys += helpKeyStyle.Render("Ctrl+K ") + mutedStyle.Render("密钥   ")
		keys += helpKeyStyle.Render("Ctrl+P ") + mutedStyle.Render("认证配置   ")
		keys += helpKeyStyle.Render("l ") + mutedStyle.Render("布局   ")
		if m.lastWorkspaceData != "" {
			keys += helpKeyStyle.Render("R ") + mutedStyle.Render("恢复上次会话   ")
		}
		keys += helpKeyStyle.Render("a ") + mutedStyle.Render("快速连接   ")
		keys += helpKeyStyle.Render("T ") + mutedStyle.Render("配色   ")
		keys += helpKeyStyle.Render("S ") + mutedStyle.Render("底部提示   ")
		if len(m.hosts) > 0 {
			keys += helpKeyStyle.Render("Enter ") + mutedStyle.Render("连接   ") +
				helpKeyStyle.Render("/ ") + mutedStyle.Render("筛选   ")
		}
		if m.activePane == hostPane && m.selectedHost() != nil {
			favoriteAction := "收藏"
			if m.selectedHost().Favorite {
				favoriteAction = "取消收藏"
			}
			keys += helpKeyStyle.Render("v ") + mutedStyle.Render(favoriteAction+"   ")
			defaultAction := "设为默认"
			if m.selectedHost().Id == m.defaultHostID {
				defaultAction = "取消默认"
			}
			keys += helpKeyStyle.Render("s ") + mutedStyle.Render(defaultAction+"   ")
		}
		if m.activePane == hostPane {
			if len(m.shortcutBars) > 0 {
				keys += helpKeyStyle.Render("m ") + mutedStyle.Render("连接栏成员   ")
			}
			shortcuts, isBar := m.currentShortcutNumbers()
			if len(shortcuts) > 0 {
				label := "收藏连接"
				if isBar {
					label = "栏内连接"
				}
				keys += helpKeyStyle.Render("1–9 ") + mutedStyle.Render(label+"   ")
			}
			if host := m.selectedHost(); host != nil && (isBar || host.Favorite) {
				keys += helpKeyStyle.Render("[ / ] ") + mutedStyle.Render("调整顺序   ")
			}
		}
		if m.canOpenStandaloneSFTP() {
			keys += helpKeyStyle.Render("f ") + mutedStyle.Render("SFTP   ")
		}
		keys += helpKeyStyle.Render("t ") + mutedStyle.Render("计划传输   ")
		if len(m.tabs) > 0 {
			keys += helpKeyStyle.Render("Ctrl+O ") + mutedStyle.Render("会话   ")
		}
		keys += helpKeyStyle.Render("r ") + mutedStyle.Render("刷新   ") +
			helpKeyStyle.Render("? ") + mutedStyle.Render("帮助   ") +
			helpKeyStyle.Render("q ") + mutedStyle.Render("退出")
	}
	if lipgloss.Width(status)+lipgloss.Width(keys)+2 > width && !m.statusErr && !m.loading {
		status = ""
	}
	left := statusStyle.Render(fitText(status, max(1, width-lipgloss.Width(keys)-2)))
	return fitLine(alignSides(left, keys, width), width)
}

func (m modelState) helpView() string {
	width := max(54, m.width-4)
	lines := []string{
		brandStyle.Render("◆  快捷键"),
		mutedStyle.Render("主界面操作说明"),
		"",
		keyHelp("↑ / ↓", "在分组或主机列表中移动"),
		keyHelp("j / k", "在分组或主机列表中移动"),
		keyHelp("鼠标", "滚轮移动；单击选择主机，双击连接"),
		keyHelp("Ctrl+O", "在主机列表与当前会话间切换"),
		keyHelp("Tab", "切换分组与主机面板"),
		keyHelp("T", "选择并保存工作台配色方案"),
		keyHelp("S", "切换 SSH 工作区底部快捷键提示；偏好设置保存在本机"),
		keyHelp("g", "在当前分组下新建；全部主机下创建顶层分组"),
		keyHelp("b", "新建独立的命名连接栏"),
		keyHelp("n", "新增主机；未选择分组时保存为未分组"),
		keyHelp("Space", "选择或取消选择当前主机，可跨分组累计选择"),
		keyHelp("B", "批量编辑所选主机；只提交启用的字段，事务失败会全部回滚"),
		keyHelp("a", "快速连接 SSH / Telnet / Rlogin；临时连接不会写入主机目录"),
		keyHelp("e", "编辑当前选中的分组或主机"),
		keyHelp("d", "删除当前选中的分组、连接栏或主机；删除前会确认"),
		keyHelp("t", "管理计划传输；工作台运行时自动执行，也可用 --run-scheduled 独立运行"),
		keyHelp("l", "打开已保存布局；可以从这里恢复之前保存的标签与分屏"),
		keyHelp("Ctrl+K", "管理 SSH 密钥；支持创建、迁移引用和安全删除"),
		keyHelp("Ctrl+Shift+H", "管理已信任的 SSH 主机指纹"),
		keyHelp("Ctrl+P", "管理可复用的 SSH 认证配置档"),
	}
	if m.lastWorkspaceData != "" {
		lines = append(lines, keyHelp("R", "恢复上次退出时打开的标签与 pane；需要重新认证"))
	}
	if m.activePane == hostPane {
		if len(m.shortcutBars) > 0 {
			lines = append(lines, keyHelp("m", "把当前主机加入/移出一个命名连接栏"))
		}
		if m.selectedHost() != nil {
			lines = append(lines, keyHelp("v", "收藏或取消收藏当前主机"))
		}
		if host := m.selectedHost(); host != nil && (host.Kind == model.HostKindSSH || host.Kind == model.HostKindFTP) {
			lines = append(lines, keyHelp("c", "管理此 SSH / FTP 主机的系统钥匙串凭据"))
		}
	}
	if len(m.hosts) > 0 {
		lines = append(lines, keyHelp("/", "按名称、地址或用户名筛选主机"))
	}
	shortcuts, isBar := m.currentShortcutNumbers()
	if m.activePane == hostPane && len(shortcuts) > 0 {
		label := "收藏主机"
		if isBar {
			label = "当前连接栏主机"
		}
		lines = append(lines, keyHelp("1–9", "连接当前快捷栏前 9 台"+label+"；主机卡片显示编号"))
	}
	if host := m.selectedHost(); m.activePane == hostPane && host != nil && (isBar || host.Favorite) {
		label := "收藏快捷栏"
		if isBar {
			label = "当前连接栏"
		}
		lines = append(lines, keyHelp("[ / ]", "将当前主机在"+label+"中前移或后移；顺序会保存在本地"))
	}
	if m.hasFileHosts() {
		lines = append(lines, keyHelp("f", "在主机列表打开独立双栏文件面板"))
	}
	lines = append(lines,
		keyHelp("拖动 / Shift+拖动", "选择终端文本；Shift 可覆盖应用自己的鼠标操作"),
		keyHelp("Cmd+点击", "打开终端中的 HTTP(S) 链接"),
		keyHelp("Enter", "连接当前 SSH、Telnet、Rlogin 主机或本地终端并新建标签"),
		keyHelp("Ctrl+]", "关闭当前 pane；最后一个 pane 会关闭标签"),
		keyHelp("r", "重新读取本地主机目录"),
		keyHelp("?", "关闭帮助"),
		keyHelp("q", "退出工作台"),
		"",
		mutedStyle.Render("主机配置和分组保存在本地 SQLite 数据库中。"),
	)
	for index := range lines {
		lines[index] = fitLine(lines[index], width)
	}
	return "\n  " + strings.Join(lines, "\n  ")
}

func (m modelState) canOpenStandaloneSFTP() bool {
	if m.activePane != hostPane || m.selectedHost() == nil {
		return false
	}
	kind := m.selectedHost().Kind
	return kind == model.HostKindSSH || kind == model.HostKindFTP
}

func (m modelState) hasFileHosts() bool {
	for _, host := range m.hosts {
		if host.Kind == model.HostKindSSH || host.Kind == model.HostKindFTP {
			return true
		}
	}
	return false
}

func (m modelState) hasSSHHosts() bool {
	for _, host := range m.hosts {
		if host.Kind == model.HostKindSSH {
			return true
		}
	}
	return false
}

func (m modelState) hostKeyView() string {
	prompt := m.trustPrompt
	lines := []string{
		brandStyle.Render("◆  首次连接确认"),
		"",
		warningStyle.Render("此主机的身份尚未验证。请通过可信渠道核对下面的指纹。"),
		"",
		sectionStyle.Render("主机"),
		fitText(prompt.Address, 68),
		sectionStyle.Render("SHA256 指纹"),
		brandStyle.Render(prompt.Fingerprint),
		"",
		mutedStyle.Render("按 Enter / y 信任并继续；按 Esc / n 取消。"),
		mutedStyle.Render("信任后会保存到本机 SSH known_hosts 文件。"),
	}
	return m.modalView("确认 SSH 主机指纹", lines, 76)
}

func (m modelState) changedHostKeyView() string {
	prompt := m.hostKeyChangePrompt
	lines := []string{
		warningStyle.Render("服务器呈现的指纹与已保存记录不同。先通过设备控制台或其他可信渠道核对新指纹。"),
		"",
		sectionStyle.Render("主机"),
		fitText(prompt.Address, 88),
		sectionStyle.Render("已保存指纹"),
	}
	for _, fingerprint := range prompt.PreviousFingerprints {
		lines = append(lines, brandStyle.Render(fingerprint))
	}
	lines = append(lines, sectionStyle.Render("服务器当前指纹"), brandStyle.Render(prompt.Fingerprint), "")
	if prompt.CanReplace {
		lines = append(lines,
			mutedStyle.Render("按 Enter / y 确认更新信任记录并重连；按 Esc / n 取消。"),
			mutedStyle.Render("更新会移除匹配此主机的旧记录，并保留共享记录中的其他主机。"),
		)
	} else {
		lines = append(lines,
			warningStyle.Render(prompt.ReplaceBlockReason),
			mutedStyle.Render("按 Esc / n 取消；记录没有被修改。"),
		)
	}
	return m.modalView("SSH 主机指纹已变化", lines, 96)
}

func (m modelState) passwordView() string {
	masked := strings.Repeat("•", len([]rune(m.passwordValue)))
	lines := []string{
		brandStyle.Render("◆  SSH 密码认证"),
		mutedStyle.Render("密钥 / SSH agent 认证未成功，请输入登录密码或私钥口令。"),
		mutedStyle.Render("每台跳板和目标主机分别认证；输入只保留到本次连接完成。"),
		"",
	}
	if m.authenticationHost != nil {
		host := m.authenticationHost
		role := "跳板机"
		if m.connectingHost != nil && host.Id == m.connectingHost.Id {
			role = "目标主机"
		}
		lines = append(lines, sectionStyle.Render("当前认证主机 · "+role),
			fitText(fmt.Sprintf("%s  ·  %s@%s:%d", host.Name, host.User, host.IP, host.Port), 68), "")
	}
	lines = append(lines,
		sectionStyle.Render("密码 / 私钥口令"),
		selectedStyle.Width(64).Render(masked+"▏"),
		"",
	)
	if m.passwordError != "" {
		lines = append(lines, warningStyle.Render("! "+m.passwordError), "")
	}
	lines = append(lines,
		mutedStyle.Render("凭据仅用于本次连接，不会写入主机配置或日志。"),
		mutedStyle.Render("Enter 连接  ·  Esc 取消"),
	)
	return m.modalView("输入 SSH 密码", lines, 76)
}

func (m modelState) keyboardAuthView() string {
	prompt := m.keyboardAuthPrompt
	if prompt == nil {
		return ""
	}
	request := prompt.request
	lines := []string{
		brandStyle.Render("◆  SSH 交互式身份验证"),
		"",
	}
	if request.host.Name != "" {
		lines = append(lines, sectionStyle.Render("连接"),
			fitText(fmt.Sprintf("%s  ·  %s@%s:%d", request.host.Name, request.host.User, request.host.IP, request.host.Port), 68), "")
	}
	if request.name != "" {
		lines = append(lines, sectionStyle.Render("验证方式"), fitText(sanitizeRemoteText(request.name), 68))
	}
	if request.instruction != "" {
		lines = append(lines, fitText(sanitizeRemoteText(request.instruction), 68))
	}
	if prompt.question >= 0 && prompt.question < len(request.questions) {
		question := sanitizeRemoteText(request.questions[prompt.question])
		lines = append(lines, "", sectionStyle.Render(fmt.Sprintf("问题 %d / %d", prompt.question+1, len(request.questions))), fitText(question, 68))
		value := prompt.value
		echo := prompt.question < len(request.echos) && request.echos[prompt.question]
		if !echo {
			value = strings.Repeat("•", len([]rune(value)))
		}
		lines = append(lines, selectedStyle.Width(64).Render(value+"▏"))
	} else {
		lines = append(lines, "", mutedStyle.Render("服务器未提供输入问题；按 Enter 继续。"))
	}
	lines = append(lines, "", mutedStyle.Render("Enter 提交  ·  Esc 取消连接"))
	return m.modalView("SSH 交互式身份验证", lines, 76)
}

func (m modelState) connectionProgressView(title, detail string) string {
	lines := []string{
		accentStyle.Render("◌  " + title),
		mutedStyle.Render(detail),
		"",
		mutedStyle.Render("Esc 可取消连接。"),
	}
	return m.modalView("SSH 连接", lines, 70)
}

func (m modelState) modalView(title string, lines []string, maxWidth int) string {
	if m.width < 54 || m.height < 12 {
		return "\n  " + warningStyle.Render("终端太小，无法显示弹窗") +
			"\n  请放大终端后重试。"
	}
	contentWidth := min(maxWidth-6, m.width-10)
	for index := range lines {
		lines[index] = fitLine(lines[index], contentWidth)
	}
	content := append([]string{titleStyle.Render(title), ""}, lines...)
	box := lipgloss.NewStyle().
		Width(contentWidth).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(palette.accent).
		Background(palette.canvas).
		Render(strings.Join(content, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m modelState) formView() string {
	form := m.form
	if m.width < 54 {
		return "\n  " + warningStyle.Render("终端太窄，无法显示表单") +
			"\n  请将宽度调整到至少 54 列。\n\n  Esc  取消"
	}
	minimumHeight := max(18, m.form.fieldCount()+10)
	if form.kind == hostForm && form.hostKind == model.HostKindSSH {
		minimumHeight++
	}
	if form.errorText != "" {
		minimumHeight += 2
	}
	if form.saving {
		minimumHeight += 2
	}
	if m.height < minimumHeight {
		return "\n  " + warningStyle.Render("终端高度不足，无法显示表单") +
			fmt.Sprintf("\n  请将高度调整到至少 %d 行。\n\n  Esc  取消", minimumHeight)
	}

	title := "新建分组"
	subtitle := "SSH 认证配置与文件面板起始目录可由本分组及子分组继承。"
	if form.editing {
		title = "编辑分组"
		subtitle = "修改分组层级、SSH 认证配置和后代主机使用的文件面板起始目录。"
	}
	if form.kind == shortcutBarForm {
		title = "新建命名连接栏"
		subtitle = "创建一个独立的主机快捷集合；连接栏只保存本机主机引用和排列顺序。"
		if form.editing {
			title = "重命名连接栏"
			subtitle = "重命名不会改变连接栏中的主机或快捷顺序。"
		}
	}
	if form.kind == hostForm {
		title = "新增主机"
		subtitle = "在本地保存 SSH、Telnet、Rlogin、串口、FTP/FTPS 或本地终端配置；文件起始目录留空时继承所属分组。"
		if form.editing {
			title = "编辑主机"
			subtitle = "修改连接配置；起始目录留空时继承分组默认，SSH 也可设置认证配置、跳板机和 X11 转发。"
		}
	}
	panelWidth := min(76, m.width-8)
	contentWidth := panelWidth - 6
	lines := []string{
		brandStyle.Render("◆  ") + titleStyle.Render(title),
		mutedStyle.Render(subtitle),
		"",
	}
	labels := form.fieldLabels()
	for index, label := range labels {
		value := m.formFieldValue(index)
		if form.isChoiceFieldAt(index) && form.field == index {
			value += mutedStyle.Render("  ←/→ 切换")
		} else if !form.isChoiceFieldAt(index) && value == "" {
			value = mutedStyle.Render("请输入")
		}
		indicator := "  "
		if form.field == index {
			indicator = selectedAccent.Render("› ")
			if !form.isChoiceFieldAt(index) {
				if form.currentText() == "" {
					value = mutedStyle.Render("请输入")
				}
				value += accentStyle.Render("▏")
			}
		}
		labelStyle := sectionStyle.Render(label)
		labelPadding := strings.Repeat(" ", max(1, 10-lipgloss.Width(label)))
		line := indicator + labelStyle + labelPadding + " " + value
		if form.field == index {
			line = selectedStyle.Width(contentWidth).Render(fitText(line, contentWidth))
		} else {
			line = fitLine(line, contentWidth)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "")
	if form.errorText != "" {
		lines = append(lines, warningStyle.Render("! ")+fitText(form.errorText, contentWidth-2), "")
	}
	if form.saving {
		lines = append(lines, accentStyle.Render("◌  正在保存到 SQLite…"), "")
	}
	lines = append(lines,
		mutedStyle.Render("文字字段可直接输入；Backspace 删除末尾字符。"),
		mutedStyle.Render("Tab / ↑↓ 切换字段  ·  Enter 下一项/保存  ·  Esc 取消"),
	)
	if form.kind == groupForm {
		lines = append(lines, mutedStyle.Render("本地与远程目录分别继承最近祖先的非空设置；本地目录仅保存在这台设备。"))
	}
	if form.kind == hostForm && (form.hostKind == model.HostKindSSH || form.hostKind == model.HostKindFTP) {
		lines = append(lines, mutedStyle.Render("主机起始目录优先；留空时继承分组。相对路径按工作台启动目录或服务器当前目录解析。"))
	}
	if form.kind == hostForm && form.hostKind == model.HostKindSSH {
		lines = append(lines, mutedStyle.Render("私钥路径支持绝对路径、~/ 和相对路径；Ctrl+G 生成密钥；不会保存私钥内容。"))
		lines = append(lines, mutedStyle.Render("SSH 连接后命令在终端首次收到输出时发送一次；仅支持单行命令，不导出到会话文件。"))
		lines = append(lines, mutedStyle.Render("提示符登录脚本填写本机 JSON 文件路径；支持 expect 正则、{{user}} 和钥匙串 {{password}}。"))
	}
	if form.kind == hostForm && form.hostKind == model.HostKindSerial && form.field == 2 {
		lines = append(lines, mutedStyle.Render("Ctrl+P 检测并选择本机串口设备；也可以手动输入设备路径。"))
	}
	for index := range lines {
		lines[index] = fitLine(lines[index], contentWidth)
	}
	boxStyle := lipgloss.NewStyle().
		Width(contentWidth).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(palette.accent).
		Background(palette.canvas)
	panel := boxStyle.Render(strings.Join(lines, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, panel)
}

func (m modelState) serialPortPickerView() string {
	picker := m.serialPortPicker
	if picker == nil {
		return ""
	}
	lines := []string{
		brandStyle.Render("◆  选择本机串口设备"),
		mutedStyle.Render("设备列表来自当前电脑；也可返回表单手动输入路径。"),
		"",
	}
	switch {
	case picker.loading:
		lines = append(lines, accentStyle.Render("◌  正在检测串口设备…"))
	case picker.errorText != "":
		lines = append(lines, warningStyle.Render("! "+picker.errorText))
	case len(picker.ports) == 0:
		lines = append(lines, mutedStyle.Render("未发现串口设备。"))
	default:
		rows := m.serialPortPickerRows()
		top := clamp(picker.top, 0, max(0, len(picker.ports)-rows))
		bottom := min(len(picker.ports), top+rows)
		for index := top; index < bottom; index++ {
			prefix, style := "  ", mutedStyle
			if index == picker.index {
				prefix, style = "› ", selectedStyle
			}
			lines = append(lines, style.Width(64).Render(prefix+sanitizeRemoteText(picker.ports[index])))
		}
	}
	lines = append(lines, "", mutedStyle.Render("↑/↓ 或 j/k 移动  ·  Enter 选择  ·  Ctrl+R 重新检测  ·  Esc 返回"))
	return m.modalView("串口设备", lines, 76)
}

func (m modelState) hasCatalogSelection() bool {
	if m.activePane == groupPane {
		selected := m.selectedGroup()
		return !selected.isAll && !selected.isFavoriteView && !selected.isRecentView
	}
	return m.selectedHost() != nil
}

func (m modelState) catalogDeleteConfirmView() string {
	confirm := m.deleteConfirm
	kind := "主机"
	if confirm.kind == deleteGroup {
		kind = "分组"
	} else if confirm.kind == deleteShortcutBar {
		kind = "连接栏"
	}
	lines := []string{
		warningStyle.Render("此操作无法撤销。"),
		"确定删除" + kind + "「" + confirm.name + "」吗？",
		"",
	}
	if confirm.kind == deleteGroup {
		lines = append(lines, mutedStyle.Render("分组必须为空才能删除；请先移动或删除其中的主机和子分组。"))
	} else if confirm.kind == deleteShortcutBar {
		lines = append(lines, mutedStyle.Render("删除连接栏只会移除快捷集合，不会删除其中的主机配置。"))
	}
	if confirm.deleting {
		lines = append(lines, accentStyle.Render("◌  正在删除…"))
	} else {
		lines = append(lines, helpKeyStyle.Render("Enter / y ")+"确认   "+helpKeyStyle.Render("Esc / n ")+"取消")
	}
	return m.modalView("删除确认", lines, 76)
}

func (m modelState) formFieldValue(field int) string {
	form := m.form
	if form.kind == shortcutBarForm {
		return form.valueAt(field)
	}
	if form.kind == hostForm && field == 1 {
		return form.valueAt(field)
	}
	if form.kind == hostForm && form.hostKind == model.HostKindSSH && field == 6 {
		if form.proxyJumpID == "" {
			return "直连"
		}
		if host := m.hostByID(form.proxyJumpID); host != nil {
			return host.Name
		}
		return "(跳板机不存在)"
	}
	if form.kind == groupForm && field == 2 {
		if form.authProfileID == "" {
			if inherited := m.inheritedGroupAuthProfileID(form.groupID); inherited != "" {
				if profile := m.authProfileByID(inherited); profile != nil {
					return "继承上级：" + profile.Name
				}
			}
			return "不设置（继承上级或不使用）"
		}
		if profile := m.authProfileByID(form.authProfileID); profile != nil {
			return profile.Name
		}
		return "(认证配置不存在)"
	}
	if form.kind == hostForm && form.hostKind == model.HostKindSSH && field == 10 {
		if form.authProfileID == "" {
			if inherited := m.inheritedGroupAuthProfileID(form.groupID); inherited != "" {
				if profile := m.authProfileByID(inherited); profile != nil {
					return "继承分组：" + profile.Name
				}
			}
			return "不使用认证配置"
		}
		if profile := m.authProfileByID(form.authProfileID); profile != nil {
			return profile.Name
		}
		return "(认证配置不存在)"
	}
	if field == form.groupFieldIndex() {
		if form.groupID == "" {
			if form.kind == groupForm {
				return "(根分组)"
			}
			return "(未分组)"
		}
		return m.groupPath(form.groupID)
	}
	return form.valueAt(field)
}

func (m modelState) smallTerminalView() string {
	return "\n  " + brandStyle.Render("SSH 终端工作台") + "\n\n  " +
		warningStyle.Render("终端太窄") + "\n  请将宽度调整到至少 54 列。\n\n  q  退出"
}

func (m modelState) hostSeparatorRow() int {
	return 2 // heading, filter hint, separator
}

func (m modelState) divider(height, junctionRow int, junction string) string {
	line := separatorStyle.Render("│")
	lines := make([]string, height)
	for row := range height {
		lines[row] = line
		if row == junctionRow {
			lines[row] = separatorStyle.Render(junction)
		}
	}
	return strings.Join(lines, "\n")
}

func (m modelState) groupPath(groupID string) string {
	if groupID == "" {
		return "未分组"
	}
	byID := make(map[string]model.Group, len(m.groups))
	for _, group := range m.groups {
		byID[group.Id] = group
	}
	path := make([]string, 0, 4)
	seen := make(map[string]bool)
	for currentID := groupID; currentID != "" && !seen[currentID]; {
		group, ok := byID[currentID]
		if !ok {
			break
		}
		seen[currentID] = true
		path = append(path, group.Name)
		currentID = group.ParentId
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	if len(path) == 0 {
		return "未知分组"
	}
	return strings.Join(path, " / ")
}

func alignSides(left, right string, width int) string {
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + right
}

func fitLine(text string, width int) string {
	return lipgloss.NewStyle().Width(width).Render(fitText(text, width))
}

func fitText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	var result strings.Builder
	for _, character := range text {
		candidate := result.String() + string(character)
		if lipgloss.Width(candidate)+1 > width {
			break
		}
		result.WriteRune(character)
	}
	return result.String() + "…"
}

func keyHelp(key, description string) string {
	return "  " + helpKeyStyle.Render(fmt.Sprintf("%-9s", key)) + secondaryStyle.Render(description)
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

func credentialLabel(reference string) string {
	if strings.TrimSpace(reference) == "" {
		return "未配置"
	}
	return "系统钥匙串引用"
}

var _ tea.Model = modelState{}
