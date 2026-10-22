package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/infra/terminalog"
	"tui-ssh-workbench/internal/model"
)

type terminalSession interface {
	Read([]byte) (int, error)
	Write([]byte) error
	Resize(width, height int) error
	Close() error
}

// sshSessionFromTerminalSession returns the SSH transport behind the UI's
// terminal wrappers. Connected SSH panes are wrapped for traffic accounting,
// while file management and port forwarding need the underlying SSH session.
func sshSessionFromTerminalSession(session terminalSession) (*sshclient.Session, bool) {
	switch value := session.(type) {
	case *sshclient.Session:
		return value, value != nil
	case *trackedTerminalSession:
		if value == nil {
			return nil, false
		}
		return sshSessionFromTerminalSession(value.terminalSession)
	default:
		return nil, false
	}
}

type splitAxis uint8

const (
	splitVertical   splitAxis = iota // panes are side by side
	splitHorizontal                  // panes are stacked
)

type paneFrameEdges struct {
	top    bool
	right  bool
	bottom bool
	left   bool
}

var fullPaneFrameEdges = paneFrameEdges{top: true, right: true, bottom: true, left: true}

func splitPaneFrameEdges(edges paneFrameEdges, axis splitAxis, first bool) paneFrameEdges {
	if axis == splitVertical {
		if first {
			edges.right = false
		} else {
			edges.left = false
		}
		return edges
	}
	if first {
		edges.bottom = false
	} else {
		edges.top = false
	}
	return edges
}

type connectionTargetKind uint8

const (
	newTabTarget connectionTargetKind = iota
	splitPaneTarget
	standaloneSFTPTarget
	reconnectPaneTarget
	sftpPeerTarget
)

type connectionTarget struct {
	kind                connectionTargetKind
	sftpSide            sftpSide
	tabID               string
	groupName           string
	parentPaneID        string
	paneID              string
	axis                splitAxis
	width               int
	height              int
	sftpGeneration      uint64
	sftpPaneTabID       uint64
	reconnectGeneration uint64
	layoutRestoreID     uint64
}

type terminalPane struct {
	id                            string
	host                          model.Host
	session                       terminalSession
	traffic                       *terminalTraffic
	terminal                      *vt.SafeEmulator
	terminalResponseReaderStarted bool
	applicationCursor             bool
	bracketedPaste                bool
	vtTail                        string
	zmodemProbe                   []byte
	mouse                         terminalMouseTracking
	statusText                    string
	statusErr                     bool
	bellUntil                     time.Time
	terminalEncoding              string
	terminalInputEncoding         string
	terminalDecodePending         []byte
	terminalEncodingSaving        bool
	horizontalOffset              int
	verticalOffset                int
	startupPending                string
	startupScript                 *startupScriptRuntime
	triggerRuntime                *terminalTriggerRuntime
	triggerScriptLoading          bool
	triggerScriptOutput           []byte
	scriptRecording               *terminalScriptRecording
	scrollbackViewing             bool
	scrollbackOffset              int
	search                        *terminalSearchState
	selection                     *terminalSelection
	selectionCandidate            *terminalSelectionCandidate
	commandHistory                []string
	commandInput                  []rune
	commandInputCursor            int
	commandInputInvalid           bool
	commandInputLastCR            bool
	commandEchoVersion            uint64
	commandEchoPrompt             string
	commandEchoBuffer             string
	commandEchoLine               []rune
	commandEchoCursor             int
	pendingCommands               []pendingTerminalCommand
	outputLogger                  *terminalog.Logger
	outputLogClosing              bool
	autoReconnect                 bool
	reconnectAttempts             int
	reconnectGeneration           uint64
	forwards                      []*sshclient.LocalForward
}

const maxAutoReconnectAttempts = 5

var autoReconnectDelays = [...]time.Duration{
	time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
}

type autoReconnectMsg struct {
	tabID      string
	paneID     string
	generation uint64
}

type workspaceTab struct {
	id           string
	groupName    string
	root         *paneNode
	activePaneID string
	syncInput    bool
	sftp         *sftpBrowserState
}

type paneNode struct {
	axis   splitAxis
	ratio  int
	first  *paneNode
	second *paneNode
	pane   *terminalPane
}

type paneRect struct {
	x, y          int
	width, height int
}

func (m *modelState) activeTab() *workspaceTab {
	for _, tab := range m.tabs {
		if tab.id == m.activeTabID {
			return tab
		}
	}
	return nil
}

func (m *modelState) activateWorkspaceTab(id string) bool {
	for _, tab := range m.tabs {
		if tab != nil && tab.id == id {
			if m.activeTabID != id {
				m.paneResizeDrag = nil
				m.paneMoveDrag = nil
			}
			m.activeTabID = id
			m.sftp = tab.sftp
			return true
		}
	}
	return false
}

func (m modelState) activeSFTPState() *sftpBrowserState {
	tab := m.activeTab()
	if tab == nil {
		return nil
	}
	return tab.sftp
}

func (m *modelState) activeTerminalPane() *terminalPane {
	tab := m.activeTab()
	if tab == nil {
		return nil
	}
	return findPane(tab.root, tab.activePaneID)
}

// writeWorkspaceInput sends terminal input to the active pane, or to every
// connected pane in the active tab while synchronized input is enabled.
func (m *modelState) writeWorkspaceInput(dataForPane func(*terminalPane) []byte, errorPrefix string) int {
	tab := m.activeTab()
	activePane := m.activeTerminalPane()
	if tab == nil || activePane == nil || activePane.session == nil {
		return 0
	}

	destinations := []*terminalPane{activePane}
	if tab.syncInput {
		destinations = collectPanes(tab.root)
	}
	writeErrors := 0
	for _, pane := range destinations {
		if pane == nil || pane.session == nil {
			continue
		}
		data := dataForPane(pane)
		if len(data) == 0 {
			continue
		}
		wireData, err := encodeTerminalInput(pane, data)
		if err != nil {
			pane.statusText = errorPrefix + err.Error()
			pane.statusErr = true
			writeErrors++
			continue
		}
		if err := pane.session.Write(wireData); err != nil {
			pane.statusText = errorPrefix + err.Error()
			pane.statusErr = true
			writeErrors++
		} else {
			captureTerminalCommandInput(pane, data)
		}
	}
	return writeErrors
}

func (m *modelState) toggleWorkspaceSyncInput() {
	tab := m.activeTab()
	if tab == nil {
		return
	}
	if tab.syncInput {
		tab.syncInput = false
		m.statusText = "已关闭当前标签页的同步输入"
		m.statusErr = false
		return
	}
	connected := connectedPaneCount(tab)
	if connected < 2 {
		m.statusText = "同步输入需要当前标签页至少有两个已连接 pane"
		m.statusErr = true
		return
	}
	tab.syncInput = true
	m.statusText = fmt.Sprintf("已开启同步输入：当前标签页的 %d 个已连接 pane 将接收键盘输入和粘贴", connected)
	m.statusErr = false
}

func connectedPaneCount(tab *workspaceTab) int {
	if tab == nil {
		return 0
	}
	connected := 0
	for _, pane := range collectPanes(tab.root) {
		if pane != nil && pane.session != nil {
			connected++
		}
	}
	return connected
}

func findPane(node *paneNode, id string) *terminalPane {
	if node == nil {
		return nil
	}
	if node.pane != nil {
		if node.pane.id == id {
			return node.pane
		}
		return nil
	}
	if pane := findPane(node.first, id); pane != nil {
		return pane
	}
	return findPane(node.second, id)
}

func paneNodeForID(node *paneNode, id string) *paneNode {
	if node == nil {
		return nil
	}
	if node.pane != nil {
		if node.pane.id == id {
			return node
		}
		return nil
	}
	if match := paneNodeForID(node.first, id); match != nil {
		return match
	}
	return paneNodeForID(node.second, id)
}

func swapPanePositions(root *paneNode, firstID, secondID string) bool {
	if firstID == secondID {
		return false
	}
	first, second := paneNodeForID(root, firstID), paneNodeForID(root, secondID)
	if first == nil || second == nil {
		return false
	}
	first.pane, second.pane = second.pane, first.pane
	return true
}

func collectPanes(node *paneNode) []*terminalPane {
	if node == nil {
		return nil
	}
	if node.pane != nil {
		return []*terminalPane{node.pane}
	}
	return append(collectPanes(node.first), collectPanes(node.second)...)
}

func countPanes(node *paneNode) int {
	if node == nil {
		return 0
	}
	if node.pane != nil {
		return 1
	}
	return countPanes(node.first) + countPanes(node.second)
}

func insertSplit(node *paneNode, targetID string, added *terminalPane, axis splitAxis) bool {
	if node == nil {
		return false
	}
	if node.pane != nil {
		if node.pane.id != targetID {
			return false
		}
		original := node.pane
		*node = paneNode{
			axis:   axis,
			ratio:  50,
			first:  &paneNode{pane: original},
			second: &paneNode{pane: added},
		}
		return true
	}
	return insertSplit(node.first, targetID, added, axis) || insertSplit(node.second, targetID, added, axis)
}

func removePane(node **paneNode, id string) bool {
	if node == nil || *node == nil {
		return false
	}
	current := *node
	if current.pane != nil {
		if current.pane.id == id {
			*node = nil
			return true
		}
		return false
	}
	if removePane(&current.first, id) || removePane(&current.second, id) {
		switch {
		case current.first == nil:
			*node = current.second
		case current.second == nil:
			*node = current.first
		}
		return true
	}
	return false
}

func splitRects(rect paneRect, axis splitAxis, ratio int) (paneRect, paneRect) {
	ratio = clamp(ratio, 1, 99)
	if axis == splitVertical {
		if rect.width < 3 {
			firstWidth := rect.width / 2
			return paneRect{x: rect.x, y: rect.y, width: firstWidth, height: rect.height},
				paneRect{x: rect.x + firstWidth + 1, y: rect.y, width: max(0, rect.width-firstWidth-1), height: rect.height}
		}
		available := rect.width - 1
		firstWidth := clamp(available*ratio/100, 1, available-1)
		return paneRect{x: rect.x, y: rect.y, width: firstWidth, height: rect.height},
			paneRect{x: rect.x + firstWidth + 1, y: rect.y, width: available - firstWidth, height: rect.height}
	}
	if rect.height < 3 {
		firstHeight := rect.height / 2
		return paneRect{x: rect.x, y: rect.y, width: rect.width, height: firstHeight},
			paneRect{x: rect.x, y: rect.y + firstHeight + 1, width: rect.width, height: max(0, rect.height-firstHeight-1)}
	}
	available := rect.height - 1
	firstHeight := clamp(available*ratio/100, 1, available-1)
	return paneRect{x: rect.x, y: rect.y, width: rect.width, height: firstHeight},
		paneRect{x: rect.x, y: rect.y + firstHeight + 1, width: rect.width, height: available - firstHeight}
}

func paneRectFor(node *paneNode, id string, rect paneRect) (paneRect, bool) {
	if node == nil {
		return paneRect{}, false
	}
	if node.pane != nil {
		return rect, node.pane.id == id
	}
	firstRect, secondRect := splitRects(rect, node.axis, node.ratio)
	if result, ok := paneRectFor(node.first, id, firstRect); ok {
		return result, true
	}
	return paneRectFor(node.second, id, secondRect)
}

func terminalPaneContentRect(rect paneRect) paneRect {
	return terminalPaneContentRectWithEdges(rect, fullPaneFrameEdges)
}

func terminalPaneContentRectWithEdges(rect paneRect, edges paneFrameEdges) paneRect {
	if rect.width < 3 || rect.height < 3 {
		return rect
	}
	left, right := edgeCell(edges.left), edgeCell(edges.right)
	top, bottom := edgeCell(edges.top), edgeCell(edges.bottom)
	return paneRect{
		x: rect.x + left, y: rect.y + top,
		width: max(0, rect.width-left-right), height: max(0, rect.height-top-bottom),
	}
}

func edgeCell(enabled bool) int {
	if enabled {
		return 1
	}
	return 0
}

func paneFrameEdgesFor(node *paneNode, paneID string) (paneFrameEdges, bool) {
	return paneFrameEdgesForNode(node, paneID, fullPaneFrameEdges)
}

func paneFrameEdgesForNode(node *paneNode, paneID string, edges paneFrameEdges) (paneFrameEdges, bool) {
	if node == nil {
		return paneFrameEdges{}, false
	}
	if node.pane != nil {
		return edges, node.pane.id == paneID
	}
	if found, ok := paneFrameEdgesForNode(node.first, paneID, splitPaneFrameEdges(edges, node.axis, true)); ok {
		return found, true
	}
	return paneFrameEdgesForNode(node.second, paneID, splitPaneFrameEdges(edges, node.axis, false))
}

func terminalPaneViewportSize(rect paneRect) (int, int) {
	return terminalPaneViewportSizeWithEdges(rect, fullPaneFrameEdges)
}

func terminalPaneViewportSizeWithEdges(rect paneRect, edges paneFrameEdges) (int, int) {
	content := terminalPaneContentRectWithEdges(rect, edges)
	return max(1, content.width), max(1, content.height-1)
}

func paneAt(node *paneNode, rect paneRect, x, y int) (*terminalPane, paneRect) {
	if node == nil {
		return nil, paneRect{}
	}
	if node.pane != nil {
		return node.pane, rect
	}
	firstRect, secondRect := splitRects(rect, node.axis, node.ratio)
	if x >= firstRect.x && x < firstRect.x+firstRect.width && y >= firstRect.y && y < firstRect.y+firstRect.height {
		return paneAt(node.first, firstRect, x, y)
	}
	if x >= secondRect.x && x < secondRect.x+secondRect.width && y >= secondRect.y && y < secondRect.y+secondRect.height {
		return paneAt(node.second, secondRect, x, y)
	}
	return nil, paneRect{}
}

func (m modelState) renderWorkspaceNode(tab *workspaceTab, node *paneNode, rect paneRect) string {
	return m.renderWorkspaceNodeWithEdges(tab, node, rect, fullPaneFrameEdges)
}

func (m modelState) renderWorkspaceNodeWithEdges(tab *workspaceTab, node *paneNode, rect paneRect, edges paneFrameEdges) string {
	if node == nil || rect.width < 1 || rect.height < 1 {
		return strings.Repeat("\n", max(0, rect.height-1))
	}
	if node.pane != nil {
		return m.renderTerminalPaneWithEdges(node.pane, rect, edges)
	}
	firstRect, secondRect := splitRects(rect, node.axis, node.ratio)
	first := m.renderWorkspaceNodeWithEdges(tab, node.first, firstRect, splitPaneFrameEdges(edges, node.axis, true))
	second := m.renderWorkspaceNodeWithEdges(tab, node.second, secondRect, splitPaneFrameEdges(edges, node.axis, false))
	if node.axis == splitVertical {
		return joinFixedColumns(rect.height,
			fixedColumn{content: first, width: firstRect.width},
			fixedColumn{content: m.workspaceDivider(rect.height, true, paneNodeContains(node, tab.activePaneID)), width: 1},
			fixedColumn{content: second, width: secondRect.width},
		)
	}
	rows := make([]string, 0, rect.height)
	firstRows := strings.Split(first, "\n")
	secondRows := strings.Split(second, "\n")
	for index := 0; index < firstRect.height; index++ {
		line := ""
		if index < len(firstRows) {
			line = firstRows[index]
		}
		rows = append(rows, fitLine(line, rect.width))
	}
	rows = append(rows, m.workspaceDivider(rect.width, false, paneNodeContains(node, tab.activePaneID)))
	for index := 0; index < secondRect.height; index++ {
		line := ""
		if index < len(secondRows) {
			line = secondRows[index]
		}
		rows = append(rows, fitLine(line, rect.width))
	}
	return strings.Join(rows, "\n")
}

func (m modelState) renderTerminalPane(pane *terminalPane, rect paneRect) string {
	return m.renderTerminalPaneWithEdges(pane, rect, fullPaneFrameEdges)
}

func (m modelState) renderTerminalPaneWithEdges(pane *terminalPane, rect paneRect, edges paneFrameEdges) string {
	if pane == nil {
		return strings.Repeat("\n", max(0, rect.height-1))
	}
	contentRect := terminalPaneContentRectWithEdges(rect, edges)
	title := pane.host.Name + " · " + hostConnectionSummary(pane.host) + " · " + terminalEncodingSummary(pane)
	if pane.scriptRecording != nil {
		title += fmt.Sprintf(" · REC %d", len(pane.scriptRecording.steps))
	}
	if traffic := terminalTrafficSummary(pane.traffic); traffic != "" {
		title += " · " + traffic
	}
	header := mutedStyle.Render("◆ " + title)
	focused := false
	if tab := m.activeTab(); tab != nil && tab.activePaneID == pane.id {
		focused = true
		header = focusBadgeStyle.Background(palette.selected).Width(contentRect.width).Render(fitText("▶ "+title, contentRect.width))
	}
	rows := []string{fitLine(header, contentRect.width)}
	bodyHeight := max(0, contentRect.height-1)
	if pane.session == nil || pane.terminal == nil {
		message := pane.statusText
		if message == "" {
			message = "终端会话已断开"
		}
		for index := 0; index < bodyHeight; index++ {
			line := ""
			if index == bodyHeight/2 {
				line = lipgloss.Place(contentRect.width, 1, lipgloss.Center, lipgloss.Center, warningStyle.Render(message))
			}
			rows = append(rows, fitLine(line, contentRect.width))
		}
		return frameTerminalPaneWithEdges(strings.Join(rows, "\n"), rect, focused, edges)
	}
	terminalRows := renderTerminalRowsWithLinks(pane)
	if pane.selection != nil {
		terminalRows = pane.selection.RenderRows()
	} else if pane.scrollbackViewing {
		terminalRows = renderTerminalScrollback(pane, bodyHeight)
	} else {
		start := clamp(pane.verticalOffset, 0, max(0, len(terminalRows)-bodyHeight))
		terminalRows = terminalRows[start:min(len(terminalRows), start+bodyHeight)]
	}
	visibleRowStart := terminalVisibleRowStart(pane, bodyHeight)
	totalTerminalRows := pane.terminal.ScrollbackLen() + pane.terminal.Height()
	highlightRow := terminalSearchVisibleRow(pane, bodyHeight)
	for index := 0; index < bodyHeight; index++ {
		line := ""
		if index < len(terminalRows) {
			line = terminalRows[index]
		}
		if pane.selection == nil && len(m.terminalHighlights) > 0 && index < len(terminalRows) && visibleRowStart+index < totalTerminalRows {
			line = renderTerminalHighlightedLine(pane, visibleRowStart+index, m.terminalHighlights)
		}
		if index == highlightRow {
			if pane.search != nil && pane.search.index >= 0 && pane.search.index < len(pane.search.matches) {
				line = renderTerminalSearchMatchLine(pane, pane.search.matches[pane.search.index])
			}
		}
		line = ansi.Cut(line, pane.horizontalOffset, pane.horizontalOffset+contentRect.width)
		line = fitLine(line, contentRect.width)
		rows = append(rows, line)
	}
	return frameTerminalPaneWithEdges(strings.Join(rows, "\n"), rect, focused, edges)
}

func frameTerminalPane(content string, rect paneRect, focused bool) string {
	return frameTerminalPaneWithEdges(content, rect, focused, fullPaneFrameEdges)
}

func frameTerminalPaneWithEdges(content string, rect paneRect, focused bool, edges paneFrameEdges) string {
	if rect.width < 3 || rect.height < 3 {
		return joinFixedColumns(rect.height, fixedColumn{content: content, width: rect.width})
	}

	// Compose the frame in terminal cells instead of applying a second block
	// layout around the content. This keeps every visible edge, especially the
	// far-right border, inside the pane's assigned rectangle after split joins.
	borderColor := palette.line
	if focused {
		borderColor = lipgloss.Color("#F6C343")
	}
	borderStyle := lipgloss.NewStyle().Foreground(borderColor)
	leftWidth, rightWidth := edgeCell(edges.left), edgeCell(edges.right)
	innerWidth := max(0, rect.width-leftWidth-rightWidth)
	topHeight, bottomHeight := edgeCell(edges.top), edgeCell(edges.bottom)
	contentHeight := max(0, rect.height-topHeight-bottomHeight)
	contentRows := strings.Split(content, "\n")
	rows := make([]string, 0, rect.height)

	if edges.top {
		line := strings.Repeat("─", innerWidth)
		if edges.left {
			line = "┌" + line
		}
		if edges.right {
			line += "┐"
		}
		rows = append(rows, borderStyle.Render(line))
	}
	for index := 0; index < contentHeight; index++ {
		line := ""
		if index < len(contentRows) {
			line = fitLine(contentRows[index], innerWidth)
		} else {
			line = strings.Repeat(" ", innerWidth)
		}
		if edges.left {
			line = borderStyle.Render("│") + line
		}
		if edges.right {
			line += borderStyle.Render("│")
		}
		rows = append(rows, line)
	}
	if edges.bottom {
		line := strings.Repeat("─", innerWidth)
		if edges.left {
			line = "└" + line
		}
		if edges.right {
			line += "┘"
		}
		rows = append(rows, borderStyle.Render(line))
	}

	return strings.Join(rows, "\n")
}

func terminalSearchVisibleRow(pane *terminalPane, bodyHeight int) int {
	if pane == nil || pane.terminal == nil || pane.search == nil || pane.search.editing || pane.search.dirty || len(pane.search.matches) == 0 {
		return -1
	}
	index := clamp(pane.search.index, 0, len(pane.search.matches)-1)
	matchRow := pane.search.matches[index].row
	start := terminalVisibleRowStart(pane, bodyHeight)
	if matchRow < start || matchRow >= start+bodyHeight {
		return -1
	}
	return matchRow - start
}

func renderTerminalScrollback(pane *terminalPane, bodyHeight int) []string {
	if pane == nil || pane.terminal == nil || bodyHeight <= 0 {
		return nil
	}
	history := pane.terminal.Scrollback()
	historyLength := 0
	if history != nil {
		historyLength = history.Len()
	}
	scrollbackOffset := clamp(pane.scrollbackOffset, 0, terminalScrollbackMax(pane, bodyHeight))
	screenRows := strings.Split(pane.terminal.Render(), "\n")
	if len(screenRows) > pane.terminal.Height() {
		screenRows = screenRows[:pane.terminal.Height()]
	}
	for len(screenRows) < pane.terminal.Height() {
		screenRows = append(screenRows, "")
	}
	totalRows := historyLength + len(screenRows)
	start := max(0, totalRows-bodyHeight-scrollbackOffset)
	rows := make([]string, 0, bodyHeight)
	for row := start; row < min(start+bodyHeight, totalRows); row++ {
		if row < historyLength {
			rows = append(rows, history.Line(row).Render())
			continue
		}
		screenRow := row - historyLength
		if screenRow >= 0 && screenRow < len(screenRows) {
			rows = append(rows, screenRows[screenRow])
		}
	}
	for len(rows) < bodyHeight {
		rows = append(rows, "")
	}
	return rows
}

func terminalScrollbackMax(pane *terminalPane, bodyHeight int) int {
	if pane == nil || pane.terminal == nil {
		return 0
	}
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	return pane.terminal.ScrollbackLen() + max(0, pane.terminal.Height()-bodyHeight)
}

func scrollTerminalPane(pane *terminalPane, delta, pageSize, bodyHeight int) {
	if pane == nil || pane.terminal == nil {
		return
	}
	maxOffset := terminalScrollbackMax(pane, bodyHeight)
	if maxOffset == 0 {
		pane.scrollbackViewing = false
		pane.scrollbackOffset = 0
		return
	}
	if pageSize < 1 {
		pageSize = 1
	}
	if delta > 0 {
		pane.scrollbackViewing = true
	}
	pane.scrollbackOffset = clamp(pane.scrollbackOffset+delta*pageSize, 0, maxOffset)
	if pane.scrollbackOffset == 0 && delta < 0 {
		pane.scrollbackViewing = false
	}
}

func (m *modelState) beginTerminalScrollback() {
	pane := m.activeTerminalPane()
	if pane == nil || pane.terminal == nil {
		return
	}
	bodyHeight := m.activePaneBodyHeight()
	maxOffset := terminalScrollbackMax(pane, bodyHeight)
	if maxOffset == 0 {
		pane.statusText = "没有可回看的终端历史或隐藏行"
		pane.statusErr = false
		return
	}
	pane.scrollbackViewing = true
	pane.scrollbackOffset = min(bodyHeight, maxOffset)
}

func (m *modelState) clearActiveTerminalScreen() {
	pane := m.activeTerminalPane()
	if pane == nil || pane.session == nil || pane.terminal == nil {
		m.statusText = "当前 pane 没有活动终端"
		m.statusErr = true
		return
	}

	// Clear only the local VT emulator. Sending these bytes through pane.session
	// would run terminal control input on the remote/local shell instead.
	pane.terminal.ClearScrollback()
	if _, err := pane.terminal.Write([]byte("\x1b[3J\x1b[H")); err != nil {
		m.statusText = "清除终端画面失败：" + err.Error()
		m.statusErr = true
		return
	}

	pane.scrollbackViewing = false
	pane.scrollbackOffset = 0
	pane.search = nil
	pane.selection = nil
	clearTerminalCommandEcho(pane)
	pane.statusText = ""
	pane.statusErr = false
	m.statusText = ""
	m.statusErr = false
}

func (m *modelState) activePaneBodyHeight() int {
	tab := m.activeTab()
	pane := m.activeTerminalPane()
	if tab != nil && pane != nil {
		if rect, ok := paneRectFor(tab.root, pane.id, paneRect{width: m.width, height: max(1, m.height-2)}); ok {
			edges, _ := paneFrameEdgesFor(tab.root, pane.id)
			_, height := terminalPaneViewportSizeWithEdges(rect, edges)
			return height
		}
	}
	return max(1, m.height-5)
}

func (m *modelState) updateWorkspaceScrollbackKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	pane := m.activeTerminalPane()
	if pane == nil || !pane.scrollbackViewing {
		return m, nil
	}
	key := msg.Key()
	pageSize := m.activePaneBodyHeight()
	switch key.Code {
	case tea.KeyUp:
		scrollTerminalPane(pane, 1, 1, pageSize)
	case tea.KeyDown:
		scrollTerminalPane(pane, -1, 1, pageSize)
	case tea.KeyPgUp:
		scrollTerminalPane(pane, 1, pageSize, pageSize)
	case tea.KeyPgDown:
		scrollTerminalPane(pane, -1, pageSize, pageSize)
	case tea.KeyHome:
		pane.scrollbackOffset = terminalScrollbackMax(pane, pageSize)
	case tea.KeyEnd:
		pane.scrollbackOffset = 0
		pane.scrollbackViewing = false
	case tea.KeyEscape, tea.KeyEnter:
		pane.scrollbackViewing = false
		pane.scrollbackOffset = 0
	default:
		if strings.EqualFold(key.Text, "q") {
			pane.scrollbackViewing = false
			pane.scrollbackOffset = 0
		}
	}
	return m, nil
}

func paneNodeContains(node *paneNode, paneID string) bool {
	if node == nil || paneID == "" {
		return false
	}
	if node.pane != nil {
		return node.pane.id == paneID
	}
	return paneNodeContains(node.first, paneID) || paneNodeContains(node.second, paneID)
}

func nearestPaneSplitFor(node *paneNode, paneID string) (*paneNode, bool) {
	if node == nil || node.pane != nil || paneID == "" {
		return nil, false
	}
	if paneNodeContains(node.first, paneID) {
		if split, activeInFirst := nearestPaneSplitFor(node.first, paneID); split != nil {
			return split, activeInFirst
		}
		return node, true
	}
	if paneNodeContains(node.second, paneID) {
		if split, activeInFirst := nearestPaneSplitFor(node.second, paneID); split != nil {
			return split, activeInFirst
		}
		return node, false
	}
	return nil, false
}

func (m *modelState) resizeActivePaneSplit(grow bool) []tea.Cmd {
	tab := m.activeTab()
	if tab == nil || tab.sftp != nil {
		return nil
	}
	split, activeInFirst := nearestPaneSplitFor(tab.root, tab.activePaneID)
	if split == nil {
		m.statusText = "当前 pane 没有可调整的分隔线"
		m.statusErr = false
		return nil
	}
	delta := 5
	if !grow {
		delta = -delta
	}
	if !activeInFirst {
		delta = -delta
	}
	previous := split.ratio
	split.ratio = clamp(split.ratio+delta, 1, 99)
	if split.ratio == previous {
		m.statusText = "当前 pane 已达到可调整边界"
		m.statusErr = false
		return nil
	}
	activeRatio := split.ratio
	if !activeInFirst {
		activeRatio = 100 - split.ratio
	}
	m.statusText = fmt.Sprintf("当前 pane 尺寸约占 %d%%", activeRatio)
	m.statusErr = false
	return m.resizeActiveWorkspace()
}

func (m modelState) workspaceDivider(length int, vertical, focused bool) string {
	style := separatorStyle
	if focused {
		style = focusLineStyle.Foreground(lipgloss.Color("#F6C343"))
	}
	if !vertical {
		return style.Render(strings.Repeat("─", max(0, length)))
	}
	lines := make([]string, length)
	for index := range lines {
		lines[index] = style.Render("│")
	}
	return strings.Join(lines, "\n")
}

func (m *modelState) resizeActiveWorkspace() []tea.Cmd {
	tab := m.activeTab()
	if tab == nil || tab.sftp != nil || tab.root == nil || !m.workspaceVisible {
		return nil
	}
	commands := make([]tea.Cmd, 0, countPanes(tab.root))
	rootRect := paneRect{width: m.width, height: max(0, m.height-2)}
	var resize func(*paneNode, paneRect)
	resize = func(node *paneNode, rect paneRect) {
		if node == nil {
			return
		}
		if node.pane == nil {
			first, second := splitRects(rect, node.axis, node.ratio)
			resize(node.first, first)
			resize(node.second, second)
			return
		}
		edges, _ := paneFrameEdgesFor(tab.root, node.pane.id)
		viewWidth, viewHeight := terminalPaneViewportSizeWithEdges(rect, edges)
		viewWidth, viewHeight = max(20, viewWidth), max(5, viewHeight)
		width, height := m.terminalSize(viewWidth, viewHeight)
		if node.pane.terminal != nil {
			node.pane.terminal.Resize(width, height)
			node.pane.horizontalOffset = clamp(node.pane.horizontalOffset, 0, max(0, width-viewWidth))
			node.pane.verticalOffset = clamp(node.pane.verticalOffset, 0, max(0, height-viewHeight))
			m.keepTerminalCursorVisible(node.pane)
			if node.pane.search != nil {
				node.pane.search.dirty = true
			}
		}
		if node.pane.session != nil {
			commands = append(commands, resizeTerminalCmd(node.pane.session, width, height))
		}
	}
	resize(tab.root, rootRect)
	return commands
}

func (m modelState) terminalSize(viewWidth, viewHeight int) (int, int) {
	config := m.config.Terminal.WithDefaults()
	width, height := max(20, viewWidth), max(5, viewHeight)
	if config.Columns > 0 {
		width = config.Columns
	}
	if config.Rows > 0 {
		height = config.Rows
	}
	return width, height
}

func (m modelState) terminalViewportSize(pane *terminalPane) (int, int) {
	if pane == nil {
		return max(1, m.width), max(1, m.height-3)
	}
	if tab := m.tabForPane(pane); tab != nil {
		if rect, ok := paneRectFor(tab.root, pane.id, paneRect{width: m.width, height: max(1, m.height-2)}); ok {
			edges, _ := paneFrameEdgesFor(tab.root, pane.id)
			return terminalPaneViewportSizeWithEdges(rect, edges)
		}
	}
	return max(1, m.width-2), max(1, m.height-5)
}

func (m *modelState) scrollActiveTerminalViewport(horizontal, vertical int) {
	pane := m.activeTerminalPane()
	if pane == nil || pane.terminal == nil {
		return
	}
	viewWidth, viewHeight := m.terminalViewportSize(pane)
	maxHorizontal := max(0, pane.terminal.Width()-viewWidth)
	maxVertical := max(0, pane.terminal.Height()-viewHeight)
	stepX, stepY := max(1, viewWidth/2), max(1, viewHeight/2)
	if maxHorizontal == 0 && maxVertical == 0 {
		pane.statusText = "终端内容未超出当前视口"
		pane.statusErr = false
		return
	}
	pane.scrollbackViewing = false
	pane.scrollbackOffset = 0
	pane.selection = nil
	pane.horizontalOffset = clamp(pane.horizontalOffset+horizontal*stepX, 0, maxHorizontal)
	pane.verticalOffset = clamp(pane.verticalOffset+vertical*stepY, 0, maxVertical)
}

func (m *modelState) keepTerminalCursorVisible(pane *terminalPane) {
	if pane == nil || pane.terminal == nil || pane.scrollbackViewing {
		return
	}
	viewWidth, viewHeight := m.terminalViewportSize(pane)
	position := pane.terminal.CursorPosition()
	if position.X < pane.horizontalOffset {
		pane.horizontalOffset = position.X
	} else if position.X >= pane.horizontalOffset+viewWidth {
		pane.horizontalOffset = position.X - viewWidth + 1
	}
	if position.Y < pane.verticalOffset {
		pane.verticalOffset = position.Y
	} else if position.Y >= pane.verticalOffset+viewHeight {
		pane.verticalOffset = position.Y - viewHeight + 1
	}
	pane.horizontalOffset = clamp(pane.horizontalOffset, 0, max(0, pane.terminal.Width()-viewWidth))
	pane.verticalOffset = clamp(pane.verticalOffset, 0, max(0, pane.terminal.Height()-viewHeight))
}

func (m *modelState) nextWorkspaceID(prefix string) string {
	for {
		m.nextID++
		candidate := fmt.Sprintf("%s-%d", prefix, m.nextID)
		inUse := false
		for _, tab := range m.tabs {
			if tab == nil {
				continue
			}
			if tab.id == candidate {
				inUse = true
				break
			}
			for _, pane := range collectPanes(tab.root) {
				if pane != nil && pane.id == candidate {
					inUse = true
					break
				}
			}
			if inUse {
				break
			}
		}
		if !inUse {
			return candidate
		}
	}
}

func (m *modelState) newTabTarget() connectionTarget {
	width, height := m.terminalSize(max(20, m.width-2), max(5, m.height-5))
	groupName := ""
	if tab := m.activeTab(); tab != nil {
		groupName = tab.groupName
	}
	return connectionTarget{
		kind:      newTabTarget,
		tabID:     m.nextWorkspaceID("tab"),
		groupName: groupName,
		paneID:    m.nextWorkspaceID("pane"),
		width:     width,
		height:    height,
	}
}

func (m *modelState) newSFTPTarget() connectionTarget {
	return connectionTarget{kind: standaloneSFTPTarget}
}

func (m *modelState) splitTarget(axis splitAxis) (connectionTarget, error) {
	tab := m.activeTab()
	pane := m.activeTerminalPane()
	if tab == nil || pane == nil || pane.session == nil {
		return connectionTarget{}, fmt.Errorf("当前 pane 没有可用的终端会话")
	}
	rect, ok := paneRectFor(tab.root, pane.id, paneRect{width: m.width, height: max(0, m.height-2)})
	if !ok {
		return connectionTarget{}, fmt.Errorf("无法确定当前 pane 的布局")
	}
	first, second := splitRects(rect, axis, 50)
	newRect := second
	if axis == splitVertical && min(first.width, second.width)-1 < 20 {
		return connectionTarget{}, fmt.Errorf("窗口太窄，无法再左右分屏")
	}
	if axis == splitHorizontal && min(first.height, second.height)-2 < 5 {
		return connectionTarget{}, fmt.Errorf("窗口太矮，无法再上下分屏")
	}
	parentEdges, _ := paneFrameEdgesFor(tab.root, pane.id)
	newEdges := splitPaneFrameEdges(parentEdges, axis, false)
	viewWidth, viewHeight := terminalPaneViewportSizeWithEdges(newRect, newEdges)
	width, height := m.terminalSize(max(20, viewWidth), max(5, viewHeight))
	return connectionTarget{
		kind:         splitPaneTarget,
		tabID:        tab.id,
		parentPaneID: pane.id,
		paneID:       m.nextWorkspaceID("pane"),
		axis:         axis,
		width:        width,
		height:       height,
	}, nil
}

func (m *modelState) reconnectActivePane() tea.Cmd {
	tab := m.activeTab()
	pane := m.activeTerminalPane()
	if tab == nil || pane == nil {
		return nil
	}
	if pane.session != nil {
		pane.statusText = "当前会话仍在连接中；断开后再按 " + m.workspaceLeaderChord("reconnect") + " 重连"
		pane.statusErr = false
		return nil
	}
	if m.connectingHost != nil {
		pane.statusText = "当前已有连接任务，请稍后再重连"
		pane.statusErr = true
		return nil
	}
	pane.reconnectGeneration++
	pane.reconnectAttempts = 0
	return m.startPaneReconnect(tab, pane)
}

func (m *modelState) startPaneReconnect(tab *workspaceTab, pane *terminalPane) tea.Cmd {
	if tab == nil || pane == nil || pane.session != nil {
		return nil
	}
	rect, ok := paneRectFor(tab.root, pane.id, paneRect{width: m.width, height: max(0, m.height-2)})
	if !ok {
		pane.statusText = "无法确定当前 pane 的布局"
		pane.statusErr = true
		return nil
	}
	edges, _ := paneFrameEdgesFor(tab.root, pane.id)
	viewWidth, viewHeight := terminalPaneViewportSizeWithEdges(rect, edges)
	width, height := m.terminalSize(max(20, viewWidth), max(5, viewHeight))
	target := connectionTarget{
		kind: reconnectPaneTarget, tabID: tab.id, parentPaneID: pane.id, paneID: pane.id,
		width: width, height: height, reconnectGeneration: pane.reconnectGeneration,
	}
	return m.startHostConnect(pane.host, target)
}

func autoReconnectDelay(attempt int) time.Duration {
	if attempt < 1 || attempt > len(autoReconnectDelays) {
		return 0
	}
	return autoReconnectDelays[attempt-1]
}

func (m *modelState) schedulePaneAutoReconnect(tab *workspaceTab, pane *terminalPane) tea.Cmd {
	if tab == nil || pane == nil || !pane.autoReconnect || pane.host.Kind != model.HostKindSSH || pane.session != nil {
		return nil
	}
	if pane.reconnectAttempts >= maxAutoReconnectAttempts {
		pane.statusText = "自动重连已达到 5 次上限；可按 " + m.workspaceLeaderChord("reconnect") + " 手动重连，或关闭后重新开启自动重连"
		pane.statusErr = true
		return nil
	}
	pane.reconnectAttempts++
	delay := autoReconnectDelay(pane.reconnectAttempts)
	pane.statusText = fmt.Sprintf("SSH 已断开，%d 秒后自动重连（%d/%d）", int(delay.Seconds()), pane.reconnectAttempts, maxAutoReconnectAttempts)
	pane.statusErr = true
	message := autoReconnectMsg{tabID: tab.id, paneID: pane.id, generation: pane.reconnectGeneration}
	return tea.Tick(delay, func(time.Time) tea.Msg { return message })
}

func (m *modelState) toggleActivePaneAutoReconnect() tea.Cmd {
	tab := m.activeTab()
	pane := m.activeTerminalPane()
	if tab == nil || pane == nil {
		m.statusText = "没有可配置的终端 pane"
		m.statusErr = true
		return nil
	}
	if pane.host.Kind != model.HostKindSSH {
		pane.statusText = "自动重连仅适用于 SSH pane"
		pane.statusErr = true
		return nil
	}
	pane.autoReconnect = !pane.autoReconnect
	pane.reconnectGeneration++
	pane.reconnectAttempts = 0
	if !pane.autoReconnect {
		pane.statusText = "已关闭此 pane 的断线自动重连"
		pane.statusErr = false
		return nil
	}
	if pane.session == nil {
		pane.statusText = "已开启自动重连；当前 pane 已断开，请按 " + m.workspaceLeaderChord("reconnect") + " 连接"
		pane.statusErr = false
		return nil
	}
	pane.statusText = "已开启此 pane 的断线自动重连（最多重试 5 次）"
	pane.statusErr = false
	return nil
}

func (m *modelState) handleConnectedPane(target connectionTarget, host model.Host, session terminalSession, script *startupLoginScript) bool {
	if target.kind == reconnectPaneTarget {
		tab := m.tabByID(target.tabID)
		if tab == nil {
			return false
		}
		pane := findPane(tab.root, target.paneID)
		if pane == nil {
			return false
		}
		pane.host = host
		pane.session = session
		if tracked, ok := session.(*trackedTerminalSession); ok {
			pane.traffic = tracked.traffic
		}
		pane.terminalEncoding = normalizedTerminalEncoding(host.TerminalEncoding)
		pane.terminalInputEncoding = effectiveTerminalInputEncoding(host)
		pane.terminalDecodePending = nil
		pane.terminalEncodingSaving = false
		pane.startupPending = startupCommandForHost(host)
		pane.terminal = vt.NewSafeEmulator(max(20, target.width), max(5, target.height))
		pane.terminal.SetScrollbackSize(5000)
		pane.applicationCursor = false
		pane.bracketedPaste = false
		pane.vtTail = ""
		pane.mouse = terminalMouseTracking{}
		pane.horizontalOffset = 0
		pane.verticalOffset = 0
		pane.scrollbackViewing = false
		pane.scrollbackOffset = 0
		pane.search = nil
		pane.selection = nil
		pane.statusText = "已重新连接 " + host.Name
		pane.statusErr = false
		pane.reconnectAttempts = 0
		pane.reconnectGeneration++
		pane.startupScript = newStartupScriptRuntime(script, pane.reconnectGeneration)
		pane.triggerRuntime = newTerminalTriggerRuntime(m.terminalTriggers, pane.reconnectGeneration)
		pane.triggerScriptLoading = false
		pane.triggerScriptOutput = nil
		pane.terminalResponseReaderStarted = false
		tab.activePaneID = pane.id
		m.activateWorkspaceTab(tab.id)
		m.workspaceVisible = true
		startTerminalResponseReader(pane)
		return true
	}
	pane := &terminalPane{
		id:                    target.paneID,
		host:                  host,
		session:               session,
		terminalEncoding:      normalizedTerminalEncoding(host.TerminalEncoding),
		terminalInputEncoding: effectiveTerminalInputEncoding(host),
		startupPending:        startupCommandForHost(host),
		terminal: vt.NewSafeEmulator(
			max(20, target.width),
			max(5, target.height),
		),
		statusText:     "已连接 " + host.Name,
		startupScript:  newStartupScriptRuntime(script, 1),
		triggerRuntime: newTerminalTriggerRuntime(m.terminalTriggers, 1),
	}
	if tracked, ok := session.(*trackedTerminalSession); ok {
		pane.traffic = tracked.traffic
	}
	pane.terminal.SetScrollbackSize(5000)
	if target.kind == newTabTarget {
		tab := &workspaceTab{
			id:           target.tabID,
			groupName:    target.groupName,
			root:         &paneNode{pane: pane},
			activePaneID: pane.id,
		}
		m.addWorkspaceTab(tab)
		m.activateWorkspaceTab(tab.id)
		m.workspaceVisible = true
		startTerminalResponseReader(pane)
		return true
	}
	tab := m.tabByID(target.tabID)
	if tab == nil || !insertSplit(tab.root, target.parentPaneID, pane, target.axis) {
		return false
	}
	tab.activePaneID = pane.id
	m.activateWorkspaceTab(tab.id)
	m.workspaceVisible = true
	startTerminalResponseReader(pane)
	return true
}

func (m *modelState) handleAutoReconnect(msg autoReconnectMsg) tea.Cmd {
	tab := m.tabByID(msg.tabID)
	if tab == nil {
		return nil
	}
	pane := findPane(tab.root, msg.paneID)
	if pane == nil || pane.reconnectGeneration != msg.generation || !pane.autoReconnect || pane.session != nil {
		return nil
	}
	if m.connectingHost != nil {
		if m.connectionTarget != nil && m.connectionTarget.kind == reconnectPaneTarget && m.connectionTarget.paneID == pane.id && m.connectionTarget.tabID == tab.id {
			return nil
		}
		return tea.Tick(time.Second, func(time.Time) tea.Msg { return msg })
	}
	return m.startPaneReconnect(tab, pane)
}

func (m *modelState) tabByID(id string) *workspaceTab {
	for _, tab := range m.tabs {
		if tab.id == id {
			return tab
		}
	}
	return nil
}

func (m *modelState) tabForPane(target *terminalPane) *workspaceTab {
	if target == nil {
		return nil
	}
	for _, tab := range m.tabs {
		if findPane(tab.root, target.id) == target {
			return tab
		}
	}
	return nil
}

func (m *modelState) activateTab(delta int) []tea.Cmd {
	tabCount := len(m.tabs) + 1 // the fixed home tab is always available
	if tabCount == 1 && !m.workspaceVisible {
		return nil
	}
	index := 0 // Home
	if m.workspaceVisible {
		for current, tab := range m.tabs {
			if tab.id == m.activeTabID {
				index = current + 1
				break
			}
		}
	}
	index = (index + delta%tabCount + tabCount) % tabCount
	return m.activateTabBarIndex(index)
}

func (m *modelState) activateTabBarIndex(index int) []tea.Cmd {
	if index == 0 {
		m.workspaceVisible = false
		m.workspaceLeaderPending = false
		return nil
	}
	return m.activateTabIndex(index - 1)
}

func (m *modelState) activateTabIndex(index int) []tea.Cmd {
	if index < 0 || index >= len(m.tabs) {
		return nil
	}
	m.activateWorkspaceTab(m.tabs[index].id)
	m.workspaceVisible = true
	if m.activeSFTPState() != nil {
		return nil
	}
	return m.resizeActiveWorkspace()
}

func (m *modelState) activatePane(delta int) []tea.Cmd {
	tab := m.activeTab()
	if tab == nil {
		return nil
	}
	panes := collectPanes(tab.root)
	if len(panes) < 2 {
		return nil
	}
	index := 0
	for current, pane := range panes {
		if pane.id == tab.activePaneID {
			index = current
			break
		}
	}
	index = (index + delta%len(panes) + len(panes)) % len(panes)
	tab.activePaneID = panes[index].id
	return m.resizeActiveWorkspace()
}

func (m *modelState) moveActivePaneToNext() []tea.Cmd {
	tab := m.activeTab()
	if tab == nil || tab.sftp != nil {
		return nil
	}
	panes := collectPanes(tab.root)
	if len(panes) < 2 {
		m.statusText = "至少需要两个 pane 才能交换位置"
		m.statusErr = false
		return nil
	}
	index := 0
	for current, pane := range panes {
		if pane.id == tab.activePaneID {
			index = current
			break
		}
	}
	next := panes[(index+1)%len(panes)]
	if !swapPanePositions(tab.root, tab.activePaneID, next.id) {
		m.statusText = "交换 pane 位置失败"
		m.statusErr = true
		return nil
	}
	m.statusText = "已将当前 pane 与下一个 pane 交换位置"
	m.statusErr = false
	return m.resizeActiveWorkspace()
}

func (m *modelState) closeActivePane() tea.Cmd {
	tab := m.activeTab()
	pane := m.activeTerminalPane()
	if tab == nil || pane == nil {
		m.workspaceVisible = false
		return nil
	}
	if len(collectPanes(tab.root)) == 1 {
		m.requestTabClose(tab.id)
		return nil
	}
	if pane.scriptRecording != nil {
		path, _, err := stopTerminalScriptRecording(pane)
		if err != nil {
			m.statusText, m.statusErr = "关闭 pane 前保存脚本录制失败："+err.Error(), true
			return nil
		}
		if path != "" {
			m.statusText, m.statusErr = "脚本录制已保存："+path, false
		}
	}
	logCloseCmd := m.stopTerminalOutputLog(pane)
	closeTerminalResponseReader(pane)
	if pane.session != nil {
		_ = pane.session.Close()
	}
	removePane(&tab.root, pane.id)
	if tab.root == nil {
		m.requestTabClose(tab.id)
		return logCloseCmd
	}
	remaining := collectPanes(tab.root)
	if findPane(tab.root, tab.activePaneID) == nil && len(remaining) > 0 {
		tab.activePaneID = remaining[0].id
	}
	m.workspaceVisible = true
	commands := m.resizeActiveWorkspace()
	if logCloseCmd != nil {
		commands = append(commands, logCloseCmd)
	}
	return tea.Batch(commands...)
}

func (m *modelState) closeActiveTab() tea.Cmd {
	if tab := m.activeTab(); tab != nil {
		m.requestTabClose(tab.id)
	}
	return nil
}

func (m *modelState) performTabClose(id string) []tea.Cmd {
	index := -1
	wasActive := m.activeTabID == id
	wasWorkspaceVisible := m.workspaceVisible
	commands := make([]tea.Cmd, 0)
	var target *workspaceTab
	for current, tab := range m.tabs {
		if tab.id == id {
			index = current
			target = tab
			break
		}
	}
	if index < 0 {
		return commands
	}
	for _, pane := range collectPanes(target.root) {
		if pane.scriptRecording == nil {
			continue
		}
		path, _, err := stopTerminalScriptRecording(pane)
		if err != nil {
			m.statusText, m.statusErr = fmt.Sprintf("关闭标签前保存 %s 的脚本录制失败：%v", pane.host.Name, err), true
			return nil
		}
		if path != "" {
			m.statusText, m.statusErr = "脚本录制已保存："+path, false
		}
	}
	for _, pane := range collectPanes(target.root) {
		closeTerminalResponseReader(pane)
		if command := m.stopTerminalOutputLog(pane); command != nil {
			commands = append(commands, command)
		}
		if pane.session != nil {
			_ = pane.session.Close()
		}
	}
	if target.sftp != nil {
		if err := closeSFTPResources(target.sftp); err != nil {
			m.statusText, m.statusErr = "关闭 SFTP 标签失败："+err.Error(), true
		}
	}
	m.tabs = append(m.tabs[:index], m.tabs[index+1:]...)
	if len(m.tabs) == 0 {
		m.activeTabID = ""
		m.sftp = nil
		m.workspaceVisible = false
		return commands
	}
	if wasActive {
		m.activateWorkspaceTab(m.tabs[min(index, len(m.tabs)-1)].id)
	}
	if wasActive {
		m.workspaceVisible = wasWorkspaceVisible
		if !m.workspaceVisible {
			return commands
		}
		if m.activeSFTPState() != nil {
			return commands
		}
		return append(commands, m.resizeActiveWorkspace()...)
	}
	return commands
}

func (m *modelState) paneForSession(session terminalSession) *terminalPane {
	for _, tab := range m.tabs {
		for _, pane := range collectPanes(tab.root) {
			if pane.session == session {
				return pane
			}
		}
	}
	return nil
}

func (m modelState) workspaceTabsView(width int) string {
	home := workspaceHomeTabText(!m.workspaceVisible)
	labels := []string{mutedStyle.Render(home)}
	if !m.workspaceVisible {
		labels[0] = selectedStyle.Render(home)
	}
	for index, tab := range m.tabs {
		if m.workspaceVisible && tab.id == m.activeTabID {
			labels = append(labels, selectedStyle.Render(m.workspaceTabText(index, tab, true)))
		} else {
			labels = append(labels, mutedStyle.Render(m.workspaceTabText(index, tab, false)))
		}
	}
	return fitLine(strings.Join(labels, mutedStyle.Render(" │ ")), width)
}

func (m modelState) workspaceTabText(index int, tab *workspaceTab, active bool) string {
	name := workspaceTabName(tab)
	label := fmt.Sprintf("%d %s", index+1, name)
	if tab.groupName != "" && (index == 0 || m.tabs[index-1].groupName != tab.groupName) {
		label = "〔" + groupDisplayName(tab.groupName) + "〕 " + label
	}
	if active {
		return "‹ " + label + " ›"
	}
	return "  " + label + "  "
}

func workspaceHomeTabText(active bool) string {
	if active {
		return "‹ 首页 ›"
	}
	return "  首页  "
}

func workspaceTabName(tab *workspaceTab) string {
	if tab == nil {
		return "新会话"
	}
	if tab.sftp != nil {
		if tab.sftp.host.Name != "" {
			return tab.sftp.host.Name + " 文件"
		}
		return "双栏文件"
	}
	panes := collectPanes(tab.root)
	if len(panes) > 0 {
		return panes[0].host.Name
	}
	return "新会话"
}

func (m modelState) workspaceTabAt(x int) int {
	position := 0
	homeWidth := lipgloss.Width(workspaceHomeTabText(!m.workspaceVisible))
	if x >= position && x < position+homeWidth {
		return 0
	}
	position += homeWidth + 3
	for index, tab := range m.tabs {
		active := m.workspaceVisible && tab.id == m.activeTabID
		labelWidth := lipgloss.Width(m.workspaceTabText(index, tab, active))
		if x >= position && x < position+labelWidth {
			return index + 1
		}
		position += labelWidth + 3 // separator between tabs
	}
	return -1
}

func (m modelState) workspaceFooter(width int) string {
	if drag := m.paneMoveDrag; drag != nil && drag.dragging {
		return fitLine(accentStyle.Render("移动 pane · 拖到另一个 pane 后释放以交换位置 · Esc 取消"), width)
	}
	if pane := m.activeTerminalPane(); pane != nil && pane.bellUntil.After(time.Now()) {
		return fitLine(warningStyle.Render("终端响铃 · "+sanitizeRemoteText(pane.host.Name)), width)
	}
	if pane := m.activeTerminalPane(); pane != nil && pane.search != nil {
		if pane.search.editing {
			mode := "文本"
			if pane.search.regex {
				mode = "正则"
			}
			return fitLine(accentStyle.Render("终端搜索 ["+mode+"] / "+pane.search.query+"▌ · Ctrl+R 切换模式 · Enter 查找 · Esc 取消"), width)
		}
		if pane.search.err != "" {
			return fitLine(statusErrorStyle.Render("正则表达式错误："+pane.search.err+" · e 修改 · Esc 退出"), width)
		}
		if pane.search.dirty {
			return fitLine(mutedStyle.Render("搜索结果待更新 · 按 n/N 刷新并跳转 · e 修改 · Esc 退出"), width)
		}
		if len(pane.search.matches) == 0 {
			return fitLine(mutedStyle.Render("未找到："+pane.search.query+" · e 修改 · Esc 退出搜索"), width)
		}
		text := fmt.Sprintf("搜索：%s · %d/%d 项 · n 下一项 · N 上一项 · e 修改 · Esc 退出", pane.search.query, pane.search.index+1, len(pane.search.matches))
		if pane.search.truncated {
			text += fmt.Sprintf(" · 仅显示前 %d 项", maxTerminalSearchResults)
		}
		return fitLine(mutedStyle.Render(text), width)
	}
	if pane := m.activeTerminalPane(); pane != nil && pane.statusErr && pane.statusText != "" {
		return fitLine(statusErrorStyle.Render(pane.statusText+" · "+m.workspaceLeaderChord("reconnect")+" 重连"), width)
	}
	if m.statusErr && m.statusText != "" {
		return fitLine(statusErrorStyle.Render(m.statusText), width)
	}
	if pane := m.activeTerminalPane(); pane != nil && pane.selection != nil {
		mode := "文本选区"
		if pane.selection.column {
			mode = "矩形选区"
		}
		text := mode + " · 双击选词 · " + terminalClipboardCopyShortcutLabel() + " 复制 · 右键菜单复制 · Esc 清除选区"
		return fitLine(accentStyle.Render(text), width)
	}
	if pane := m.activeTerminalPane(); pane != nil && pane.outputLogger != nil {
		text := "记录终端输出（可能含敏感信息）· " + filepath.Base(pane.outputLogger.Path()) + " · " + m.workspaceLeaderChord("toggle_recording") + " 停止"
		return fitLine(accentStyle.Render(text), width)
	}
	if pane := m.activeTerminalPane(); pane != nil && pane.scriptRecording != nil {
		text := fmt.Sprintf("Shell 脚本录制中 · %d 条命令（命令参数可能含敏感信息）· %s 停止并保存", len(pane.scriptRecording.steps), m.workspaceLeaderChord("toggle_script_recording"))
		return fitLine(accentStyle.Render(text), width)
	}
	if pane := m.activeTerminalPane(); pane != nil && pane.outputLogClosing {
		return fitLine(mutedStyle.Render("正在写完并关闭终端日志…"), width)
	}
	if pane := m.activeTerminalPane(); pane != nil && pane.autoReconnect {
		return fitLine(accentStyle.Render("断线自动重连已开启 · 最多重试 5 次 · "+m.workspaceLeaderChord("auto_reconnect")+" 关闭"), width)
	}
	if pane := m.activeTerminalPane(); pane != nil && pane.scrollbackViewing {
		if pane.terminal != nil && terminalScrollbackMax(pane, m.activePaneBodyHeight()) == 0 {
			return fitLine(mutedStyle.Render("暂无可回看的终端历史 · Esc 返回实时终端"), width)
		}
		text := fmt.Sprintf("终端历史回看 · ↑/↓ 滚动 · PgUp/PgDn 翻页 · Home/End 定位 · Esc 返回实时终端 · 偏移 %d 行", pane.scrollbackOffset)
		return fitLine(mutedStyle.Render(text), width)
	}
	if m.workspaceLeaderPending {
		return fitLine(accentStyle.Render("前缀已按下 · "+m.workspaceLeaderChord("help")+" 查看全部快捷键 · Esc 取消"), width)
	}
	if tab := m.activeTab(); tab != nil && tab.syncInput {
		text := fmt.Sprintf("同步输入已开启 · 键盘输入和粘贴将发送到当前标签页的 %d 个已连接 pane · %s 关闭", connectedPaneCount(tab), m.workspaceLeaderChord("sync_input"))
		return fitLine(accentStyle.Render(text), width)
	}
	if pane := m.activeTerminalPane(); pane != nil && pane.terminal != nil {
		viewWidth, viewHeight := m.terminalViewportSize(pane)
		terminalWidth, terminalHeight := pane.terminal.Width(), pane.terminal.Height()
		if terminalWidth > viewWidth || terminalHeight > viewHeight {
			config := m.config.WithDefaults().Workspace
			text := fmt.Sprintf("终端 %d×%d · 视口列 %d–%d 行 %d–%d",
				terminalWidth, terminalHeight, pane.horizontalOffset+1, min(pane.horizontalOffset+viewWidth, terminalWidth),
				pane.verticalOffset+1, min(pane.verticalOffset+viewHeight, terminalHeight))
			if config.ShowFooterShortcuts {
				text += fmt.Sprintf(" · %s 后 %s/%s 左右、%s/%s 上下滚动",
					displayWorkspaceKey(config.Leader), displayWorkspaceKey(config.Bindings["screen_left"]), displayWorkspaceKey(config.Bindings["screen_right"]),
					displayWorkspaceKey(config.Bindings["screen_up"]), displayWorkspaceKey(config.Bindings["screen_down"]))
			}
			return fitLine(mutedStyle.Render(text), width)
		}
	}
	config := m.config.WithDefaults().Workspace
	items := make([]string, 0, len(config.StatusBarItems))
	for _, item := range config.StatusBarItems {
		if !config.ShowFooterShortcuts && workspaceFooterItemIsShortcut(item) {
			continue
		}
		if text := m.workspaceFooterItem(item, config); text != "" {
			items = append(items, text)
		}
	}
	text := strings.Join(items, " · ")
	return fitLine(mutedStyle.Render(text), width)
}

func workspaceFooterItemIsShortcut(item string) bool {
	switch item {
	case "leader", "split", "command_history", "quick_command", "scheduled_transfers", "sftp", "help", "close_pane":
		return true
	default:
		return false
	}
}

func (m modelState) workspaceFooterItem(item string, config model.WorkspaceConfig) string {
	switch item {
	case "leader":
		return m.workspaceLeaderKeyLabel() + " 前缀"
	case "session":
		if pane := m.activeTerminalPane(); pane != nil {
			return "会话 " + sanitizeRemoteText(pane.host.Name)
		}
		return ""
	case "traffic":
		if pane := m.activeTerminalPane(); pane != nil {
			return terminalTrafficSummary(pane.traffic)
		}
		return ""
	case "split":
		return displayWorkspaceKey(config.Bindings["split_vertical"]) + "/" + displayWorkspaceKey(config.Bindings["split_horizontal"]) + " 分屏"
	case "command_history":
		return displayWorkspaceKey(config.Bindings["command_history"]) + " 命令历史"
	case "quick_command":
		return displayWorkspaceKey(config.Bindings["quick_command"]) + " 快速命令"
	case "scheduled_transfers":
		return displayWorkspaceKey(config.Bindings["scheduled_transfers"]) + " 计划传输"
	case "sftp":
		return displayWorkspaceKey(config.Bindings["sftp"]) + " 远程文件"
	case "help":
		return m.workspaceLeaderChord("help") + " 快捷键"
	case "close_pane":
		return "Ctrl+] 关 pane"
	default:
		return ""
	}
}

func (m modelState) renderWorkspace() string {
	tab := m.activeTab()
	if tab == nil {
		return m.view()
	}
	if tab.sftp != nil {
		return strings.Join([]string{
			m.workspaceTabsView(m.width),
			m.sftpViewForState(tab.sftp),
		}, "\n")
	}
	rootRect := paneRect{width: max(1, m.width), height: max(1, m.height-2)}
	return strings.Join([]string{
		m.workspaceTabsView(m.width),
		m.renderWorkspaceNode(tab, tab.root, rootRect),
		m.workspaceFooter(m.width),
	}, "\n")
}

func (m *modelState) workspaceCursor() *tea.Cursor {
	tab := m.activeTab()
	pane := m.activeTerminalPane()
	if tab == nil || pane == nil || pane.terminal == nil || pane.session == nil || pane.scrollbackViewing || pane.terminal.CursorHidden() {
		return nil
	}
	rect, ok := paneRectFor(tab.root, pane.id, paneRect{width: m.width, height: max(1, m.height-2)})
	if !ok {
		return nil
	}
	edges, _ := paneFrameEdgesFor(tab.root, pane.id)
	contentRect := terminalPaneContentRectWithEdges(rect, edges)
	position := pane.terminal.CursorPosition()
	x, y := position.X-pane.horizontalOffset, position.Y-pane.verticalOffset
	if x < 0 || x >= contentRect.width || y < 0 || y >= contentRect.height-1 {
		return nil
	}
	cursor := tea.NewCursor(
		clamp(contentRect.x+x, 0, max(0, m.width-1)),
		clamp(contentRect.y+y+2, 0, max(0, m.height-1)),
	)
	style, blink := pane.terminal.CursorStyle()
	switch style {
	case vt.CursorUnderline:
		cursor.Shape = tea.CursorUnderline
	case vt.CursorBar:
		cursor.Shape = tea.CursorBar
	default:
		cursor.Shape = tea.CursorBlock
	}
	cursor.Blink = blink
	cursor.Color = pane.terminal.CursorColor()
	return cursor
}

func (m *modelState) workspaceMouseMode() tea.MouseMode {
	pane := m.activeTerminalPane()
	if pane != nil && pane.scrollbackViewing {
		return tea.MouseModeCellMotion
	}
	if pane != nil && pane.mouse.enabled() && pane.mouse.bubbleTeaMode() == tea.MouseModeAllMotion {
		return tea.MouseModeAllMotion
	}
	// Capture clicks even in a single pane so the workspace can show its
	// right-click menu. Shift-click remains available for terminal selection.
	return tea.MouseModeCellMotion
}

func (pane *terminalPane) updateTerminalModes(data []byte) {
	sequence := pane.vtTail + string(data)
	if enabled, found := privateModeState(sequence, "1"); found {
		pane.applicationCursor = enabled
	}
	if enabled, found := privateModeState(sequence, "2004"); found {
		pane.bracketedPaste = enabled
	}
	for _, mode := range []struct {
		code  string
		state *bool
	}{
		{code: "9", state: &pane.mouse.press},
		{code: "1000", state: &pane.mouse.click},
		{code: "1002", state: &pane.mouse.drag},
		{code: "1003", state: &pane.mouse.motion},
		{code: "1006", state: &pane.mouse.sgr},
	} {
		if enabled, found := privateModeState(sequence, mode.code); found {
			*mode.state = enabled
		}
	}
	if len(sequence) > 16 {
		sequence = sequence[len(sequence)-16:]
	}
	pane.vtTail = sequence
}
