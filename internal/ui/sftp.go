package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"tui-ssh-workbench/internal/infra/ftpclient"
	"tui-ssh-workbench/internal/infra/remotefs"
	"tui-ssh-workbench/internal/infra/scpclient"
	"tui-ssh-workbench/internal/infra/sftpclient"
	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/model"
)

type sftpSide uint8

const (
	sftpLocalSide sftpSide = iota
	sftpRemoteSide
)

func (m *modelState) bindSFTPStateForMessage(message tea.Msg) {
	var generation uint64
	switch msg := message.(type) {
	case sftpOpenedMsg:
		generation = msg.generation
	case sftpPeerOpenedMsg:
		generation = msg.generation
	case ftpConnectedMsg:
		generation = msg.generation
	case ftpRawCommandFinishedMsg:
		generation = msg.generation
	case sftpReopenedMsg:
		generation = msg.generation
	case sftpListedMsg:
		generation = msg.generation
	case sftpLocalPaneTabListedMsg:
		generation = msg.generation
	case sftpSynchronizedListedMsg:
		generation = msg.generation
	case sftpSearchCompletedMsg:
		generation = msg.generation
	case sftpCompareCompletedMsg:
		generation = msg.generation
	case sftpMirrorDeleteCompletedMsg:
		generation = msg.generation
	case sftpOperationMsg:
		generation = msg.generation
	case sftpOverwriteRequiredMsg:
		generation = msg.generation
	case sftpQueuePreflightMsg:
		generation = msg.generation
	case sftpProgressMsg:
		generation = msg.generation
	case sftpEditorLoadedMsg:
		generation = msg.generation
	case sftpEditorSavedMsg:
		generation = msg.generation
	case sftpExternalEditorFinishedMsg:
		generation = msg.generation
	case sftpImagePreparedMsg:
		generation = msg.generation
	case sftpImageEditorFinishedMsg:
		generation = msg.generation
	case sftpImageUploadedMsg:
		generation = msg.generation
	case terminalConnectedMsg:
		if msg.target.kind == sftpPeerTarget {
			generation = msg.target.sftpGeneration
		}
	case sshCredentialsLoadedMsg:
		if msg.target.kind == sftpPeerTarget {
			generation = msg.target.sftpGeneration
		}
	case ftpCredentialLoadedMsg:
		if msg.target.kind == sftpPeerTarget {
			generation = msg.target.sftpGeneration
		}
	}
	if generation == 0 {
		return
	}
	for _, tab := range m.tabs {
		if tab != nil && tab.sftp != nil && tab.sftp.generation == generation {
			m.sftp = tab.sftp
			return
		}
	}
}

type sftpBrowserState struct {
	paneID          string
	generation      uint64
	host            model.Host
	session         *sshclient.Session
	client          remotefs.Client
	standalone      bool
	primaryLocal    bool
	ownsSession     bool
	peerHost        model.Host
	peerSession     *sshclient.Session
	peerClient      remotefs.Client
	ftpPassword     string
	peerFTPPassword string

	path                   string
	entries                []sftpclient.Entry
	remoteFilter           sftpEntryFilter
	remoteFilteredEntries  []sftpclient.Entry
	selected               int
	top                    int
	localPath              string
	localEntries           []sftpclient.Entry
	localFilter            sftpEntryFilter
	localFilteredEntries   []sftpclient.Entry
	peerPath               string
	localSelected          int
	localTop               int
	activeSide             sftpSide
	localMarks             map[string]sftpclient.Entry
	peerMarks              map[string]sftpclient.Entry
	remoteMarks            map[string]sftpclient.Entry
	synchronizedBrowsing   bool
	synchronizedLocalRoot  string
	synchronizedRemoteRoot string
	requestID              uint64
	busy                   bool
	searchActive           bool
	searchFilterMode       bool
	searchQuery            string
	searchRoot             string
	searchSide             sftpSide
	searchResults          []sftpSearchResult
	searchSelected         int
	searchTop              int
	searchTruncated        bool
	searchCancel           context.CancelFunc
	imageEditor            *sftpImageEditorState
	comparison             *sftpSyncComparison
	compareCancel          context.CancelFunc
	mirrorPlan             *sftpMirrorPlan
	mirrorDeleteCancel     context.CancelFunc
	syncIssueCount         int
	pendingSelectionSide   sftpSide
	pendingSelectionName   string

	prompt               *sftpPrompt
	editor               *sftpEditorState
	externalEditorOnLoad bool
	externalEditorOnly   bool
	properties           *sftpProperties
	ftpCommand           *ftpCommandOutput
	status               string
	statusErr            bool
	transferred          int64
	transferTotal        int64
	currentProgress      sftpProgress
	progressChannel      chan sftpProgress
	transferCancel       context.CancelFunc
	transferQueue        []sftpTransferRequest
	queueReviewActive    bool
	queueReviewSide      sftpSide
	queueReviewIndex     int
	queueReviewTop       int
	queueActive          bool
	queueCancelled       bool
	queueTotal           int
	queueDone            int
	queueSkipped         int
	queueFailures        []string
	queueCurrent         *sftpTransferRequest
	queueApproved        []sftpQueueJob
	queueProgress        map[int]sftpProgress
	queueContext         context.Context
	transferPauseGate    *sftpclient.TransferPauseGate
	transferPaused       bool
	lastMouseClick       time.Time
	lastMouseEntry       int
	lastMouseSide        sftpSide
	paneTabs             [2][]*sftpPaneTab
	activePaneTab        [2]int
	nextPaneTabID        uint64
}

// sftpPaneTab stores the endpoint and navigation state for one side of the
// standalone dual-pane file workspace. The currently selected tab is mirrored
// into sftpBrowserState's legacy left/right fields so existing file operations
// can keep using one active endpoint per side.
type sftpPaneTab struct {
	id          uint64
	remote      bool
	host        model.Host
	session     *sshclient.Session
	client      remotefs.Client
	ownsSession bool
	ftpPassword string
	path        string
	entries     []sftpclient.Entry
	filter      sftpEntryFilter
	selected    int
	top         int
	marks       map[string]sftpclient.Entry
}

func nextSFTPPaneTabID(state *sftpBrowserState) uint64 {
	if state == nil {
		return 0
	}
	state.nextPaneTabID++
	return state.nextPaneTabID
}

func captureSFTPPaneTab(state *sftpBrowserState, side sftpSide) *sftpPaneTab {
	if state == nil {
		return nil
	}
	tab := &sftpPaneTab{remote: sftpSideIsRemote(state, side)}
	if side == sftpLocalSide {
		tab.selected, tab.top, tab.entries = state.localSelected, state.localTop, state.localEntries
		tab.filter, tab.marks = state.localFilter, state.localMarks
		if tab.remote {
			tab.host, tab.session, tab.client = state.peerHost, state.peerSession, state.peerClient
			tab.ownsSession = state.peerSession != nil
			tab.ftpPassword, tab.path = state.peerFTPPassword, state.peerPath
		} else {
			tab.path = state.localPath
		}
	} else {
		tab.selected, tab.top, tab.entries = state.selected, state.top, state.entries
		tab.filter, tab.marks = state.remoteFilter, state.remoteMarks
		tab.path = state.path
		if tab.remote {
			tab.host, tab.session, tab.client = state.host, state.session, state.client
			tab.ownsSession, tab.ftpPassword = state.ownsSession, state.ftpPassword
		}
	}
	if tab.marks == nil {
		tab.marks = make(map[string]sftpclient.Entry)
	}
	return tab
}

func ensureSFTPPaneTabs(state *sftpBrowserState) {
	if state == nil || !state.standalone {
		return
	}
	for _, side := range []sftpSide{sftpLocalSide, sftpRemoteSide} {
		if len(state.paneTabs[side]) > 0 {
			continue
		}
		tab := captureSFTPPaneTab(state, side)
		tab.id = nextSFTPPaneTabID(state)
		state.paneTabs[side] = []*sftpPaneTab{tab}
		state.activePaneTab[side] = 0
	}
}

func saveSFTPActivePaneTab(state *sftpBrowserState, side sftpSide) {
	if state == nil || !state.standalone {
		return
	}
	ensureSFTPPaneTabs(state)
	index := state.activePaneTab[side]
	if index < 0 || index >= len(state.paneTabs[side]) || state.paneTabs[side][index] == nil {
		return
	}
	tab := captureSFTPPaneTab(state, side)
	tab.id = state.paneTabs[side][index].id
	state.paneTabs[side][index] = tab
}

func activateSFTPPaneTab(state *sftpBrowserState, side sftpSide, index int) bool {
	if state == nil || index < 0 || index >= len(state.paneTabs[side]) {
		return false
	}
	tab := state.paneTabs[side][index]
	if tab == nil {
		return false
	}
	state.activePaneTab[side] = index
	if side == sftpLocalSide {
		state.peerHost, state.peerSession, state.peerClient = model.Host{}, nil, nil
		state.peerFTPPassword, state.peerPath = "", ""
		state.localPath = tab.path
		state.localEntries, state.localFilter = tab.entries, tab.filter
		state.localSelected, state.localTop = tab.selected, tab.top
		state.localMarks = tab.marks
		if tab.remote {
			state.peerHost, state.peerSession, state.peerClient = tab.host, tab.session, tab.client
			state.peerFTPPassword, state.peerPath = tab.ftpPassword, tab.path
		}
	} else {
		state.host, state.session, state.client = tab.host, tab.session, tab.client
		state.ownsSession, state.ftpPassword = tab.ownsSession, tab.ftpPassword
		state.path, state.entries = tab.path, tab.entries
		state.remoteFilter, state.selected, state.top = tab.filter, tab.selected, tab.top
		state.remoteMarks = tab.marks
		state.primaryLocal = !tab.remote
	}
	if side == sftpLocalSide && tab.remote {
		state.peerPath = tab.path
	}
	state.activeSide = side
	rebuildSFTPEntryViews(state)
	return true
}

func addSFTPPaneTab(state *sftpBrowserState, side sftpSide, tab *sftpPaneTab) {
	if state == nil || tab == nil {
		return
	}
	ensureSFTPPaneTabs(state)
	saveSFTPActivePaneTab(state, side)
	if tab.id == 0 {
		tab.id = nextSFTPPaneTabID(state)
	}
	if tab.marks == nil {
		tab.marks = make(map[string]sftpclient.Entry)
	}
	state.paneTabs[side] = append(state.paneTabs[side], tab)
	_ = activateSFTPPaneTab(state, side, len(state.paneTabs[side])-1)
}

func sftpPaneTabTitle(tab *sftpPaneTab) string {
	if tab == nil || !tab.remote {
		return "本地"
	}
	if strings.TrimSpace(tab.host.Name) == "" {
		return "远程"
	}
	return sanitizeRemoteText(tab.host.Name)
}

type sftpProperties struct {
	side  sftpSide
	entry sftpclient.Entry
	path  string
}

type sftpPromptAction uint8

const (
	sftpPromptUpload sftpPromptAction = iota
	sftpPromptDownload
	sftpPromptMkdir
	sftpPromptRename
	sftpPromptChmod
	sftpPromptFTPCommand
	sftpPromptGoToPath
	sftpPromptSearch
	sftpPromptFilter
	sftpPromptSaveFilterName
	sftpPromptDelete
	sftpPromptOverwrite
	sftpPromptDiscard
	sftpPromptMirrorConfirm
	sftpPromptEditorFind
	sftpPromptEditorReplaceFind
	sftpPromptEditorReplaceValue
	sftpPromptExternalEditorCommand
	sftpPromptClosePaneTab
)

type sftpPrompt struct {
	action           sftpPromptAction
	title            string
	value            string
	recursive        bool
	filterExpression string
	transfer         *sftpTransferRequest
	paneTabID        uint64
	paneTabSide      sftpSide
}

type ftpCommandOutput struct {
	command string
	code    int
	lines   []string
	err     string
	top     int
}

type sftpTransferDirection uint8

const (
	sftpUpload sftpTransferDirection = iota
	sftpDownload
)

type sftpTransferRequest struct {
	direction    sftpTransferDirection
	side         sftpSide
	directory    bool
	sourceRemote bool
	destRemote   bool
	sourcePath   string
	destPath     string
	entryPath    string
	localPath    string
	remotePath   string
	permissions  os.FileMode
}

type sftpQueueJob struct {
	transfer  sftpTransferRequest
	overwrite bool
}

type sftpOpenedMsg struct {
	paneID       string
	generation   uint64
	session      *sshclient.Session
	client       remotefs.Client
	path         string
	entries      []sftpclient.Entry
	localPath    string
	localEntries []sftpclient.Entry
	err          error
}

type sftpPeerOpenedMsg struct {
	generation uint64
	paneTabID  uint64
	side       sftpSide
	host       model.Host
	session    *sshclient.Session
	client     remotefs.Client
	path       string
	entries    []sftpclient.Entry
	err        error
}

type ftpConnectedMsg struct {
	generation   uint64
	host         model.Host
	target       connectionTarget
	client       *ftpclient.Client
	path         string
	entries      []sftpclient.Entry
	localPath    string
	localEntries []sftpclient.Entry
	err          error
}

type ftpRawCommandFinishedMsg struct {
	generation uint64
	side       sftpSide
	command    string
	code       int
	response   string
	path       string
	entries    []sftpclient.Entry
	err        error
}

type sftpReopenedMsg struct {
	generation   uint64
	client       remotefs.Client
	path         string
	entries      []sftpclient.Entry
	peerClient   remotefs.Client
	peerPath     string
	peerEntries  []sftpclient.Entry
	localEntries []sftpclient.Entry
	err          error
}

type sftpListedMsg struct {
	requestID  uint64
	generation uint64
	side       sftpSide
	path       string
	entries    []sftpclient.Entry
	err        error
}

type sftpLocalPaneTabListedMsg struct {
	generation uint64
	paneTabID  uint64
	side       sftpSide
	path       string
	entries    []sftpclient.Entry
	err        error
}

type sftpSynchronizedListedMsg struct {
	requestID     uint64
	generation    uint64
	localPath     string
	localEntries  []sftpclient.Entry
	remotePath    string
	remoteEntries []sftpclient.Entry
	err           error
}

type sftpSearchResult struct {
	Path  string
	Entry sftpclient.Entry
}

type sftpSearchCompletedMsg struct {
	requestID  uint64
	generation uint64
	side       sftpSide
	root       string
	query      string
	filterMode bool
	results    []sftpSearchResult
	truncated  bool
	err        error
}

type sftpOperationMsg struct {
	requestID      uint64
	generation     uint64
	side           sftpSide
	path           string
	status         string
	entries        []sftpclient.Entry
	queueRefreshes []sftpQueueRefresh
	queueResults   []sftpQueueResult
	cancelled      bool
	err            error
}

type sftpQueueRefresh struct {
	side    sftpSide
	path    string
	entries []sftpclient.Entry
	err     error
}

type sftpOverwriteRequiredMsg struct {
	requestID  uint64
	generation uint64
	transfer   sftpTransferRequest
}

type sftpQueuePreflightMsg struct {
	requestID  uint64
	generation uint64
	transfer   sftpTransferRequest
	exists     bool
	cancelled  bool
	err        error
}

type sftpProgress struct {
	index       int
	transferred int64
	total       int64
	currentPath string
	filesDone   int
	filesTotal  int
}

type sftpProgressMsg struct {
	requestID  uint64
	generation uint64
	progress   sftpProgress
	closed     bool
}

type sftpEditorMode uint8

const (
	sftpPreviewMode sftpEditorMode = iota
	sftpEditMode
)

type sftpEditorState struct {
	mode             sftpEditorMode
	leaderPending    bool
	side             sftpSide
	hostName         string
	path             string
	name             string
	encoding         string
	originalEncoding string
	bom              bool
	originalBOM      bool
	rawContent       []byte
	version          sftpclient.FileVersion
	original         string
	lines            []string
	highlightedLines []string
	lineEndings      []string
	newline          string
	searchQuery      string
	replaceQuery     string
	row              int
	column           int
	topLine          int
	dirty            bool
	closeAfterSave   bool
}

type sftpEditorLoadedMsg struct {
	requestID  uint64
	generation uint64
	side       sftpSide
	hostName   string
	path       string
	mode       sftpEditorMode
	content    []byte
	rawContent []byte
	encoding   string
	bom        bool
	version    sftpclient.FileVersion
	err        error
}

type sftpEditorSavedMsg struct {
	requestID  uint64
	generation uint64
	version    sftpclient.FileVersion
	content    []byte
	bom        bool
	err        error
}

type sftpExternalEditorFinishedMsg struct {
	requestID  uint64
	generation uint64
	tempPath   string
	autoSave   bool
	err        error
}

func (m *modelState) openActiveSFTP() tea.Cmd {
	pane := m.activeTerminalPane()
	if pane == nil {
		return nil
	}
	if pane.host.Kind != model.HostKindSSH {
		pane.statusText = "远程文件管理仅支持已连接的 SSH 主机"
		pane.statusErr = true
		return nil
	}
	sshSession, ok := sshSessionFromTerminalSession(pane.session)
	if !ok {
		pane.statusText = "当前 SSH 会话不支持远程文件管理"
		pane.statusErr = true
		return nil
	}
	localPath, err := resolveLocalStartDir(pane.host.LocalStartDir)
	if err != nil {
		pane.statusText = "读取本地起始目录失败：" + err.Error()
		pane.statusErr = true
		return nil
	}
	m.sftpGeneration++
	generation := m.sftpGeneration
	state := &sftpBrowserState{
		paneID: pane.id, generation: generation, host: pane.host, session: sshSession,
		localPath: localPath, activeSide: sftpRemoteSide, standalone: true,
		busy: true, status: "正在打开双栏文件工作区…",
	}
	m.addSFTPWorkspaceTab(state)
	return func() tea.Msg {
		client, err := openSSHFileClient(sshSession, pane.host)
		if err != nil {
			return sftpOpenedMsg{paneID: pane.id, generation: generation, session: sshSession, err: err}
		}
		remotePath, pathErr := resolveRemoteStartDir(client, pane.host.RemoteStartDir)
		if pathErr != nil {
			_ = client.Close()
			return sftpOpenedMsg{paneID: pane.id, generation: generation, session: sshSession, err: pathErr}
		}
		entries, err := client.List(remotePath)
		if err != nil {
			_ = client.Close()
			return sftpOpenedMsg{paneID: pane.id, generation: generation, session: sshSession, err: err}
		}
		localEntries, err := listLocalDirectory(localPath)
		if err != nil {
			_ = client.Close()
			return sftpOpenedMsg{paneID: pane.id, generation: generation, session: sshSession, err: fmt.Errorf("读取本地目录失败：%w", err)}
		}
		return sftpOpenedMsg{
			paneID: pane.id, generation: generation, session: sshSession, client: client,
			path: remotePath, entries: sortSFTPEntries(entries),
			localPath: localPath, localEntries: localEntries,
		}
	}
}

func (m *modelState) openStandaloneSFTP(host model.Host, session *sshclient.Session) tea.Cmd {
	localPath, err := resolveLocalStartDir(host.LocalStartDir)
	if err != nil {
		_ = session.Close()
		m.statusText, m.statusErr = "读取本地起始目录失败："+err.Error(), true
		return nil
	}
	m.sftpGeneration++
	generation := m.sftpGeneration
	state := &sftpBrowserState{
		generation: generation, host: host, session: session,
		standalone: true, ownsSession: true,
		localPath: localPath, activeSide: sftpLocalSide,
		busy: true, status: "正在打开独立远程文件面板…",
	}
	m.addSFTPWorkspaceTab(state)
	return func() tea.Msg {
		client, err := openSSHFileClient(session, host)
		if err != nil {
			return sftpOpenedMsg{generation: generation, session: session, err: err}
		}
		remotePath, pathErr := resolveRemoteStartDir(client, host.RemoteStartDir)
		if pathErr != nil {
			_ = client.Close()
			return sftpOpenedMsg{generation: generation, session: session, err: pathErr}
		}
		remoteEntries, err := client.List(remotePath)
		if err != nil {
			_ = client.Close()
			return sftpOpenedMsg{generation: generation, session: session, err: err}
		}
		localEntries, err := listLocalDirectory(localPath)
		if err != nil {
			_ = client.Close()
			return sftpOpenedMsg{generation: generation, session: session, err: fmt.Errorf("读取本地目录失败：%w", err)}
		}
		return sftpOpenedMsg{
			generation: generation, session: session, client: client,
			path: remotePath, entries: sortSFTPEntries(remoteEntries),
			localPath: localPath, localEntries: localEntries,
		}
	}
}

func (m *modelState) addSFTPWorkspaceTab(state *sftpBrowserState) {
	if state == nil {
		return
	}
	groupName := ""
	if tab := m.activeTab(); tab != nil {
		groupName = tab.groupName
	}
	tab := &workspaceTab{id: m.nextWorkspaceID("sftp"), groupName: groupName, sftp: state}
	m.addWorkspaceTab(tab)
	m.activateWorkspaceTab(tab.id)
	m.workspaceVisible = true
}

func openSSHFileClient(session *sshclient.Session, host model.Host) (remotefs.Client, error) {
	if session == nil {
		return nil, errors.New("SSH 连接不可用")
	}
	if host.Kind != model.HostKindSSH {
		return nil, errors.New("远程文件工作区仅支持 SSH 主机")
	}
	if host.FileProtocol == model.SSHFileProtocolSCP {
		return scpclient.New(func() (scpclient.Command, error) {
			return session.OpenCommand()
		})
	}
	return session.OpenSFTP()
}

func resolveLocalStartDir(configured string) (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("读取当前工作目录：%w", err)
	}
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return filepath.Abs(current)
	}
	if configured == "~" || strings.HasPrefix(configured, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("读取用户主目录：%w", err)
		}
		if configured == "~" {
			configured = home
		} else {
			configured = filepath.Join(home, strings.TrimPrefix(configured, "~/"))
		}
	}
	if !filepath.IsAbs(configured) {
		configured = filepath.Join(current, configured)
	}
	return filepath.Abs(filepath.Clean(configured))
}

func resolveRemoteStartDir(client remotefs.Client, configured string) (string, error) {
	base, err := client.WorkingDirectory()
	if err != nil || strings.TrimSpace(base) == "" {
		base, err = client.RealPath(".")
	}
	if err != nil || strings.TrimSpace(base) == "" {
		base = "."
	}
	return cleanRemoteStartDir(base, configured), nil
}

func cleanRemoteStartDir(base, configured string) string {
	base = sftpclient.CleanRemotePath(base)
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return base
	}
	if !path.IsAbs(configured) {
		configured = path.Join(base, configured)
	}
	return sftpclient.CleanRemotePath(configured)
}

func (m *modelState) promptFTPPassword(host model.Host, target connectionTarget) {
	if host.Kind != model.HostKindFTP {
		return
	}
	m.ftpPendingHost = &host
	m.ftpPendingTarget = &target
	m.ftpPasswordValue = ""
	m.ftpPasswordError = ""
	m.ftpPasswordPrompt = true
}

func (m *modelState) startFTPConnect(host model.Host, target connectionTarget, password string) tea.Cmd {
	m.clearConnectCancel()
	ctx, cancel := context.WithCancel(context.Background())
	m.connectCancel = cancel
	m.connectingHost = &host
	m.connectionTarget = &target
	generation := target.sftpGeneration
	if target.kind == standaloneSFTPTarget {
		m.sftpGeneration++
		generation = m.sftpGeneration
		target.sftpGeneration = generation
		m.connectionTarget = &target
		localPath, err := resolveLocalStartDir(host.LocalStartDir)
		if err != nil {
			cancel()
			m.lastFTPPassword = ""
			m.connectingHost = nil
			m.connectionTarget = nil
			m.statusText, m.statusErr = "读取本地起始目录失败："+err.Error(), true
			return nil
		}
		state := &sftpBrowserState{
			generation: generation, host: host, standalone: true,
			localPath: localPath, activeSide: sftpLocalSide,
			busy: true, status: "正在连接 FTP/FTPS…",
		}
		m.addSFTPWorkspaceTab(state)
	} else if target.kind == sftpPeerTarget {
		if m.sftp == nil || m.sftp.generation != generation || !m.sftp.standalone {
			cancel()
			m.lastFTPPassword = ""
			m.connectingHost = nil
			m.connectionTarget = nil
			m.ftpPasswordError = "文件面板已关闭，请重新选择连接。"
			return nil
		}
		m.sftp.busy = true
		m.sftp.status = "正在连接" + sftpSideLabel(target.sftpSide) + " FTP/FTPS 主机…"
		m.sftp.statusErr = false
	}
	m.lastFTPPassword = password
	return func() tea.Msg {
		client, err := ftpclient.Dial(ctx, ftpclient.Config{
			Address: host.IP, Port: host.Port, Username: host.User,
			Password: password, TLSMode: ftpclient.TLSMode(host.TLSMode), ActiveMode: host.FTPActiveMode,
		})
		if err != nil {
			return ftpConnectedMsg{generation: generation, host: host, target: target, err: err}
		}
		remotePath, pathErr := resolveRemoteStartDir(client, host.RemoteStartDir)
		if pathErr != nil {
			_ = client.Close()
			return ftpConnectedMsg{generation: generation, host: host, target: target, err: pathErr}
		}
		entries, err := client.List(remotePath)
		if err != nil {
			_ = client.Close()
			return ftpConnectedMsg{generation: generation, host: host, target: target, err: err}
		}
		var localPath string
		var localEntries []sftpclient.Entry
		if target.kind == standaloneSFTPTarget {
			localPath, err = resolveLocalStartDir(host.LocalStartDir)
			if err == nil {
				localEntries, err = listLocalDirectory(localPath)
			}
			if err != nil {
				_ = client.Close()
				return ftpConnectedMsg{generation: generation, host: host, target: target, err: fmt.Errorf("读取本地目录失败：%w", err)}
			}
		}
		return ftpConnectedMsg{
			generation: generation, host: host, target: target, client: client,
			path: remotePath, entries: sortSFTPEntries(entries), localPath: localPath, localEntries: localEntries,
		}
	}
}

func (m *modelState) handleFTPConnected(msg ftpConnectedMsg) tea.Cmd {
	ftpPassword := m.lastFTPPassword
	m.lastFTPPassword = ""
	if m.connectCancel != nil {
		m.connectCancel()
		m.connectCancel = nil
	}
	m.connectingHost = nil
	m.connectionTarget = nil
	if msg.target.kind == sftpPeerTarget {
		state := m.sftp
		if state == nil || state.generation != msg.generation || !state.standalone {
			if msg.client != nil {
				_ = msg.client.Close()
			}
			return nil
		}
		state.busy = false
		if msg.err != nil {
			state.status = "连接" + sftpSideLabel(msg.target.sftpSide) + " FTP/FTPS 主机失败：" + msg.err.Error()
			state.statusErr = true
			return nil
		}
		ensureSFTPPaneTabs(state)
		addSFTPPaneTab(state, msg.target.sftpSide, &sftpPaneTab{
			id: msg.target.sftpPaneTabID, remote: true, host: msg.host, client: msg.client,
			ftpPassword: ftpPassword, path: msg.path, entries: msg.entries,
			marks: make(map[string]sftpclient.Entry),
		})
		state.activeSide = msg.target.sftpSide
		state.synchronizedBrowsing = false
		state.status = fmt.Sprintf("%s已打开主机 %s，共 %d 项", sftpSideLabel(msg.target.sftpSide), msg.host.Name, len(msg.entries))
		state.statusErr = false
		m.statusText, m.statusErr = "已连接 "+msg.host.Name, false
		return m.recordRecentHostConnection(msg.host)
	}
	state := m.sftp
	if state == nil || state.generation != msg.generation || state.host.Id != msg.host.Id {
		if msg.client != nil {
			_ = msg.client.Close()
		}
		return nil
	}
	state.busy = false
	if msg.err != nil {
		state.status = "FTP/FTPS 连接失败：" + msg.err.Error()
		state.statusErr = true
		m.statusText, m.statusErr = state.status, true
		return nil
	}
	state.client, state.path, state.entries = msg.client, msg.path, msg.entries
	state.localPath, state.localEntries = msg.localPath, msg.localEntries
	state.ftpPassword = ftpPassword
	rebuildSFTPEntryViews(state)
	ensureSFTPPaneTabs(state)
	state.status = fmt.Sprintf("已连接 %s，共 %d 项", msg.host.Name, len(state.entries))
	state.statusErr = false
	m.statusText, m.statusErr = "已连接 "+msg.host.Name, false
	return m.recordRecentHostConnection(msg.host)
}

func (m *modelState) handleFTPCommandFinished(msg ftpRawCommandFinishedMsg) {
	state := m.sftp
	if state == nil || state.generation != msg.generation {
		return
	}
	state.busy = false
	if msg.path != "" {
		entries := sortSFTPEntries(msg.entries)
		if msg.side == sftpRemoteSide {
			state.path, state.entries = msg.path, entries
			state.remoteFilteredEntries = nil
			state.remoteFilter = sftpEntryFilter{}
			state.selected, state.top = 0, 0
		} else {
			state.peerPath, state.localEntries = msg.path, entries
			state.localFilteredEntries = nil
			state.localFilter = sftpEntryFilter{}
			state.localSelected, state.localTop = 0, 0
		}
		rebuildSFTPEntryViews(state)
	}
	response := msg.response
	truncated := false
	responseRunes := []rune(response)
	if len(responseRunes) > 8192 {
		response = string(responseRunes[:8192])
		truncated = true
	}
	response = strings.ReplaceAll(response, "\r\n", "\n")
	lines := strings.Split(response, "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		lines = []string{"（服务器未返回文本）"}
	}
	for index := range lines {
		lines[index] = sanitizeRemoteText(strings.TrimSuffix(lines[index], "\r"))
	}
	if truncated {
		lines = append(lines, "…回复超过 8192 个字符，已截断")
	}
	errorText := ""
	if msg.err != nil {
		errorText = msg.err.Error()
		lines = append(lines, "错误："+sanitizeRemoteText(errorText))
	}
	state.ftpCommand = &ftpCommandOutput{
		command: sanitizeRemoteText(msg.command), code: msg.code,
		lines: lines, err: errorText,
	}
	state.status = fmt.Sprintf("FTP 响应 %d", msg.code)
	state.statusErr = msg.err != nil || msg.code >= 400
	if msg.code >= 400 && msg.err == nil {
		state.status = fmt.Sprintf("FTP 服务器返回错误 %d", msg.code)
	}
}

func (m *modelState) openSFTPRemotePeer(host model.Host, session *sshclient.Session, generation uint64) tea.Cmd {
	return m.openSFTPRemoteSource(host, session, generation, sftpLocalSide, 0)
}

func (m *modelState) openSFTPRemoteSource(host model.Host, session *sshclient.Session, generation uint64, side sftpSide, paneTabID uint64) tea.Cmd {
	state := m.sftp
	if state == nil || state.generation != generation || !state.standalone {
		_ = session.Close()
		return nil
	}
	state.busy = true
	state.activeSide = side
	state.status = "正在连接" + sftpSideLabel(side) + "远程目录…"
	state.statusErr = false
	return func() tea.Msg {
		client, err := openSSHFileClient(session, host)
		if err != nil {
			return sftpPeerOpenedMsg{generation: generation, paneTabID: paneTabID, side: side, host: host, session: session, err: err}
		}
		remotePath, err := resolveRemoteStartDir(client, host.RemoteStartDir)
		if err != nil {
			_ = client.Close()
			return sftpPeerOpenedMsg{generation: generation, paneTabID: paneTabID, side: side, host: host, session: session, err: err}
		}
		entries, err := client.List(remotePath)
		if err != nil {
			_ = client.Close()
			return sftpPeerOpenedMsg{generation: generation, paneTabID: paneTabID, side: side, host: host, session: session, err: err}
		}
		return sftpPeerOpenedMsg{
			generation: generation, paneTabID: paneTabID, side: side, host: host, session: session, client: client,
			path: remotePath, entries: sortSFTPEntries(entries),
		}
	}
}

func (m *modelState) handleSFTPRemotePeerOpened(msg sftpPeerOpenedMsg) tea.Cmd {
	state := m.sftp
	if state == nil || state.generation != msg.generation || !state.standalone {
		if msg.client != nil {
			_ = msg.client.Close()
		}
		if msg.session != nil {
			_ = msg.session.Close()
		}
		return nil
	}
	state.busy = false
	if msg.err != nil {
		if msg.session != nil {
			_ = msg.session.Close()
		}
		state.status = "连接" + sftpSideLabel(msg.side) + "远程主机失败：" + msg.err.Error()
		state.statusErr = true
		return nil
	}
	ensureSFTPPaneTabs(state)
	addSFTPPaneTab(state, msg.side, &sftpPaneTab{
		id: msg.paneTabID, remote: true, host: msg.host, session: msg.session,
		client: msg.client, ownsSession: true, path: msg.path, entries: msg.entries,
		marks: make(map[string]sftpclient.Entry),
	})
	state.synchronizedBrowsing = false
	state.synchronizedLocalRoot = ""
	state.synchronizedRemoteRoot = ""
	state.activeSide = msg.side
	state.status = fmt.Sprintf("%s已打开远程主机 %s，共 %d 项", sftpSideLabel(msg.side), msg.host.Name, len(msg.entries))
	state.statusErr = false
	return m.recordRecentHostConnection(msg.host)
}

func (m *modelState) openSFTPRemotePeerPicker() {
	state := m.sftp
	if state == nil || !state.standalone || state.busy {
		return
	}
	side := state.activeSide
	hosts := make([]model.Host, 0, len(m.hosts))
	for _, host := range m.hosts {
		if host.Kind == model.HostKindSSH || host.Kind == model.HostKindFTP {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) == 0 {
		state.status = "没有其他 SSH 或 FTP/FTPS 主机可连接"
		state.statusErr = true
		return
	}
	ensureSFTPPaneTabs(state)
	target := connectionTarget{
		kind: sftpPeerTarget, sftpGeneration: state.generation, sftpSide: side,
		sftpPaneTabID: nextSFTPPaneTabID(state),
	}
	m.hostPicker = &hostPickerState{target: target, hosts: hosts}
}

func (m *modelState) cycleSFTPPaneTab(side sftpSide, delta int) {
	state := m.sftp
	if state == nil || !state.standalone || state.busy {
		return
	}
	ensureSFTPPaneTabs(state)
	tabs := state.paneTabs[side]
	if len(tabs) < 2 {
		state.status = sftpSideLabel(side) + "只有一个标签"
		state.statusErr = false
		return
	}
	index := (state.activePaneTab[side] + delta + len(tabs)) % len(tabs)
	saveSFTPActivePaneTab(state, side)
	if !activateSFTPPaneTab(state, side, index) {
		return
	}
	state.status = fmt.Sprintf("已切换%s标签 %d/%d · %s", sftpSideLabel(side), index+1, len(tabs), sftpPaneTabTitle(tabs[index]))
	state.statusErr = false
}

func (m *modelState) requestCloseActiveSFTPPaneTab() {
	state := m.sftp
	if state == nil || !state.standalone || state.busy {
		return
	}
	ensureSFTPPaneTabs(state)
	side := state.activeSide
	tabs := state.paneTabs[side]
	if len(tabs) <= 1 {
		state.status = "每栏至少保留一个标签"
		state.statusErr = true
		return
	}
	tab := tabs[state.activePaneTab[side]]
	state.prompt = &sftpPrompt{
		action:    sftpPromptClosePaneTab,
		title:     fmt.Sprintf("确认关闭%s标签“%s”？按 Enter/y 确认，Esc 取消", sftpSideLabel(side), sftpPaneTabTitle(tab)),
		paneTabID: tab.id, paneTabSide: side,
	}
}

func (m *modelState) closeSFTPPaneTab(side sftpSide, paneTabID uint64) {
	state := m.sftp
	if state == nil || !state.standalone || state.busy {
		return
	}
	ensureSFTPPaneTabs(state)
	saveSFTPActivePaneTab(state, side)
	tabs := state.paneTabs[side]
	if len(tabs) <= 1 {
		state.status = "每栏至少保留一个标签"
		state.statusErr = true
		return
	}
	index := -1
	for candidate, tab := range tabs {
		if tab != nil && tab.id == paneTabID {
			index = candidate
			break
		}
	}
	if index < 0 {
		state.status = "要关闭的标签已不存在"
		state.statusErr = true
		return
	}
	closed := tabs[index]
	closeSFTPPaneTabResources(closed)
	state.paneTabs[side] = append(tabs[:index], tabs[index+1:]...)
	active := state.activePaneTab[side]
	wasActive := index == active
	if index < active {
		active--
	} else if wasActive {
		active = min(index, len(state.paneTabs[side])-1)
	}
	state.activePaneTab[side] = active
	_ = activateSFTPPaneTab(state, side, active)
	state.status = fmt.Sprintf("已关闭%s标签“%s”", sftpSideLabel(side), sftpPaneTabTitle(closed))
	state.statusErr = false
}

func closeSFTPPaneTabResources(tab *sftpPaneTab) {
	if tab == nil {
		return
	}
	if tab.client != nil {
		_ = tab.client.Close()
		tab.client = nil
	}
	if tab.ownsSession && tab.session != nil {
		_ = tab.session.Close()
		tab.session = nil
	}
	tab.ftpPassword = ""
}

func (m *modelState) disconnectSFTPRemotePeer() tea.Cmd {
	state := m.sftp
	if state == nil || state.peerClient == nil {
		return nil
	}
	_ = state.peerClient.Close()
	if state.peerSession != nil {
		_ = state.peerSession.Close()
	}
	state.peerClient = nil
	state.peerSession = nil
	state.peerHost = model.Host{}
	state.peerFTPPassword = ""
	state.peerPath = ""
	state.synchronizedBrowsing = false
	state.synchronizedLocalRoot = ""
	state.synchronizedRemoteRoot = ""
	state.localMarks = make(map[string]sftpclient.Entry)
	state.status = "已断开左侧远程主机，恢复本地目录…"
	state.statusErr = false
	return m.beginSFTPList(sftpLocalSide, state.localPath)
}

func (m *modelState) switchSFTPSourceToLocal(side sftpSide) tea.Cmd {
	state := m.sftp
	if state == nil || !state.standalone || state.busy {
		return nil
	}
	localPath, err := os.Getwd()
	if err != nil {
		state.status, state.statusErr = "读取本地工作目录失败："+err.Error(), true
		return nil
	}
	ensureSFTPPaneTabs(state)
	paneTabID := nextSFTPPaneTabID(state)
	state.activeSide = side
	state.busy = true
	state.synchronizedBrowsing = false
	state.status = "正在读取" + sftpSideLabel(side) + "本地目录…"
	state.statusErr = false
	return func() tea.Msg {
		entries, err := listLocalDirectory(localPath)
		return sftpLocalPaneTabListedMsg{
			generation: state.generation, paneTabID: paneTabID, side: side,
			path: localPath, entries: entries, err: err,
		}
	}
}

func (m *modelState) handleSFTPLocalPaneTabListed(msg sftpLocalPaneTabListedMsg) {
	state := m.sftp
	if state == nil || state.generation != msg.generation || !state.standalone {
		return
	}
	state.busy = false
	if msg.err != nil {
		state.status = "读取本地目录失败：" + msg.err.Error()
		state.statusErr = true
		return
	}
	addSFTPPaneTab(state, msg.side, &sftpPaneTab{
		id: msg.paneTabID, path: msg.path, entries: sortSFTPEntries(msg.entries),
		marks: make(map[string]sftpclient.Entry),
	})
	state.synchronizedBrowsing = false
	state.status = fmt.Sprintf("%s已打开本地目录，共 %d 项", sftpSideLabel(msg.side), len(msg.entries))
	state.statusErr = false
}

func (m *modelState) handleSFTPOpened(msg sftpOpenedMsg) tea.Cmd {
	state := m.sftp
	if state == nil || state.generation != msg.generation || state.paneID != msg.paneID || state.session != msg.session {
		if msg.client != nil {
			_ = msg.client.Close()
		}
		return nil
	}
	state.busy = false
	if msg.err != nil {
		state.status = "远程文件工作区打开失败：" + msg.err.Error()
		if state.queueCancelled {
			state.status = "传输队列已取消；恢复远程文件连接失败：" + msg.err.Error()
			state.queueCancelled = false
		}
		state.statusErr = true
		if state.ownsSession && state.session != nil {
			_ = state.session.Close()
			state.session = nil
		}
		return nil
	}
	state.client = msg.client
	state.path = msg.path
	state.entries = msg.entries
	state.localPath = msg.localPath
	state.localEntries = msg.localEntries
	rebuildSFTPEntryViews(state)
	ensureSFTPPaneTabs(state)
	state.localSelected = clamp(state.localSelected, 0, max(0, len(sftpPaneEntries(state, sftpLocalSide))-1))
	if state.queueCancelled {
		state.status = "传输队列已取消；远程文件连接已恢复"
		state.queueCancelled = false
	} else {
		state.status = fmt.Sprintf("已连接 %s，共 %d 项", state.host.Name, len(sftpPaneEntries(state, sftpRemoteSide)))
	}
	state.statusErr = false
	return m.recordRecentHostConnection(state.host)
}

func (m *modelState) beginSFTPList(side sftpSide, targetPath string) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy || (sftpSideIsRemote(state, side) && sftpClientForSide(state, side) == nil) {
		return nil
	}
	state.requestID++
	requestID := state.requestID
	client := sftpClientForSide(state, side)
	remote := sftpSideIsRemote(state, side)
	if remote {
		targetPath = sftpclient.CleanRemotePath(targetPath)
	} else {
		targetPath = filepath.Clean(targetPath)
	}
	state.busy = true
	state.status = "正在读取目录…"
	state.statusErr = false
	return func() tea.Msg {
		var entries []sftpclient.Entry
		var err error
		if remote {
			entries, err = client.List(targetPath)
			entries = sortSFTPEntries(entries)
		} else {
			entries, err = listLocalDirectory(targetPath)
		}
		return sftpListedMsg{requestID: requestID, generation: state.generation, side: side, path: targetPath, entries: entries, err: err}
	}
}

func (m *modelState) toggleSFTPSynchronizedBrowsing() {
	state := m.sftp
	if state == nil || !state.standalone {
		return
	}
	if state.synchronizedBrowsing {
		state.synchronizedBrowsing = false
		state.synchronizedLocalRoot = ""
		state.synchronizedRemoteRoot = ""
		state.status = "已关闭同步浏览"
		state.statusErr = false
		return
	}
	if state.peerClient == nil || state.client == nil {
		state.status = "同步浏览需要左右两栏都连接远程主机"
		state.statusErr = true
		return
	}
	state.synchronizedLocalRoot = sftpclient.CleanRemotePath(state.peerPath)
	state.synchronizedRemoteRoot = sftpclient.CleanRemotePath(state.path)
	state.synchronizedBrowsing = true
	state.status = "已开启同步浏览；两栏将按相对目录同步切换"
	state.statusErr = false
}

func (m *modelState) navigateSFTPDirectory(side sftpSide, targetPath string) tea.Cmd {
	if m.sftp != nil && m.sftp.synchronizedBrowsing {
		return m.beginSFTPSynchronizedNavigation(side, targetPath)
	}
	return m.beginSFTPList(side, targetPath)
}

func (m *modelState) beginSFTPSynchronizedNavigation(side sftpSide, targetPath string) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy || !state.synchronizedBrowsing {
		return nil
	}
	if state.peerClient == nil || state.client == nil {
		state.synchronizedBrowsing = false
		state.status = "同步浏览已关闭：远程连接不可用"
		state.statusErr = true
		return nil
	}
	targetPath = sftpclient.CleanRemotePath(targetPath)
	localTarget, remoteTarget := "", ""
	if side == sftpLocalSide {
		relative, ok := sftpRelativeRemotePath(state.synchronizedLocalRoot, targetPath)
		if !ok {
			state.status = "已到同步浏览起始目录；按 y 可关闭同步浏览后继续向上"
			state.statusErr = false
			return nil
		}
		localTarget = targetPath
		remoteTarget = joinSFTPRemotePath(state.synchronizedRemoteRoot, relative)
	} else {
		relative, ok := sftpRelativeRemotePath(state.synchronizedRemoteRoot, targetPath)
		if !ok {
			state.status = "已到同步浏览起始目录；按 y 可关闭同步浏览后继续向上"
			state.statusErr = false
			return nil
		}
		remoteTarget = targetPath
		localTarget = joinSFTPRemotePath(state.synchronizedLocalRoot, relative)
	}
	localClient, remoteClient := state.peerClient, state.client
	state.requestID++
	requestID, generation := state.requestID, state.generation
	state.busy = true
	state.status = "正在同步切换两侧目录…"
	state.statusErr = false
	return func() tea.Msg {
		localEntries, err := localClient.List(localTarget)
		if err != nil {
			return sftpSynchronizedListedMsg{requestID: requestID, generation: generation, err: fmt.Errorf("读取左栏目录失败：%w", err)}
		}
		remoteEntries, err := remoteClient.List(remoteTarget)
		if err != nil {
			return sftpSynchronizedListedMsg{requestID: requestID, generation: generation, err: fmt.Errorf("读取右栏目录失败：%w", err)}
		}
		return sftpSynchronizedListedMsg{
			requestID: requestID, generation: generation,
			localPath: localTarget, localEntries: sortSFTPEntries(localEntries),
			remotePath: remoteTarget, remoteEntries: sortSFTPEntries(remoteEntries),
		}
	}
}

func sftpRelativeRemotePath(root, target string) (string, bool) {
	root, target = path.Clean(root), path.Clean(target)
	if target == root {
		return ".", true
	}
	if root == "." {
		if path.IsAbs(target) || target == ".." || strings.HasPrefix(target, "../") {
			return "", false
		}
		return target, true
	}
	prefix := root
	if prefix != "/" {
		prefix += "/"
	}
	if !strings.HasPrefix(target, prefix) {
		return "", false
	}
	relative := strings.TrimPrefix(target, prefix)
	if relative == "" || relative == ".." || strings.HasPrefix(relative, "../") || path.IsAbs(relative) {
		return "", false
	}
	return relative, true
}

func joinSFTPRemotePath(root, relative string) string {
	if relative == "." {
		return path.Clean(root)
	}
	return path.Join(root, relative)
}

func (m *modelState) handleSFTPSynchronizedListed(msg sftpSynchronizedListedMsg) {
	state := m.sftp
	if state == nil || state.requestID != msg.requestID || state.generation != msg.generation {
		return
	}
	state.busy = false
	if msg.err != nil {
		state.status = "同步浏览失败；两栏目录保持不变：" + msg.err.Error()
		state.statusErr = true
		state.pendingSelectionName = ""
		return
	}
	state.peerPath, state.localEntries = msg.localPath, msg.localEntries
	state.path, state.entries = msg.remotePath, msg.remoteEntries
	state.localSelected, state.localTop = 0, 0
	state.selected, state.top = 0, 0
	rebuildSFTPEntryViews(state)
	if state.pendingSelectionName != "" {
		entries := sftpPaneEntries(state, state.pendingSelectionSide)
		selected := 0
		for index, entry := range entries {
			if entry.Name == state.pendingSelectionName {
				selected = index
				break
			}
		}
		if state.pendingSelectionSide == sftpRemoteSide {
			state.selected = selected
			state.top = keepVisible(selected, state.top, m.sftpVisibleRowsFor(state, m.height))
		} else {
			state.localSelected = selected
			state.localTop = keepVisible(selected, state.localTop, m.sftpVisibleRowsFor(state, m.height))
		}
		state.pendingSelectionName = ""
	}
	state.status = "两栏已同步切换到对应目录"
	state.statusErr = false
}

func (m *modelState) handleSFTPListed(msg sftpListedMsg) {
	state := m.sftp
	if state == nil || msg.requestID != state.requestID {
		return
	}
	state.busy = false
	if msg.err != nil {
		state.status = "读取目录失败：" + msg.err.Error()
		state.statusErr = true
		return
	}
	if msg.side == sftpRemoteSide {
		state.path = msg.path
		state.entries = msg.entries
		rebuildSFTPEntryViews(state)
		state.selected = clamp(state.selected, 0, max(0, len(sftpPaneEntries(state, sftpRemoteSide))-1))
		state.top = keepVisible(state.selected, state.top, m.sftpVisibleRowsFor(state, m.height))
	} else if sftpSideIsRemote(state, msg.side) {
		state.peerPath = msg.path
		state.localEntries = msg.entries
		rebuildSFTPEntryViews(state)
		state.localSelected = clamp(state.localSelected, 0, max(0, len(sftpPaneEntries(state, msg.side))-1))
		state.localTop = keepVisible(state.localSelected, state.localTop, m.sftpVisibleRowsFor(state, m.height))
	} else {
		state.localPath = msg.path
		state.localEntries = msg.entries
		rebuildSFTPEntryViews(state)
		state.localSelected = clamp(state.localSelected, 0, max(0, len(sftpPaneEntries(state, msg.side))-1))
		state.localTop = keepVisible(state.localSelected, state.localTop, m.sftpVisibleRowsFor(state, m.height))
	}
	if state.pendingSelectionName != "" && state.pendingSelectionSide == msg.side {
		selected := 0
		for index, entry := range sftpPaneEntries(state, msg.side) {
			if entry.Name == state.pendingSelectionName {
				selected = index
				break
			}
		}
		if msg.side == sftpRemoteSide {
			state.selected = selected
			state.top = keepVisible(selected, state.top, m.sftpVisibleRowsFor(state, m.height))
		} else {
			state.localSelected = selected
			state.localTop = keepVisible(selected, state.localTop, m.sftpVisibleRowsFor(state, m.height))
		}
		state.pendingSelectionName = ""
	}
	state.status = fmt.Sprintf("目录已更新，共 %d 项", len(msg.entries))
	state.statusErr = false
}

func (m *modelState) updateSFTPKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.sftp
	if state == nil {
		return m, nil
	}
	key := msg.Key()
	if state.ftpCommand != nil {
		visible := ftpCommandVisibleRows(m.height)
		switch key.Code {
		case tea.KeyEscape, tea.KeyEnter:
			state.ftpCommand = nil
			state.status, state.statusErr = "已返回文件列表", false
		case tea.KeyUp:
			state.ftpCommand.top = max(0, state.ftpCommand.top-1)
		case tea.KeyDown:
			state.ftpCommand.top = min(max(0, len(state.ftpCommand.lines)-visible), state.ftpCommand.top+1)
		}
		return m, nil
	}
	if state.properties != nil {
		if key.Code == tea.KeyEscape || key.Code == tea.KeyEnter || key.Text == "i" {
			state.properties = nil
			state.status, state.statusErr = "已返回文件列表", false
		}
		return m, nil
	}
	if state.editor != nil && !state.externalEditorOnly {
		if state.prompt != nil {
			return m.updateSFTPPrompt(key)
		}
		return m.updateSFTPTextEditor(key)
	}
	if state.queueReviewActive {
		return m, m.updateSFTPQueueReviewKey(key)
	}
	if state.transferPaused && state.transferPauseGate != nil && key.Code == tea.KeySpace {
		return m, m.toggleSFTPTransferPause()
	}
	if state.prompt != nil {
		return m.updateSFTPPrompt(key)
	}
	if key.Code == tea.KeySpace && state.transferPauseGate != nil {
		return m, m.toggleSFTPTransferPause()
	}
	if state.busy {
		if key.Code == tea.KeyEscape && state.imageEditor != nil && state.imageEditor.cancel != nil {
			state.imageEditor.cancel()
			state.status = "正在取消图片文件处理…"
			state.statusErr = false
		} else if key.Code == tea.KeyEscape && state.searchCancel != nil {
			state.searchCancel()
			state.status = "正在取消文件搜索…"
			if state.searchFilterMode {
				state.status = "正在取消递归过滤…"
			}
			state.statusErr = false
		} else if key.Code == tea.KeyEscape && state.compareCancel != nil {
			state.compareCancel()
			state.status = "正在取消目录比较…"
			state.statusErr = false
		} else if key.Code == tea.KeyEscape && state.mirrorDeleteCancel != nil {
			state.mirrorDeleteCancel()
			state.status = "正在取消镜像删除并恢复远程文件连接…"
			state.statusErr = false
		} else if key.Code == tea.KeyEscape && state.transferCancel != nil {
			if state.transferPauseGate != nil {
				state.transferPauseGate.Resume()
			}
			state.transferPaused = false
			if state.queueActive {
				state.queueCancelled = true
				state.queueActive = false
				state.transferQueue = nil
				state.queueCurrent = nil
				state.queueApproved = nil
				state.queueTotal = 0
				state.queueDone = 0
				state.queueSkipped = 0
				state.queueFailures = nil
				state.queueProgress = nil
				state.syncIssueCount = 0
				state.mirrorPlan = nil
			}
			state.transferCancel()
			state.status = "正在取消传输并恢复远程文件连接…"
			state.statusErr = false
		}
		return m, nil
	}
	if state.comparison != nil {
		return m, m.updateSFTPComparisonKey(key)
	}
	if key.Text == "F" || (key.Mod&tea.ModShift != 0 && key.Code == 'f') {
		return m, m.openSFTPFilterManager()
	}
	if key.Text == "B" || (key.Mod&tea.ModShift != 0 && key.Code == 'b') {
		return m, m.openSFTPBookmarkManager()
	}
	if state.searchActive {
		switch key.Code {
		case tea.KeyEscape:
			state.searchActive = false
			state.searchFilterMode = false
			state.searchResults = nil
			state.status, state.statusErr = "已返回文件列表", false
			return m, nil
		case tea.KeyUp:
			state.searchSelected = clamp(state.searchSelected-1, 0, max(0, len(state.searchResults)-1))
			state.searchTop = keepVisible(state.searchSelected, state.searchTop, max(1, m.height-6))
			return m, nil
		case tea.KeyDown:
			state.searchSelected = clamp(state.searchSelected+1, 0, max(0, len(state.searchResults)-1))
			state.searchTop = keepVisible(state.searchSelected, state.searchTop, max(1, m.height-6))
			return m, nil
		case tea.KeyEnter:
			return m, m.openSFTPSearchResult()
		}
		switch strings.ToLower(key.Text) {
		case "j":
			state.searchSelected = clamp(state.searchSelected+1, 0, max(0, len(state.searchResults)-1))
			state.searchTop = keepVisible(state.searchSelected, state.searchTop, max(1, m.height-6))
		case "k":
			state.searchSelected = clamp(state.searchSelected-1, 0, max(0, len(state.searchResults)-1))
			state.searchTop = keepVisible(state.searchSelected, state.searchTop, max(1, m.height-6))
		case "/":
			m.openSFTPFileSearch()
		}
		return m, nil
	}
	if key.Text == "E" || (key.Mod&tea.ModShift != 0 && key.Code == 'e') {
		m.openSFTPExternalEditorCommandPrompt()
		return m, nil
	}
	if key.Code == tea.KeyEscape {
		return m, nil
	}
	if key.Mod&tea.ModCtrl != 0 && (key.Code == 'w' || key.Code == 'W') {
		m.requestCloseActiveSFTPPaneTab()
		return m, nil
	}
	if state.standalone && (key.Text == "[" || key.Text == "]") {
		delta := -1
		if key.Text == "]" {
			delta = 1
		}
		m.cycleSFTPPaneTab(state.activeSide, delta)
		return m, nil
	}
	if key.Text == "q" {
		if tab := m.activeTab(); tab != nil && tab.sftp == state {
			m.requestTabClose(tab.id)
			return m, nil
		}
		m.closeSFTPBrowser()
		return m, nil
	}
	switch key.Code {
	case tea.KeySpace:
		if state.standalone {
			m.toggleSFTPMark()
		}
		return m, nil
	case tea.KeyTab:
		if state.standalone {
			if state.activeSide == sftpLocalSide {
				state.activeSide = sftpRemoteSide
			} else {
				state.activeSide = sftpLocalSide
			}
		}
		return m, nil
	case tea.KeyLeft:
		if state.standalone {
			state.activeSide = sftpLocalSide
		}
		return m, nil
	case tea.KeyRight:
		if state.standalone {
			state.activeSide = sftpRemoteSide
		}
		return m, nil
	case tea.KeyUp:
		m.moveSFTPSelection(-1)
		return m, nil
	case tea.KeyDown:
		m.moveSFTPSelection(1)
		return m, nil
	case tea.KeyEnter:
		return m, m.enterSFTPSelection()
	case tea.KeyBackspace:
		return m, m.goSFTPParent()
	}
	if key.Text == ":" {
		if !state.standalone || ftpClientForSide(state, state.activeSide) == nil {
			state.status, state.statusErr = "原始 FTP 命令只适用于 FTP/FTPS 远程栏", true
			return m, nil
		}
		m.openSFTPRawCommand()
		return m, nil
	}
	switch strings.ToLower(key.Text) {
	case "k":
		m.moveSFTPSelection(-1)
	case "j":
		m.moveSFTPSelection(1)
	case "r":
		return m, m.beginSFTPList(state.activeSide, m.currentSFTPPath(state.activeSide))
	case "g":
		m.openSFTPPrompt(sftpPromptGoToPath, "跳转到路径（支持绝对路径或相对当前目录）", "")
	case "c":
		m.openSFTPRemotePeerPicker()
	case "b":
		return m, m.switchSFTPSourceToLocal(state.activeSide)
	case "u":
		if state.standalone {
			return m, m.beginSFTPTransfer(sftpUpload)
		}
		m.openSFTPPrompt(sftpPromptUpload, "输入本地文件或目录路径", "")
		state.prompt.transfer.direction = sftpUpload
		state.prompt.transfer.side = sftpRemoteSide
	case "d":
		if state.standalone {
			return m, m.beginSFTPTransfer(sftpDownload)
		}
		entry := m.selectedSFTPEntry(sftpRemoteSide)
		if entry == nil {
			state.status, state.statusErr = "请先选择要下载的远程文件或目录", true
			return m, nil
		}
		if entry.IsSymlink || (!entry.IsDir && !entry.Mode.IsRegular()) {
			state.status, state.statusErr = "当前仅支持下载普通文件和目录", true
			return m, nil
		}
		m.openSFTPPrompt(sftpPromptDownload, "保存到本地路径", filepath.Join(state.localPath, entry.Name))
		state.prompt.transfer = &sftpTransferRequest{
			direction: sftpDownload, side: sftpLocalSide, directory: entry.IsDir,
			remotePath:   m.sftpEntryPath(sftpRemoteSide, entry.Name),
			sourceRemote: true, sourcePath: m.sftpEntryPath(sftpRemoteSide, entry.Name),
			destPath: filepath.Join(state.localPath, entry.Name),
		}
	case "m":
		if state.standalone {
			m.openSFTPPrompt(sftpPromptMkdir, "新建目录名", "")
			return m, nil
		}
		m.openSFTPPrompt(sftpPromptMkdir, "新建目录名", "")
	case "n":
		side := sftpRemoteSide
		if state.standalone {
			side = state.activeSide
		}
		entry := m.selectedSFTPEntry(side)
		if entry == nil {
			return m, nil
		}
		m.openSFTPPrompt(sftpPromptRename, "重命名为（仅输入新名称）", entry.Name)
		state.prompt.transfer = &sftpTransferRequest{side: side, entryPath: m.sftpEntryPath(side, entry.Name)}
	case "p":
		side := sftpRemoteSide
		if state.standalone {
			side = state.activeSide
		}
		entry := m.selectedSFTPEntry(side)
		if entry == nil {
			return m, nil
		}
		if !sftpSideSupportsPermissions(state, side) {
			state.status, state.statusErr = "当前文件系统不支持权限修改", true
			return m, nil
		}
		if entry.IsSymlink || (!entry.IsDir && entry.ModeKnown && !entry.Mode.IsRegular()) {
			state.status, state.statusErr = "仅支持修改普通文件或目录的权限", true
			return m, nil
		}
		permissions := entry.Mode.Perm()
		if !entry.ModeKnown {
			permissions = 0o644
			if entry.IsDir {
				permissions = 0o755
			}
		}
		m.openSFTPPrompt(sftpPromptChmod, "输入八进制权限（如 644 或 0755）", fmt.Sprintf("%03o", permissions))
		state.prompt.transfer = &sftpTransferRequest{side: side, entryPath: m.sftpEntryPath(side, entry.Name)}
	case "i":
		side := sftpRemoteSide
		if state.standalone {
			side = state.activeSide
		}
		entry := m.selectedSFTPEntry(side)
		if entry == nil {
			state.status, state.statusErr = "请先选择要查看属性的文件或目录", true
			return m, nil
		}
		state.properties = &sftpProperties{side: side, entry: *entry, path: m.sftpEntryPath(side, entry.Name)}
	case "y":
		m.toggleSFTPSynchronizedBrowsing()
	case "/":
		m.openSFTPFileSearch()
	case "f":
		m.openSFTPFilterPrompt()
	case "=":
		return m, m.startSFTPDirectoryComparison()
	case "e":
		side := sftpRemoteSide
		if state.standalone {
			side = state.activeSide
		}
		if entry := m.selectedSFTPEntry(side); entry != nil && isSFTPImageName(entry.Name) {
			return m, m.openSFTPImageEditor(true)
		}
		return m, m.openSFTPTextEditor(sftpEditMode)
	case "v":
		side := sftpRemoteSide
		if state.standalone {
			side = state.activeSide
		}
		if entry := m.selectedSFTPEntry(side); entry != nil && isSFTPImageName(entry.Name) {
			return m, m.openSFTPImageEditor(false)
		}
		return m, m.openSFTPTextEditor(sftpPreviewMode)
	case "x":
		side := sftpRemoteSide
		if state.standalone {
			side = state.activeSide
		}
		entry := m.selectedSFTPEntry(side)
		if entry == nil {
			return m, nil
		}
		state.prompt = &sftpPrompt{
			action:   sftpPromptDelete,
			title:    "确认删除所选项目？目录将递归删除。输入 y 确认，其他键取消",
			value:    "",
			transfer: &sftpTransferRequest{side: side, directory: entry.IsDir && !entry.IsSymlink, entryPath: m.sftpEntryPath(side, entry.Name)},
		}
	case "?":
		state.status = "↑↓/j/k 选择 Enter进入 [ ]切换当前栏标签 Ctrl+W关闭标签 c新建远程标签 b新建本地标签 g跳转路径 /搜索 f过滤 F过滤方案 B目录书签 Backspace上级 u上传 d下载 v预览 e编辑图片/文本 E设置编辑器 i属性 n重命名 p权限 m建目录 x删除 r刷新 Esc返回"
		state.statusErr = false
	}
	return m, nil
}

func (m *modelState) updateSFTPPrompt(key tea.Key) (tea.Model, tea.Cmd) {
	state := m.sftp
	prompt := state.prompt
	if prompt == nil {
		return m, nil
	}
	if prompt.action == sftpPromptClosePaneTab {
		if key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "y") {
			side, paneTabID := prompt.paneTabSide, prompt.paneTabID
			state.prompt = nil
			m.closeSFTPPaneTab(side, paneTabID)
			return m, nil
		}
		if key.Code == tea.KeyEscape || key.Text != "" {
			state.prompt = nil
			state.status, state.statusErr = "已取消关闭标签", false
		}
		return m, nil
	}
	if prompt.action == sftpPromptEditorFind || prompt.action == sftpPromptEditorReplaceFind || prompt.action == sftpPromptEditorReplaceValue {
		return m.updateSFTPTextEditorPrompt(key)
	}
	if prompt.action == sftpPromptExternalEditorCommand && key.Code == tea.KeyEnter {
		command := strings.TrimSpace(prompt.value)
		if command != "" {
			if _, err := parseEditorCommand(command); err != nil {
				prompt.title = "编辑器命令无效：" + err.Error()
				return m, nil
			}
		}
		state.prompt = nil
		return m, m.saveSFTPExternalEditorCommand(command, state)
	}
	if prompt.action == sftpPromptFilter && key.Mod&tea.ModCtrl != 0 && (key.Code == 'r' || key.Code == 'R') {
		prompt.recursive = !prompt.recursive
		scope := "当前目录"
		if prompt.recursive {
			scope = "递归子目录"
		}
		prompt.title = "文件过滤（" + scope + "）：*.go, size>=1MiB；expr: 支持括号、&&、||、! · Ctrl+R 切换 · Ctrl+S 保存"
		return m, nil
	}
	if prompt.action == sftpPromptMirrorConfirm {
		switch strings.ToLower(key.Text) {
		case "y":
			state.prompt = nil
			return m, m.confirmSFTPMirrorPlan()
		default:
			if key.Code == tea.KeyEscape || key.Code == tea.KeyEnter || key.Text != "" {
				state.prompt = nil
				state.mirrorPlan = nil
				state.status, state.statusErr = "镜像同步已取消，未执行任何更改", false
			}
			return m, nil
		}
	}
	if prompt.action == sftpPromptFilter && key.Mod&tea.ModCtrl != 0 && (key.Code == 's' || key.Code == 'S') {
		if _, ok := m.catalog.(sftpFilterCatalog); !ok {
			state.status, state.statusErr = "当前目录存储不支持已保存过滤方案", true
			return m, nil
		}
		expression := strings.TrimSpace(prompt.value)
		if expression == "" {
			state.status, state.statusErr = "空过滤条件用于清除过滤，不能保存为空方案", true
			return m, nil
		}
		if _, err := parseSFTPEntryFilter(expression); err != nil {
			state.status, state.statusErr = err.Error(), true
			prompt.title = "过滤规则无效：" + err.Error()
			return m, nil
		}
		side := prompt.transfer.side
		state.prompt = &sftpPrompt{
			action: sftpPromptSaveFilterName, title: "输入过滤方案名称", transfer: &sftpTransferRequest{side: side},
			filterExpression: expression,
		}
		return m, nil
	}
	if prompt.action == sftpPromptDelete || prompt.action == sftpPromptOverwrite {
		switch strings.ToLower(key.Text) {
		case "y":
			transfer := *prompt.transfer
			state.prompt = nil
			if prompt.action == sftpPromptDelete {
				return m, m.deleteSFTPEntry(transfer)
			}
			if state.queueActive {
				state.queueApproved = append(state.queueApproved, sftpQueueJob{transfer: transfer, overwrite: true})
				state.queueCurrent = nil
				return m, m.startNextSFTPTransfer()
			}
			return m, m.startSFTPTransfer(transfer, true)
		case "q":
			if prompt.action == sftpPromptOverwrite && state.queueActive {
				state.prompt = nil
				m.cancelSFTPTransferQueue("传输队列已取消")
				return m, nil
			}
		default:
			if key.Code == tea.KeyEscape || key.Code == tea.KeyEnter || key.Text != "" {
				state.prompt = nil
				if prompt.action == sftpPromptOverwrite && state.queueActive {
					state.queueSkipped++
					state.queueDone++
					state.queueCurrent = nil
					state.status = "已跳过 " + sftpTransferLabel(*prompt.transfer)
					state.statusErr = false
					return m, m.startNextSFTPTransfer()
				}
				state.status = "操作已取消"
				state.statusErr = false
			}
			return m, nil
		}
	}
	if prompt.action == sftpPromptDiscard {
		switch strings.ToLower(key.Text) {
		case "s":
			state.prompt = nil
			if state.editor != nil {
				state.editor.closeAfterSave = true
			}
			return m, m.saveSFTPTextEditor()
		case "d":
			state.prompt = nil
			state.editor = nil
			state.status, state.statusErr = "已放弃未保存的修改", false
			return m, nil
		default:
			if key.Code == tea.KeyEscape || key.Text != "" {
				state.prompt = nil
			}
			return m, nil
		}
	}
	switch key.Code {
	case tea.KeyEscape:
		state.prompt = nil
		return m, nil
	case tea.KeyEnter:
		if prompt.action == sftpPromptSaveFilterName {
			name := strings.TrimSpace(prompt.value)
			if name == "" {
				state.status, state.statusErr = "请输入过滤方案名称", true
				return m, nil
			}
			filter := model.SFTPFilter{Name: name, Expression: prompt.filterExpression}
			state.prompt = nil
			return m, m.saveSFTPFilterCmd(filter)
		}
		if prompt.action == sftpPromptSearch {
			query := strings.TrimSpace(prompt.value)
			if query == "" {
				state.status, state.statusErr = "请输入要搜索的文件名", true
				return m, nil
			}
			side := prompt.transfer.side
			root := m.currentSFTPPath(side)
			state.prompt = nil
			return m, m.startSFTPFileSearch(side, root, query)
		}
		if prompt.action == sftpPromptFilter {
			filter, err := parseSFTPEntryFilter(prompt.value)
			if err != nil {
				state.status, state.statusErr = err.Error(), true
				prompt.title = "过滤规则无效：" + err.Error()
				return m, nil
			}
			side := prompt.transfer.side
			if prompt.recursive && filter.raw != "" {
				root := m.currentSFTPPath(side)
				state.prompt = nil
				return m, m.startSFTPFilterSearch(side, root, filter)
			}
			state.prompt = nil
			setSFTPFilter(state, side, filter)
			if filter.raw == "" {
				state.status, state.statusErr = "已清除当前栏过滤条件", false
			} else {
				state.status, state.statusErr = "已应用文件过滤："+filter.raw, false
			}
			return m, nil
		}
		if prompt.action == sftpPromptChmod {
			mode, err := parseSFTPPermissions(prompt.value)
			if err != nil {
				state.status, state.statusErr = err.Error(), true
				return m, nil
			}
			transfer := *prompt.transfer
			transfer.permissions = mode
			state.prompt = nil
			return m, m.changeSFTPEntryPermissions(transfer)
		}
		if prompt.action == sftpPromptFTPCommand {
			command, err := ftpclient.ValidateRawCommand(prompt.value)
			if err != nil {
				state.status, state.statusErr = err.Error(), true
				prompt.title = "FTP 命令无效：" + err.Error()
				return m, nil
			}
			side := prompt.transfer.side
			state.prompt = nil
			return m, m.runSFTPFTPCommand(side, command)
		}
		if prompt.action == sftpPromptGoToPath {
			side := prompt.transfer.side
			targetPath, err := resolveSFTPPath(m.currentSFTPPath(side), prompt.value, sftpSideIsRemote(state, side))
			if err != nil {
				state.status, state.statusErr = err.Error(), true
				prompt.title = "路径无效：" + err.Error()
				return m, nil
			}
			state.prompt = nil
			return m, m.navigateSFTPDirectory(side, targetPath)
		}
		value := strings.TrimSpace(prompt.value)
		if value == "" {
			state.status = "请输入有效名称"
			state.statusErr = true
			return m, nil
		}
		if value == "." || value == ".." || strings.ContainsAny(value, "/\\") {
			state.status = "名称不能包含路径分隔符，也不能是 . 或 .."
			state.statusErr = true
			return m, nil
		}
		state.prompt = nil
		switch prompt.action {
		case sftpPromptUpload:
			localPath := expandLocalPath(value)
			info, err := os.Lstat(localPath)
			if err != nil {
				state.status, state.statusErr = "读取本地源失败："+err.Error(), true
				return m, nil
			}
			if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
				state.status, state.statusErr = "仅支持上传普通文件和目录", true
				return m, nil
			}
			transfer := sftpTransferRequest{
				direction: sftpUpload, side: sftpRemoteSide, directory: info.IsDir(),
				localPath:  localPath,
				remotePath: path.Join(state.path, filepath.Base(localPath)),
				sourcePath: localPath, destPath: path.Join(state.path, filepath.Base(localPath)), destRemote: true,
			}
			return m, m.startSFTPTransfer(transfer, false)
		case sftpPromptDownload:
			transfer := *prompt.transfer
			transfer.localPath = expandLocalPath(value)
			transfer.destPath = transfer.localPath
			return m, m.startSFTPTransfer(transfer, false)
		case sftpPromptMkdir:
			return m, m.makeSFTPDirectory(prompt.transfer.side, value)
		case sftpPromptRename:
			return m, m.renameSFTPEntry(*prompt.transfer, value)
		}
	case tea.KeyBackspace:
		value := []rune(prompt.value)
		if len(value) > 0 {
			prompt.value = string(value[:len(value)-1])
		}
		return m, nil
	}
	limit := 1024
	if prompt.action == sftpPromptSaveFilterName {
		limit = 80
	}
	if key.Text != "" && key.Mod == 0 && len([]rune(prompt.value))+len([]rune(key.Text)) <= limit &&
		(prompt.action != sftpPromptExternalEditorCommand || len(prompt.value)+len(key.Text) <= limit) {
		prompt.value += key.Text
	}
	return m, nil
}

func parseSFTPPermissions(value string) (os.FileMode, error) {
	if len(value) < 3 || len(value) > 4 || (len(value) == 4 && value[0] != '0') {
		return 0, errors.New("权限请使用三位八进制数字，例如 644 或 0755")
	}
	parsed, err := strconv.ParseUint(value, 8, 9)
	if err != nil || parsed > 0o777 {
		return 0, errors.New("权限只能是 000 到 777 的八进制数字")
	}
	return os.FileMode(parsed), nil
}

func (m *modelState) openSFTPPrompt(action sftpPromptAction, title, value string) {
	if m.sftp == nil {
		return
	}
	m.sftp.prompt = &sftpPrompt{
		action: action, title: title, value: value,
		transfer: &sftpTransferRequest{side: m.sftp.activeSide},
	}
}

func (m *modelState) openSFTPRawCommand() {
	state := m.sftp
	if state == nil || state.busy {
		return
	}
	side := state.activeSide
	if ftpClientForSide(state, side) == nil {
		state.status, state.statusErr = "当前栏不是 FTP/FTPS 连接", true
		return
	}
	m.openSFTPPrompt(sftpPromptFTPCommand, "FTP 原始命令 · 仅单行非传输命令；服务器会执行", "")
	state.prompt.transfer.side = side
}

func resolveSFTPPath(currentPath, input string, remote bool) (string, error) {
	for _, character := range input {
		if unicode.IsControl(character) {
			return "", errors.New("路径不能包含控制字符")
		}
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return "", errors.New("请输入目标路径")
	}
	if len(input) > 4096 {
		return "", errors.New("路径不能超过 4096 字节")
	}
	if remote {
		if path.IsAbs(input) {
			return path.Clean(input), nil
		}
		if currentPath == "" {
			currentPath = "."
		}
		return path.Clean(path.Join(currentPath, input)), nil
	}
	if input == "~" || strings.HasPrefix(input, "~/") {
		input = expandLocalPath(input)
	}
	if !filepath.IsAbs(input) {
		if currentPath == "" {
			currentPath = "."
		}
		input = filepath.Join(currentPath, input)
	}
	return filepath.Clean(input), nil
}

func (m *modelState) runSFTPFTPCommand(side sftpSide, command string) tea.Cmd {
	state := m.sftp
	client := ftpClientForSide(state, side)
	if state == nil || state.busy || client == nil {
		if state != nil {
			state.status, state.statusErr = "FTP 连接不可用或正在执行其他操作", true
		}
		return nil
	}
	generation := state.generation
	state.busy = true
	state.status, state.statusErr = "正在发送 FTP 命令…", false
	return func() tea.Msg {
		code, response, err := client.RawCommand(context.Background(), command)
		var remotePath string
		var entries []sftpclient.Entry
		if err == nil && code >= 200 && code < 400 {
			fields := strings.Fields(command)
			if len(fields) > 0 && (strings.EqualFold(fields[0], "CWD") || strings.EqualFold(fields[0], "CDUP")) {
				remotePath, err = client.WorkingDirectory()
				if err == nil {
					entries, err = client.List(remotePath)
				}
			}
		}
		return ftpRawCommandFinishedMsg{
			generation: generation, side: side, command: command, code: code,
			response: response, path: remotePath, entries: sortSFTPEntries(entries), err: err,
		}
	}
}

func (m *modelState) openSFTPFileSearch() {
	state := m.sftp
	if state == nil || state.busy {
		return
	}
	side := sftpRemoteSide
	if state.standalone {
		side = state.activeSide
	}
	m.openSFTPPrompt(sftpPromptSearch, "递归搜索文件名（不区分大小写）", "")
	state.prompt.transfer.side = side
}

func (m *modelState) openSFTPFilterPrompt() {
	state := m.sftp
	if state == nil || state.busy {
		return
	}
	side := sftpRemoteSide
	if state.standalone {
		side = state.activeSide
	}
	m.openSFTPPrompt(sftpPromptFilter, "文件过滤（当前目录）：*.go, size>=1MiB, uid=1000；expr: 支持括号、&&、||、! · Ctrl+R 切换 · Ctrl+S 保存", sftpFilterForSide(state, side).raw)
	state.prompt.transfer.side = side
}

func (m *modelState) startSFTPFileSearch(side sftpSide, root, query string) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy {
		return nil
	}
	remote := sftpSideIsRemote(state, side)
	client := sftpClientForSide(state, side)
	if remote && client == nil {
		state.status, state.statusErr = "远程文件连接不可用", true
		return nil
	}
	root = strings.TrimSpace(root)
	if remote {
		root = sftpclient.CleanRemotePath(root)
	} else {
		root = filepath.Clean(root)
	}
	ctx, cancel := context.WithCancel(context.Background())
	state.requestID++
	requestID := state.requestID
	generation := state.generation
	state.searchActive = true
	state.searchFilterMode = false
	state.searchQuery = query
	state.searchRoot = root
	state.searchSide = side
	state.searchResults = nil
	state.searchSelected = 0
	state.searchTop = 0
	state.searchTruncated = false
	state.searchCancel = cancel
	state.busy = true
	state.status = "正在递归搜索文件…"
	state.statusErr = false
	return func() tea.Msg {
		results := make([]sftpSearchResult, 0)
		var truncated bool
		var err error
		if remote {
			var matches []sftpclient.SearchMatch
			matches, truncated, err = client.Search(ctx, root, query, sftpclient.MaxSearchResults)
			if err == nil {
				for _, match := range matches {
					results = append(results, sftpSearchResult{Path: match.Path, Entry: match.Entry})
				}
			}
		} else {
			results, truncated, err = searchLocalSFTP(ctx, root, query, sftpclient.MaxSearchResults)
		}
		return sftpSearchCompletedMsg{
			requestID: requestID, generation: generation, side: side, root: root, query: query,
			results: results, truncated: truncated, err: err,
		}
	}
}

func (m *modelState) handleSFTPFileSearchCompleted(msg sftpSearchCompletedMsg) {
	state := m.sftp
	if state == nil || state.requestID != msg.requestID || state.generation != msg.generation {
		return
	}
	if state.searchCancel != nil {
		state.searchCancel()
		state.searchCancel = nil
	}
	state.busy = false
	if msg.err != nil {
		state.searchActive = false
		state.searchFilterMode = false
		state.searchResults = nil
		if errors.Is(msg.err, context.Canceled) {
			state.status = "文件搜索已取消"
			if msg.filterMode {
				state.status = "递归过滤已取消"
			}
			state.statusErr = false
		} else {
			label := "文件搜索失败："
			if msg.filterMode {
				label = "递归过滤失败："
			}
			state.status, state.statusErr = label+msg.err.Error(), true
		}
		return
	}
	state.searchActive = true
	state.searchFilterMode = msg.filterMode
	state.searchSide = msg.side
	state.searchRoot = msg.root
	state.searchQuery = msg.query
	state.searchResults = msg.results
	state.searchSelected = 0
	state.searchTop = 0
	state.searchTruncated = msg.truncated
	state.status = fmt.Sprintf("搜索完成：找到 %d 项", len(msg.results))
	if msg.filterMode {
		state.status = fmt.Sprintf("递归过滤完成：找到 %d 项", len(msg.results))
	}
	if msg.truncated {
		state.status += "（结果已达到上限）"
	}
	if len(msg.results) == 0 {
		state.status = "没有找到匹配项目"
	}
	state.statusErr = false
}

func (m *modelState) openSFTPSearchResult() tea.Cmd {
	state := m.sftp
	if state == nil || state.searchSelected < 0 || state.searchSelected >= len(state.searchResults) {
		return nil
	}
	result := state.searchResults[state.searchSelected]
	side := state.searchSide
	parent := filepath.Dir(result.Path)
	if sftpSideIsRemote(state, side) {
		parent = path.Dir(result.Path)
	}
	state.searchActive = false
	state.searchFilterMode = false
	state.searchResults = nil
	setSFTPFilter(state, side, sftpEntryFilter{})
	state.pendingSelectionSide = side
	state.pendingSelectionName = result.Entry.Name
	return m.navigateSFTPDirectory(side, parent)
}

func searchLocalSFTP(ctx context.Context, root, query string, resultLimit int) ([]sftpSearchResult, bool, error) {
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return nil, false, fmt.Errorf("读取本地搜索目录失败：%w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, false, errors.New("搜索起点必须是普通目录")
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, false, errors.New("搜索内容不能为空")
	}
	if resultLimit <= 0 || resultLimit > sftpclient.MaxSearchResults {
		resultLimit = sftpclient.MaxSearchResults
	}
	results := make([]sftpSearchResult, 0)
	truncated := false
	directories := 0
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == root {
			return nil
		}
		if entry.IsDir() {
			directories++
			if directories > 100000 {
				truncated = true
				return fs.SkipAll
			}
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return fmt.Errorf("读取本地项目 %s 失败：%w", current, err)
		}
		if strings.Contains(strings.ToLower(entry.Name()), query) {
			if len(results) == resultLimit {
				truncated = true
				return fs.SkipAll
			}
			mode := fileInfo.Mode()
			results = append(results, sftpSearchResult{
				Path: current,
				Entry: sftpclient.Entry{
					Name: entry.Name(), Size: fileInfo.Size(), Mode: mode, ModeKnown: true,
					ModTime: fileInfo.ModTime(), IsDir: mode.IsDir(), IsSymlink: mode&os.ModeSymlink != 0,
				},
			})
		}
		return nil
	})
	if errors.Is(err, fs.SkipAll) {
		err = nil
	}
	if err != nil {
		return nil, truncated, err
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Path < results[j].Path })
	return results, truncated, nil
}

func (m *modelState) moveSFTPSelection(delta int) {
	state := m.sftp
	if state == nil {
		return
	}
	if state.activeSide == sftpRemoteSide {
		entries := sftpPaneEntries(state, sftpRemoteSide)
		if len(entries) == 0 {
			return
		}
		state.selected = clamp(state.selected+delta, 0, len(entries)-1)
		state.top = keepVisible(state.selected, state.top, m.sftpVisibleRowsFor(state, m.height))
		return
	}
	entries := sftpPaneEntries(state, sftpLocalSide)
	if len(entries) == 0 {
		return
	}
	state.localSelected = clamp(state.localSelected+delta, 0, len(entries)-1)
	state.localTop = keepVisible(state.localSelected, state.localTop, m.sftpVisibleRowsFor(state, m.height))
}

func (m *modelState) selectedSFTPEntry(side sftpSide) *sftpclient.Entry {
	state := m.sftp
	if state == nil {
		return nil
	}
	entries := sftpPaneEntries(state, side)
	if side == sftpRemoteSide {
		if state.selected < 0 || state.selected >= len(entries) {
			return nil
		}
		return &entries[state.selected]
	}
	if state.localSelected < 0 || state.localSelected >= len(entries) {
		return nil
	}
	return &entries[state.localSelected]
}

func (m *modelState) sftpMarks(side sftpSide) map[string]sftpclient.Entry {
	if m.sftp == nil {
		return nil
	}
	if side == sftpRemoteSide {
		if m.sftp.remoteMarks == nil {
			m.sftp.remoteMarks = make(map[string]sftpclient.Entry)
		}
		return m.sftp.remoteMarks
	}
	if m.sftp.localMarks == nil {
		m.sftp.localMarks = make(map[string]sftpclient.Entry)
	}
	return m.sftp.localMarks
}

func (m *modelState) toggleSFTPMark() {
	state := m.sftp
	if state == nil || !state.standalone {
		return
	}
	entry := m.selectedSFTPEntry(state.activeSide)
	if entry == nil {
		return
	}
	if entry.IsSymlink || (!entry.IsDir && !entry.Mode.IsRegular()) {
		state.status, state.statusErr = "符号链接和特殊文件不能加入传输选择", true
		return
	}
	entryPath := m.sftpEntryPath(state.activeSide, entry.Name)
	marks := m.sftpMarks(state.activeSide)
	if _, exists := marks[entryPath]; exists {
		delete(marks, entryPath)
		state.status = "已取消选择 " + entry.Name
	} else {
		marks[entryPath] = *entry
		state.status = "已选择 " + entry.Name
	}
	state.statusErr = false
}

func (m modelState) sftpEntryMarked(state *sftpBrowserState, side sftpSide, entry sftpclient.Entry) bool {
	if state == nil {
		return false
	}
	entryPath := m.sftpPathJoin(state, side, entry.Name)
	marks := state.localMarks
	if side == sftpRemoteSide {
		marks = state.remoteMarks
	}
	_, marked := marks[entryPath]
	return marked
}

func (m *modelState) currentSFTPPath(side sftpSide) string {
	if m.sftp == nil {
		return ""
	}
	if side == sftpRemoteSide {
		return m.sftp.path
	}
	if sftpSideIsRemote(m.sftp, side) {
		return m.sftp.peerPath
	}
	return m.sftp.localPath
}

func (m *modelState) sftpEntryPath(side sftpSide, name string) string {
	return m.sftpPathJoin(m.sftp, side, name)
}

func (m modelState) sftpPathJoin(state *sftpBrowserState, side sftpSide, name string) string {
	if sftpSideIsRemote(state, side) {
		return path.Join(m.currentSFTPPathFor(state, side), name)
	}
	return filepath.Join(m.currentSFTPPathFor(state, side), name)
}

func (m modelState) currentSFTPPathFor(state *sftpBrowserState, side sftpSide) string {
	if state == nil {
		return ""
	}
	if side == sftpRemoteSide {
		return state.path
	}
	if state.peerClient != nil {
		return state.peerPath
	}
	return state.localPath
}

func sftpSideIsRemote(state *sftpBrowserState, side sftpSide) bool {
	if state == nil {
		return false
	}
	if side == sftpRemoteSide {
		return !state.primaryLocal
	}
	return state.peerClient != nil
}

func sftpClientForSide(state *sftpBrowserState, side sftpSide) remotefs.Client {
	if state == nil {
		return nil
	}
	if side == sftpRemoteSide {
		if state.primaryLocal {
			return nil
		}
		return state.client
	}
	if state.peerClient != nil {
		return state.peerClient
	}
	return nil
}

func ftpClientForSide(state *sftpBrowserState, side sftpSide) *ftpclient.Client {
	client, _ := sftpClientForSide(state, side).(*ftpclient.Client)
	return client
}

func (m *modelState) beginSFTPTransfer(direction sftpTransferDirection) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy {
		return nil
	}
	sourceSide := sftpLocalSide
	if direction == sftpDownload {
		sourceSide = sftpRemoteSide
	}
	transfers := make([]sftpTransferRequest, 0, 1)
	if state.standalone {
		marks := m.sftpMarks(sourceSide)
		if len(marks) > 0 {
			paths := make([]string, 0, len(marks))
			for entryPath := range marks {
				paths = append(paths, entryPath)
			}
			sort.Strings(paths)
			for _, entryPath := range paths {
				entry := marks[entryPath]
				if entry.IsSymlink || (!entry.IsDir && !entry.Mode.IsRegular()) {
					state.status, state.statusErr = "选择中包含不支持的文件类型", true
					return nil
				}
				transfers = append(transfers, m.sftpTransferForEntry(direction, entryPath, entry))
			}
		} else {
			entry := m.selectedSFTPEntry(sourceSide)
			if entry == nil {
				state.status, state.statusErr = "请先在对应栏选择一个文件或目录", true
				return nil
			}
			if entry.IsSymlink || (!entry.IsDir && !entry.Mode.IsRegular()) {
				state.status, state.statusErr = "当前仅支持普通文件和目录", true
				return nil
			}
			transfers = append(transfers, m.sftpTransferForEntry(direction, m.sftpEntryPath(sourceSide, entry.Name), *entry))
		}
		if len(transfers) > 1 {
			state.transferQueue = append([]sftpTransferRequest(nil), transfers...)
			state.queueReviewActive = true
			state.queueReviewSide = sourceSide
			state.queueReviewIndex = 0
			state.queueReviewTop = 0
			state.status = fmt.Sprintf("已准备 %d 项传输；可调整优先级后开始", len(transfers))
			state.statusErr = false
			return nil
		}
		clearSFTPMarks(state, sourceSide)
		return m.startSFTPTransferQueue(transfers)
	}

	entry := m.selectedSFTPEntry(sourceSide)
	if entry == nil {
		state.status = "请先在对应栏选择一个文件或目录"
		state.statusErr = true
		return nil
	}
	if entry.IsSymlink || (!entry.IsDir && !entry.Mode.IsRegular()) {
		state.status = "当前仅支持普通文件和目录"
		state.statusErr = true
		return nil
	}
	return m.startSFTPTransfer(m.sftpTransferForEntry(direction, m.sftpEntryPath(sourceSide, entry.Name), *entry), false)
}

func clearSFTPMarks(state *sftpBrowserState, side sftpSide) {
	if state == nil {
		return
	}
	if side == sftpRemoteSide {
		state.remoteMarks = make(map[string]sftpclient.Entry)
	} else {
		state.localMarks = make(map[string]sftpclient.Entry)
	}
}

func (m *modelState) updateSFTPQueueReviewKey(key tea.Key) tea.Cmd {
	state := m.sftp
	if state == nil || !state.queueReviewActive {
		return nil
	}
	visible := max(1, (m.height-7)/2)
	switch key.Code {
	case tea.KeyEscape:
		state.queueReviewActive = false
		state.transferQueue = nil
		state.queueReviewIndex = 0
		state.queueReviewTop = 0
		state.status, state.statusErr = "已取消传输队列；没有文件开始传输", false
		return nil
	case tea.KeyUp:
		state.queueReviewIndex = clamp(state.queueReviewIndex-1, 0, max(0, len(state.transferQueue)-1))
	case tea.KeyDown:
		state.queueReviewIndex = clamp(state.queueReviewIndex+1, 0, max(0, len(state.transferQueue)-1))
	case tea.KeyEnter:
		transfers := append([]sftpTransferRequest(nil), state.transferQueue...)
		cmd := m.startSFTPTransferQueue(transfers)
		if cmd == nil {
			state.status, state.statusErr = "无法启动传输队列；请检查远程连接状态", true
			return nil
		}
		clearSFTPMarks(state, state.queueReviewSide)
		state.queueReviewActive = false
		state.queueReviewIndex = 0
		state.queueReviewTop = 0
		return cmd
	}
	switch key.Text {
	case "+", "=":
		if state.queueReviewIndex > 0 {
			index := state.queueReviewIndex
			state.transferQueue[index-1], state.transferQueue[index] = state.transferQueue[index], state.transferQueue[index-1]
			state.queueReviewIndex--
		}
	case "-":
		if state.queueReviewIndex+1 < len(state.transferQueue) {
			index := state.queueReviewIndex
			state.transferQueue[index+1], state.transferQueue[index] = state.transferQueue[index], state.transferQueue[index+1]
			state.queueReviewIndex++
		}
	}
	state.queueReviewTop = keepVisible(state.queueReviewIndex, state.queueReviewTop, visible)
	return nil
}

func (m *modelState) sftpTransferForEntry(direction sftpTransferDirection, sourcePath string, entry sftpclient.Entry) sftpTransferRequest {
	sourceSide, destinationSide := sftpLocalSide, sftpRemoteSide
	if direction == sftpDownload {
		sourceSide, destinationSide = sftpRemoteSide, sftpLocalSide
	}
	transfer := sftpTransferRequest{
		direction: direction, side: destinationSide, directory: entry.IsDir,
		sourceRemote: sftpSideIsRemote(m.sftp, sourceSide),
		destRemote:   sftpSideIsRemote(m.sftp, destinationSide),
		sourcePath:   sourcePath,
		destPath:     m.sftpEntryPath(destinationSide, entry.Name),
	}
	if transfer.sourceRemote {
		transfer.remotePath = transfer.sourcePath
	} else {
		transfer.localPath = transfer.sourcePath
	}
	if transfer.destRemote {
		transfer.remotePath = transfer.destPath
	} else {
		transfer.localPath = transfer.destPath
	}
	return transfer
}

func (m *modelState) startSFTPTransferQueue(transfers []sftpTransferRequest) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy || len(transfers) == 0 {
		return nil
	}
	for _, transfer := range transfers {
		if (transfer.sourceRemote && sftpClientForSide(state, sftpTransferSourceSide(transfer)) == nil) ||
			(transfer.destRemote && sftpClientForSide(state, transfer.side) == nil) {
			return nil
		}
	}
	state.transferQueue = append([]sftpTransferRequest(nil), transfers...)
	state.queueApproved = nil
	state.queueProgress = make(map[int]sftpProgress)
	state.queueActive = true
	state.queueCancelled = false
	state.queueTotal = len(transfers)
	state.queueDone = 0
	state.queueSkipped = 0
	state.queueFailures = nil
	state.queueCurrent = nil
	state.requestID++
	state.transferPauseGate = sftpclient.NewTransferPauseGate()
	state.transferPaused = false
	baseContext, cancel := context.WithCancel(context.Background())
	state.transferCancel = cancel
	state.queueContext = sftpclient.WithTransferPauseGate(baseContext, state.transferPauseGate)
	return m.startNextSFTPTransfer()
}

func (m *modelState) toggleSFTPTransferPause() tea.Cmd {
	state := m.sftp
	if state == nil || state.transferPauseGate == nil {
		return nil
	}
	if state.transferPaused {
		state.transferPaused = false
		state.transferPauseGate.Resume()
		state.status = "正在恢复传输…"
		state.statusErr = false
		if !state.busy && state.queueActive {
			return m.startNextSFTPTransfer()
		}
		return nil
	}
	state.transferPaused = true
	state.transferPauseGate.Pause()
	state.status = "传输已暂停"
	state.statusErr = false
	return nil
}

func (m *modelState) cancelSFTPTransferQueue(status string) {
	state := m.sftp
	if state == nil {
		return
	}
	state.queueActive = false
	state.queueReviewActive = false
	state.transferQueue = nil
	state.queueCurrent = nil
	state.queueTotal = 0
	state.queueDone = 0
	state.queueSkipped = 0
	state.queueFailures = nil
	state.queueApproved = nil
	state.queueProgress = nil
	state.queueContext = nil
	if state.transferPauseGate != nil {
		state.transferPauseGate.Resume()
	}
	state.transferPauseGate = nil
	state.transferPaused = false
	state.syncIssueCount = 0
	state.mirrorPlan = nil
	if state.transferCancel != nil {
		state.transferCancel()
		state.transferCancel = nil
	}
	state.status = status
	state.statusErr = false
}

func (m *modelState) startNextSFTPTransfer() tea.Cmd {
	state := m.sftp
	if state == nil || !state.queueActive || state.transferPaused {
		return nil
	}
	if len(state.transferQueue) == 0 {
		return m.startSFTPQueueTransfers()
	}
	transfer := state.transferQueue[0]
	state.transferQueue = state.transferQueue[1:]
	state.queueCurrent = &transfer
	state.busy = true
	state.status = fmt.Sprintf("检查传输目标 %d/%d：%s", state.queueDone+1, state.queueTotal, sftpTransferLabel(transfer))
	state.statusErr = false
	requestID := state.requestID
	ctx := state.queueContext
	sourceClient, destinationClient := sftpClientsForTransfer(state, transfer)
	return func() tea.Msg {
		stopClose := context.AfterFunc(ctx, func() { closeSFTPTransferClients(sourceClient, destinationClient) })
		defer stopClose()
		var exists bool
		var err error
		if transfer.destRemote {
			if destinationClient == nil {
				err = errors.New("远程文件连接不可用")
			} else {
				exists, err = destinationClient.Exists(transfer.destPath)
			}
		} else {
			_, statErr := os.Lstat(transfer.destPath)
			switch {
			case statErr == nil:
				exists = true
			case errors.Is(statErr, os.ErrNotExist):
			default:
				err = statErr
			}
		}
		return sftpQueuePreflightMsg{
			requestID: requestID, generation: state.generation, transfer: transfer, exists: exists,
			cancelled: ctx.Err() != nil, err: err,
		}
	}
}

const sftpQueueConcurrency = 3

func (m *modelState) startSFTPQueueTransfers() tea.Cmd {
	state := m.sftp
	if state == nil || !state.queueActive || state.transferPaused {
		return nil
	}
	if len(state.queueApproved) == 0 {
		return m.finishSFTPQueue(nil, nil)
	}
	state.busy = true
	state.queueCurrent = nil
	state.transferred, state.transferTotal = 0, 0
	state.currentProgress = sftpProgress{}
	state.status = fmt.Sprintf("传输队列：%d 项，最多 %d 项并发", len(state.queueApproved), sftpQueueConcurrency)
	state.statusErr = false
	requestID := state.requestID
	jobs := append([]sftpQueueJob(nil), state.queueApproved...)
	ctx := state.queueContext
	cancel := state.transferCancel
	progressChannel := make(chan sftpProgress, 32)
	state.progressChannel = progressChannel
	sideClients := map[sftpSide]remotefs.Client{
		sftpLocalSide:  sftpClientForSide(state, sftpLocalSide),
		sftpRemoteSide: sftpClientForSide(state, sftpRemoteSide),
	}
	refreshes := make([]sftpQueueRefresh, 0, 2)
	seenSides := make(map[sftpSide]struct{}, 2)
	for _, job := range jobs {
		side := job.transfer.side
		if _, exists := seenSides[side]; exists {
			continue
		}
		seenSides[side] = struct{}{}
		refreshes = append(refreshes, sftpQueueRefresh{side: side, path: m.currentSFTPPath(side)})
	}
	return tea.Batch(
		func() tea.Msg {
			stopClose := context.AfterFunc(ctx, func() {
				closeSFTPTransferClients(sideClients[sftpLocalSide], sideClients[sftpRemoteSide])
			})
			results := runSFTPQueueJobs(ctx, jobs, func(index int, job sftpQueueJob) error {
				progress := sftpTransferProgress(ctx, progressChannel, index)
				transferContext := withSFTPTransferProgressReporter(ctx, progressChannel, index)
				sourceSide := sftpTransferSourceSide(job.transfer)
				return executeSFTPTransfer(transferContext, job.transfer, job.overwrite,
					sideClients[sourceSide], sideClients[job.transfer.side], progress)
			})
			cancelled := ctx.Err() != nil
			var refreshErrs []error
			if !cancelled {
				for index := range refreshes {
					refresh := &refreshes[index]
					refresh.entries, refresh.err = listSFTPPath(sideClients[refresh.side], refresh.side, refresh.path)
					if refresh.err != nil {
						refreshErrs = append(refreshErrs, fmt.Errorf("刷新%s目录失败：%w", sftpSideLabel(refresh.side), refresh.err))
					}
				}
			}
			close(progressChannel)
			stopClose()
			cancel()
			return sftpOperationMsg{
				requestID: requestID, generation: state.generation, queueRefreshes: refreshes,
				cancelled: cancelled, err: errors.Join(refreshErrs...), queueResults: results,
			}
		},
		readSFTPProgressCmd(requestID, state.generation, progressChannel),
	)
}

func sftpClientsForTransfer(state *sftpBrowserState, transfer sftpTransferRequest) (remotefs.Client, remotefs.Client) {
	return sftpClientForSide(state, sftpTransferSourceSide(transfer)), sftpClientForSide(state, transfer.side)
}

func sftpTransferSourceSide(transfer sftpTransferRequest) sftpSide {
	if transfer.direction == sftpDownload {
		return sftpRemoteSide
	}
	return sftpLocalSide
}

type sftpQueueResult struct {
	transfer sftpTransferRequest
	err      error
}

func runSFTPQueueJobs(ctx context.Context, jobs []sftpQueueJob, execute func(index int, job sftpQueueJob) error) []sftpQueueResult {
	results := make([]sftpQueueResult, len(jobs))
	if len(jobs) == 0 {
		return results
	}
	type indexedJob struct {
		index int
		job   sftpQueueJob
	}
	jobQueue := make(chan indexedJob, len(jobs))
	for index, job := range jobs {
		jobQueue <- indexedJob{index: index, job: job}
	}
	close(jobQueue)
	workers := min(sftpQueueConcurrency, len(jobs))
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for indexed := range jobQueue {
				var err error
				if ctx.Err() != nil {
					err = ctx.Err()
				} else {
					err = sftpclient.WaitIfTransferPaused(ctx)
					if err == nil {
						err = execute(indexed.index, indexed.job)
					}
				}
				results[indexed.index] = sftpQueueResult{transfer: indexed.job.transfer, err: err}
			}
		}()
	}
	wait.Wait()
	return results
}

func closeSFTPTransferClients(clients ...remotefs.Client) {
	seen := make(map[remotefs.Client]struct{}, len(clients))
	for _, client := range clients {
		if client == nil {
			continue
		}
		if _, exists := seen[client]; exists {
			continue
		}
		_ = client.Close()
		seen[client] = struct{}{}
	}
}

func (m *modelState) handleSFTPQueuePreflight(msg sftpQueuePreflightMsg) tea.Cmd {
	state := m.sftp
	if state == nil || msg.requestID != state.requestID {
		return nil
	}
	state.busy = false
	if msg.cancelled || state.queueCancelled {
		state.client = nil
		state.peerClient = nil
		state.transferCancel = nil
		state.queueContext = nil
		state.transferPauseGate = nil
		state.transferPaused = false
		state.busy = true
		state.status = "传输已取消，正在恢复远程文件连接…"
		state.statusErr = false
		return tea.Batch(m.reopenSFTPAfterTransferCancel(), m.showTransferNotification("文件传输已取消", true))
	}
	if msg.err != nil {
		state.queueFailures = append(state.queueFailures, sftpTransferLabel(msg.transfer)+"："+msg.err.Error())
		state.queueDone++
		state.queueCurrent = nil
		return m.startNextSFTPTransfer()
	}
	if msg.exists {
		state.prompt = &sftpPrompt{
			action:   sftpPromptOverwrite,
			title:    "目标已存在：y 覆盖，其他键跳过，q 取消队列",
			transfer: &msg.transfer,
		}
		return nil
	}
	state.queueApproved = append(state.queueApproved, sftpQueueJob{transfer: msg.transfer})
	state.queueCurrent = nil
	return m.startNextSFTPTransfer()
}

func (m *modelState) finishSFTPQueue(results []sftpQueueResult, refreshErr error) tea.Cmd {
	state := m.sftp
	if state == nil {
		return nil
	}
	for _, result := range results {
		state.queueDone++
		if result.err != nil {
			state.queueFailures = append(state.queueFailures, sftpTransferLabel(result.transfer)+"："+result.err.Error())
		}
	}
	succeeded := state.queueDone - state.queueSkipped - len(state.queueFailures)
	mirrorPlan := state.mirrorPlan
	mirrorBlocked := len(state.queueFailures) > 0 || state.queueSkipped > 0 || refreshErr != nil
	state.status = fmt.Sprintf("传输队列完成：%d/%d 成功", max(0, succeeded), state.queueTotal)
	if state.queueSkipped > 0 {
		state.status += fmt.Sprintf("，%d 项跳过", state.queueSkipped)
	}
	if len(state.queueFailures) > 0 {
		state.status += fmt.Sprintf("，%d 项失败：%s", len(state.queueFailures), state.queueFailures[0])
	}
	if state.syncIssueCount > 0 {
		state.status += fmt.Sprintf("；%d 项因类型冲突或不支持的文件类型未同步", state.syncIssueCount)
	}
	if refreshErr != nil {
		state.status += "；刷新目录失败：" + refreshErr.Error()
	}
	state.statusErr = len(state.queueFailures) > 0 || refreshErr != nil
	notice := fmt.Sprintf("文件传输队列完成：%d/%d 成功", max(0, succeeded), state.queueTotal)
	if state.queueSkipped > 0 {
		notice += fmt.Sprintf("，%d 项跳过", state.queueSkipped)
	}
	if len(state.queueFailures) > 0 {
		notice += fmt.Sprintf("，%d 项失败", len(state.queueFailures))
	}
	if refreshErr != nil {
		notice += "，目录刷新失败"
	}
	noticeErr := len(state.queueFailures) > 0 || refreshErr != nil
	if state.transferCancel != nil {
		state.transferCancel()
	}
	state.busy = false
	state.progressChannel = nil
	state.transferCancel = nil
	state.queueContext = nil
	state.queueActive = false
	state.queueTotal = 0
	state.queueDone = 0
	state.queueSkipped = 0
	state.queueFailures = nil
	state.queueApproved = nil
	state.queueProgress = nil
	state.queueCurrent = nil
	state.transferQueue = nil
	state.transferPauseGate = nil
	state.transferPaused = false
	state.syncIssueCount = 0
	if mirrorPlan != nil {
		if mirrorBlocked {
			state.mirrorPlan = nil
			state.status += "；传输有失败或跳过项，未删除目标侧多余文件"
			state.statusErr = true
			return m.showTransferNotification(notice+"；镜像删除未执行", true)
		}
		state.syncIssueCount = mirrorPlan.issueCount
		return m.startSFTPMirrorDelete(mirrorPlan)
	}
	return m.showTransferNotification(notice, noticeErr)
}

func sftpTransferLabel(transfer sftpTransferRequest) string {
	name := filepath.Base(transfer.sourcePath)
	if transfer.sourceRemote {
		name = path.Base(transfer.sourcePath)
	}
	if transfer.directory {
		return "目录 " + name
	}
	return name
}

func (m *modelState) enterSFTPSelection() tea.Cmd {
	state := m.sftp
	if state == nil {
		return nil
	}
	entry := m.selectedSFTPEntry(state.activeSide)
	if entry == nil {
		return nil
	}
	if entry.IsSymlink {
		state.status = "已识别符号链接；为避免意外访问目标，暂不自动跟随"
		state.statusErr = false
		return nil
	}
	if !entry.IsDir {
		if sftpSideIsRemote(state, state.activeSide) {
			state.status = "已选择远程文件：d 下载，v 预览，e 编辑，i 属性，p 权限"
		} else {
			state.status = "已选择本地文件：u 上传，v 预览，e 编辑，i 属性，p 权限"
		}
		state.statusErr = false
		return nil
	}
	return m.navigateSFTPDirectory(state.activeSide, m.sftpEntryPath(state.activeSide, entry.Name))
}

func (m *modelState) goSFTPParent() tea.Cmd {
	if m.sftp == nil {
		return nil
	}
	side := m.sftp.activeSide
	current := m.currentSFTPPath(side)
	parent := ""
	if sftpSideIsRemote(m.sftp, side) {
		parent = path.Dir(path.Clean(current))
		if parent == "." && path.Clean(current) == "." {
			parent = ".."
		}
	} else {
		parent = filepath.Dir(filepath.Clean(current))
	}
	return m.navigateSFTPDirectory(side, parent)
}

func (m *modelState) makeSFTPDirectory(side sftpSide, name string) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy || (sftpSideIsRemote(state, side) && sftpClientForSide(state, side) == nil) {
		return nil
	}
	state.requestID++
	requestID, client := state.requestID, sftpClientForSide(state, side)
	remote := sftpSideIsRemote(state, side)
	currentPath := m.currentSFTPPath(side)
	newPath := m.sftpEntryPath(side, name)
	state.busy = true
	state.status = "正在创建目录…"
	state.statusErr = false
	return func() tea.Msg {
		var err error
		if remote {
			err = client.MakeDirectory(newPath)
		} else {
			err = os.Mkdir(newPath, 0o755)
		}
		entries, listErr := listSFTPPath(client, side, currentPath)
		if err == nil {
			err = listErr
		}
		status := "本地目录已创建"
		if remote {
			status = "远程目录已创建"
		}
		return sftpOperationMsg{requestID: requestID, generation: state.generation, side: side, path: currentPath, status: status, entries: entries, err: err}
	}
}

func (m *modelState) deleteSFTPEntry(transfer sftpTransferRequest) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy || (sftpSideIsRemote(state, transfer.side) && sftpClientForSide(state, transfer.side) == nil) {
		return nil
	}
	state.requestID++
	requestID, client := state.requestID, sftpClientForSide(state, transfer.side)
	remote := sftpSideIsRemote(state, transfer.side)
	currentPath := m.currentSFTPPath(transfer.side)
	state.busy = true
	state.status = "正在删除项目…"
	state.statusErr = false
	return func() tea.Msg {
		var err error
		if remote {
			if transfer.directory {
				err = client.RemoveTree(transfer.entryPath)
			} else {
				err = client.Remove(transfer.entryPath, false)
			}
		} else {
			if transfer.directory {
				err = os.RemoveAll(transfer.entryPath)
			} else {
				err = os.Remove(transfer.entryPath)
			}
		}
		entries, listErr := listSFTPPath(client, transfer.side, currentPath)
		if err == nil {
			err = listErr
		}
		status := "本地项目已删除"
		if remote {
			status = "远程项目已删除"
		}
		return sftpOperationMsg{requestID: requestID, generation: state.generation, side: transfer.side, path: currentPath, status: status, entries: entries, err: err}
	}
}

func (m *modelState) renameSFTPEntry(transfer sftpTransferRequest, newName string) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy || (sftpSideIsRemote(state, transfer.side) && sftpClientForSide(state, transfer.side) == nil) {
		return nil
	}
	remote := sftpSideIsRemote(state, transfer.side)
	newPath := filepath.Join(m.currentSFTPPath(transfer.side), newName)
	if remote {
		newPath = path.Join(m.currentSFTPPath(transfer.side), newName)
		if path.Clean(transfer.entryPath) == path.Clean(newPath) {
			state.status = "名称未改变"
			state.statusErr = false
			return nil
		}
	} else if filepath.Clean(transfer.entryPath) == filepath.Clean(newPath) {
		state.status = "名称未改变"
		state.statusErr = false
		return nil
	}
	state.requestID++
	requestID, client := state.requestID, sftpClientForSide(state, transfer.side)
	currentPath := m.currentSFTPPath(transfer.side)
	state.busy = true
	state.status = "正在重命名项目…"
	state.statusErr = false
	return func() tea.Msg {
		var err error
		if remote {
			exists, checkErr := client.Exists(newPath)
			err = checkErr
			if err == nil && exists {
				err = os.ErrExist
			}
			if err == nil {
				err = client.Rename(transfer.entryPath, newPath)
			}
		} else {
			if _, statErr := os.Lstat(newPath); statErr == nil {
				err = os.ErrExist
			} else if !errors.Is(statErr, os.ErrNotExist) {
				err = statErr
			}
			if err == nil {
				err = os.Rename(transfer.entryPath, newPath)
			}
		}
		entries, listErr := listSFTPPath(client, transfer.side, currentPath)
		if err == nil {
			err = listErr
		}
		status := "本地项目已重命名"
		if remote {
			status = "远程项目已重命名"
		}
		return sftpOperationMsg{requestID: requestID, generation: state.generation, side: transfer.side, path: currentPath, status: status, entries: entries, err: err}
	}
}

func (m *modelState) changeSFTPEntryPermissions(transfer sftpTransferRequest) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy || (sftpSideIsRemote(state, transfer.side) && sftpClientForSide(state, transfer.side) == nil) {
		return nil
	}
	remote := sftpSideIsRemote(state, transfer.side)
	client := sftpClientForSide(state, transfer.side)
	currentPath := m.currentSFTPPath(transfer.side)
	state.requestID++
	requestID := state.requestID
	state.busy = true
	state.status = "正在修改权限…"
	state.statusErr = false
	return func() tea.Msg {
		var err error
		if remote {
			err = client.SetPermissions(transfer.entryPath, transfer.permissions)
		} else {
			err = setLocalSFTPPermissions(transfer.entryPath, transfer.permissions)
		}
		entries, listErr := listSFTPPath(client, transfer.side, currentPath)
		if err == nil {
			err = listErr
		}
		status := fmt.Sprintf("本地权限已修改为 %03o", transfer.permissions.Perm())
		if remote {
			status = fmt.Sprintf("远程权限已修改为 %03o", transfer.permissions.Perm())
		}
		return sftpOperationMsg{requestID: requestID, generation: state.generation, side: transfer.side, path: currentPath, status: status, entries: entries, err: err}
	}
}

func setLocalSFTPPermissions(filePath string, mode os.FileMode) error {
	if mode&^os.FileMode(0o777) != 0 {
		return errors.New("权限值必须只包含 rwx 权限位")
	}
	before, err := os.Lstat(filePath)
	if err != nil {
		return fmt.Errorf("读取本地项目权限失败：%w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || (!before.Mode().IsRegular() && !before.IsDir()) {
		return errors.New("仅支持修改普通文件或目录的权限")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("打开本地项目失败：%w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return fmt.Errorf("读取本地项目状态失败：%w", err)
	}
	if !os.SameFile(before, after) {
		return errors.New("本地项目在修改权限前发生了变化")
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("修改本地项目权限失败：%w", err)
	}
	updated, err := file.Stat()
	if err != nil {
		return fmt.Errorf("读取修改后的本地项目权限失败：%w", err)
	}
	if updated.Mode().Perm() != mode.Perm() {
		return errors.New("本地文件系统未能应用请求的 rwx 权限")
	}
	return nil
}

func listSFTPPath(client remotefs.Client, side sftpSide, directory string) ([]sftpclient.Entry, error) {
	if client != nil {
		entries, err := client.List(directory)
		return sortSFTPEntries(entries), err
	}
	return listLocalDirectory(directory)
}

func listLocalDirectory(directory string) ([]sftpclient.Entry, error) {
	items, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	entries := make([]sftpclient.Entry, 0, len(items))
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			return nil, fmt.Errorf("读取 %s 信息失败：%w", item.Name(), err)
		}
		mode := info.Mode()
		entries = append(entries, sftpclient.Entry{
			Name: item.Name(), Size: info.Size(), Mode: mode, ModeKnown: true,
			ModTime: info.ModTime(), IsDir: mode.IsDir(),
			IsSymlink: mode&os.ModeSymlink != 0,
		})
	}
	return sortSFTPEntries(entries), nil
}

func (m *modelState) startSFTPTransfer(transfer sftpTransferRequest, overwrite bool) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy {
		return nil
	}
	sourceRemote, destRemote := transfer.sourceRemote, transfer.destRemote
	sourceSide := sftpTransferSourceSide(transfer)
	sourceClient := sftpClientForSide(state, sourceSide)
	destinationClient := sftpClientForSide(state, transfer.side)
	if (sourceRemote && sourceClient == nil) || (destRemote && destinationClient == nil) {
		state.status = "远程文件连接不可用"
		state.statusErr = true
		return nil
	}
	sourcePath, destinationPath := transfer.sourcePath, transfer.destPath
	if sourcePath == "" {
		sourcePath = transfer.localPath
		if transfer.direction == sftpDownload {
			sourcePath = transfer.remotePath
		}
	}
	if destinationPath == "" {
		destinationPath = transfer.remotePath
		if transfer.direction == sftpDownload {
			destinationPath = transfer.localPath
		}
	}
	state.requestID++
	requestID := state.requestID
	currentPath := m.currentSFTPPath(transfer.side)
	state.busy = true
	state.transferred, state.transferTotal = 0, 0
	state.currentProgress = sftpProgress{}
	name := sftpTransferLabel(transfer)
	if state.queueActive {
		state.status = fmt.Sprintf("传输队列 %d/%d：%s", state.queueDone+1, state.queueTotal, sftpTransferLabel(transfer))
	} else if transfer.directory {
		state.status = "正在传输目录 " + name
	} else if transfer.direction == sftpUpload {
		state.status = "正在上传 " + name
	} else {
		state.status = "正在下载 " + name
	}
	state.statusErr = false
	baseContext, cancel := context.WithCancel(context.Background())
	state.transferPauseGate = sftpclient.NewTransferPauseGate()
	state.transferPaused = false
	ctx := sftpclient.WithTransferPauseGate(baseContext, state.transferPauseGate)
	state.transferCancel = cancel
	progressChannel := make(chan sftpProgress, 32)
	state.progressChannel = progressChannel
	transferCmd := func() tea.Msg {
		stopClose := context.AfterFunc(ctx, func() {
			seen := make(map[remotefs.Client]struct{}, 2)
			for _, client := range []remotefs.Client{sourceClient, destinationClient} {
				if client != nil {
					if _, exists := seen[client]; !exists {
						_ = client.Close()
						seen[client] = struct{}{}
					}
				}
			}
		})
		defer func() {
			stopClose()
			cancel()
		}()
		progress := sftpTransferProgress(ctx, progressChannel, 0)
		transferContext := withSFTPTransferProgressReporter(ctx, progressChannel, 0)
		var err error
		if !overwrite {
			var exists bool
			if destRemote {
				exists, err = destinationClient.Exists(destinationPath)
			} else {
				_, statErr := os.Lstat(destinationPath)
				switch {
				case statErr == nil:
					exists = true
				case errors.Is(statErr, os.ErrNotExist):
				default:
					err = statErr
				}
			}
			if err == nil && exists {
				close(progressChannel)
				return sftpOverwriteRequiredMsg{requestID: requestID, generation: state.generation, transfer: transfer}
			}
		}
		if err == nil {
			err = executeSFTPTransfer(transferContext, transfer, overwrite, sourceClient, destinationClient, progress)
		}
		cancelled := errors.Is(err, context.Canceled) || ctx.Err() != nil
		var entries []sftpclient.Entry
		if !cancelled {
			var listErr error
			entries, listErr = listSFTPPath(destinationClient, transfer.side, currentPath)
			if err == nil {
				err = listErr
			}
		}
		close(progressChannel)
		status := "下载完成"
		if transfer.direction == sftpUpload {
			status = "上传完成"
		}
		if transfer.directory {
			status = "目录" + strings.TrimSuffix(status, "完成") + "完成"
		}
		if cancelled {
			status = "传输已取消；正在重新连接远程文件连接"
		}
		return sftpOperationMsg{
			requestID: requestID, generation: state.generation, side: transfer.side, path: currentPath,
			status: status, entries: entries, cancelled: cancelled, err: err,
		}
	}
	return tea.Batch(transferCmd, readSFTPProgressCmd(requestID, state.generation, progressChannel))
}

func sftpTransferProgress(ctx context.Context, progressChannel chan sftpProgress, index int) func(int64, int64) {
	return func(transferred, total int64) {
		// SFTP implementations also pause inside their transfer loops. Keeping
		// this checkpoint here gives other remote protocols (FTP/FTPS) the same
		// queue pause behavior without coupling their adapters to SFTP code.
		if err := sftpclient.WaitIfTransferPaused(ctx); err != nil {
			return
		}
		select {
		case progressChannel <- sftpProgress{index: index, transferred: transferred, total: total}:
		default:
		}
	}
}

func withSFTPTransferProgressReporter(ctx context.Context, progressChannel chan sftpProgress, index int) context.Context {
	return remotefs.WithTransferProgressReporter(ctx, func(progress remotefs.TransferProgress) {
		sendLatestSFTPProgress(progressChannel, sftpProgress{
			index: index, transferred: progress.Transferred, total: progress.Total,
			currentPath: progress.CurrentPath, filesDone: progress.FilesCompleted, filesTotal: progress.FilesTotal,
		})
	})
}

func sendLatestSFTPProgress(progressChannel chan sftpProgress, progress sftpProgress) {
	select {
	case progressChannel <- progress:
		return
	default:
	}
	select {
	case <-progressChannel:
	default:
	}
	select {
	case progressChannel <- progress:
	default:
	}
}

func executeSFTPTransfer(
	ctx context.Context,
	transfer sftpTransferRequest,
	overwrite bool,
	sourceClient, destinationClient remotefs.Client,
	progress func(int64, int64),
) error {
	sourceRemote := transfer.sourceRemote
	destRemote := transfer.destRemote
	sourcePath, destinationPath := transfer.sourcePath, transfer.destPath
	if sourcePath == "" {
		sourcePath = transfer.localPath
		if transfer.direction == sftpDownload {
			sourcePath = transfer.remotePath
		}
	}
	if destinationPath == "" {
		destinationPath = transfer.remotePath
		if transfer.direction == sftpDownload {
			destinationPath = transfer.localPath
		}
	}
	switch {
	case !sourceRemote && !destRemote:
		return copyLocalSFTPPath(ctx, sourcePath, destinationPath, overwrite, transfer.directory, progress)
	case sourceRemote && destRemote:
		if sourceClient == nil || destinationClient == nil {
			return errors.New("远程文件连接不可用")
		}
		if transfer.directory {
			return sourceClient.CopyRemoteDirectoryTo(ctx, destinationClient, sourcePath, destinationPath, overwrite, progress)
		}
		return sourceClient.CopyRemoteFileTo(ctx, destinationClient, sourcePath, destinationPath, overwrite, progress)
	case sourceRemote:
		if sourceClient == nil {
			return errors.New("远程文件连接不可用")
		}
		if transfer.directory {
			return sourceClient.DownloadDirectory(ctx, sourcePath, destinationPath, overwrite, progress)
		}
		return sourceClient.Download(ctx, sourcePath, destinationPath, overwrite, progress)
	case destRemote:
		if destinationClient == nil {
			return errors.New("远程文件连接不可用")
		}
		if transfer.directory {
			return destinationClient.UploadDirectory(ctx, sourcePath, destinationPath, overwrite, progress)
		}
		return destinationClient.Upload(ctx, sourcePath, destinationPath, overwrite, progress)
	default:
		return errors.New("不支持的 SFTP 传输方向")
	}
}

func copyLocalSFTPPath(ctx context.Context, sourcePath, destinationPath string, overwrite, directory bool, progress func(int64, int64)) error {
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return fmt.Errorf("读取本地源文件失败：%w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return errors.New("本地传输仅支持普通文件和目录")
	}
	if directory != info.IsDir() {
		return errors.New("本地传输源类型发生变化，请刷新后重试")
	}
	if !directory {
		return copyLocalSFTPFile(ctx, sourcePath, destinationPath, info.Mode().Perm(), info.Size(), overwrite, progress)
	}
	sourceAbsolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return err
	}
	destinationAbsolute, err := filepath.Abs(destinationPath)
	if err != nil {
		return err
	}
	relativeDestination, err := filepath.Rel(sourceAbsolute, destinationAbsolute)
	if err != nil {
		return err
	}
	if relativeDestination == "." || (relativeDestination != ".." && !strings.HasPrefix(relativeDestination, ".."+string(os.PathSeparator))) {
		return errors.New("不能把目录复制到自身内部")
	}
	if err := os.MkdirAll(destinationPath, info.Mode().Perm()); err != nil {
		return fmt.Errorf("创建本地目标目录失败：%w", err)
	}
	return filepath.WalkDir(sourcePath, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		itemInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if itemInfo.Mode()&os.ModeSymlink != 0 || (!itemInfo.IsDir() && !itemInfo.Mode().IsRegular()) {
			return fmt.Errorf("目录中包含不支持的项目：%s", current)
		}
		relative, err := filepath.Rel(sourcePath, current)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destinationPath, relative)
		if itemInfo.IsDir() {
			return os.MkdirAll(target, itemInfo.Mode().Perm())
		}
		return copyLocalSFTPFile(ctx, current, target, itemInfo.Mode().Perm(), itemInfo.Size(), overwrite, progress)
	})
}

func copyLocalSFTPFile(ctx context.Context, sourcePath, destinationPath string, mode os.FileMode, size int64, overwrite bool, progress func(int64, int64)) (result error) {
	if !overwrite {
		if _, err := os.Lstat(destinationPath); err == nil {
			return os.ErrExist
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	defer func() {
		if err := destination.Close(); result == nil && err != nil {
			result = err
		}
	}()
	buffer := make([]byte, 64*1024)
	var copied int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written, writeErr := destination.Write(buffer[:count])
			copied += int64(written)
			if writeErr != nil {
				return writeErr
			}
			if written != count {
				return io.ErrShortWrite
			}
			if progress != nil {
				progress(copied, size)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func readSFTPProgressCmd(requestID, generation uint64, progressChannel <-chan sftpProgress) tea.Cmd {
	return func() tea.Msg {
		progress, ok := <-progressChannel
		return sftpProgressMsg{requestID: requestID, generation: generation, progress: progress, closed: !ok}
	}
}

func (m *modelState) handleSFTPProgress(msg sftpProgressMsg) tea.Cmd {
	state := m.sftp
	if state == nil || msg.requestID != state.requestID || !state.busy || msg.closed {
		return nil
	}
	if state.queueActive {
		if state.queueProgress == nil {
			state.queueProgress = make(map[int]sftpProgress)
		}
		previous := state.queueProgress[msg.progress.index]
		if msg.progress.filesTotal == 0 && previous.filesTotal > 0 {
			msg.progress.currentPath = previous.currentPath
			msg.progress.filesDone = previous.filesDone
			msg.progress.filesTotal = previous.filesTotal
		}
		state.queueProgress[msg.progress.index] = msg.progress
		state.currentProgress = msg.progress
		state.transferred, state.transferTotal = 0, 0
		for _, progress := range state.queueProgress {
			state.transferred += progress.transferred
			state.transferTotal += progress.total
		}
	} else {
		state.transferred = msg.progress.transferred
		state.transferTotal = msg.progress.total
		state.currentProgress = msg.progress
	}
	return readSFTPProgressCmd(msg.requestID, msg.generation, stateProgressChannel(state))
}

func sftpProgressSuffix(progress sftpProgress) string {
	if progress.filesTotal <= 0 {
		return ""
	}
	suffix := fmt.Sprintf(" · 文件 %d/%d", progress.filesDone, progress.filesTotal)
	if progress.currentPath != "" && progress.filesDone < progress.filesTotal {
		suffix += " · " + sanitizeRemoteText(progress.currentPath)
	}
	return suffix
}

func sftpQueueProgressSuffix(progresses map[int]sftpProgress) string {
	if len(progresses) == 0 {
		return ""
	}
	indexes := make([]int, 0, len(progresses))
	for index := range progresses {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	var filesDone, filesTotal int
	paths := make([]string, 0, len(indexes))
	for _, index := range indexes {
		progress := progresses[index]
		if progress.filesTotal <= 0 {
			continue
		}
		filesDone += progress.filesDone
		filesTotal += progress.filesTotal
		if progress.currentPath != "" && progress.filesDone < progress.filesTotal {
			paths = append(paths, sanitizeRemoteText(progress.currentPath))
		}
	}
	if filesTotal == 0 {
		return ""
	}
	suffix := fmt.Sprintf(" · 文件 %d/%d", filesDone, filesTotal)
	if len(paths) > 0 {
		limit := min(2, len(paths))
		suffix += " · " + strings.Join(paths[:limit], "、")
		if len(paths) > limit {
			suffix += fmt.Sprintf(" 等 %d 项", len(paths)-limit)
		}
	}
	return suffix
}

// progress channels are attached to a transfer until its completion message;
// this field is kept separately to avoid exposing a channel through the view.
func stateProgressChannel(state *sftpBrowserState) <-chan sftpProgress {
	return state.progressChannel
}

func (m *modelState) handleSFTPOverwriteRequired(msg sftpOverwriteRequiredMsg) {
	state := m.sftp
	if state == nil || msg.requestID != state.requestID {
		return
	}
	state.busy = false
	state.progressChannel = nil
	state.transferCancel = nil
	state.transferPauseGate = nil
	state.transferPaused = false
	title := "目标已存在，覆盖？输入 y 确认，其他键取消"
	if state.queueActive {
		title = "目标已存在：y 覆盖，其他键跳过，q 取消队列"
	}
	state.prompt = &sftpPrompt{
		action:   sftpPromptOverwrite,
		title:    title,
		transfer: &msg.transfer,
	}
}

func (m *modelState) handleSFTPOperation(msg sftpOperationMsg) tea.Cmd {
	state := m.sftp
	if state == nil || msg.requestID != state.requestID {
		return nil
	}
	state.busy = false
	state.progressChannel = nil
	state.transferCancel = nil
	state.transferPauseGate = nil
	state.transferPaused = false
	if msg.cancelled {
		state.syncIssueCount = 0
		state.mirrorPlan = nil
		state.client = nil
		state.peerClient = nil
		state.transferCancel = nil
		state.queueContext = nil
		state.busy = true
		state.status = "传输已取消，正在恢复远程文件连接…"
		state.statusErr = false
		return tea.Batch(m.reopenSFTPAfterTransferCancel(), m.showTransferNotification("文件传输已取消", true))
	}
	if msg.entries != nil {
		m.applySFTPListing(msg.side, msg.path, msg.entries)
	}
	for _, refresh := range msg.queueRefreshes {
		if refresh.err == nil && refresh.entries != nil {
			m.applySFTPListing(refresh.side, refresh.path, refresh.entries)
		}
	}
	if state.queueActive {
		return m.finishSFTPQueue(msg.queueResults, msg.err)
	}
	if msg.err != nil {
		state.status = "操作失败：" + msg.err.Error()
		state.statusErr = true
		if strings.Contains(msg.status, "上传") || strings.Contains(msg.status, "下载") {
			return m.showTransferNotification("文件传输失败", true)
		}
		return nil
	}
	state.status = msg.status
	state.statusErr = false
	if strings.Contains(msg.status, "上传") || strings.Contains(msg.status, "下载") {
		return m.showTransferNotification(msg.status, false)
	}
	return nil
}

func (m *modelState) applySFTPListing(side sftpSide, directory string, entries []sftpclient.Entry) {
	state := m.sftp
	if state == nil || entries == nil {
		return
	}
	if side == sftpRemoteSide {
		state.path, state.entries = directory, entries
		rebuildSFTPEntryViews(state)
		state.selected = clamp(state.selected, 0, max(0, len(sftpPaneEntries(state, side))-1))
		state.top = keepVisible(state.selected, state.top, m.sftpVisibleRowsFor(state, m.height))
		return
	}
	if sftpSideIsRemote(state, side) {
		state.peerPath, state.localEntries = directory, entries
	} else {
		state.localPath, state.localEntries = directory, entries
	}
	rebuildSFTPEntryViews(state)
	state.localSelected = clamp(state.localSelected, 0, max(0, len(sftpPaneEntries(state, side))-1))
	state.localTop = keepVisible(state.localSelected, state.localTop, m.sftpVisibleRowsFor(state, m.height))
}

func sftpSideLabel(side sftpSide) string {
	if side == sftpRemoteSide {
		return "右栏"
	}
	return "左栏"
}

func sftpSideSupportsPermissions(state *sftpBrowserState, side sftpSide) bool {
	return state != nil
}

func (m *modelState) reopenSFTPAfterTransferCancel() tea.Cmd {
	state := m.sftp
	if state == nil {
		return nil
	}
	generation := state.generation
	primarySession, peerSession := state.session, state.peerSession
	host, peerHost := state.host, state.peerHost
	primaryPassword, peerPassword := state.ftpPassword, state.peerFTPPassword
	remotePath, peerPath, localPath, standalone := state.path, state.peerPath, state.localPath, state.standalone
	if host.Kind == model.HostKindFTP {
		return func() tea.Msg {
			client, restoredPath, entries, err := reopenFTPClient(host, primaryPassword, remotePath)
			if err != nil {
				return sftpReopenedMsg{generation: generation, err: err}
			}
			var localEntries, peerEntries []sftpclient.Entry
			var peerClient remotefs.Client
			if standalone {
				switch {
				case peerSession != nil:
					peerClient, err = openSSHFileClient(peerSession, peerHost)
					if err == nil {
						if peerPath == "" {
							peerPath, err = peerClient.WorkingDirectory()
						}
						if err != nil || peerPath == "" {
							peerPath, err = peerClient.RealPath(".")
						}
						if err == nil {
							peerPath = sftpclient.CleanRemotePath(peerPath)
							peerEntries, err = peerClient.List(peerPath)
						}
					}
				case peerHost.Kind == model.HostKindFTP:
					var peerFTP *ftpclient.Client
					peerFTP, peerPath, peerEntries, err = reopenFTPClient(peerHost, peerPassword, peerPath)
					peerClient = peerFTP
				default:
					localEntries, err = listLocalDirectory(localPath)
				}
				if err != nil {
					_ = client.Close()
					if peerClient != nil {
						_ = peerClient.Close()
					}
					return sftpReopenedMsg{generation: generation, err: fmt.Errorf("恢复另一侧文件面板失败：%w", err)}
				}
			}
			return sftpReopenedMsg{
				generation: generation, client: client,
				path: restoredPath, entries: sortSFTPEntries(entries),
				peerClient: peerClient, peerPath: peerPath, peerEntries: sortSFTPEntries(peerEntries), localEntries: localEntries,
			}
		}
	}
	if primarySession == nil {
		return nil
	}
	return func() tea.Msg {
		client, err := openSSHFileClient(primarySession, host)
		if err != nil {
			return sftpReopenedMsg{generation: generation, err: err}
		}
		if remotePath == "" {
			remotePath, err = client.WorkingDirectory()
		}
		if err != nil || remotePath == "" {
			remotePath, err = client.RealPath(".")
		}
		if err != nil || remotePath == "" {
			remotePath = "."
		}
		remotePath = sftpclient.CleanRemotePath(remotePath)
		remoteEntries, err := client.List(remotePath)
		if err != nil {
			_ = client.Close()
			return sftpReopenedMsg{generation: generation, err: err}
		}
		var localEntries []sftpclient.Entry
		var peerClient remotefs.Client
		var peerEntries []sftpclient.Entry
		if standalone {
			if peerSession != nil {
				peerClient, err = openSSHFileClient(peerSession, peerHost)
				if err != nil {
					_ = client.Close()
					return sftpReopenedMsg{generation: generation, err: fmt.Errorf("恢复第二台远程文件连接失败：%w", err)}
				}
				if peerPath == "" {
					peerPath, err = peerClient.WorkingDirectory()
				}
				if err != nil || peerPath == "" {
					peerPath, err = peerClient.RealPath(".")
				}
				if err != nil || peerPath == "" {
					peerPath = "."
				}
				peerPath = sftpclient.CleanRemotePath(peerPath)
				peerEntries, err = peerClient.List(peerPath)
				if err != nil {
					_ = client.Close()
					_ = peerClient.Close()
					return sftpReopenedMsg{generation: generation, err: err}
				}
				peerEntries = sortSFTPEntries(peerEntries)
			} else if peerHost.Kind == model.HostKindFTP {
				peerClient, peerPath, peerEntries, err = reopenFTPClient(peerHost, peerPassword, peerPath)
				if err != nil {
					_ = client.Close()
					return sftpReopenedMsg{generation: generation, err: fmt.Errorf("恢复第二台 FTP/FTPS 主机失败：%w", err)}
				}
				peerEntries = sortSFTPEntries(peerEntries)
			} else {
				localEntries, err = listLocalDirectory(localPath)
				if err != nil {
					_ = client.Close()
					return sftpReopenedMsg{generation: generation, err: fmt.Errorf("读取本地目录失败：%w", err)}
				}
			}
		}
		return sftpReopenedMsg{
			generation: generation, client: client,
			path: remotePath, entries: sortSFTPEntries(remoteEntries),
			peerClient: peerClient, peerPath: peerPath, peerEntries: peerEntries, localEntries: localEntries,
		}
	}
}

func reopenFTPClient(host model.Host, password, previousPath string) (*ftpclient.Client, string, []sftpclient.Entry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := ftpclient.Dial(ctx, ftpclient.Config{
		Address: host.IP, Port: host.Port, Username: host.User,
		Password: password, TLSMode: ftpclient.TLSMode(host.TLSMode), ActiveMode: host.FTPActiveMode,
	})
	if err != nil {
		return nil, "", nil, err
	}
	remotePath := previousPath
	if remotePath == "" {
		remotePath, err = client.WorkingDirectory()
	}
	if err != nil || remotePath == "" {
		remotePath, err = client.RealPath(".")
	}
	if err != nil || remotePath == "" {
		remotePath = "."
	}
	remotePath = sftpclient.CleanRemotePath(remotePath)
	entries, err := client.List(remotePath)
	if err != nil {
		_ = client.Close()
		return nil, "", nil, err
	}
	return client, remotePath, entries, nil
}

func (m *modelState) handleSFTPReopened(msg sftpReopenedMsg) {
	state := m.sftp
	if state == nil || state.generation != msg.generation {
		if msg.client != nil {
			_ = msg.client.Close()
		}
		if msg.peerClient != nil {
			_ = msg.peerClient.Close()
		}
		return
	}
	state.busy = false
	if msg.err != nil {
		if msg.client != nil {
			_ = msg.client.Close()
		}
		if msg.peerClient != nil {
			_ = msg.peerClient.Close()
		}
		state.status = "传输已取消，但恢复远程文件连接失败：" + msg.err.Error()
		state.statusErr = true
		return
	}
	state.client, state.path, state.entries = msg.client, msg.path, msg.entries
	state.peerClient, state.peerPath = msg.peerClient, msg.peerPath
	if msg.peerClient != nil {
		state.localEntries = msg.peerEntries
	} else if state.standalone {
		state.localEntries = msg.localEntries
	}
	rebuildSFTPEntryViews(state)
	state.selected = clamp(state.selected, 0, max(0, len(sftpPaneEntries(state, sftpRemoteSide))-1))
	state.localSelected = clamp(state.localSelected, 0, max(0, len(sftpPaneEntries(state, sftpLocalSide))-1))
	state.status = "传输已取消；远程文件连接已恢复"
	state.statusErr = false
}

func (m *modelState) closeSFTPBrowser() {
	if m.sftp != nil {
		_ = closeSFTPResources(m.sftp)
	}
	m.sftp = nil
}

func closeSFTPResources(state *sftpBrowserState) error {
	if state == nil {
		return nil
	}
	if state.imageEditor != nil {
		if state.imageEditor.cancel != nil {
			state.imageEditor.cancel()
		}
		if state.imageEditor.tempDir != "" {
			_ = os.RemoveAll(state.imageEditor.tempDir)
		}
		state.imageEditor = nil
	}
	if state.searchCancel != nil {
		state.searchCancel()
		state.searchCancel = nil
	}
	if state.compareCancel != nil {
		state.compareCancel()
		state.compareCancel = nil
	}
	if state.transferCancel != nil {
		state.transferCancel()
	}
	if state.mirrorDeleteCancel != nil {
		state.mirrorDeleteCancel()
		state.mirrorDeleteCancel = nil
	}
	var closeErr error
	if len(state.paneTabs[sftpLocalSide])+len(state.paneTabs[sftpRemoteSide]) > 0 {
		saveSFTPActivePaneTab(state, sftpLocalSide)
		saveSFTPActivePaneTab(state, sftpRemoteSide)
		for _, side := range []sftpSide{sftpLocalSide, sftpRemoteSide} {
			for _, tab := range state.paneTabs[side] {
				if tab == nil {
					continue
				}
				if tab.client != nil {
					if err := tab.client.Close(); closeErr == nil && err != nil {
						closeErr = err
					}
					tab.client = nil
				}
				if tab.ownsSession && tab.session != nil {
					if err := tab.session.Close(); closeErr == nil && err != nil {
						closeErr = err
					}
					tab.session = nil
				}
				tab.ftpPassword = ""
			}
		}
	} else {
		for _, client := range []remotefs.Client{state.client, state.peerClient} {
			if client != nil {
				if err := client.Close(); closeErr == nil && err != nil {
					closeErr = err
				}
			}
		}
		if state.ownsSession && state.session != nil {
			if err := state.session.Close(); closeErr == nil && err != nil {
				closeErr = err
			}
		}
		if state.peerSession != nil {
			if err := state.peerSession.Close(); closeErr == nil && err != nil {
				closeErr = err
			}
		}
	}
	state.ftpPassword = ""
	state.peerFTPPassword = ""
	return closeErr
}

func (m *modelState) updateSFTPMouse(msg tea.MouseMsg) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy || state.prompt != nil || state.properties != nil || (state.editor != nil && !state.externalEditorOnly) || state.ftpCommand != nil || state.imageEditor != nil {
		return nil
	}
	if state.queueReviewActive {
		if wheel, ok := msg.(tea.MouseWheelMsg); ok {
			delta := 3
			if wheel.Button == tea.MouseWheelUp {
				delta = -3
			}
			state.queueReviewIndex = clamp(state.queueReviewIndex+delta, 0, max(0, len(state.transferQueue)-1))
			state.queueReviewTop = keepVisible(state.queueReviewIndex, state.queueReviewTop, max(1, (m.height-7)/2))
			return nil
		}
		click, ok := msg.(tea.MouseClickMsg)
		if !ok || click.Button != tea.MouseLeft || msg.Mouse().Y < 3 || msg.Mouse().Y >= m.height-2 {
			return nil
		}
		index := state.queueReviewTop + (msg.Mouse().Y-3)/2
		if index >= 0 && index < len(state.transferQueue) {
			doubleClick := state.queueReviewIndex == index && state.lastMouseEntry == index &&
				state.lastMouseSide == state.queueReviewSide && time.Since(state.lastMouseClick) <= mouseDoubleClickWindow
			state.queueReviewIndex = index
			state.queueReviewTop = keepVisible(index, state.queueReviewTop, max(1, (m.height-7)/2))
			if doubleClick {
				state.lastMouseClick = time.Time{}
				return m.updateSFTPQueueReviewKey(tea.Key{Code: tea.KeyEnter})
			}
			state.lastMouseEntry = index
			state.lastMouseSide = state.queueReviewSide
			state.lastMouseClick = time.Now()
		}
		return nil
	}
	if state.comparison != nil {
		return nil
	}
	mouse := msg.Mouse()
	if state.searchActive {
		if wheel, ok := msg.(tea.MouseWheelMsg); ok {
			delta := 3
			if wheel.Button == tea.MouseWheelUp {
				delta = -3
			}
			state.searchSelected = clamp(state.searchSelected+delta, 0, max(0, len(state.searchResults)-1))
			state.searchTop = keepVisible(state.searchSelected, state.searchTop, max(1, m.height-7))
			return nil
		}
		click, ok := msg.(tea.MouseClickMsg)
		if !ok || click.Button != tea.MouseLeft || mouse.Y < 4 || mouse.Y >= m.height-2 {
			return nil
		}
		index := state.searchTop + mouse.Y - 4
		if index < 0 || index >= len(state.searchResults) {
			return nil
		}
		if state.searchSelected == index && state.lastMouseEntry == index && state.lastMouseSide == state.searchSide && time.Since(state.lastMouseClick) <= mouseDoubleClickWindow {
			state.lastMouseClick = time.Time{}
			return m.openSFTPSearchResult()
		}
		state.searchSelected = index
		state.lastMouseEntry = index
		state.lastMouseSide = state.searchSide
		state.lastMouseClick = time.Now()
		return nil
	}
	leftWidth := max(1, (max(3, m.width)-1)/2)
	side := sftpRemoteSide
	if state.standalone {
		side = sftpLocalSide
		if mouse.X >= leftWidth+1 {
			side = sftpRemoteSide
		}
	}
	tabRow := 0
	if m.workspaceVisible && len(m.tabs) > 0 {
		tabRow = 1
	}
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft && mouse.Y == tabRow {
		columnX := mouse.X
		columnWidth := leftWidth
		if side == sftpRemoteSide {
			columnX -= leftWidth + 1
			columnWidth = max(1, max(3, m.width)-leftWidth-1)
		}
		if index := sftpPaneTabAtColumn(state, side, columnX, columnWidth); index >= 0 {
			ensureSFTPPaneTabs(state)
			saveSFTPActivePaneTab(state, side)
			if activateSFTPPaneTab(state, side, index) {
				state.status = fmt.Sprintf("已切换%s标签 %d/%d · %s", sftpSideLabel(side), index+1, len(state.paneTabs[side]), sftpPaneTabTitle(state.paneTabs[side][index]))
				state.statusErr = false
			}
		}
		return nil
	}
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		delta := 3
		if wheel.Button == tea.MouseWheelUp {
			delta = -3
		}
		state.activeSide = side
		m.moveSFTPSelection(delta)
		return nil
	}
	listFirstRow := sftpListFirstRow
	if state.standalone {
		listFirstRow = sftpPaneListFirstRow
	}
	if m.workspaceVisible && len(m.tabs) > 0 {
		listFirstRow++
	}
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseRight {
		if mouse.Y < listFirstRow || mouse.Y >= m.height-2 {
			return nil
		}
		index := sftpPaneTop(state, side) + mouse.Y - listFirstRow
		entries := sftpPaneEntries(state, side)
		if index < 0 || index >= len(entries) {
			index = -1
		} else {
			selected := sftpPaneSelection(state, side)
			*selected = index
			state.lastMouseEntry = index
			state.lastMouseSide = side
		}
		state.lastMouseClick = time.Time{}
		state.activeSide = side
		m.openSFTPContextMenu(side, index, mouse.X, mouse.Y)
		return nil
	}
	click, ok := msg.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft || mouse.Y < listFirstRow || mouse.Y >= m.height-2 {
		return nil
	}
	state.activeSide = side
	index := sftpPaneTop(state, side) + mouse.Y - listFirstRow
	entries := sftpPaneEntries(state, side)
	if index < 0 || index >= len(entries) {
		return nil
	}
	selected := sftpPaneSelection(state, side)
	if *selected == index && state.lastMouseEntry == index && state.lastMouseSide == side && time.Since(state.lastMouseClick) <= mouseDoubleClickWindow {
		state.lastMouseClick = time.Time{}
		return m.enterSFTPSelection()
	}
	*selected = index
	state.lastMouseEntry = index
	state.lastMouseSide = side
	state.lastMouseClick = time.Now()
	return nil
}

func sftpPaneTabAtColumn(state *sftpBrowserState, side sftpSide, x, paneWidth int) int {
	if state == nil || x < 0 {
		return -1
	}
	tabs := state.paneTabs[side]
	if len(tabs) == 0 {
		return 0
	}
	cursor := 0
	for index, tab := range tabs {
		if index > 0 {
			cursor++
		}
		label := fmt.Sprintf(" %d %s ", index+1, sftpPaneTabTitle(tab))
		width := lipgloss.Width(label)
		if cursor+width > paneWidth {
			return -1
		}
		if x >= cursor && x < cursor+width {
			return index
		}
		cursor += width
	}
	return -1
}

const sftpListFirstRow = 4
const sftpPaneListFirstRow = 5

func sftpVisibleRows(height int) int {
	return max(1, height-sftpListFirstRow-2)
}

func sftpPaneVisibleRows(height int) int {
	return max(1, height-sftpPaneListFirstRow)
}

func (m modelState) sftpVisibleRowsFor(state *sftpBrowserState, height int) int {
	firstRow := sftpListFirstRow
	if state != nil && state.standalone {
		firstRow = sftpPaneListFirstRow
	}
	reservedRows := 0
	if m.workspaceVisible && len(m.tabs) > 0 {
		reservedRows = 1
	}
	return max(1, height-reservedRows-firstRow-2)
}

func sftpPaneEntries(state *sftpBrowserState, side sftpSide) []sftpclient.Entry {
	return sftpFilteredPaneEntries(state, side)
}

func sftpPaneRawEntries(state *sftpBrowserState, side sftpSide) []sftpclient.Entry {
	if state == nil {
		return nil
	}
	if side == sftpRemoteSide {
		return state.entries
	}
	return state.localEntries
}

func sftpPaneTop(state *sftpBrowserState, side sftpSide) int {
	if side == sftpRemoteSide {
		return state.top
	}
	return state.localTop
}

func sftpPaneSelection(state *sftpBrowserState, side sftpSide) *int {
	if side == sftpRemoteSide {
		return &state.selected
	}
	return &state.localSelected
}

func (m modelState) sftpView() string {
	state := m.sftp
	if state == nil {
		return ""
	}
	width, height := max(1, m.width), max(8, m.height)
	if state.ftpCommand != nil {
		return renderFTPCommandResult(state.ftpCommand, width, height)
	}
	if state.properties != nil {
		return renderSFTPProperties(state, state.properties, width, height)
	}
	if state.editor != nil && !state.externalEditorOnly {
		content := renderSFTPTextEditor(state.editor, width, height, state.status, state.statusErr,
			m.workspaceLeaderChord("external_edit"), m.workspaceLeaderKeyLabel(), displayWorkspaceKey(m.config.WithDefaults().Workspace.Bindings["external_edit"]))
		if state.prompt != nil {
			lines := strings.Split(content, "\n")
			lines[height-2] = fitLine(warningStyle.Render(state.prompt.title), width)
			help := "s 保存并返回  d 放弃修改  Esc 继续编辑"
			if state.prompt.action == sftpPromptEditorFind || state.prompt.action == sftpPromptEditorReplaceFind || state.prompt.action == sftpPromptEditorReplaceValue {
				lines[height-1] = fitLine(selectedStyle.Width(width).Render("  "+state.prompt.value), width)
			} else {
				lines[height-1] = fitLine(mutedStyle.Render(help), width)
			}
			content = strings.Join(lines, "\n")
		}
		return content
	}
	if state.comparison != nil {
		return renderSFTPSyncComparison(state.comparison, state.status, state.statusErr, width, height, state.busy)
	}
	if state.queueReviewActive {
		return renderSFTPQueueReview(state, width, height)
	}
	if state.searchActive {
		return renderSFTPSearchView(state, width, height)
	}
	if state.standalone {
		return m.standaloneSFTPView(state)
	}
	lines := make([]string, height)
	lines[0] = fitLine(brandStyle.Render("远程文件")+"  "+titleStyle.Render(sanitizeRemoteText(state.host.Name)), width)
	pathLine := "路径  " + sanitizeRemoteText(state.path)
	if state.remoteFilter.raw != "" {
		pathLine += "  过滤 " + sanitizeRemoteText(state.remoteFilter.raw)
	}
	lines[1] = fitLine(mutedStyle.Render(pathLine), width)
	lines[2] = separatorStyle.Render(strings.Repeat("─", width))
	lines[3] = formatSFTPListHeader(width)
	entries := sftpPaneEntries(state, sftpRemoteSide)
	visible := sftpVisibleRows(height)
	top := clamp(state.top, 0, max(0, len(entries)-visible))
	for row := 0; row < visible && sftpListFirstRow+row < height-2; row++ {
		index := top + row
		if index >= len(entries) {
			break
		}
		lines[sftpListFirstRow+row] = formatSFTPEntry(entries[index], index == state.selected, m.sftpEntryMarked(state, sftpRemoteSide, entries[index]), width)
	}
	status := state.status
	if state.busy && state.transferTotal > 0 {
		percent := clamp(int(float64(state.transferred)/float64(state.transferTotal)*100), 0, 100)
		status = fmt.Sprintf("%s  %s / %s  %d%%", state.status,
			formatByteCount(state.transferred), formatByteCount(state.transferTotal), percent)
	}
	if state.busy {
		if state.queueActive {
			status += sftpQueueProgressSuffix(state.queueProgress)
		} else {
			status += sftpProgressSuffix(state.currentProgress)
		}
	}
	if state.busy && status == "" {
		status = "正在处理…"
	}
	if state.transferPaused {
		status += "  · 已暂停"
	}
	if state.statusErr {
		lines[height-2] = fitLine(statusErrorStyle.Render(sanitizeRemoteText(status)), width)
	} else {
		lines[height-2] = fitLine(secondaryStyle.Render(sanitizeRemoteText(status)), width)
	}
	help := "右键文件打开菜单  ·  Esc 保持打开  q 关闭标签(确认)  ↑↓/j/k 选择  Enter 进入/预览  g 跳转路径  / 搜索  f 过滤  F 过滤方案  B 目录书签  Backspace 上级  u 上传  d 下载  v 预览  e 编辑  E 编辑器设置  i 属性  n 重命名  p 权限  m 建目录  x 删除  r 刷新"
	if state.transferPauseGate != nil {
		help = "Space 暂停/恢复传输  ·  Esc 取消传输"
	}
	lines[height-1] = fitLine(mutedStyle.Render(help), width)
	if state.prompt != nil {
		lines[height-2] = fitLine(warningStyle.Render(state.prompt.title), width)
		if state.prompt.action != sftpPromptDelete && state.prompt.action != sftpPromptOverwrite && state.prompt.action != sftpPromptDiscard && state.prompt.action != sftpPromptMirrorConfirm {
			lines[height-1] = fitLine(selectedStyle.Width(width).Render("  "+state.prompt.value), width)
		}
	}
	return strings.Join(lines, "\n")
}

func (m modelState) sftpViewForState(state *sftpBrowserState) string {
	if state == nil {
		return ""
	}
	m.sftp = state
	m.height = max(8, m.height-1) // reserve the workspace tab row
	return m.sftpView()
}

func renderSFTPQueueReview(state *sftpBrowserState, width, height int) string {
	lines := make([]string, height)
	lines[0] = fitLine(brandStyle.Render("传输队列")+"  "+titleStyle.Render(fmt.Sprintf("%d 项", len(state.transferQueue))), width)
	lines[1] = fitLine(mutedStyle.Render("传输开始前调整优先级；上下移动选择项目，+ 提前，- 延后"), width)
	lines[2] = fitLine(separatorStyle.Render(strings.Repeat("─", width)), width)
	visible := max(1, (height-7)/2)
	top := clamp(state.queueReviewTop, 0, max(0, len(state.transferQueue)-visible))
	for row := 0; row < visible; row++ {
		index := top + row
		if index >= len(state.transferQueue) {
			break
		}
		transfer := state.transferQueue[index]
		firstRow := 3 + row*2
		secondRow := firstRow + 1
		if secondRow >= height-2 {
			break
		}
		marker := "  "
		if index == state.queueReviewIndex {
			marker = "› "
		}
		summary := fmt.Sprintf("%s%02d  %-12s %s", marker, index+1, sftpTransferRoute(transfer), sanitizeRemoteText(sftpTransferLabel(transfer)))
		detail := fmt.Sprintf("    %s → %s", sftpTransferSourcePath(transfer), sftpTransferDestinationPath(transfer))
		if index == state.queueReviewIndex {
			lines[firstRow] = fitLine(selectedStyle.Width(width).Render(summary), width)
			lines[secondRow] = fitLine(selectedStyle.Width(width).Render(detail), width)
		} else {
			lines[firstRow] = fitLine(secondaryStyle.Render(summary), width)
			lines[secondRow] = fitLine(mutedStyle.Render(detail), width)
		}
	}
	if state.statusErr {
		lines[height-2] = fitLine(statusErrorStyle.Render(sanitizeRemoteText(state.status)), width)
	} else {
		lines[height-2] = fitLine(secondaryStyle.Render(sanitizeRemoteText(state.status)), width)
	}
	lines[height-1] = fitLine(mutedStyle.Render("↑/↓ 选择  + 提前  - 延后  Enter 开始  Esc 取消"), width)
	return strings.Join(lines, "\n")
}

func renderFTPCommandResult(output *ftpCommandOutput, width, height int) string {
	width, height = max(1, width), max(8, height)
	lines := make([]string, height)
	lines[0] = fitLine(brandStyle.Render("FTP 原始命令回复"), width)
	lines[1] = fitLine(mutedStyle.Render("命令  "+sanitizeRemoteText(output.command)), width)
	reply := fmt.Sprintf("服务器响应  %03d", output.code)
	if output.code == 0 {
		reply = "服务器响应  无"
	}
	lines[2] = fitLine(secondaryStyle.Render(reply), width)
	lines[3] = fitLine(separatorStyle.Render(strings.Repeat("─", width)), width)
	visible := ftpCommandVisibleRows(height)
	top := clamp(output.top, 0, max(0, len(output.lines)-visible))
	for row := 0; row < visible && row+4 < height-2; row++ {
		index := top + row
		if index >= len(output.lines) {
			break
		}
		line := sanitizeRemoteText(output.lines[index])
		if strings.HasPrefix(line, "错误：") {
			lines[row+4] = fitLine(statusErrorStyle.Render(line), width)
		} else {
			lines[row+4] = fitLine(secondaryStyle.Render(line), width)
		}
	}
	status := "命令执行完成"
	if output.err != "" {
		status = "命令执行失败：" + sanitizeRemoteText(output.err)
	} else if output.code >= 400 {
		status = fmt.Sprintf("服务器拒绝命令（%d）", output.code)
	}
	if output.err != "" || output.code >= 400 {
		lines[height-2] = fitLine(statusErrorStyle.Render(status), width)
	} else {
		lines[height-2] = fitLine(secondaryStyle.Render(status), width)
	}
	lines[height-1] = fitLine(mutedStyle.Render("↑/↓ 翻阅回复  Enter/Esc 返回文件列表"), width)
	return strings.Join(lines, "\n")
}

func ftpCommandVisibleRows(height int) int {
	return max(1, max(8, height)-6)
}

func sftpTransferRoute(transfer sftpTransferRequest) string {
	switch {
	case transfer.sourceRemote && transfer.destRemote:
		return "远程→远程"
	case transfer.sourceRemote:
		return "远程→本地"
	case transfer.destRemote:
		return "本地→远程"
	default:
		return "本地→本地"
	}
}

func sftpTransferSourcePath(transfer sftpTransferRequest) string {
	if transfer.sourcePath != "" {
		return sanitizeRemoteText(transfer.sourcePath)
	}
	if transfer.direction == sftpDownload {
		return sanitizeRemoteText(transfer.remotePath)
	}
	return sanitizeRemoteText(transfer.localPath)
}

func sftpTransferDestinationPath(transfer sftpTransferRequest) string {
	if transfer.destPath != "" {
		return sanitizeRemoteText(transfer.destPath)
	}
	if transfer.direction == sftpDownload {
		return sanitizeRemoteText(transfer.localPath)
	}
	return sanitizeRemoteText(transfer.remotePath)
}

func renderSFTPSearchView(state *sftpBrowserState, width, height int) string {
	lines := make([]string, height)
	viewTitle, matchDescription := "文件搜索", "名称匹配（不区分大小写） · Enter 打开所在目录并定位"
	if state.searchFilterMode {
		viewTitle, matchDescription = "递归过滤", "应用当前过滤表达式 · Enter 打开所在目录并定位"
	}
	lines[0] = fitLine(brandStyle.Render(viewTitle)+"  "+titleStyle.Render(sanitizeRemoteText(state.searchQuery)), width)
	lines[1] = fitLine(mutedStyle.Render("搜索范围  "+sanitizeRemoteText(state.searchRoot)), width)
	lines[2] = fitLine(separatorStyle.Render(strings.Repeat("─", width)), width)
	lines[3] = fitLine(mutedStyle.Render(matchDescription), width)
	visible := max(0, height-7)
	top := clamp(state.searchTop, 0, max(0, len(state.searchResults)-visible))
	for row := 0; row < visible; row++ {
		index := top + row
		if index >= len(state.searchResults) {
			break
		}
		result := state.searchResults[index]
		kind := "文件"
		if result.Entry.IsDir {
			kind = "目录"
		} else if result.Entry.IsSymlink {
			kind = "链接"
		}
		line := fmt.Sprintf("%-5s %s", kind, sanitizeRemoteText(result.Path))
		if index == state.searchSelected {
			lines[4+row] = fitLine(selectedStyle.Width(width).Render(line), width)
		} else {
			lines[4+row] = fitLine(secondaryStyle.Render(line), width)
		}
	}
	status := state.status
	if state.busy && status == "" {
		status = "正在递归搜索文件…"
		if state.searchFilterMode {
			status = "正在递归过滤文件…"
		}
	}
	if state.statusErr {
		lines[height-2] = fitLine(statusErrorStyle.Render(sanitizeRemoteText(status)), width)
	} else {
		lines[height-2] = fitLine(secondaryStyle.Render(sanitizeRemoteText(status)), width)
	}
	help := "↑↓/j/k 选择  Enter 定位  / 重新搜索  Esc 返回"
	if state.busy {
		help = "搜索中；Esc 取消搜索"
	}
	lines[height-1] = fitLine(mutedStyle.Render(help), width)
	return strings.Join(lines, "\n")
}

func (m modelState) standaloneSFTPView(state *sftpBrowserState) string {
	width, height := max(1, m.width), max(8, m.height)
	leftWidth := max(1, (width-1)/2)
	rightWidth := max(1, width-leftWidth-1)
	bodyHeight := max(1, height-2)
	body := joinFixedColumns(bodyHeight,
		fixedColumn{content: m.renderSFTPColumn(state, sftpLocalSide, leftWidth, bodyHeight), width: leftWidth},
		fixedColumn{content: focusLineStyle.Render(strings.TrimSuffix(strings.Repeat("│\n", bodyHeight), "\n")), width: 1},
		fixedColumn{content: m.renderSFTPColumn(state, sftpRemoteSide, rightWidth, bodyHeight), width: rightWidth},
	)
	status := state.status
	if state.busy && state.transferTotal > 0 {
		percent := clamp(int(float64(state.transferred)/float64(state.transferTotal)*100), 0, 100)
		status = fmt.Sprintf("%s  %s / %s  %d%%", state.status,
			formatByteCount(state.transferred), formatByteCount(state.transferTotal), percent)
	}
	if state.busy {
		if state.queueActive {
			status += sftpQueueProgressSuffix(state.queueProgress)
		} else {
			status += sftpProgressSuffix(state.currentProgress)
		}
	}
	if state.transferPaused {
		status += "  · 已暂停"
	}
	if state.synchronizedBrowsing {
		status += "  · 同步浏览"
	}
	if state.statusErr {
		status = statusErrorStyle.Render(sanitizeRemoteText(status))
	} else {
		status = secondaryStyle.Render(sanitizeRemoteText(status))
	}
	help := "右键文件打开菜单  ·  Esc 保持打开  q 关闭工作区  Tab/←→ 切栏  [ ] 切换当前栏标签  Ctrl+W 关闭当前栏标签  c 添加远程  b 添加本地  ↑↓/j/k 选择  Space 标记  Enter 进入  g 跳转路径  / 搜索  f 过滤  F 过滤方案  B 目录书签  = 比较/同步  y 同步浏览  u 传到右栏  d 传到左栏  v 预览  e 编辑  E 编辑器设置  i 属性  p 权限  m 新建  n 重命名  x 删除"
	if ftpClientForSide(state, state.activeSide) != nil {
		help = strings.Replace(help, "f 过滤  F", "f 过滤  : FTP 命令  F", 1)
	}
	if state.transferPauseGate != nil {
		help = "Space 暂停/恢复传输  ·  Esc 取消传输"
	}
	if state.prompt != nil {
		status = warningStyle.Render(sanitizeRemoteText(state.prompt.title))
		if state.prompt.action != sftpPromptDelete && state.prompt.action != sftpPromptOverwrite && state.prompt.action != sftpPromptDiscard && state.prompt.action != sftpPromptMirrorConfirm && state.prompt.action != sftpPromptClosePaneTab {
			help = selectedStyle.Render("  " + state.prompt.value)
		}
	}
	return body + "\n" + fitLine(status, width) + "\n" + fitLine(mutedStyle.Render(help), width)
}

func renderSFTPProperties(state *sftpBrowserState, properties *sftpProperties, width, height int) string {
	lines := make([]string, height)
	entry := properties.entry
	lines[0] = fitLine(brandStyle.Render("文件属性")+"  "+titleStyle.Render(sanitizeRemoteText(entry.Name)), width)
	lines[1] = fitLine(separatorStyle.Render(strings.Repeat("─", width)), width)

	protocol := "本地文件系统"
	if sftpSideIsRemote(state, properties.side) {
		host := state.host
		if properties.side == sftpLocalSide {
			host = state.peerHost
		}
		switch {
		case host.Kind == model.HostKindFTP && host.TLSMode == model.FTPTLSImplicit:
			protocol = "FTPS（隐式）· " + host.Name
		case host.Kind == model.HostKindFTP && host.TLSMode == model.FTPTLSExplicit:
			protocol = "FTPS（显式）· " + host.Name
		case host.Kind == model.HostKindFTP:
			protocol = "FTP · " + host.Name
		case host.FileProtocol == model.SSHFileProtocolSCP:
			protocol = "SCP · " + host.Name
		default:
			protocol = "SFTP · " + host.Name
		}
	}
	kind := sftpPropertyType(entry)
	size := fmt.Sprintf("%s（%d 字节）", formatByteCount(entry.Size), entry.Size)
	modified := "未知"
	if !entry.ModTime.IsZero() {
		modified = entry.ModTime.Local().Format("2006-01-02 15:04:05 MST")
	}
	permissions := "服务器未提供"
	if entry.ModeKnown {
		permissions = fmt.Sprintf("%s（%03o）", entry.Mode.Perm(), entry.Mode.Perm())
	}
	ownership := "未提供"
	if entry.OwnershipKnown {
		ownership = fmt.Sprintf("UID %d · GID %d", entry.UID, entry.GID)
	}
	rows := []string{
		"来源  " + sanitizeRemoteText(protocol) + " · " + kind,
		"位置  " + sanitizeRemoteText(properties.path),
		"大小  " + size + "    修改时间  " + modified,
		"权限  " + permissions + "    属主/属组  " + ownership,
	}
	for index, row := range rows {
		line := index + 2
		if line >= height-2 {
			break
		}
		lines[line] = fitLine(secondaryStyle.Render(row), width)
	}
	lines[height-2] = fitLine(mutedStyle.Render("只读属性；缺失的服务器元数据不会推断。"), width)
	lines[height-1] = fitLine(mutedStyle.Render("Enter / Esc / i 返回文件列表"), width)
	return strings.Join(lines, "\n")
}

func sftpPropertyType(entry sftpclient.Entry) string {
	if entry.IsSymlink {
		return "符号链接"
	}
	if entry.IsDir {
		return "目录"
	}
	switch {
	case entry.Mode&os.ModeSocket != 0:
		return "套接字"
	case entry.Mode&os.ModeNamedPipe != 0:
		return "命名管道"
	case entry.Mode&os.ModeDevice != 0 && entry.Mode&os.ModeCharDevice != 0:
		return "字符设备"
	case entry.Mode&os.ModeDevice != 0:
		return "块设备"
	case !entry.ModeKnown:
		return "文件（类型未提供）"
	default:
		return "普通文件"
	}
}

func (m modelState) renderSFTPColumn(state *sftpBrowserState, side sftpSide, width, height int) string {
	entries := sftpPaneEntries(state, side)
	rawEntries := sftpPaneRawEntries(state, side)
	filter := sftpFilterForSide(state, side)
	selected := *sftpPaneSelection(state, side)
	top := clamp(sftpPaneTop(state, side), 0, max(0, len(entries)-sftpPaneVisibleRows(height)))
	remoteSide := sftpSideIsRemote(state, side)
	currentPath, label := m.currentSFTPPathFor(state, side), "本地"
	if remoteSide {
		label = "远程"
	}
	countText := fmt.Sprintf("%02d 项", len(entries))
	if filter.raw != "" {
		countText = fmt.Sprintf("%02d/%02d 项", len(entries), len(rawEntries))
	}
	marker := "  "
	if state.activeSide == side {
		marker = "▶ "
	}
	titleText := fmt.Sprintf("%s%s  %s", marker, label, countText)
	markedCount := len(state.localMarks)
	if remoteSide {
		markedCount = len(state.remoteMarks)
		if side == sftpLocalSide {
			markedCount = len(state.localMarks)
		}
	}
	if markedCount > 0 {
		titleText += fmt.Sprintf("  已选 %d", markedCount)
	}
	if side == sftpRemoteSide && remoteSide {
		titleText = fmt.Sprintf("%s远程 · %s  %s", marker, sanitizeRemoteText(state.host.Name), countText)
		if markedCount > 0 {
			titleText += fmt.Sprintf("  已选 %d", markedCount)
		}
	} else if side == sftpLocalSide && remoteSide {
		titleText = fmt.Sprintf("%s远程 · %s  %s", marker, sanitizeRemoteText(state.peerHost.Name), countText)
		if markedCount > 0 {
			titleText += fmt.Sprintf("  已选 %d", markedCount)
		}
	}
	title := titleText
	if state.activeSide == side {
		title = paneFocusStyle.Width(width).Render(fitText(title, width))
	} else {
		title = sectionStyle.Render(title)
	}
	lines := make([]string, height)
	lines[0] = renderSFTPPaneTabLine(state, side, width)
	if height > 1 {
		lines[1] = fitLine(title, width)
	}
	pathText := "路径  " + sanitizeRemoteText(currentPath)
	if filter.raw != "" {
		pathText += "  ·  过滤 " + sanitizeRemoteText(filter.raw)
	}
	if height > 2 {
		lines[2] = fitLine(mutedStyle.Render(pathText), width)
	}
	if height > 3 {
		lines[3] = separatorStyle.Render(strings.Repeat("─", width))
	}
	if height > 4 {
		lines[4] = formatSFTPListHeader(width)
	}
	for row := sftpPaneListFirstRow; row < height; row++ {
		index := top + row - sftpPaneListFirstRow
		if index >= len(entries) {
			break
		}
		lines[row] = formatSFTPEntry(entries[index], index == selected, m.sftpEntryMarked(state, side, entries[index]), width)
	}
	return strings.Join(lines, "\n")
}

func renderSFTPPaneTabLine(state *sftpBrowserState, side sftpSide, width int) string {
	if state == nil || width <= 0 {
		return ""
	}
	tabs := state.paneTabs[side]
	if len(tabs) == 0 {
		label := "本地"
		if sftpSideIsRemote(state, side) {
			if side == sftpLocalSide {
				label = state.peerHost.Name
			} else {
				label = state.host.Name
			}
		}
		style := selectedStyle
		if side == state.activeSide {
			style = paneFocusStyle
		}
		return fitLine(style.Render(" 1 "+sanitizeRemoteText(label)+" "), width)
	}
	var builder strings.Builder
	used := 0
	for index, tab := range tabs {
		separator := " "
		separatorWidth := lipgloss.Width(separator)
		label := fmt.Sprintf("%d %s", index+1, sftpPaneTabTitle(tab))
		segment := " " + label + " "
		segmentWidth := lipgloss.Width(segment)
		if used+separatorWidth+segmentWidth > width {
			remaining := width - used
			if remaining > 0 {
				builder.WriteString(fitLine(mutedStyle.Render(" … "), remaining))
			}
			break
		}
		if index > 0 {
			builder.WriteString(mutedStyle.Render(separator))
			used += separatorWidth
		}
		style := mutedStyle
		if index == state.activePaneTab[side] {
			style = selectedStyle
			if side == state.activeSide {
				style = paneFocusStyle
			}
		}
		builder.WriteString(style.Render(segment))
		used += segmentWidth
	}
	return fitLine(builder.String(), width)
}

func formatSFTPEntry(entry sftpclient.Entry, selected, marked bool, width int) string {
	width = max(1, width)
	icon, name := "·", sanitizeRemoteText(entry.Name)
	iconColor := palette.muted
	nameStyle := secondaryStyle
	switch {
	case entry.IsSymlink:
		icon, iconColor, nameStyle = "↪", palette.accent, accentStyle
	case entry.IsDir:
		icon, iconColor, nameStyle = "▸", palette.accent, accentStyle
	case entry.ModeKnown && entry.Mode&0o111 != 0:
		icon, iconColor, nameStyle = "◆", palette.warning, warningStyle
	case entry.ModeKnown && !entry.Mode.IsRegular():
		icon, iconColor, nameStyle = "◇", palette.warning, warningStyle
	default:
		icon, iconColor, nameStyle = sftpFileAppearance(name)
	}
	marker := "  "
	if marked {
		marker = "✓ "
	}
	if selected {
		marker = "› "
		if marked {
			marker = "✓›"
		}
	}
	columns := sftpEntryColumnsForWidth(width)
	metadata := make([]string, 0, 4)
	addMetadata := func(value string, columnWidth int, rightAligned bool) {
		if rightAligned {
			metadata = append(metadata, padSFTPColumn(value, columnWidth))
			return
		}
		metadata = append(metadata, padSFTPColumnLeft(value, columnWidth))
	}
	if columns.permissions {
		permissions := "---"
		if entry.ModeKnown {
			permissions = permissionString(entry.Mode)
		}
		addMetadata(permissions, 10, false)
	}
	if columns.size {
		size := "—"
		switch {
		case entry.IsDir:
			size = "目录"
		case entry.IsSymlink:
			size = "链接"
		case entry.Mode.IsRegular():
			size = formatByteCount(entry.Size)
		}
		addMetadata(size, 9, true)
	}
	if columns.modified {
		modified := "—"
		if !entry.ModTime.IsZero() {
			modified = entry.ModTime.Local().Format("01-02 15:04")
		}
		addMetadata(modified, 14, false)
	}
	if columns.owner {
		owner := "—"
		if entry.OwnershipKnown {
			owner = fmt.Sprintf("%d:%d", entry.UID, entry.GID)
		}
		addMetadata(owner, 13, false)
	}
	nameWidth := sftpEntryNameWidth(width, columns)
	if entry.IsDir && !entry.IsSymlink {
		name += "/"
	}
	name = padSFTPColumnLeft(name, nameWidth)
	line := marker + icon + " " + name
	for _, value := range metadata {
		line += "  " + value
	}
	if selected {
		return selectedStyle.Width(width).Render(fitText(line, width))
	}
	markerStyle := mutedStyle
	if marked {
		markerStyle = accentStyle
	}
	iconStyle := lipgloss.NewStyle().Foreground(iconColor)
	colored := markerStyle.Render(marker) + iconStyle.Render(icon+" ") + nameStyle.Render(name)
	for _, value := range metadata {
		colored += mutedStyle.Render("  " + value)
	}
	return fitLine(colored, width)
}

type sftpListColumns struct {
	permissions bool
	size        bool
	modified    bool
	owner       bool
}

func sftpEntryColumnsForWidth(width int) sftpListColumns {
	return sftpListColumns{
		permissions: width >= 116,
		size:        width >= 34,
		modified:    width >= 76,
		owner:       width >= 116,
	}
}

func formatSFTPListHeader(width int) string {
	width = max(1, width)
	columns := sftpEntryColumnsForWidth(width)
	fields := make([]string, 0, 4)
	if columns.permissions {
		fields = append(fields, padSFTPColumnLeft("权限", 10))
	}
	if columns.size {
		fields = append(fields, padSFTPColumn("大小", 9))
	}
	if columns.modified {
		fields = append(fields, padSFTPColumnLeft("修改时间", 14))
	}
	if columns.owner {
		fields = append(fields, padSFTPColumnLeft("属主", 13))
	}
	line := "    " + padSFTPColumnLeft("名称", sftpEntryNameWidth(width, columns))
	for _, field := range fields {
		line += "  " + field
	}
	return fitLine(mutedStyle.Render(line), width)
}

func sftpEntryNameWidth(width int, columns sftpListColumns) int {
	metadataWidth := 0
	count := 0
	if columns.permissions {
		metadataWidth += 10
		count++
	}
	if columns.size {
		metadataWidth += 9
		count++
	}
	if columns.modified {
		metadataWidth += 14
		count++
	}
	if columns.owner {
		metadataWidth += 13
		count++
	}
	if count == 0 {
		return max(1, width-4)
	}
	metadataWidth += 2 * (count - 1)
	return max(1, width-4-metadataWidth-2) // two-cell gap before the metadata columns
}

func sftpFileAppearance(name string) (string, color.Color, lipgloss.Style) {
	extension := path.Ext(strings.ToLower(name))
	switch extension {
	case ".go", ".rs", ".py", ".js", ".jsx", ".ts", ".tsx", ".java", ".c", ".h", ".cpp", ".hpp", ".sh", ".zsh", ".lua", ".rb", ".php":
		return "◇", palette.accent, accentStyle
	case ".md", ".txt", ".rst", ".pdf", ".doc", ".docx", ".odt":
		return "≡", palette.primary, secondaryStyle
	case ".zip", ".gz", ".bz2", ".xz", ".zst", ".7z", ".rar", ".tar":
		return "▤", palette.warning, warningStyle
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp", ".ico":
		return "▧", palette.accent, secondaryStyle
	case ".toml", ".yaml", ".yml", ".json", ".ini", ".conf", ".config", ".xml":
		return "▦", palette.warning, secondaryStyle
	default:
		return "·", palette.muted, secondaryStyle
	}
}

func permissionString(mode fs.FileMode) string {
	permission := []byte("---------")
	for index, bit := range []fs.FileMode{0o400, 0o200, 0o100, 0o040, 0o020, 0o010, 0o004, 0o002, 0o001} {
		if mode.Perm()&bit != 0 {
			permission[index] = "rwx"[index%3]
		}
	}
	return string(permission)
}

func padSFTPColumn(value string, width int) string {
	value = fitText(value, width)
	return strings.Repeat(" ", max(0, width-lipgloss.Width(value))) + value
}

func padSFTPColumnLeft(value string, width int) string {
	value = fitText(value, width)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func sortSFTPEntries(entries []sftpclient.Entry) []sftpclient.Entry {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries
}

func formatByteCount(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, unit := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= 1024
		if value < 1024 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/1024)
}

func sanitizeRemoteText(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return '�'
		}
		return character
	}, value)
}

func sftpPromptCursor(state *sftpBrowserState, width, height int) *tea.Cursor {
	if state == nil || state.prompt == nil || state.prompt.action == sftpPromptDelete || state.prompt.action == sftpPromptOverwrite || state.prompt.action == sftpPromptDiscard || state.prompt.action == sftpPromptMirrorConfirm {
		return nil
	}
	if state.editor != nil && state.prompt.action != sftpPromptEditorFind && state.prompt.action != sftpPromptEditorReplaceFind && state.prompt.action != sftpPromptEditorReplaceValue {
		return nil
	}
	valueWidth := min(len([]rune(state.prompt.value))+2, max(1, width-1))
	return tea.NewCursor(valueWidth, max(0, height-1))
}

func expandLocalPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "~" || strings.HasPrefix(value, "~/") {
		if currentUser, err := user.Current(); err == nil {
			if value == "~" {
				value = currentUser.HomeDir
			} else {
				value = filepath.Join(currentUser.HomeDir, strings.TrimPrefix(value, "~/"))
			}
		}
	}
	return filepath.Clean(value)
}
