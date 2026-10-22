package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const mouseDoubleClickWindow = 450 * time.Millisecond

type workspaceMouseClick struct {
	paneID string
	row    int
	col    int
	at     time.Time
}

type terminalSelectionCandidate struct {
	row int
	col int
}

type paneResizeDragState struct {
	tabID      string
	root       *paneNode
	node       *paneNode
	parentRect paneRect
}

type paneMoveDragState struct {
	tabID        string
	root         *paneNode
	sourcePaneID string
	targetPaneID string
	startX       int
	startY       int
	dragging     bool
}

func paneDividerAt(node *paneNode, rect paneRect, x, y int) (*paneNode, paneRect) {
	if node == nil || node.pane != nil {
		return nil, paneRect{}
	}
	first, second := splitRects(rect, node.axis, node.ratio)
	if node.axis == splitVertical {
		if x == first.x+first.width && y >= rect.y && y < rect.y+rect.height {
			return node, rect
		}
	} else if y == first.y+first.height && x >= rect.x && x < rect.x+rect.width {
		return node, rect
	}
	if x >= first.x && x < first.x+first.width && y >= first.y && y < first.y+first.height {
		return paneDividerAt(node.first, first, x, y)
	}
	if x >= second.x && x < second.x+second.width && y >= second.y && y < second.y+second.height {
		return paneDividerAt(node.second, second, x, y)
	}
	return nil, paneRect{}
}

func setPaneSplitRatioFromPointer(node *paneNode, rect paneRect, x, y int) bool {
	if node == nil {
		return false
	}
	available, position, minimum := rect.width-1, x-rect.x, 8
	if node.axis == splitHorizontal {
		available, position, minimum = rect.height-1, y-rect.y, 4
	}
	if available < 2 {
		return false
	}
	minimum = min(minimum, available/2)
	position = clamp(position, minimum, available-minimum)
	ratio := clamp((position*100+available/2)/available, 1, 99)
	if ratio == node.ratio {
		return false
	}
	node.ratio = ratio
	return true
}

func paneNodeContainsNode(root, target *paneNode) bool {
	if root == nil || target == nil {
		return false
	}
	if root == target {
		return true
	}
	return paneNodeContainsNode(root.first, target) || paneNodeContainsNode(root.second, target)
}

type terminalMouseTracking struct {
	press  bool
	click  bool
	drag   bool
	motion bool
	sgr    bool
}

func (tracking terminalMouseTracking) enabled() bool {
	return tracking.press || tracking.click || tracking.drag || tracking.motion
}

func (tracking terminalMouseTracking) bubbleTeaMode() tea.MouseMode {
	if tracking.motion {
		return tea.MouseModeAllMotion
	}
	if tracking.enabled() {
		return tea.MouseModeCellMotion
	}
	return tea.MouseModeNone
}

func terminalMouseBytes(msg tea.MouseMsg, x, y int, tracking terminalMouseTracking) []byte {
	mouse := msg.Mouse()
	motion, release := false, false

	switch msg.(type) {
	case tea.MouseClickMsg:
		if !tracking.enabled() {
			return nil
		}
	case tea.MouseReleaseMsg:
		if !tracking.click && !tracking.drag && !tracking.motion {
			return nil
		}
		release = true
	case tea.MouseWheelMsg:
		if !tracking.enabled() {
			return nil
		}
	case tea.MouseMotionMsg:
		motion = true
		if !tracking.motion && (!tracking.drag || mouse.Button == tea.MouseNone) {
			return nil
		}
	default:
		return nil
	}

	mouse.X, mouse.Y = x, y
	button := ansi.EncodeMouseButton(
		mouse.Button,
		motion,
		mouse.Mod.Contains(tea.ModShift),
		mouse.Mod.Contains(tea.ModAlt),
		mouse.Mod.Contains(tea.ModCtrl),
	)
	if tracking.sgr {
		return []byte(ansi.MouseSgr(button, x, y, release))
	}
	if release {
		button = ansi.EncodeMouseButton(tea.MouseNone, false,
			mouse.Mod.Contains(tea.ModShift),
			mouse.Mod.Contains(tea.ModAlt),
			mouse.Mod.Contains(tea.ModCtrl))
	}
	return []byte(ansi.MouseX10(button, x, y))
}

func (m *modelState) updateMouse(msg tea.MouseMsg) tea.Cmd {
	if m.connectingHost != nil || m.passwordPrompt || m.ftpPasswordPrompt || m.keyboardAuthPrompt != nil || m.trustPrompt != nil || m.hostKeyChangePrompt != nil || m.trustingHost || m.hostPicker != nil {
		return nil
	}
	if m.contextMenu != nil {
		return m.updateContextMenuMouse(msg)
	}
	if m.sftp != nil && m.workspaceVisible && len(m.tabs) > 0 && msg.Mouse().Y == 0 {
		if click, ok := msg.(tea.MouseClickMsg); ok {
			if index := m.workspaceTabAt(msg.Mouse().X); index >= 0 {
				if click.Button == tea.MouseLeft {
					return tea.Batch(m.activateTabBarIndex(index)...)
				}
				if click.Button == tea.MouseRight && index > 0 {
					m.openTabContextMenu(m.tabs[index-1].id, msg.Mouse().X, msg.Mouse().Y)
				}
			}
		}
		return nil
	}
	if m.sftp != nil && (m.workspaceVisible || len(m.tabs) == 0) {
		return m.updateSFTPMouse(msg)
	}
	if m.workspaceVisible {
		return m.updateWorkspaceMouse(msg)
	}
	mouse := msg.Mouse()
	if mouse.Y == 0 {
		if click, ok := msg.(tea.MouseClickMsg); ok {
			if index := m.workspaceTabAt(mouse.X); index >= 0 {
				if click.Button == tea.MouseLeft {
					return tea.Batch(m.activateTabBarIndex(index)...)
				}
				if click.Button == tea.MouseRight && index > 0 {
					m.openTabContextMenu(m.tabs[index-1].id, mouse.X, mouse.Y)
				}
			}
		}
		return nil
	}
	// The fixed tab bar is one row above the home dashboard; keep the
	// dashboard's existing mouse hit regions unchanged.
	mouse.Y--
	if m.form != nil || m.showHelp || m.searching || m.loading || m.connectingHost != nil {
		return nil
	}

	activePane, insidePane := m.mousePaneAt(mouse.X)
	if !insidePane {
		return nil
	}
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		if activePane == groupPane && mouse.Y >= 6 {
			m.activePane = groupPane
			switch wheel.Button {
			case tea.MouseWheelUp:
				m.moveSelection(-3)
			case tea.MouseWheelDown:
				m.moveSelection(3)
			}
		} else if activePane == hostPane && mouse.Y >= 7 {
			m.activePane = hostPane
			switch wheel.Button {
			case tea.MouseWheelUp:
				m.moveSelection(-3)
			case tea.MouseWheelDown:
				m.moveSelection(3)
			}
		}
		return nil
	}
	if _, ok := msg.(tea.MouseClickMsg); !ok || mouse.Button != tea.MouseLeft {
		return nil
	}

	if activePane == groupPane {
		entries := m.groupEntries()
		index := m.groupTop + mouse.Y - 6
		if mouse.Y < 6 || index < 0 || index >= len(entries) {
			return nil
		}
		m.activePane = groupPane
		m.groupIndex = index
		m.groupTop = keepVisible(m.groupIndex, m.groupTop, m.listRows())
		m.hostIndex, m.hostTop = 0, 0
		m.ensureHostSelection()
		m.lastMouseHostID = ""
		return nil
	}

	if mouse.Y == 4 {
		m.activePane = hostPane
		m.searching = true
		m.query = ""
		m.hostIndex, m.hostTop = 0, 0
		return nil
	}
	if mouse.Y < 7 {
		return nil
	}
	hosts := m.visibleHosts()
	index := m.hostTop + mouse.Y - 7
	if index < 0 || index >= len(hosts) {
		return nil
	}
	m.activePane = hostPane
	m.hostIndex = index
	m.hostTop = keepVisible(m.hostIndex, m.hostTop, m.hostRows())
	host := hosts[index]
	now := time.Now()
	if m.lastMouseHostID == host.Id && now.Sub(m.lastMouseClickAt) <= mouseDoubleClickWindow {
		m.lastMouseHostID = ""
		return m.startHostConnect(host, m.newTabTarget())
	}
	m.lastMouseHostID = host.Id
	m.lastMouseClickAt = now
	return nil
}

func (m *modelState) updateWorkspaceMouse(msg tea.MouseMsg) tea.Cmd {
	if m.contextMenu != nil {
		return m.updateContextMenuMouse(msg)
	}
	tab := m.activeTab()
	if tab == nil {
		return nil
	}
	mouse := msg.Mouse()
	if drag := m.paneMoveDrag; drag != nil {
		switch msg.(type) {
		case tea.MouseReleaseMsg:
			m.paneMoveDrag = nil
			if !drag.dragging || drag.tabID != tab.id || drag.root != tab.root {
				return nil
			}
			rootRect := paneRect{width: m.width, height: max(1, m.height-2)}
			targetPane, _ := paneAt(tab.root, rootRect, mouse.X, mouse.Y-1)
			if targetPane == nil || targetPane.id == drag.sourcePaneID || !swapPanePositions(tab.root, drag.sourcePaneID, targetPane.id) {
				return nil
			}
			tab.activePaneID = drag.sourcePaneID
			m.statusText = "已交换 pane 位置"
			m.statusErr = false
			return tea.Batch(m.resizeActiveWorkspace()...)
		case tea.MouseMotionMsg:
			if mouse.Button != tea.MouseLeft || drag.tabID != tab.id || drag.root != tab.root || findPane(tab.root, drag.sourcePaneID) == nil {
				m.paneMoveDrag = nil
				return nil
			}
			if mouse.X != drag.startX || mouse.Y != drag.startY {
				drag.dragging = true
			}
			if drag.dragging {
				rootRect := paneRect{width: m.width, height: max(1, m.height-2)}
				targetPane, _ := paneAt(tab.root, rootRect, mouse.X, mouse.Y-1)
				if targetPane != nil && targetPane.id != drag.sourcePaneID {
					drag.targetPaneID = targetPane.id
				} else {
					drag.targetPaneID = ""
				}
			}
			return nil
		case tea.MouseClickMsg, tea.MouseWheelMsg:
			m.paneMoveDrag = nil
		}
	}
	if drag := m.paneResizeDrag; drag != nil {
		switch msg.(type) {
		case tea.MouseReleaseMsg:
			m.paneResizeDrag = nil
			return nil
		case tea.MouseMotionMsg:
			if mouse.Button != tea.MouseLeft {
				m.paneResizeDrag = nil
				return nil
			}
			if drag.tabID != tab.id || drag.root != tab.root || !paneNodeContainsNode(tab.root, drag.node) {
				m.paneResizeDrag = nil
				return nil
			}
			if setPaneSplitRatioFromPointer(drag.node, drag.parentRect, mouse.X, mouse.Y-1) {
				return tea.Batch(m.resizeActiveWorkspace()...)
			}
			return nil
		case tea.MouseClickMsg, tea.MouseWheelMsg:
			m.paneResizeDrag = nil
		}
	}
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft {
		rootRect := paneRect{width: m.width, height: max(1, m.height-2)}
		if node, parentRect := paneDividerAt(tab.root, rootRect, mouse.X, mouse.Y-1); node != nil {
			m.paneResizeDrag = &paneResizeDragState{tabID: tab.id, root: tab.root, node: node, parentRect: parentRect}
			return nil
		}
	}
	activePane := findPane(tab.root, tab.activePaneID)
	if activePane != nil && activePane.selection != nil && activePane.selection.dragging {
		switch msg.(type) {
		case tea.MouseMotionMsg, tea.MouseReleaseMsg:
			rootRect := paneRect{width: m.width, height: max(1, m.height-2)}
			if rect, ok := paneRectFor(tab.root, activePane.id, rootRect); ok {
				edges, _ := paneFrameEdgesFor(tab.root, activePane.id)
				contentRect := terminalPaneContentRectWithEdges(rect, edges)
				bodyRow := mouse.Y - contentRect.y - 2 // tab strip + pane title row
				bodyCol := mouse.X - contentRect.x + activePane.horizontalOffset
				activePane.selection.setFocus(bodyRow, bodyCol)
			}
			if _, released := msg.(tea.MouseReleaseMsg); released {
				activePane.selection.dragging = false
			}
			return nil
		}
	}
	if release, ok := msg.(tea.MouseReleaseMsg); ok && activePane != nil && activePane.selectionCandidate != nil {
		activePane.selectionCandidate = nil
		if release.Button == tea.MouseLeft {
			return nil
		}
	}
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseRight {
		if mouse.Y == 0 {
			if index := m.workspaceTabAt(mouse.X); index >= 0 {
				if index > 0 {
					m.openTabContextMenu(m.tabs[index-1].id, mouse.X, mouse.Y)
				}
			}
			return nil
		}
		rootRect := paneRect{width: m.width, height: max(1, m.height-2)}
		if targetPane, _ := paneAt(tab.root, rootRect, mouse.X, mouse.Y-1); targetPane != nil {
			tab.activePaneID = targetPane.id
			m.openPaneContextMenu(tab.id, targetPane.id, mouse.X, mouse.Y)
		}
		return nil
	}
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft && mouse.Y == 0 {
		if index := m.workspaceTabAt(mouse.X); index >= 0 {
			return tea.Batch(m.activateTabBarIndex(index)...)
		}
		return nil
	}
	rootRect := paneRect{width: m.width, height: max(1, m.height-2)}
	targetPane, rect := paneAt(tab.root, rootRect, mouse.X, mouse.Y-1)
	if targetPane == nil {
		return nil
	}
	edges, _ := paneFrameEdgesFor(tab.root, targetPane.id)
	contentRect := terminalPaneContentRectWithEdges(rect, edges)
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft && countPanes(tab.root) > 1 && mouse.Y == contentRect.y+1 {
		m.paneMoveDrag = &paneMoveDragState{
			tabID: tab.id, root: tab.root, sourcePaneID: targetPane.id, startX: mouse.X, startY: mouse.Y,
		}
	}
	terminalY := mouse.Y - contentRect.y - 2 // tab strip + pane title row
	insideTerminal := mouse.X >= contentRect.x && mouse.X < contentRect.x+contentRect.width && terminalY >= 0 && terminalY < contentRect.height-1
	terminalCol := mouse.X - contentRect.x + targetPane.horizontalOffset
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft &&
		(mouse.Mod.Contains(tea.ModSuper) || mouse.Mod.Contains(tea.ModMeta)) {
		if insideTerminal && targetPane.terminal != nil {
			if link := terminalLinkAt(targetPane, terminalY+targetPane.verticalOffset, terminalCol); link != "" {
				return m.openTerminalLink(link)
			}
		}
		return nil
	}
	if motion, ok := msg.(tea.MouseMotionMsg); ok && motion.Button == tea.MouseLeft && targetPane.selectionCandidate != nil && !targetPane.mouse.enabled() && targetPane.terminal != nil {
		candidate := *targetPane.selectionCandidate
		bodyRow := clamp(terminalY, 0, max(0, contentRect.height-2))
		bodyCol := clamp(terminalCol, 0, max(0, targetPane.terminal.Width()-1))
		if insideTerminal && (bodyRow != candidate.row || bodyCol != candidate.col) && targetPane.session != nil {
			targetPane.selectionCandidate = nil
			var commands []tea.Cmd
			if targetPane.id != tab.activePaneID {
				tab.activePaneID = targetPane.id
				commands = m.resizeActiveWorkspace()
			}
			if beginTerminalSelection(targetPane, max(1, contentRect.height-1), candidate.row, candidate.col, false) {
				targetPane.selection.anchor = terminalSelectionPoint{row: candidate.row, col: candidate.col}
				targetPane.selection.setFocus(bodyRow, bodyCol)
				return tea.Batch(commands...)
			}
		}
	}
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft {
		canSelectWord := !targetPane.mouse.enabled() || mouse.Mod.Contains(tea.ModShift)
		if insideTerminal && canSelectWord && targetPane.session != nil && targetPane.terminal != nil && targetPane.search == nil {
			now := time.Now()
			previous := m.lastWorkspaceClick
			doubleClick := previous.paneID == targetPane.id && previous.row == terminalY && previous.col == terminalCol &&
				!previous.at.IsZero() && now.Sub(previous.at) <= mouseDoubleClickWindow
			m.lastWorkspaceClick = workspaceMouseClick{paneID: targetPane.id, row: terminalY, col: terminalCol, at: now}
			if doubleClick {
				m.lastWorkspaceClick = workspaceMouseClick{}
				targetPane.selectionCandidate = nil
				var commands []tea.Cmd
				if targetPane.id != tab.activePaneID {
					tab.activePaneID = targetPane.id
					commands = m.resizeActiveWorkspace()
				}
				if selectTerminalWord(targetPane, max(1, contentRect.height-1), terminalY, terminalCol) {
					return tea.Batch(commands...)
				}
			} else if !targetPane.mouse.enabled() {
				targetPane.selectionCandidate = &terminalSelectionCandidate{row: terminalY, col: terminalCol}
			}
		} else {
			m.lastWorkspaceClick = workspaceMouseClick{}
		}
	}
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft && mouse.Mod.Contains(tea.ModShift) {
		if targetPane.search != nil || targetPane.session == nil || targetPane.terminal == nil {
			return nil
		}
		terminalY := mouse.Y - contentRect.y - 2 // tab strip + pane title row
		if mouse.X < contentRect.x || mouse.X >= contentRect.x+contentRect.width || terminalY < 0 || terminalY >= contentRect.height-1 {
			return nil
		}
		var commands []tea.Cmd
		if targetPane.id != tab.activePaneID {
			tab.activePaneID = targetPane.id
			commands = m.resizeActiveWorkspace()
		}
		targetPane.selectionCandidate = nil
		if beginTerminalSelection(targetPane, max(1, contentRect.height-1), terminalY, terminalCol, mouse.Mod.Contains(tea.ModAlt)) {
			return tea.Batch(commands...)
		}
	}
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft && targetPane.selection != nil {
		targetPane.selection = nil
	}
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		targetPane.selection = nil
		if targetPane.id != tab.activePaneID {
			tab.activePaneID = targetPane.id
			commands := m.resizeActiveWorkspace()
			if !targetPane.scrollbackViewing && targetPane.mouse.enabled() && !mouse.Mod.Contains(tea.ModShift) {
				return tea.Batch(commands...)
			}
			if wheel.Button == tea.MouseWheelUp {
				scrollTerminalPane(targetPane, 1, 3, max(1, contentRect.height-1))
			} else if wheel.Button == tea.MouseWheelDown {
				scrollTerminalPane(targetPane, -1, 3, max(1, contentRect.height-1))
			}
			return tea.Batch(commands...)
		}
		if targetPane.scrollbackViewing || !targetPane.mouse.enabled() || mouse.Mod.Contains(tea.ModShift) {
			switch wheel.Button {
			case tea.MouseWheelUp:
				scrollTerminalPane(targetPane, 1, 3, max(1, contentRect.height-1))
			case tea.MouseWheelDown:
				scrollTerminalPane(targetPane, -1, 3, max(1, contentRect.height-1))
			}
			return nil
		}
	}
	if targetPane.id != tab.activePaneID {
		tab.activePaneID = targetPane.id
		return tea.Batch(m.resizeActiveWorkspace()...)
	}
	if mouse.Y <= contentRect.y+1 || targetPane.session == nil || !targetPane.mouse.enabled() || targetPane.scrollbackViewing {
		return nil
	}
	terminalY = mouse.Y - contentRect.y - 2 // tab strip + pane title row
	if mouse.X < contentRect.x || mouse.X >= contentRect.x+contentRect.width || terminalY < 0 || terminalY >= contentRect.height-1 {
		return nil
	}
	data := terminalMouseBytes(msg, mouse.X-contentRect.x+targetPane.horizontalOffset, terminalY+targetPane.verticalOffset, targetPane.mouse)
	if len(data) == 0 {
		return nil
	}
	if err := targetPane.session.Write(data); err != nil {
		targetPane.statusText = "发送鼠标事件失败：" + err.Error()
		targetPane.statusErr = true
	}
	return nil
}

func (m modelState) mousePaneAt(x int) (pane, bool) {
	if x < 2 || x >= m.width-2 {
		return 0, false
	}
	innerWidth := m.width - 4
	if innerWidth < 66 {
		return m.activePane, true
	}
	groupWidth := 22
	if innerWidth >= 90 {
		groupWidth = 28
	}
	groupStart := 2
	groupEnd := groupStart + groupWidth
	if x >= groupStart && x < groupEnd {
		return groupPane, true
	}
	if x > groupEnd {
		return hostPane, true
	}
	return 0, false
}
