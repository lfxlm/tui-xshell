package ui

import (
	"bytes"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"tui-ssh-workbench/internal/infra/sftpclient"
	"tui-ssh-workbench/internal/model"
)

type contextMenuTarget uint8

const (
	paneMenuTarget contextMenuTarget = iota
	tabMenuTarget
	sftpMenuTarget
)

type contextMenuAction uint8

const (
	menuCopySelection contextMenuAction = iota
	menuSendSavedPassword
	menuSplitVertical
	menuSplitHorizontal
	menuOpenSFTP
	menuClosePane
	menuCloseTab
	menuActivateTab
	menuShowHosts
	menuPortForward
	menuToggleOutputLog
	menuToggleScriptRecording
	menuToggleAutoReconnect
	menuViewTerminalLogs
	menuSendASCIIFile
	menuOpenXYMODEM
	menuNextTerminalEncoding
	menuLockScreen
	menuScreenLockPassword
	menuSFTPEnter
	menuSFTPPreview
	menuSFTPEdit
	menuSFTPExternalEdit
	menuSFTPTransfer
	menuSFTPRename
	menuSFTPDelete
	menuSFTPProperties
	menuSFTPMkdir
	menuSFTPRefresh
	menuSFTPParent
)

type contextMenuItem struct {
	label    string
	action   contextMenuAction
	disabled bool
}

type contextMenuState struct {
	target    contextMenuTarget
	tabID     string
	paneID    string
	sftpSide  sftpSide
	sftpIndex int
	x         int
	y         int
	selected  int
}

type savedTerminalPasswordLoadedMsg struct {
	tabID         string
	paneID        string
	credentialRef string
	password      []byte
	err           error
}

const savedTerminalPasswordLoadingStatus = "正在读取系统钥匙串密码…"

func (m *modelState) openPaneContextMenu(tabID, paneID string, x, y int) {
	m.workspaceLeaderPending = false
	m.contextMenu = &contextMenuState{
		target: paneMenuTarget,
		tabID:  tabID,
		paneID: paneID,
		x:      x,
		y:      y,
	}
	m.selectFirstContextMenuItem()
}

func (m *modelState) openTabContextMenu(tabID string, x, y int) {
	m.workspaceLeaderPending = false
	m.contextMenu = &contextMenuState{
		target: tabMenuTarget,
		tabID:  tabID,
		x:      x,
		y:      y,
	}
	m.selectFirstContextMenuItem()
}

func (m *modelState) openSFTPContextMenu(side sftpSide, index, x, y int) {
	if m.sftp == nil {
		return
	}
	m.workspaceLeaderPending = false
	m.sftp.activeSide = side
	if index >= 0 {
		if side == sftpLocalSide {
			m.sftp.localSelected = index
		} else {
			m.sftp.selected = index
		}
	}
	m.contextMenu = &contextMenuState{
		target:    sftpMenuTarget,
		sftpSide:  side,
		sftpIndex: index,
		x:         x,
		y:         y,
	}
	m.selectFirstContextMenuItem()
}

func (m modelState) contextMenuItems() []contextMenuItem {
	menu := m.contextMenu
	if menu == nil {
		return nil
	}
	if menu.target == tabMenuTarget {
		return []contextMenuItem{
			{label: "切换到此标签", action: menuActivateTab, disabled: menu.tabID == m.activeTabID},
			{label: "关闭此标签", action: menuCloseTab},
			{label: "返回主机列表", action: menuShowHosts},
		}
	}
	if menu.target == sftpMenuTarget {
		state := m.sftp
		if state == nil {
			return nil
		}
		var entry *sftpclient.Entry
		entries := sftpPaneEntries(state, menu.sftpSide)
		if menu.sftpIndex >= 0 && menu.sftpIndex < len(entries) {
			entry = &entries[menu.sftpIndex]
		}
		items := make([]contextMenuItem, 0, 9)
		if entry != nil {
			if entry.IsDir && !entry.IsSymlink {
				items = append(items, contextMenuItem{label: "进入目录", action: menuSFTPEnter})
			} else if !entry.IsSymlink && (entry.Mode.IsRegular() || !entry.ModeKnown) {
				items = append(items, contextMenuItem{label: "预览", action: menuSFTPPreview})
			} else {
				items = append(items, contextMenuItem{label: "打开", action: menuSFTPEnter, disabled: true})
			}
			transferDisabled := state.busy || entry.IsSymlink || (!entry.IsDir && entry.ModeKnown && !entry.Mode.IsRegular())
			transferLabel := "下载到本地"
			if menu.sftpSide == sftpLocalSide {
				transferLabel = "上传到远程"
			}
			items = append(items,
				contextMenuItem{label: transferLabel, action: menuSFTPTransfer, disabled: transferDisabled},
				contextMenuItem{label: "编辑", action: menuSFTPEdit, disabled: state.busy || entry.IsDir || entry.IsSymlink || (entry.ModeKnown && !entry.Mode.IsRegular())},
				contextMenuItem{label: "使用外部编辑器打开", action: menuSFTPExternalEdit, disabled: state.busy || entry.IsDir || entry.IsSymlink || (entry.ModeKnown && !entry.Mode.IsRegular())},
				contextMenuItem{label: "重命名", action: menuSFTPRename, disabled: state.busy},
				contextMenuItem{label: "删除…", action: menuSFTPDelete, disabled: state.busy},
				contextMenuItem{label: "属性", action: menuSFTPProperties},
			)
		}
		items = append(items,
			contextMenuItem{label: "新建文件夹…", action: menuSFTPMkdir, disabled: state.busy},
			contextMenuItem{label: "刷新当前目录", action: menuSFTPRefresh, disabled: state.busy},
			contextMenuItem{label: "返回上级目录", action: menuSFTPParent, disabled: state.busy},
		)
		return items
	}

	tab := m.tabByID(menu.tabID)
	var paneHasSession bool
	var paneCanSFTP bool
	var paneCanForward bool
	var paneHasOutputLog bool
	var paneHasScriptRecording bool
	var paneLogClosing bool
	var paneAutoReconnect bool
	var paneIsSSH bool
	var paneHasSavedPassword bool
	var paneHasSelection bool
	terminalEncoding := model.TerminalEncodingUTF8
	var paneEncodingSaving bool
	var targetPane *terminalPane
	if tab != nil {
		if targetPane = findPane(tab.root, menu.paneID); targetPane != nil {
			paneHasSession = targetPane.session != nil
			paneHasSelection = targetPane.selection != nil
			paneHasOutputLog = targetPane.outputLogger != nil
			paneHasScriptRecording = targetPane.scriptRecording != nil
			paneLogClosing = targetPane.outputLogClosing
			paneAutoReconnect = targetPane.autoReconnect
			paneIsSSH = targetPane.host.Kind == model.HostKindSSH
			paneHasSavedPassword = paneIsSSH && targetPane.host.CredentialRef != "" && m.credentials != nil
			terminalEncoding = terminalEncodingSummary(targetPane)
			paneEncodingSaving = targetPane.terminalEncodingSaving
			_, paneCanSFTP = sshSessionFromTerminalSession(targetPane.session)
			paneCanSFTP = paneCanSFTP && targetPane.host.Kind == model.HostKindSSH
			_, paneCanForward = sshSessionFromTerminalSession(targetPane.session)
			paneCanForward = paneCanForward && targetPane.host.Kind == model.HostKindSSH
		}
	}
	logLabel := "开始终端输出记录  " + m.workspaceLeaderChord("toggle_recording")
	if paneHasOutputLog {
		logLabel = "停止终端输出记录  " + m.workspaceLeaderChord("toggle_recording")
	}
	scriptRecordingLabel := "开始脚本录制（可能含敏感命令）  " + m.workspaceLeaderChord("toggle_script_recording")
	if paneHasScriptRecording {
		scriptRecordingLabel = "停止并保存脚本录制  " + m.workspaceLeaderChord("toggle_script_recording")
	}
	autoReconnectLabel := "开启断线自动重连  " + m.workspaceLeaderChord("auto_reconnect")
	if paneAutoReconnect {
		autoReconnectLabel = "关闭断线自动重连  " + m.workspaceLeaderChord("auto_reconnect")
	}
	encodingLabel := "终端编码：" + terminalEncoding + "（切换下一个）"
	if paneEncodingSaving {
		encodingLabel = "正在保存终端编码…"
	}
	items := []contextMenuItem{
		{label: "复制当前选区  " + terminalClipboardCopyShortcutLabel(), action: menuCopySelection, disabled: !paneHasSelection},
		{label: encodingLabel, action: menuNextTerminalEncoding, disabled: paneEncodingSaving},
		{label: "发送文本文件到终端  " + m.workspaceLeaderChord("send_ascii_file"), action: menuSendASCIIFile, disabled: !paneHasSession},
		{label: "XMODEM / YMODEM 传输  " + m.workspaceLeaderChord("xymodem"), action: menuOpenXYMODEM, disabled: !paneHasSession},
		{label: "打开远程文件管理  " + m.workspaceLeaderChord("sftp"), action: menuOpenSFTP, disabled: !paneCanSFTP},
		{label: "SSH 转发管理  " + m.workspaceLeaderChord("port_forward"), action: menuPortForward, disabled: !paneCanForward},
		{label: "查看终端输出日志  " + m.workspaceLeaderChord("view_logs"), action: menuViewTerminalLogs},
		{label: scriptRecordingLabel, action: menuToggleScriptRecording, disabled: !paneHasSession},
		{label: "锁定工作区屏幕  " + m.workspaceLeaderChord("screen_lock"), action: menuLockScreen},
		{label: "锁屏密码设置  " + m.workspaceLeaderChord("screen_lock_password"), action: menuScreenLockPassword},
		{label: logLabel, action: menuToggleOutputLog, disabled: paneLogClosing || (!paneHasSession && !paneHasOutputLog)},
		{label: autoReconnectLabel, action: menuToggleAutoReconnect, disabled: !paneIsSSH},
		{label: "左右分屏并选择连接  " + m.workspaceLeaderChord("split_vertical"), action: menuSplitVertical, disabled: !paneHasSession},
		{label: "上下分屏并选择连接  " + m.workspaceLeaderChord("split_horizontal"), action: menuSplitHorizontal, disabled: !paneHasSession},
		{label: "关闭此 pane", action: menuClosePane},
		{label: "关闭此标签", action: menuCloseTab},
		{label: "返回主机列表", action: menuShowHosts},
	}
	if paneIsSSH {
		passwordLabel := "发送已保存密码"
		if !paneHasSavedPassword {
			passwordLabel += "（未配置）"
		}
		passwordItem := contextMenuItem{label: passwordLabel, action: menuSendSavedPassword, disabled: !paneHasSession || !paneHasSavedPassword}
		items = append(items, contextMenuItem{})
		copy(items[2:], items[1:])
		items[1] = passwordItem
	}
	return items
}

func (m *modelState) openActivePaneContextMenu() {
	tab := m.activeTab()
	pane := m.activeTerminalPane()
	if tab == nil || pane == nil {
		return
	}
	rect, ok := paneRectFor(tab.root, pane.id, paneRect{width: m.width, height: max(1, m.height-2)})
	if !ok {
		return
	}
	m.openPaneContextMenu(tab.id, pane.id, rect.x+rect.width/2, rect.y+max(1, rect.height/2)+1)
}

func (m *modelState) openActiveTabContextMenu() {
	if tab := m.activeTab(); tab != nil {
		m.openTabContextMenu(tab.id, max(0, m.width/2), 0)
	}
}

func (m *modelState) selectFirstContextMenuItem() {
	items := m.contextMenuItems()
	if m.contextMenu == nil {
		return
	}
	for index, item := range items {
		if !item.disabled {
			m.contextMenu.selected = index
			return
		}
	}
}

func (m *modelState) moveContextMenuSelection(delta int) {
	if m.contextMenu == nil {
		return
	}
	items := m.contextMenuItems()
	if len(items) == 0 {
		return
	}
	index := m.contextMenu.selected
	for range len(items) {
		index = (index + delta%len(items) + len(items)) % len(items)
		if !items[index].disabled {
			m.contextMenu.selected = index
			return
		}
	}
}

func (m *modelState) updateContextMenuKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	switch key.Code {
	case tea.KeyEscape:
		m.contextMenu = nil
		return m, nil
	case tea.KeyUp:
		m.moveContextMenuSelection(-1)
		return m, nil
	case tea.KeyDown:
		m.moveContextMenuSelection(1)
		return m, nil
	case tea.KeyEnter, tea.KeySpace:
		return m.executeContextMenuAction(m.contextMenu.selected)
	}
	switch key.Text {
	case "k":
		m.moveContextMenuSelection(-1)
	case "j":
		m.moveContextMenuSelection(1)
	case "q":
		m.contextMenu = nil
	default:
		if key.Mod&tea.ModCtrl != 0 && key.Code == 'c' {
			m.contextMenu = nil
		}
	}
	return m, nil
}

func (m *modelState) updateContextMenuMouse(msg tea.MouseMsg) tea.Cmd {
	menu, x, y, width, height := m.contextMenuLayout()
	if menu == "" {
		m.contextMenu = nil
		return nil
	}
	mouse := msg.Mouse()
	switch click := msg.(type) {
	case tea.MouseWheelMsg:
		if mouse.X >= x && mouse.X < x+width && mouse.Y >= y && mouse.Y < y+height {
			delta := 1
			if click.Button == tea.MouseWheelUp {
				delta = -1
			}
			m.moveContextMenuSelection(delta)
			return nil
		}
	case tea.MouseClickMsg:
		if click.Button == tea.MouseRight {
			m.contextMenu = nil
			return m.updateMouse(msg)
		}
		if click.Button != tea.MouseLeft {
			return nil
		}
		if mouse.X >= x && mouse.X < x+width && mouse.Y >= y && mouse.Y < y+height {
			itemIndex := mouse.Y - y - 1 // one border row above the first item
			if itemIndex >= 0 && itemIndex < len(m.contextMenuItems()) && mouse.X > x && mouse.X < x+width-1 {
				_, command := m.executeContextMenuAction(itemIndex)
				return command
			}
			return nil
		}
		m.contextMenu = nil
		return m.updateMouse(msg)
	}
	return nil
}

func (m *modelState) executeContextMenuAction(index int) (tea.Model, tea.Cmd) {
	menu := m.contextMenu
	items := m.contextMenuItems()
	if menu == nil || index < 0 || index >= len(items) || items[index].disabled {
		return m, nil
	}
	item := items[index]
	tabID, paneID := menu.tabID, menu.paneID
	m.contextMenu = nil
	if item.action >= menuSFTPEnter {
		if m.sftp == nil {
			return m, nil
		}
		m.sftp.activeSide = menu.sftpSide
		switch item.action {
		case menuSFTPEnter:
			return m, m.enterSFTPSelection()
		case menuSFTPPreview:
			return m.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "v", Code: 'v'}))
		case menuSFTPEdit:
			return m.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "e", Code: 'e'}))
		case menuSFTPExternalEdit:
			entry := m.selectedSFTPEntry(menu.sftpSide)
			if entry == nil {
				return m, nil
			}
			if isSFTPImageName(entry.Name) {
				return m, m.openSFTPImageEditor(true)
			}
			m.sftp.externalEditorOnLoad = true
			command := m.openSFTPTextEditor(sftpEditMode)
			if command == nil {
				m.sftp.externalEditorOnLoad = false
			}
			return m, command
		case menuSFTPTransfer:
			key := "d"
			if menu.sftpSide == sftpLocalSide {
				key = "u"
			}
			return m.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: key, Code: rune(key[0])}))
		case menuSFTPRename:
			return m.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "n", Code: 'n'}))
		case menuSFTPDelete:
			return m.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))
		case menuSFTPProperties:
			return m.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "i", Code: 'i'}))
		case menuSFTPMkdir:
			return m.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "m", Code: 'm'}))
		case menuSFTPRefresh:
			return m.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "r", Code: 'r'}))
		case menuSFTPParent:
			return m.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
		}
		return m, nil
	}

	switch item.action {
	case menuCopySelection:
		tab := m.tabByID(tabID)
		if tab == nil {
			return m, nil
		}
		pane := findPane(tab.root, paneID)
		if pane == nil || pane.selection == nil {
			return m, nil
		}
		m.activateWorkspaceTab(tabID)
		tab.activePaneID = paneID
		m.workspaceVisible = true
		return m, m.copyTerminalSelection(pane)
	case menuSendSavedPassword:
		return m, m.sendSavedTerminalPassword(tabID, paneID)
	case menuNextTerminalEncoding:
		tab := m.tabByID(tabID)
		if tab == nil || findPane(tab.root, paneID) == nil {
			return m, nil
		}
		m.activateWorkspaceTab(tabID)
		tab.activePaneID = paneID
		m.workspaceVisible = true
		return m, m.cycleTerminalEncoding(tabID, paneID)
	case menuLockScreen:
		return m, m.beginScreenLock(screenLockPurposeLock)
	case menuScreenLockPassword:
		return m, m.beginScreenLock(screenLockPurposeChange)
	case menuSendASCIIFile:
		tab := m.tabByID(tabID)
		if tab == nil || findPane(tab.root, paneID) == nil {
			return m, nil
		}
		m.activateWorkspaceTab(tabID)
		tab.activePaneID = paneID
		m.workspaceVisible = true
		m.openASCIIFileSend()
		return m, nil
	case menuOpenXYMODEM:
		tab := m.tabByID(tabID)
		if tab == nil || findPane(tab.root, paneID) == nil {
			return m, nil
		}
		m.activateWorkspaceTab(tabID)
		tab.activePaneID = paneID
		m.workspaceVisible = true
		m.openXYMODEMPrompt()
		return m, nil
	case menuSplitVertical, menuSplitHorizontal:
		tab := m.tabByID(tabID)
		if tab == nil || findPane(tab.root, paneID) == nil {
			return m, nil
		}
		m.activateWorkspaceTab(tabID)
		tab.activePaneID = paneID
		m.workspaceVisible = true
		axis := splitVertical
		if item.action == menuSplitHorizontal {
			axis = splitHorizontal
		}
		return m, m.splitActivePane(axis)
	case menuOpenSFTP:
		tab := m.tabByID(tabID)
		if tab == nil || findPane(tab.root, paneID) == nil {
			return m, nil
		}
		m.activateWorkspaceTab(tabID)
		tab.activePaneID = paneID
		m.workspaceVisible = true
		return m, m.openActiveSFTP()
	case menuPortForward:
		tab := m.tabByID(tabID)
		if tab == nil || findPane(tab.root, paneID) == nil {
			return m, nil
		}
		m.activateWorkspaceTab(tabID)
		tab.activePaneID = paneID
		m.workspaceVisible = true
		return m, m.openPortForwardManager(paneID)
	case menuToggleOutputLog:
		tab := m.tabByID(tabID)
		if tab == nil || findPane(tab.root, paneID) == nil {
			return m, nil
		}
		m.activateWorkspaceTab(tabID)
		tab.activePaneID = paneID
		m.workspaceVisible = true
		return m, m.toggleActiveTerminalOutputLog()
	case menuToggleScriptRecording:
		tab := m.tabByID(tabID)
		if tab == nil {
			return m, nil
		}
		pane := findPane(tab.root, paneID)
		if pane == nil {
			return m, nil
		}
		m.activateWorkspaceTab(tabID)
		tab.activePaneID = paneID
		m.workspaceVisible = true
		m.toggleTerminalScriptRecording(pane)
		return m, nil
	case menuToggleAutoReconnect:
		tab := m.tabByID(tabID)
		if tab == nil || findPane(tab.root, paneID) == nil {
			return m, nil
		}
		m.activateWorkspaceTab(tabID)
		tab.activePaneID = paneID
		m.workspaceVisible = true
		return m, m.toggleActivePaneAutoReconnect()
	case menuViewTerminalLogs:
		tab := m.tabByID(tabID)
		if tab == nil || findPane(tab.root, paneID) == nil {
			return m, nil
		}
		m.activateWorkspaceTab(tabID)
		tab.activePaneID = paneID
		m.workspaceVisible = true
		return m, m.openTerminalLogViewer()
	case menuClosePane:
		tab := m.tabByID(tabID)
		if tab == nil || findPane(tab.root, paneID) == nil {
			return m, nil
		}
		m.activateWorkspaceTab(tabID)
		tab.activePaneID = paneID
		m.workspaceVisible = true
		return m, m.closeActivePane()
	case menuCloseTab:
		m.requestTabClose(tabID)
		return m, nil
	case menuActivateTab:
		for index, tab := range m.tabs {
			if tab.id == tabID {
				return m, tea.Batch(m.activateTabIndex(index)...)
			}
		}
	case menuShowHosts:
		m.workspaceVisible = false
		return m, nil
	}
	return m, nil
}

func (m *modelState) sendSavedTerminalPassword(tabID, paneID string) tea.Cmd {
	tab := m.tabByID(tabID)
	if tab == nil {
		return nil
	}
	pane := findPane(tab.root, paneID)
	if pane == nil || pane.session == nil || pane.host.Kind != model.HostKindSSH || pane.host.CredentialRef == "" || m.credentials == nil {
		return nil
	}

	m.activateWorkspaceTab(tabID)
	tab.activePaneID = paneID
	m.workspaceVisible = true
	pane.statusText, pane.statusErr = savedTerminalPasswordLoadingStatus, false
	vault, credentialRef := m.credentials, pane.host.CredentialRef
	return func() tea.Msg {
		secret, err := vault.Get(credentialRef)
		password := []byte(secret)
		secret = ""
		return savedTerminalPasswordLoadedMsg{
			tabID: tabID, paneID: paneID, credentialRef: credentialRef,
			password: password, err: err,
		}
	}
}

func (m *modelState) handleSavedTerminalPasswordLoaded(msg savedTerminalPasswordLoadedMsg) {
	defer clearSavedTerminalPassword(msg.password)
	tab := m.tabByID(msg.tabID)
	if tab == nil {
		return
	}
	pane := findPane(tab.root, msg.paneID)
	if pane == nil {
		return
	}
	if m.activeTabID != msg.tabID || tab.activePaneID != msg.paneID || !m.workspaceVisible || pane.session == nil ||
		pane.host.Kind != model.HostKindSSH || pane.host.CredentialRef != msg.credentialRef {
		if pane.statusText == savedTerminalPasswordLoadingStatus {
			pane.statusText, pane.statusErr = "", false
		}
		return
	}
	if msg.err != nil {
		pane.statusText = "读取已保存密码失败：" + msg.err.Error()
		pane.statusErr = true
		return
	}
	if len(msg.password) == 0 {
		pane.statusText, pane.statusErr = "已保存的密码为空", true
		return
	}
	if bytes.ContainsAny(msg.password, "\r\n\x00") {
		pane.statusText, pane.statusErr = "已保存的密码包含换行或空字符，已取消发送", true
		return
	}

	input := append(append([]byte(nil), msg.password...), '\r')
	defer clearSavedTerminalPassword(input)
	wireData, err := encodeTerminalInput(pane, input)
	if err != nil {
		pane.statusText, pane.statusErr = "编码已保存密码失败："+err.Error(), true
		return
	}
	defer clearSavedTerminalPassword(wireData)
	if err := pane.session.Write(wireData); err != nil {
		pane.statusText, pane.statusErr = "发送已保存密码失败："+err.Error(), true
		return
	}
	pane.statusText, pane.statusErr = "已向当前 pane 发送保存的密码", false
}

func clearSavedTerminalPassword(password []byte) {
	for index := range password {
		password[index] = 0
	}
}

func (m modelState) contextMenuLayout() (content string, x, y, width, height int) {
	items := m.contextMenuItems()
	if len(items) == 0 {
		return "", 0, 0, 0, 0
	}
	innerWidth := 0
	for _, item := range items {
		innerWidth = max(innerWidth, lipgloss.Width(item.label)+2)
	}
	innerWidth = clamp(innerWidth, 18, max(18, m.width-2))

	rows := make([]string, 0, len(items))
	for index, item := range items {
		label := fitText("  "+item.label, innerWidth)
		style := lipgloss.NewStyle().Width(innerWidth).Background(palette.canvas).Foreground(palette.secondary)
		if item.disabled {
			style = lipgloss.NewStyle().Width(innerWidth).Background(palette.canvas).Foreground(palette.muted)
		} else if index == m.contextMenu.selected {
			label = fitText("› "+item.label, innerWidth)
			style = selectedAccent.Width(innerWidth)
		}
		rows = append(rows, style.Render(label))
	}
	content = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(palette.accent).
		Background(palette.canvas).
		Render(strings.Join(rows, "\n"))
	width = lipgloss.Width(content)
	height = lipgloss.Height(content)
	if width < 1 || height < 1 || m.width < 1 || m.height < 1 {
		return "", 0, 0, 0, 0
	}
	width = min(width, m.width)
	height = min(height, m.height)
	x = clamp(m.contextMenu.x, 0, max(0, m.width-width))
	y = clamp(m.contextMenu.y, 0, max(0, m.height-height))
	return content, x, y, width, height
}

func overlayContextMenu(base string, menu string, x, y, width, height, screenWidth int) string {
	rows := strings.Split(base, "\n")
	menuRows := strings.Split(menu, "\n")
	for index := 0; index < height && index < len(menuRows); index++ {
		rowIndex := y + index
		if rowIndex < 0 || rowIndex >= len(rows) {
			continue
		}
		line := fitLine(rows[rowIndex], screenWidth)
		menuRow := ansi.Cut(menuRows[index], 0, width)
		left := ansi.Cut(line, 0, x)
		if leftWidth := ansi.StringWidth(left); leftWidth < x {
			left += strings.Repeat(" ", x-leftWidth)
		}
		right := ansi.Cut(line, x+width, screenWidth)
		rows[rowIndex] = left + menuRow + "\x1b[0m" + right
	}
	return strings.Join(rows, "\n")
}
