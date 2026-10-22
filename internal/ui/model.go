package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"tui-ssh-workbench/internal/infra/credentialstore"
	"tui-ssh-workbench/internal/infra/desktopnotify"
	"tui-ssh-workbench/internal/infra/localterminal"
	"tui-ssh-workbench/internal/infra/rloginclient"
	"tui-ssh-workbench/internal/infra/serialclient"
	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/infra/telnetclient"
	"tui-ssh-workbench/internal/infra/terminalog"
	"tui-ssh-workbench/internal/model"
	"tui-ssh-workbench/internal/plugin"
)

// Catalog loads and creates saved groups and hosts for the home screen.
type Catalog interface {
	ListGroups(context.Context) ([]model.Group, error)
	ListHosts(context.Context) ([]model.Host, error)
	CreateGroup(context.Context, model.Group) (model.Group, error)
	UpdateGroup(context.Context, model.Group) error
	DeleteGroup(context.Context, string) error
	CreateHost(context.Context, model.Host) (model.Host, error)
	UpdateHost(context.Context, model.Host) error
	DeleteHost(context.Context, string) error
}

type authProfileCatalog interface {
	ListAuthProfiles(context.Context) ([]model.AuthProfile, error)
	CreateAuthProfile(context.Context, model.AuthProfile) (model.AuthProfile, error)
	UpdateAuthProfile(context.Context, model.AuthProfile) error
	DeleteAuthProfile(context.Context, string) error
}

type identityReferenceRotationCatalog interface {
	ReplaceIdentityFileReferences(context.Context, []model.IdentityFileReference, string) error
}

type defaultSessionCatalog interface {
	DefaultHostID(context.Context) (string, error)
	SetDefaultHostID(context.Context, string) error
}

type lastWorkspaceCatalog interface {
	LastWorkspaceData(context.Context) (string, error)
	SetLastWorkspaceData(context.Context, string) error
}

type appearancePreferenceCatalog interface {
	AppearanceScheme(context.Context) (string, error)
	SetAppearanceScheme(context.Context, string) error
}

type workspaceFooterPreferenceCatalog interface {
	FooterShortcutsVisible(context.Context) (bool, bool, error)
	SetFooterShortcutsVisible(context.Context, bool) error
}

type sftpEditorCommandPreferenceCatalog interface {
	SFTPExternalEditorCommand(context.Context) (string, error)
	SetSFTPExternalEditorCommand(context.Context, string) error
}

type portForwardRuleCatalog interface {
	ListPortForwardRules(context.Context, string) ([]model.PortForwardRule, error)
	SavePortForwardRule(context.Context, model.PortForwardRule) (model.PortForwardRule, error)
	DeletePortForwardRule(context.Context, string) error
}

type quickCommandCatalog interface {
	ListQuickCommands(context.Context) ([]model.QuickCommand, error)
	SaveQuickCommand(context.Context, model.QuickCommand) (model.QuickCommand, error)
	DeleteQuickCommand(context.Context, string) error
}

type sftpFilterCatalog interface {
	ListSFTPFilters(context.Context) ([]model.SFTPFilter, error)
	SaveSFTPFilter(context.Context, model.SFTPFilter) (model.SFTPFilter, error)
	DeleteSFTPFilter(context.Context, string) error
}

type sftpBookmarkCatalog interface {
	ListSFTPBookmarks(context.Context, string, bool) ([]model.SFTPBookmark, error)
	SaveSFTPBookmark(context.Context, model.SFTPBookmark) (model.SFTPBookmark, error)
	DeleteSFTPBookmark(context.Context, string) error
}

type scheduledTransferCatalog interface {
	ListScheduledTransfers(context.Context) ([]model.ScheduledTransfer, error)
	SaveScheduledTransfer(context.Context, model.ScheduledTransfer) (model.ScheduledTransfer, error)
	DeleteScheduledTransfer(context.Context, string) error
	RequestScheduledTransferNow(context.Context, string) error
}

type workspaceLayoutCatalog interface {
	ListWorkspaceLayouts(context.Context) ([]model.WorkspaceLayout, error)
	SaveWorkspaceLayout(context.Context, model.WorkspaceLayout) (model.WorkspaceLayout, error)
	DeleteWorkspaceLayout(context.Context, string) error
}

type shortcutBarCatalog interface {
	ListShortcutBars(context.Context) ([]model.ShortcutBar, error)
	SaveShortcutBar(context.Context, model.ShortcutBar) (model.ShortcutBar, error)
	DeleteShortcutBar(context.Context, string) error
	SetShortcutBarHosts(context.Context, string, []string) error
	MoveShortcutBarHost(context.Context, string, string, int) ([]string, error)
}

type batchHostCatalog interface {
	BatchUpdateHosts(context.Context, []string, model.HostBatchPatch) error
}

type pane uint8

const (
	groupPane pane = iota
	hostPane
)

type catalogLoadedMsg struct {
	groups                 []model.Group
	hosts                  []model.Host
	shortcutBars           []model.ShortcutBar
	authProfiles           []model.AuthProfile
	defaultHostID          string
	defaultHostErr         error
	lastWorkspaceData      string
	lastWorkspaceErr       error
	appearanceScheme       string
	appearanceErr          error
	footerShortcutsVisible bool
	footerShortcutsSet     bool
	footerShortcutsErr     error
	sftpEditorCommand      string
	sftpEditorErr          error
	shortcutBarsErr        error
	err                    error
}

type defaultSessionUpdatedMsg struct {
	hostID string
	err    error
}

type footerShortcutsPreferenceSavedMsg struct {
	enabled  bool
	previous bool
	err      error
}

type groupCreatedMsg struct {
	group model.Group
	err   error
}

type hostCreatedMsg struct {
	host model.Host
	err  error
}

type groupUpdatedMsg struct {
	group model.Group
	err   error
}

type hostUpdatedMsg struct {
	host model.Host
	err  error
}

type hostFavoriteUpdatedMsg struct {
	hostID   string
	favorite bool
	err      error
}

type terminalEncodingSavedMsg struct {
	tabID         string
	paneID        string
	hostID        string
	previous      string
	previousInput string
	encoding      string
	err           error
}

type hostFavoriteOrderUpdatedMsg struct {
	ids []string
	err error
}

type shortcutBarSavedMsg struct {
	bar     model.ShortcutBar
	editing bool
	err     error
}

type shortcutBarHostsUpdatedMsg struct {
	barID   string
	hostIDs []string
	err     error
}

type shortcutBarOrderUpdatedMsg struct {
	barID        string
	hostIDs      []string
	selectedHost string
	err          error
}

type batchHostsUpdatedMsg struct {
	ids            []string
	selectedHostID string
	patch          model.HostBatchPatch
	hosts          []model.Host
	updateErr      error
	reloadErr      error
}

type hostConnectionRecordedMsg struct {
	hostID string
	at     time.Time
	err    error
}

type catalogDeleteKind uint8

const (
	deleteGroup catalogDeleteKind = iota + 1
	deleteHost
	deleteShortcutBar
)

type catalogDeleteConfirm struct {
	kind          catalogDeleteKind
	id            string
	name          string
	credentialRef string
	deleting      bool
}

type appConfirmationKind uint8

const (
	appConfirmQuit appConfirmationKind = iota + 1
	appConfirmCloseTab
)

type appConfirmationState struct {
	kind  appConfirmationKind
	tabID string
}

type shortcutBarPickerState struct {
	hostID    string
	index     int
	busy      bool
	errorText string
}

type catalogDeletedMsg struct {
	kind       catalogDeleteKind
	id         string
	err        error
	cleanupErr error
}

type terminalConnectedMsg struct {
	host               model.Host
	authenticationHost model.Host
	session            terminalSession
	target             connectionTarget
	startupScript      *startupLoginScript
	err                error
}

type hostTrustedMsg struct{ err error }
type hostKeyChangedMsg struct{ err error }

type terminalOutputMsg struct {
	session terminalSession
	data    []byte
	err     error
}

type terminalBellExpiredMsg struct{ paneID string }

type terminalLinkOpenedMsg struct {
	url string
	err error
}

type asciiFileSendFinishedMsg struct {
	requestID uint64
	sent      int64
	err       error
}

type asciiFileSendProgressMsg struct {
	requestID uint64
	sent      int64
	closed    bool
}

type hostPickerState struct {
	target connectionTarget
	hosts  []model.Host
	index  int
	top    int
}

type serialPortPickerState struct {
	requestID uint64
	ports     []string
	index     int
	top       int
	loading   bool
	errorText string
}

type serialPortsLoadedMsg struct {
	requestID uint64
	ports     []string
	err       error
}

type workspaceLayoutsLoadedMsg struct {
	requestID uint64
	layouts   []model.WorkspaceLayout
	err       error
}

type workspaceLayoutSavedMsg struct {
	layout model.WorkspaceLayout
	err    error
}

type workspaceLayoutDeletedMsg struct {
	id  string
	err error
}

type workspaceLayoutManagerState struct {
	layouts          []model.WorkspaceLayout
	index            int
	top              int
	requestID        uint64
	loading          bool
	busy             bool
	editingName      bool
	name             string
	confirmingDelete bool
	errorText        string
}

type quickConnectState struct {
	address string
	error   string
}

type modelState struct {
	catalog                   Catalog
	credentials               credentialVault
	config                    model.Config
	appearancePicker          *appearancePickerState
	footerShortcutsUpdating   bool
	sftpExternalEditorCommand string
	sftpPreviewTempDirs       []string
	terminalHighlights        []terminalHighlightRule
	terminalTriggers          []compiledTerminalTrigger
	desktopNotifier           desktopnotify.Notifier
	groups                    []model.Group
	hosts                     []model.Host
	sshProbeStates            map[string]sshProbeStatus
	sshProbeGeneration        uint64
	sshProbeTimerGeneration   uint64
	shortcutBars              []model.ShortcutBar
	batchSelectedHostIDs      map[string]bool
	authProfiles              []model.AuthProfile
	favoriteUpdatingID        string
	favoriteOrderUpdating     bool
	defaultHostID             string
	defaultSessionUpdating    bool
	defaultSessionStarted     bool
	lastWorkspaceStarted      bool
	lastWorkspaceData         string
	lastWorkspaceErr          error

	width  int
	height int

	activePane pane
	groupIndex int
	hostIndex  int
	groupTop   int
	hostTop    int

	query                   string
	searching               bool
	showHelp                bool
	form                    *formState
	sshKeyGeneration        *sshKeyGenerationState
	sshKeyManager           *sshKeyManagerState
	sshHostKeyManager       *sshHostKeyManagerState
	authProfileManager      *authProfileManagerState
	quickConnect            *quickConnectState
	credentialPrompt        *credentialPromptState
	deleteConfirm           *catalogDeleteConfirm
	shortcutBarPicker       *shortcutBarPickerState
	shortcutBarSavingID     string
	batchEdit               *batchEditState
	hostPicker              *hostPickerState
	serialPortPicker        *serialPortPickerState
	serialPortLister        func() ([]string, error)
	nextSerialPortRequestID uint64
	workspaceLayouts        *workspaceLayoutManagerState
	nextWorkspaceLayoutID   uint64
	asciiFileSend           *asciiFileSendState
	nextASCIIFileSendID     uint64
	zmodemTransfer          *zmodemTransferState
	nextZMODEMRequestID     uint64
	layoutRestore           *workspaceLayoutRestoreState
	loading                 bool
	statusText              string
	statusErr               bool

	connectingHost      *model.Host
	authenticationHost  *model.Host
	connectionPasswords map[string]string
	passwordAttempts    map[string]int
	connectCancel       context.CancelFunc
	trustPrompt         *sshclient.UnknownHostKeyError
	hostKeyChangePrompt *sshclient.ChangedHostKeyError
	trustingHost        bool
	replacingHostKey    bool
	passwordPrompt      bool
	passwordValue       string
	passwordError       string
	ftpPasswordPrompt   bool
	ftpPasswordValue    string
	ftpPasswordError    string
	ftpPendingHost      *model.Host
	ftpPendingTarget    *connectionTarget
	lastFTPPassword     string
	keyboardAuthFlow    *keyboardAuthFlow
	keyboardAuthPrompt  *keyboardAuthPromptState

	connectionTarget            *connectionTarget
	tabs                        []*workspaceTab
	activeTabID                 string
	workspaceVisible            bool
	workspaceLeaderPending      bool
	paneResizeDrag              *paneResizeDragState
	paneMoveDrag                *paneMoveDragState
	appConfirmation             *appConfirmationState
	screenLock                  *screenLockState
	screenLockHash              string
	screenLockLoaded            bool
	lastActivity                time.Time
	nextScreenLockID            uint64
	workspaceHelp               bool
	workspaceHelpTop            int
	tabGroupManager             *tabGroupManagerState
	commandHistoryView          *terminalCommandHistoryViewState
	nextID                      int
	contextMenu                 *contextMenuState
	portForwardManager          *portForwardManagerState
	quickCommands               *quickCommandManagerState
	composePane                 *composePaneState
	clipboardPasteRequest       *terminalClipboardPasteRequest
	outputLoggers               map[*terminalog.Logger]struct{}
	terminalLogViewer           *terminalLogViewerState
	terminalLogsGeneration      uint64
	pluginRegistry              *plugin.Registry
	pluginPalette               *pluginPaletteState
	sftp                        *sftpBrowserState
	sftpFilters                 *sftpFilterManagerState
	sftpBookmarks               *sftpBookmarkManagerState
	scheduledTransfers          *scheduledTransferManagerState
	scheduledTransferLastFinish map[string]time.Time
	scheduledTransfersStartedAt time.Time
	scheduledTransferNotice     string
	scheduledTransferNoticeErr  bool
	scheduledTransferNoticeID   uint64
	sftpGeneration              uint64
	lastMouseHostID             string
	lastMouseClickAt            time.Time
	lastWorkspaceClick          workspaceMouseClick
}

// NewModel creates the home screen and loads its saved groups and hosts asynchronously.
func NewModel(catalog Catalog, registries ...*plugin.Registry) tea.Model {
	return NewModelWithConfig(catalog, model.DefaultConfig(), registries...)
}

func NewModelWithConfig(catalog Catalog, config model.Config, registries ...*plugin.Registry) tea.Model {
	keyboardValidationErr := config.Terminal.Keyboard.Validate()
	config = config.WithDefaults()
	if keyboardValidationErr != nil || config.Validate() != nil {
		config = model.DefaultConfig()
	}
	terminalHighlights := compileTerminalHighlights(config.Terminal.Highlights)
	terminalTriggers := compileTerminalTriggers(config.Terminal.Triggers)
	applyAppearance(config.Appearance)
	state := modelState{
		catalog:                     catalog,
		credentials:                 credentialstore.New(),
		config:                      config,
		terminalHighlights:          terminalHighlights,
		terminalTriggers:            terminalTriggers,
		desktopNotifier:             desktopnotify.SystemNotifier{},
		loading:                     true,
		width:                       120,
		height:                      32,
		lastActivity:                time.Now(),
		scheduledTransfersStartedAt: time.Now(),
	}
	if len(registries) > 0 {
		state.pluginRegistry = registries[0]
	}
	return state
}

func (m modelState) Init() tea.Cmd {
	commands := []tea.Cmd{m.loadCatalogCmd()}
	if m.config.Security.AutoLockMinutes > 0 {
		commands = append(commands, m.loadAutoScreenLock(), m.screenLockIdleTickCmd())
	}
	if _, ok := m.catalog.(scheduledTransferCatalog); ok {
		commands = append(commands, m.pollScheduledTransfersCmd())
	}
	return tea.Batch(commands...)
}

// Close releases any transient SSH resources when the Bubble Tea program exits.
func (m modelState) Close() error {
	if m.connectCancel != nil {
		m.connectCancel()
	}
	if m.pluginPalette != nil && m.pluginPalette.runCancel != nil {
		m.pluginPalette.runCancel()
	}
	if m.asciiFileSend != nil && m.asciiFileSend.cancel != nil {
		m.asciiFileSend.cancel()
	}
	if m.zmodemTransfer != nil {
		if m.zmodemTransfer.cancel != nil {
			m.zmodemTransfer.cancel()
		}
		m.zmodemTransfer.input.Close(context.Canceled)
	}
	var closeErr error
	hasTerminalTabs := false
	for _, tab := range m.tabs {
		if tab != nil && tab.sftp == nil && tab.root != nil {
			hasTerminalTabs = true
			break
		}
	}
	if store, ok := m.catalog.(lastWorkspaceCatalog); ok && hasTerminalTabs {
		data, err := encodeWorkspaceLayout(m)
		if err != nil {
			closeErr = fmt.Errorf("保存上次工作区失败：%w", err)
		} else if err := store.SetLastWorkspaceData(context.Background(), data); err != nil {
			closeErr = fmt.Errorf("保存上次工作区失败：%w", err)
		}
	}
	if m.terminalLogViewer != nil && m.terminalLogViewer.preview != nil {
		if err := m.terminalLogViewer.preview.Close(); closeErr == nil && err != nil {
			closeErr = err
		}
	}
	closedSFTP := make(map[*sftpBrowserState]struct{})
	for _, tab := range m.tabs {
		if tab != nil && tab.sftp != nil {
			closedSFTP[tab.sftp] = struct{}{}
			if err := closeSFTPResources(tab.sftp); closeErr == nil && err != nil {
				closeErr = err
			}
		}
	}
	if m.sftp != nil {
		if _, closed := closedSFTP[m.sftp]; !closed {
			if err := closeSFTPResources(m.sftp); closeErr == nil && err != nil {
				closeErr = err
			}
		}
	}
	cleanupSFTPPreviewTempDirs(m.sftpPreviewTempDirs)
	if err := m.stopAllTerminalOutputLogs(); closeErr == nil && err != nil {
		closeErr = err
	}
	for _, tab := range m.tabs {
		for _, pane := range collectPanes(tab.root) {
			closeTerminalResponseReader(pane)
			if pane.session != nil {
				if err := pane.session.Close(); closeErr == nil && err != nil {
					closeErr = err
				}
			}
		}
	}
	return closeErr
}

func (m modelState) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.bindSFTPStateForMessage(msg)
	if m.screenLock == nil {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.PasteMsg, tea.MouseMsg:
			m.lastActivity = time.Now()
		}
	}
	switch msg := msg.(type) {
	case screenLockLoadedMsg:
		m.handleScreenLockLoaded(msg)
		return m, nil
	case screenLockVerifiedMsg:
		m.handleScreenLockVerified(msg)
		return m, nil
	case screenLockSavedMsg:
		m.handleScreenLockSaved(msg)
		return m, nil
	case screenLockDeletedMsg:
		m.handleScreenLockDeleted(msg)
		return m, nil
	case screenLockIdleTickMsg:
		return m, m.handleScreenLockIdleTick()
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.paneResizeDrag = nil
		m.paneMoveDrag = nil
		if viewer := m.terminalLogViewer; viewer != nil && viewer.preview != nil {
			viewer.preview.Resize(max(20, msg.Width), max(5, msg.Height-3))
		}
		if m.workspaceVisible {
			return m, tea.Batch(m.resizeActiveWorkspace()...)
		}
		return m, nil
	case quickCommandsLoadedMsg:
		m.handleQuickCommandsLoaded(msg)
		return m, nil
	case quickCommandSavedMsg:
		m.handleQuickCommandSaved(msg)
		return m, nil
	case quickCommandDeletedMsg:
		m.handleQuickCommandDeleted(msg)
		return m, nil
	case sshKeyGeneratedMsg:
		m.handleSSHKeyGenerated(msg)
		return m, nil
	case sshKeysLoadedMsg:
		m.handleSSHKeysLoaded(msg)
		return m, nil
	case sshKeyDeletedMsg:
		return m, m.handleSSHKeyDeleted(msg)
	case sshIdentityReferencesRotatedMsg:
		return m, m.handleSSHIdentityReferencesRotated(msg)
	case sshHostKeysLoadedMsg:
		m.handleSSHHostKeysLoaded(msg)
		return m, nil
	case sshHostKeyRemovedMsg:
		return m, m.handleSSHHostKeyRemoved(msg)
	case sshHostKeysImportPlannedMsg:
		m.handleSSHHostKeysImportPlanned(msg)
		return m, nil
	case sshHostKeysImportAppliedMsg:
		return m, m.handleSSHHostKeysImportApplied(msg)
	case sshHostKeysExportedMsg:
		m.handleSSHHostKeysExported(msg)
		return m, nil
	case authProfilesLoadedMsg:
		m.handleAuthProfilesLoaded(msg)
		return m, nil
	case authProfileSavedMsg:
		m.handleAuthProfileSaved(msg)
		return m, nil
	case authProfileDeletedMsg:
		m.handleAuthProfileDeleted(msg)
		return m, nil
	case authProfileCredentialSavedMsg:
		m.handleAuthProfileCredentialSaved(msg)
		return m, nil
	case authProfileCredentialDeletedMsg:
		m.handleAuthProfileCredentialDeleted(msg)
		return m, nil
	case sftpFiltersLoadedMsg:
		m.handleSFTPFiltersLoaded(msg)
		return m, nil
	case sftpFilterSavedMsg:
		m.handleSFTPFilterSaved(msg)
		return m, nil
	case sftpFilterDeletedMsg:
		m.handleSFTPFilterDeleted(msg)
		return m, nil
	case sftpBookmarksLoadedMsg:
		m.handleSFTPBookmarksLoaded(msg)
		return m, nil
	case sftpBookmarkSavedMsg:
		m.handleSFTPBookmarkSaved(msg)
		return m, nil
	case sftpBookmarkDeletedMsg:
		m.handleSFTPBookmarkDeleted(msg)
		return m, nil
	case scheduledTransfersLoadedMsg:
		m.handleScheduledTransfersLoaded(msg)
		return m, nil
	case scheduledTransferSavedMsg:
		m.handleScheduledTransferSaved(msg)
		return m, nil
	case scheduledTransferDeletedMsg:
		m.handleScheduledTransferDeleted(msg)
		return m, nil
	case scheduledTransferRequestedMsg:
		m.handleScheduledTransferRequested(msg)
		return m, nil
	case scheduledTransfersPolledMsg:
		var noticeCmd tea.Cmd
		if msg.err == nil {
			noticeCmd = m.handleScheduledTransfersPolled(msg.transfers)
		}
		return m, tea.Batch(m.scheduledTransfersPollTickCmd(), noticeCmd)
	case scheduledTransfersPollTickMsg:
		return m, m.pollScheduledTransfersCmd()
	case portForwardStatsTickMsg:
		if m.portForwardManager == nil || m.portForwardManager.paneID != msg.paneID {
			return m, nil
		}
		return m, portForwardStatsTick(msg.paneID)
	case scheduledTransferRunnerFailedMsg:
		if msg.err != nil {
			m.statusText = "计划传输后台停止：" + msg.err.Error()
			m.statusErr = true
		}
		return m, nil
	case scheduledTransferNoticeExpiredMsg:
		if msg.id == m.scheduledTransferNoticeID {
			m.scheduledTransferNotice = ""
			m.scheduledTransferNoticeErr = false
		}
		return m, nil
	case terminalBellExpiredMsg:
		if pane := m.paneForID(msg.paneID); pane != nil && !pane.bellUntil.After(time.Now()) {
			pane.bellUntil = time.Time{}
		}
		return m, nil
	case terminalLinkOpenedMsg:
		if msg.err != nil {
			m.statusText = "打开链接失败：" + msg.err.Error()
			m.statusErr = true
		} else {
			m.statusText = "已请求系统浏览器打开链接"
			m.statusErr = false
		}
		return m, nil
	case sshProbeCompletedMsg:
		if msg.generation != m.sshProbeGeneration {
			return m, nil
		}
		m.sshProbeStates = msg.states
		return m, m.scheduleSSHProbeTick()
	case sshProbeTickMsg:
		if msg.generation != m.sshProbeTimerGeneration {
			return m, nil
		}
		return m, m.startSSHServerProbes()
	case pluginCommandCompletedMsg:
		m.handlePluginCommandCompleted(msg)
		return m, nil
	case pluginCommandTimeoutMsg:
		m.handlePluginCommandTimeout(msg)
		return m, nil
	case terminalOutputLogClosedMsg:
		m.handleTerminalOutputLogClosed(msg)
		return m, nil
	case terminalOutputLogPollMsg:
		return m, m.handleTerminalOutputLogPoll(msg)
	case terminalLogsLoadedMsg:
		m.handleTerminalLogsLoaded(msg)
		return m, nil
	case terminalLogDeletedMsg:
		m.handleTerminalLogDeleted(msg)
		return m, nil
	case terminalLogPreviewLoadedMsg:
		m.handleTerminalLogPreviewLoaded(msg)
		return m, nil
	case autoReconnectMsg:
		return m, m.handleAutoReconnect(msg)
	case catalogLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.statusText = "读取本地主机目录失败：" + msg.err.Error()
			m.statusErr = true
			return m, nil
		}
		m.groups, m.hosts, m.shortcutBars, m.authProfiles = msg.groups, msg.hosts, msg.shortcutBars, msg.authProfiles
		for hostID := range m.batchSelectedHostIDs {
			if m.hostByID(hostID) == nil {
				delete(m.batchSelectedHostIDs, hostID)
			}
		}
		if len(m.batchSelectedHostIDs) == 0 {
			m.batchSelectedHostIDs = nil
		}
		m.defaultHostID = msg.defaultHostID
		m.lastWorkspaceData = msg.lastWorkspaceData
		m.lastWorkspaceErr = msg.lastWorkspaceErr
		if msg.appearanceErr == nil && msg.appearanceScheme != "" {
			m.config.Appearance.Scheme = msg.appearanceScheme
			applyAppearance(m.config.Appearance)
		}
		if msg.footerShortcutsErr == nil && msg.footerShortcutsSet {
			m.config.Workspace.ShowFooterShortcuts = msg.footerShortcutsVisible
		} else if msg.footerShortcutsErr != nil {
			m.statusText = "读取底部快捷键设置失败：" + msg.footerShortcutsErr.Error()
			m.statusErr = true
		}
		if msg.sftpEditorErr == nil {
			m.sftpExternalEditorCommand = msg.sftpEditorCommand
		}
		m.ensureGroupSelection()
		m.ensureHostSelection()
		probeCmd := m.startSSHServerProbes()
		if m.config.Workspace.RestoreLastOnStartup && !m.lastWorkspaceStarted {
			m.lastWorkspaceStarted = true
			if m.lastWorkspaceErr == nil && m.lastWorkspaceData != "" {
				return m, tea.Batch(m.restoreLastWorkspace(), probeCmd)
			}
		}
		if msg.defaultHostErr != nil {
			m.statusText = "读取默认会话失败：" + msg.defaultHostErr.Error()
			m.statusErr = true
			return m, probeCmd
		}
		if m.defaultHostID != "" && !m.defaultSessionStarted {
			if host := m.hostByID(m.defaultHostID); host != nil {
				m.defaultSessionStarted = true
				return m, tea.Batch(m.startHostConnect(*host, m.newTabTarget()), probeCmd)
			}
		}
		if msg.lastWorkspaceErr != nil {
			m.statusText = "读取上次工作区失败：" + msg.lastWorkspaceErr.Error()
			m.statusErr = true
		} else if msg.shortcutBarsErr != nil {
			m.statusText = "读取命名连接栏失败：" + msg.shortcutBarsErr.Error()
			m.statusErr = true
		} else if m.lastWorkspaceData != "" {
			m.statusText = "本地目录已就绪 · 按 R 恢复上次工作区"
			m.statusErr = false
		} else {
			m.statusText = "本地目录已就绪"
			m.statusErr = false
		}
		return m, probeCmd
	case defaultSessionUpdatedMsg:
		m.defaultSessionUpdating = false
		if msg.err != nil {
			m.statusText = "保存默认会话失败：" + msg.err.Error()
			m.statusErr = true
			return m, nil
		}
		m.defaultHostID = msg.hostID
		if msg.hostID == "" {
			m.statusText = "已清除默认会话"
		} else if host := m.hostByID(msg.hostID); host != nil {
			m.statusText = "已将「" + host.Name + "」设为默认会话，下次启动时自动连接"
		} else {
			m.statusText = "默认会话已更新，下次启动时自动连接"
		}
		m.statusErr = false
		return m, nil
	case serialPortsLoadedMsg:
		picker := m.serialPortPicker
		if picker == nil || picker.requestID != msg.requestID {
			return m, nil
		}
		if m.form == nil || m.form.kind != hostForm || m.form.hostKind != model.HostKindSerial || m.form.field != 2 {
			m.serialPortPicker = nil
			return m, nil
		}
		picker.loading = false
		if msg.err != nil {
			picker.errorText = "检测串口设备失败：" + msg.err.Error()
			return m, nil
		}
		picker.ports = msg.ports
		picker.errorText = ""
		picker.index, picker.top = 0, 0
		if len(picker.ports) == 0 {
			picker.errorText = "没有检测到串口设备。可以按 Esc 返回并手动输入设备路径。"
			return m, nil
		}
		for index, port := range picker.ports {
			if port == strings.TrimSpace(m.form.address) {
				picker.index = index
				break
			}
		}
		return m, nil
	case workspaceLayoutsLoadedMsg:
		manager := m.workspaceLayouts
		if manager == nil || manager.requestID != msg.requestID {
			return m, nil
		}
		manager.loading = false
		if msg.err != nil {
			manager.errorText = "读取布局失败：" + msg.err.Error()
			return m, nil
		}
		manager.layouts = msg.layouts
		manager.index = clamp(manager.index, 0, max(0, len(manager.layouts)-1))
		manager.errorText = ""
		return m, nil
	case workspaceLayoutSavedMsg:
		manager := m.workspaceLayouts
		if manager == nil {
			return m, nil
		}
		manager.busy = false
		if msg.err != nil {
			manager.errorText = "保存布局失败：" + msg.err.Error()
			return m, nil
		}
		manager.editingName = false
		manager.name = ""
		manager.errorText = "布局已保存"
		return m.refreshWorkspaceLayouts()
	case workspaceLayoutDeletedMsg:
		manager := m.workspaceLayouts
		if manager == nil {
			return m, nil
		}
		manager.busy = false
		manager.confirmingDelete = false
		if msg.err != nil {
			manager.errorText = "删除布局失败：" + msg.err.Error()
			return m, nil
		}
		manager.errorText = "布局已删除"
		return m.refreshWorkspaceLayouts()
	case groupCreatedMsg:
		if m.form == nil || m.form.kind != groupForm {
			return m, nil
		}
		if msg.err != nil {
			m.form.saving = false
			m.form.errorText = "保存失败：" + msg.err.Error()
			return m, nil
		}
		m.groups = append(m.groups, msg.group)
		m.form = nil
		m.query = ""
		m.activePane = groupPane
		m.groupIndex = m.groupIndexForID(msg.group.Id)
		m.ensureGroupSelection()
		m.statusText = "分组已保存"
		m.statusErr = false
		return m, nil
	case hostCreatedMsg:
		if m.form == nil || m.form.kind != hostForm {
			return m, nil
		}
		if msg.err != nil {
			m.form.saving = false
			m.form.errorText = "保存失败：" + msg.err.Error()
			return m, nil
		}
		m.hosts = append(m.hosts, msg.host)
		m.form = nil
		m.query = ""
		m.activePane = hostPane
		if msg.host.GroupId != "" {
			m.groupIndex = m.groupIndexForID(msg.host.GroupId)
		} else {
			m.groupIndex = 0
		}
		m.hostIndex = 0
		m.ensureHostSelection()
		for index, host := range m.visibleHosts() {
			if host.Id == msg.host.Id {
				m.hostIndex = index
				break
			}
		}
		m.hostTop = keepVisible(m.hostIndex, m.hostTop, m.hostRows())
		m.statusText = "主机已保存"
		m.statusErr = false
		return m, m.startSSHServerProbes()
	case groupUpdatedMsg:
		if m.form == nil || m.form.kind != groupForm || !m.form.editing {
			return m, nil
		}
		if msg.err != nil {
			m.form.saving = false
			m.form.errorText = "保存失败：" + msg.err.Error()
			return m, nil
		}
		for index := range m.groups {
			if m.groups[index].Id == msg.group.Id {
				m.groups[index] = msg.group
				break
			}
		}
		m.form = nil
		m.activePane = groupPane
		m.groupIndex = m.groupIndexForID(msg.group.Id)
		m.ensureGroupSelection()
		m.statusText = "分组已更新"
		m.statusErr = false
		return m, nil
	case shortcutBarSavedMsg:
		if m.form == nil || m.form.kind != shortcutBarForm {
			return m, nil
		}
		if msg.err != nil {
			m.form.saving = false
			m.form.errorText = "保存失败：" + msg.err.Error()
			return m, nil
		}
		if msg.editing {
			for index := range m.shortcutBars {
				if m.shortcutBars[index].Id == msg.bar.Id {
					msg.bar.HostIDs = append([]string(nil), m.shortcutBars[index].HostIDs...)
					msg.bar.SortOrder = m.shortcutBars[index].SortOrder
					m.shortcutBars[index] = msg.bar
					break
				}
			}
		} else {
			msg.bar.HostIDs = []string{}
			m.shortcutBars = append(m.shortcutBars, msg.bar)
		}
		m.form = nil
		m.query = ""
		m.activePane = groupPane
		m.groupIndex = m.groupIndexForID(msg.bar.Id)
		m.ensureGroupSelection()
		m.ensureHostSelection()
		m.statusText, m.statusErr = "连接栏已保存", false
		return m, nil
	case shortcutBarHostsUpdatedMsg:
		if m.shortcutBarSavingID == msg.barID {
			m.shortcutBarSavingID = ""
		}
		if msg.err != nil {
			if m.shortcutBarPicker != nil {
				m.shortcutBarPicker.busy = false
				m.shortcutBarPicker.errorText = "保存失败：" + msg.err.Error()
			} else {
				m.statusText, m.statusErr = "更新连接栏失败："+msg.err.Error(), true
			}
			return m, nil
		}
		if bar := m.shortcutBarByID(msg.barID); bar != nil {
			bar.HostIDs = append([]string(nil), msg.hostIDs...)
		}
		m.shortcutBarPicker = nil
		m.ensureGroupSelection()
		m.ensureHostSelection()
		m.statusText, m.statusErr = "连接栏主机已更新", false
		return m, nil
	case shortcutBarOrderUpdatedMsg:
		if m.shortcutBarSavingID == msg.barID {
			m.shortcutBarSavingID = ""
		}
		if msg.err != nil {
			m.statusText, m.statusErr = "调整连接栏顺序失败："+msg.err.Error(), true
			return m, nil
		}
		if bar := m.shortcutBarByID(msg.barID); bar != nil {
			bar.HostIDs = append([]string(nil), msg.hostIDs...)
		}
		m.ensureHostSelection()
		if msg.selectedHost != "" {
			for index, host := range m.visibleHosts() {
				if host.Id == msg.selectedHost {
					m.hostIndex = index
					break
				}
			}
			m.hostTop = keepVisible(m.hostIndex, m.hostTop, m.hostRows())
		}
		m.statusText, m.statusErr = "连接栏顺序已调整", false
		return m, nil
	case batchHostsUpdatedMsg:
		if m.batchEdit == nil {
			return m, nil
		}
		if msg.updateErr != nil {
			m.batchEdit.busy = false
			m.batchEdit.errorText = "批量保存失败：" + msg.updateErr.Error()
			return m, nil
		}
		m.batchEdit = nil
		m.batchSelectedHostIDs = nil
		if msg.reloadErr == nil {
			m.hosts = msg.hosts
		} else {
			for index := range m.hosts {
				if !containsString(msg.ids, m.hosts[index].Id) {
					continue
				}
				applyBatchPatchToHost(&m.hosts[index], msg.patch)
			}
		}
		if msg.patch.GroupID != nil {
			m.groupIndex = m.groupIndexForID(*msg.patch.GroupID)
		}
		m.activePane = hostPane
		m.ensureGroupSelection()
		m.ensureHostSelection()
		if msg.selectedHostID != "" {
			for index, host := range m.visibleHosts() {
				if host.Id == msg.selectedHostID {
					m.hostIndex = index
					break
				}
			}
			m.hostTop = keepVisible(m.hostIndex, m.hostTop, m.hostRows())
		}
		if msg.reloadErr != nil {
			m.statusText = "已保存批量修改，但刷新主机列表失败：" + msg.reloadErr.Error()
			m.statusErr = true
		} else {
			m.statusText = fmt.Sprintf("已批量更新 %d 台主机", len(msg.ids))
			m.statusErr = false
		}
		return m, m.startSSHServerProbes()
	case hostUpdatedMsg:
		if m.form == nil || m.form.kind != hostForm || !m.form.editing {
			return m, nil
		}
		if msg.err != nil {
			m.form.saving = false
			m.form.errorText = "保存失败：" + msg.err.Error()
			return m, nil
		}
		for index := range m.hosts {
			if m.hosts[index].Id == msg.host.Id {
				m.hosts[index] = msg.host
				break
			}
		}
		m.form = nil
		m.query = ""
		m.activePane = hostPane
		if msg.host.GroupId != "" {
			m.groupIndex = m.groupIndexForID(msg.host.GroupId)
		} else {
			m.groupIndex = 0
		}
		m.hostIndex = 0
		m.ensureHostSelection()
		for index, host := range m.visibleHosts() {
			if host.Id == msg.host.Id {
				m.hostIndex = index
				break
			}
		}
		m.hostTop = keepVisible(m.hostIndex, m.hostTop, m.hostRows())
		m.statusText = "主机已更新；已打开的连接保持当前配置"
		m.statusErr = false
		return m, m.startSSHServerProbes()
	case hostFavoriteUpdatedMsg:
		if m.favoriteUpdatingID == msg.hostID {
			m.favoriteUpdatingID = ""
		}
		if msg.err != nil {
			m.statusText = "更新收藏失败：" + msg.err.Error()
			m.statusErr = true
			return m, nil
		}
		if host := m.hostByID(msg.hostID); host != nil {
			host.Favorite = msg.favorite
			if msg.favorite {
				host.FavoriteOrder = m.nextFavoriteOrder()
			} else {
				host.FavoriteOrder = 0
			}
		}
		m.ensureHostSelection()
		if msg.favorite {
			m.statusText = "已加入收藏"
		} else {
			m.statusText = "已取消收藏"
		}
		m.statusErr = false
		return m, nil
	case terminalEncodingSavedMsg:
		m.handleTerminalEncodingSaved(msg)
		return m, nil
	case hostFavoriteOrderUpdatedMsg:
		m.favoriteOrderUpdating = false
		if msg.err != nil {
			m.statusText = "调整收藏快捷顺序失败：" + msg.err.Error()
			m.statusErr = true
			return m, nil
		}
		selectedID := ""
		if selected := m.selectedHost(); selected != nil {
			selectedID = selected.Id
		}
		for index, id := range msg.ids {
			if host := m.hostByID(id); host != nil {
				host.FavoriteOrder = index + 1
			}
		}
		m.ensureHostSelection()
		if selectedID != "" {
			for index, host := range m.visibleHosts() {
				if host.Id == selectedID {
					m.hostIndex = index
					m.hostTop = keepVisible(index, m.hostTop, m.hostRows())
					break
				}
			}
		}
		m.statusText = "收藏快捷顺序已调整"
		m.statusErr = false
		return m, nil
	case hostConnectionRecordedMsg:
		if msg.err != nil {
			m.statusText = "连接已建立，但保存最近连接失败：" + msg.err.Error()
			m.statusErr = true
			return m, nil
		}
		if host := m.hostByID(msg.hostID); host != nil && msg.at.After(host.LastConnectedAt) {
			host.LastConnectedAt = msg.at
			if m.selectedGroup().isRecentView {
				m.ensureHostSelection()
			}
		}
		return m, nil
	case credentialSavedMsg:
		m.handleCredentialSaved(msg)
		return m, nil
	case credentialDeletedMsg:
		m.handleCredentialDeleted(msg)
		return m, nil
	case savedTerminalPasswordLoadedMsg:
		m.handleSavedTerminalPasswordLoaded(msg)
		return m, nil
	case sshCredentialsLoadedMsg:
		if m.connectingHost == nil || m.connectingHost.Id != msg.host.Id || m.connectionTarget == nil || *m.connectionTarget != msg.target {
			for key := range msg.passwords {
				msg.passwords[key] = ""
				delete(msg.passwords, key)
			}
			return m, nil
		}
		m.connectionPasswords = msg.passwords
		if msg.warning != "" {
			m.statusText, m.statusErr = "读取系统钥匙串失败，将尝试交互认证："+msg.warning, true
		}
		return m, m.startSSHConnect(msg.host, msg.target)
	case ftpCredentialLoadedMsg:
		if m.connectingHost == nil || m.connectingHost.Id != msg.host.Id || m.connectionTarget == nil || *m.connectionTarget != msg.target {
			msg.password = ""
			return m, nil
		}
		if msg.err == nil && msg.password != "" {
			return m, m.startFTPConnect(msg.host, msg.target, msg.password)
		}
		m.connectingHost = nil
		m.connectionTarget = nil
		m.promptFTPPassword(msg.host, msg.target)
		if msg.err != nil && !errors.Is(msg.err, credentialstore.ErrNotFound) {
			m.ftpPasswordError = "读取系统钥匙串失败，请重新输入密码：" + msg.err.Error()
		}
		msg.password = ""
		return m, nil
	case catalogDeletedMsg:
		m.deleteConfirm = nil
		var probeCmd tea.Cmd
		if msg.err != nil {
			m.statusText = "删除失败：" + msg.err.Error()
			m.statusErr = true
			return m, nil
		}
		if msg.kind == deleteGroup {
			for index, group := range m.groups {
				if group.Id == msg.id {
					m.groups = append(m.groups[:index], m.groups[index+1:]...)
					break
				}
			}
			m.groupIndex = clamp(m.groupIndex, 0, len(m.groupEntries())-1)
			m.ensureGroupSelection()
			m.statusText = "分组已删除"
		} else if msg.kind == deleteShortcutBar {
			for index, bar := range m.shortcutBars {
				if bar.Id == msg.id {
					m.shortcutBars = append(m.shortcutBars[:index], m.shortcutBars[index+1:]...)
					break
				}
			}
			m.ensureGroupSelection()
			m.ensureHostSelection()
			m.statusText = "连接栏已删除；主机配置未受影响"
		} else {
			for index, host := range m.hosts {
				if host.Id == msg.id {
					m.hosts = append(m.hosts[:index], m.hosts[index+1:]...)
					break
				}
			}
			delete(m.sshProbeStates, msg.id)
			delete(m.batchSelectedHostIDs, msg.id)
			if len(m.batchSelectedHostIDs) == 0 {
				m.batchSelectedHostIDs = nil
			}
			if m.defaultHostID == msg.id {
				m.defaultHostID = ""
			}
			for index := range m.shortcutBars {
				members := m.shortcutBars[index].HostIDs[:0]
				for _, hostID := range m.shortcutBars[index].HostIDs {
					if hostID != msg.id {
						members = append(members, hostID)
					}
				}
				m.shortcutBars[index].HostIDs = members
			}
			m.ensureHostSelection()
			m.statusText = "主机已删除"
			probeCmd = m.startSSHServerProbes()
			if msg.cleanupErr != nil {
				m.statusText += "；系统钥匙串凭据清理失败：" + msg.cleanupErr.Error()
				m.statusErr = true
				return m, probeCmd
			}
		}
		m.statusErr = false
		return m, probeCmd
	case terminalConnectedMsg:
		if m.connectingHost == nil || m.connectingHost.Id != msg.host.Id ||
			m.connectionTarget == nil || *m.connectionTarget != msg.target {
			if msg.session != nil {
				_ = msg.session.Close()
			}
			return m, nil
		}
		m.clearConnectCancel()
		m.keyboardAuthFlow = nil
		m.clearKeyboardAuthPrompt()
		if msg.err != nil {
			var changedHostKey *sshclient.ChangedHostKeyError
			if errors.As(msg.err, &changedHostKey) {
				m.hostKeyChangePrompt = changedHostKey
				return m, nil
			}
			var unknownHost *sshclient.UnknownHostKeyError
			if errors.As(msg.err, &unknownHost) {
				m.trustPrompt = unknownHost
				return m, nil
			}
			if sshclient.IsAuthenticationError(msg.err) {
				authenticationHost := msg.authenticationHost
				if authenticationHost.Id == "" && authenticationHost.IP == "" {
					authenticationHost = *m.connectingHost
				}
				m.authenticationHost = &authenticationHost
				m.passwordPrompt = true
				m.passwordError = ""
				if m.passwordAttempts[sshclient.HostCredentialKey(authenticationHost)] > 0 {
					m.passwordError = "认证失败，请检查用户名和密码后重试。"
				}
				return m, nil
			}
			m.clearConnectionCredentials()
			if msg.host.Kind == model.HostKindLocal {
				m.statusText = "本地终端启动失败：" + msg.err.Error()
			} else if msg.host.Kind == model.HostKindSerial {
				m.statusText = "串口连接失败：" + msg.err.Error()
			} else if msg.host.Kind == model.HostKindTelnet {
				m.statusText = terminalProtocolName(msg.host) + " 连接失败：" + msg.err.Error()
			} else {
				m.statusText = "SSH 连接失败：" + msg.err.Error()
			}
			m.statusErr = true
			var reconnectCmd tea.Cmd
			if msg.target.kind == splitPaneTarget {
				if tab := m.tabByID(msg.target.tabID); tab != nil {
					if pane := findPane(tab.root, msg.target.parentPaneID); pane != nil {
						pane.statusText = m.statusText
						pane.statusErr = true
					}
				}
			} else if msg.target.kind == reconnectPaneTarget {
				if tab := m.tabByID(msg.target.tabID); tab != nil {
					if pane := findPane(tab.root, msg.target.paneID); pane != nil {
						pane.statusText = m.statusText
						pane.statusErr = true
						var scriptConfigErr *startupScriptConfigurationError
						if pane.reconnectGeneration == msg.target.reconnectGeneration && !errors.As(msg.err, &scriptConfigErr) {
							reconnectCmd = m.schedulePaneAutoReconnect(tab, pane)
						}
					}
				}
			}
			if msg.target.kind == sftpPeerTarget && m.sftp != nil && m.sftp.generation == msg.target.sftpGeneration {
				m.sftp.busy = false
				m.sftp.status = "连接" + sftpSideLabel(msg.target.sftpSide) + "远程主机失败：" + msg.err.Error()
				m.sftp.statusErr = true
			}
			m.connectingHost = nil
			m.connectionTarget = nil
			layoutRestoreCmd := m.finishWorkspaceLayoutConnection(msg.target, true)
			return m, tea.Batch(reconnectCmd, layoutRestoreCmd)
		}
		m.connectingHost = nil
		m.connectionTarget = nil
		m.passwordValue = ""
		m.passwordPrompt = false
		m.clearConnectionCredentials()
		m.statusText = "已连接 " + msg.host.Name
		m.statusErr = false
		if msg.target.kind == standaloneSFTPTarget {
			sshSession, ok := msg.session.(*sshclient.Session)
			if !ok {
				_ = msg.session.Close()
				m.statusText = "当前连接不支持 SFTP"
				m.statusErr = true
				return m, nil
			}
			return m, m.openStandaloneSFTP(msg.host, sshSession)
		}
		if msg.target.kind == sftpPeerTarget {
			sshSession, ok := msg.session.(*sshclient.Session)
			if !ok {
				_ = msg.session.Close()
				if m.sftp != nil && m.sftp.generation == msg.target.sftpGeneration {
					m.sftp.busy = false
				}
				m.statusText = "当前连接不支持 SFTP"
				m.statusErr = true
				return m, nil
			}
			return m, m.openSFTPRemoteSource(msg.host, sshSession, msg.target.sftpGeneration, msg.target.sftpSide, msg.target.sftpPaneTabID)
		}
		trackedSession := trackTerminalSession(msg.session)
		if !m.handleConnectedPane(msg.target, msg.host, trackedSession, msg.startupScript) {
			_ = msg.session.Close()
			m.statusText = "无法将终端会话加入 pane，请重试"
			m.statusErr = true
			return m, m.finishWorkspaceLayoutConnection(msg.target, true)
		}
		commands := append(m.resizeActiveWorkspace(), readTerminalOutputCmd(trackedSession))
		if pane := m.paneForSession(trackedSession); pane != nil && pane.startupScript != nil {
			pane.statusText = startupScriptWaitingStatus(pane.startupScript)
			commands = append(commands, startupScriptTimeoutCmd(pane.id, pane.startupScript))
		}
		if layoutRestoreCmd := m.finishWorkspaceLayoutConnection(msg.target, false); layoutRestoreCmd != nil {
			commands = append(commands, layoutRestoreCmd)
		}
		if recentConnectionCmd := m.recordRecentHostConnection(msg.host); recentConnectionCmd != nil {
			commands = append(commands, recentConnectionCmd)
		}
		if sshSession, ok := msg.session.(*sshclient.Session); ok && msg.host.Kind == model.HostKindSSH && msg.host.Id != "" {
			if pane := m.paneForSession(trackedSession); pane != nil {
				if restore := m.restorePortForwardRules(pane.id, msg.host.Id, sshSession); restore != nil {
					commands = append(commands, restore)
				}
			}
		}
		return m, tea.Batch(commands...)
	case sftpPeerOpenedMsg:
		return m, m.handleSFTPRemotePeerOpened(msg)
	case sftpLocalPaneTabListedMsg:
		m.handleSFTPLocalPaneTabListed(msg)
		return m, nil
	case ftpConnectedMsg:
		return m, m.handleFTPConnected(msg)
	case ftpRawCommandFinishedMsg:
		m.handleFTPCommandFinished(msg)
		return m, nil
	case sftpReopenedMsg:
		m.handleSFTPReopened(msg)
		return m, nil
	case terminalStartupSentMsg:
		if msg.err != nil {
			if pane := m.paneByID(msg.paneID); pane != nil {
				pane.statusText = "连接后命令发送失败：" + msg.err.Error()
				pane.statusErr = true
			}
		}
		return m, nil
	case terminalTriggerSentMsg:
		pane := m.paneByID(msg.paneID)
		if pane == nil || pane.triggerRuntime == nil || pane.triggerRuntime.generation != msg.generation {
			return m, nil
		}
		if msg.err != nil {
			pane.statusText = fmt.Sprintf("触发器「%s」发送失败：%s", msg.ruleName, msg.err)
			pane.statusErr = true
		} else {
			pane.statusText = fmt.Sprintf("触发器「%s」已发送", msg.ruleName)
			pane.statusErr = false
		}
		return m, nil
	case terminalTriggerScriptLoadedMsg:
		pane := m.paneByID(msg.paneID)
		if pane == nil || pane.triggerRuntime == nil || pane.triggerRuntime.generation != msg.generation {
			return m, nil
		}
		pane.triggerScriptLoading = false
		if msg.err != nil {
			pane.triggerScriptOutput = nil
			pane.statusText = fmt.Sprintf("触发器「%s」脚本加载失败：%s", msg.ruleName, msg.err)
			pane.statusErr = true
			return m, nil
		}
		if pane.session == nil || pane.startupScript != nil {
			pane.triggerScriptOutput = nil
			pane.statusText = fmt.Sprintf("触发器「%s」脚本未启动：当前 pane 已断开或正有脚本运行", msg.ruleName)
			pane.statusErr = true
			return m, nil
		}
		pendingOutput := pane.triggerScriptOutput
		pane.triggerScriptOutput = nil
		pane.startupScript = newStartupScriptRuntime(msg.script, msg.generation)
		pane.statusText = startupScriptWaitingStatus(pane.startupScript)
		pane.statusErr = false
		matchCmd := m.matchStartupScriptOutput(pane, pendingOutput)
		return m, tea.Batch(startupScriptTimeoutCmd(pane.id, pane.startupScript), matchCmd)
	case startupScriptStepSentMsg:
		pane := m.paneByID(msg.paneID)
		if pane == nil || pane.startupScript == nil || pane.startupScript.generation != msg.generation || pane.startupScript.stepIndex != msg.stepIndex {
			return m, nil
		}
		if msg.err != nil {
			pane.startupScript = nil
			pane.statusText = "提示符登录脚本已停止：" + msg.err.Error()
			pane.statusErr = true
			return m, nil
		}
		pane.startupScript.stepIndex++
		pane.startupScript.matchText = pane.startupScript.deferredText
		pane.startupScript.deferredText = ""
		pane.startupScript.sendPending = false
		if pane.startupScript.stepIndex >= len(pane.startupScript.script.steps) {
			pane.startupScript = nil
			pane.statusText = "提示符登录脚本已完成"
			pane.statusErr = false
			return m, nil
		}
		if pane.startupScript.script.steps[pane.startupScript.stepIndex].matcher.MatchString(pane.startupScript.matchText) {
			pane.startupScript.matchText = ""
			pane.startupScript.sendPending = true
			return m, sendStartupScriptStepCmd(pane.session, pane.id, pane.startupScript, pane.host.User, pane.host.CredentialRef, pane.terminalInputEncoding, m.credentials)
		}
		pane.statusText = startupScriptWaitingStatus(pane.startupScript)
		pane.statusErr = false
		return m, startupScriptTimeoutCmd(pane.id, pane.startupScript)
	case startupScriptTimeoutMsg:
		pane := m.paneByID(msg.paneID)
		if pane == nil || pane.startupScript == nil || pane.startupScript.generation != msg.generation || pane.startupScript.stepIndex != msg.stepIndex {
			return m, nil
		}
		pane.startupScript = nil
		pane.statusText = fmt.Sprintf("提示符登录脚本等待第 %d 步超时，已停止", msg.stepIndex+1)
		pane.statusErr = true
		return m, nil
	case keyboardAuthPromptMsg:
		if m.keyboardAuthFlow != msg.flow || m.connectingHost == nil {
			select {
			case msg.request.response <- keyboardAuthResponse{err: context.Canceled}:
			default:
			}
			return m, nil
		}
		m.keyboardAuthPrompt = newKeyboardAuthPromptState(msg.request)
		return m, nil
	case hostTrustedMsg:
		m.trustingHost = false
		if msg.err != nil {
			m.statusText = "保存主机指纹失败：" + msg.err.Error()
			m.statusErr = true
			var target connectionTarget
			if m.connectionTarget != nil {
				target = *m.connectionTarget
			}
			m.connectingHost = nil
			m.connectionTarget = nil
			m.trustPrompt = nil
			m.clearConnectionCredentials()
			if target.kind == sftpPeerTarget && m.sftp != nil && m.sftp.generation == target.sftpGeneration {
				m.sftp.busy = false
				m.sftp.status = "保存 SSH 主机指纹失败：" + msg.err.Error()
				m.sftp.statusErr = true
			}
			return m, m.finishWorkspaceLayoutConnection(target, true)
		}
		m.trustPrompt = nil
		if m.connectingHost == nil || m.connectionTarget == nil {
			return m, nil
		}
		return m, m.startSSHConnect(*m.connectingHost, *m.connectionTarget)
	case hostKeyChangedMsg:
		m.trustingHost = false
		m.replacingHostKey = false
		if msg.err != nil {
			m.statusText = "更新 SSH 主机指纹失败：" + msg.err.Error()
			m.statusErr = true
			var target connectionTarget
			if m.connectionTarget != nil {
				target = *m.connectionTarget
			}
			m.connectingHost = nil
			m.connectionTarget = nil
			m.hostKeyChangePrompt = nil
			m.clearConnectionCredentials()
			if target.kind == sftpPeerTarget && m.sftp != nil && m.sftp.generation == target.sftpGeneration {
				m.sftp.busy = false
				m.sftp.status = "更新 SSH 主机指纹失败：" + msg.err.Error()
				m.sftp.statusErr = true
			}
			return m, m.finishWorkspaceLayoutConnection(target, true)
		}
		m.hostKeyChangePrompt = nil
		if m.connectingHost == nil || m.connectionTarget == nil {
			return m, nil
		}
		return m, m.startSSHConnect(*m.connectingHost, *m.connectionTarget)
	case terminalOutputMsg:
		pane := m.paneForSession(msg.session)
		if pane == nil {
			return m, nil
		}
		terminalData := msg.data
		transferCaptured := false
		if transfer := m.zmodemTransfer; transfer != nil && transfer.paneID == pane.id {
			appendErr := transfer.input.Append(msg.data)
			switch appendErr {
			case nil:
				terminalData = nil
				transferCaptured = true
			case errZMODEMInputClosed:
				// The worker has stopped reading. Keep bytes arriving after its
				// protocol finish in the terminal stream.
			case errZMODEMInputLimit:
				transfer.errorText = "协议输入缓存超限，已取消传输"
				m.cancelZMODEMTransfer()
				terminalData = nil
				transferCaptured = true
			default:
				transfer.errorText = "无法缓存 ZMODEM 数据：" + appendErr.Error()
				m.cancelZMODEMTransfer()
				terminalData = nil
				transferCaptured = true
			}
		}
		if !transferCaptured && (m.zmodemTransfer == nil || m.zmodemTransfer.paneID != pane.id) && len(terminalData) > 0 {
			display, protocol, pending, mode, found := detectZMODEMStart(pane.zmodemProbe, terminalData)
			pane.zmodemProbe = pending
			terminalData = display
			if found && !m.openZMODEMPrompt(pane, mode, protocol) {
				terminalData = append(terminalData, protocol...)
			}
		}
		if msg.err != nil && len(pane.zmodemProbe) > 0 {
			terminalData = append(terminalData, pane.zmodemProbe...)
			pane.zmodemProbe = nil
		}
		displayTerminalData, terminalDecodeErr := decodeTerminalOutput(pane, terminalData, msg.err != nil)
		var bellCmd tea.Cmd
		if !transferCaptured && m.config.Terminal.WithDefaults().Bell == model.TerminalBellVisual && bytes.IndexByte(displayTerminalData, '\a') >= 0 {
			pane.bellUntil = time.Now().Add(800 * time.Millisecond)
			paneID := pane.id
			bellCmd = tea.Tick(800*time.Millisecond, func(time.Time) tea.Msg {
				return terminalBellExpiredMsg{paneID: paneID}
			})
		}
		var startupCmd tea.Cmd
		if msg.err == nil && !transferCaptured && len(terminalData) > 0 && pane.startupPending != "" {
			command := pane.startupPending
			pane.startupPending = ""
			startupCmd = sendStartupCommandCmd(pane.session, pane.id, command, pane.terminalInputEncoding)
		}
		var startupScriptCmd tea.Cmd
		if msg.err == nil && !transferCaptured && len(terminalData) > 0 {
			startupScriptCmd = m.matchStartupScriptOutput(pane, displayTerminalData)
		}
		var triggerCmd tea.Cmd
		if msg.err == nil && !transferCaptured && len(terminalData) > 0 {
			if pane.triggerScriptLoading {
				pane.triggerScriptOutput = appendTerminalTriggerPendingOutput(pane.triggerScriptOutput, displayTerminalData)
			}
			triggerMatches := pane.triggerRuntime.match(displayTerminalData, time.Now())
			triggerCmd = m.handleTerminalTriggerMatches(pane, triggerMatches, displayTerminalData)
		}
		if m.asciiFileSend != nil && m.asciiFileSend.paneID == pane.id && m.asciiFileSend.promptFeed != nil {
			m.asciiFileSend.promptFeed.Append(displayTerminalData)
		}
		logCloseCmd := m.renderTerminalDecodedBytes(pane, terminalData, displayTerminalData, terminalDecodeErr)
		if msg.err != nil {
			var recordingPath string
			var recordingErr error
			if pane.scriptRecording != nil {
				recordingPath, _, recordingErr = stopTerminalScriptRecording(pane)
			}
			if transfer := m.zmodemTransfer; transfer != nil && transfer.paneID == pane.id {
				transfer.input.Close(msg.err)
				if transfer.cancel != nil {
					transfer.cancel()
				}
				m.zmodemTransfer = nil
			}
			if m.asciiFileSend != nil && m.asciiFileSend.paneID == pane.id && m.asciiFileSend.cancel != nil {
				m.asciiFileSend.cancel()
			}
			closeTerminalResponseReader(pane)
			_ = msg.session.Close()
			if pane.outputLogger != nil {
				logCloseCmd = m.stopTerminalOutputLog(pane)
			}
			if m.sftp != nil && m.sftp.paneID == pane.id {
				_ = closeSFTPResources(m.sftp)
				m.sftp.client = nil
				m.sftp.peerClient = nil
				m.sftp.peerSession = nil
				m.sftp.busy = false
				m.sftp.status = "SSH 会话已断开，SFTP 不可继续使用"
				m.sftp.statusErr = true
			}
			pane.session = nil
			pane.startupScript = nil
			pane.triggerRuntime = nil
			pane.triggerScriptLoading = false
			pane.triggerScriptOutput = nil
			pane.zmodemProbe = nil
			clearTerminalCommandCapture(pane)
			pane.forwards = nil
			pane.mouse = terminalMouseTracking{}
			if m.portForwardManager != nil && m.portForwardManager.paneID == pane.id {
				m.portForwardManager = nil
			}
			if msg.err == io.EOF && pane.host.Kind == model.HostKindSerial {
				pane.statusText = "串口会话已结束；按 Ctrl+] 关闭 pane"
			} else if msg.err == io.EOF && pane.host.Kind == model.HostKindLocal {
				pane.statusText = "本地 Shell 已结束；按 Ctrl+] 关闭 pane"
			} else if msg.err == io.EOF && pane.host.Kind == model.HostKindTelnet {
				pane.statusText = terminalProtocolName(pane.host) + " 服务器已关闭连接"
			} else if msg.err == io.EOF {
				pane.statusText = "远程 Shell 已结束；按 Ctrl+] 关闭 pane"
			} else if pane.host.Kind == model.HostKindSerial {
				pane.statusText = "串口连接已断开：" + msg.err.Error()
			} else if pane.host.Kind == model.HostKindLocal {
				pane.statusText = "本地终端已结束：" + msg.err.Error()
			} else if pane.host.Kind == model.HostKindTelnet {
				pane.statusText = terminalProtocolName(pane.host) + " 连接已断开：" + msg.err.Error()
			} else {
				pane.statusText = "SSH 会话已断开：" + msg.err.Error()
			}
			pane.statusErr = msg.err != io.EOF || recordingErr != nil
			if recordingErr != nil {
				if pane.scriptRecording != nil {
					pane.scriptRecording.paused = true
				}
				pane.statusText += "；脚本录制保存失败：" + recordingErr.Error() + "（可从 pane 菜单重试）"
			} else if recordingPath != "" {
				pane.statusText += "；脚本已保存：" + recordingPath
			}
			var reconnectCmd tea.Cmd
			if errors.Is(msg.err, sshclient.ErrTransportLost) && pane.host.Kind == model.HostKindSSH {
				reconnectCmd = m.schedulePaneAutoReconnect(m.tabForPane(pane), pane)
			}
			return m, tea.Batch(logCloseCmd, reconnectCmd)
		}
		return m, tea.Batch(logCloseCmd, startupCmd, startupScriptCmd, triggerCmd, bellCmd, readTerminalOutputCmd(msg.session))
	case zmodemTransferFinishedMsg:
		m.handleZMODEMFinished(msg)
		return m, nil
	case zmodemTransferProgressMsg:
		return m, m.updateZMODEMProgress(msg)
	case asciiFileSendLoadedMsg:
		m.handleASCIIFileSendLoaded(msg)
		return m, nil
	case asciiFileSendFinishedMsg:
		m.handleASCIIFileSendFinished(msg)
		return m, nil
	case asciiFileSendProgressMsg:
		if m.asciiFileSend == nil || m.asciiFileSend.requestID != msg.requestID {
			return m, nil
		}
		m.asciiFileSend.sent = msg.sent
		if msg.closed {
			return m, nil
		}
		return m, readASCIIFileSendProgressCmd(msg.requestID, m.asciiFileSend.progress)
	case portForwardStartedMsg:
		if msg.err != nil {
			if msg.forward != nil {
				_ = msg.forward.Close()
			}
			if m.portForwardManager != nil && m.portForwardManager.paneID == msg.paneID {
				m.portForwardManager.starting = false
				m.portForwardManager.errorText = "启动失败：" + msg.err.Error()
			}
			return m, nil
		}
		var targetPane *terminalPane
		for _, tab := range m.tabs {
			if pane := findPane(tab.root, msg.paneID); pane != nil {
				targetPane = pane
				break
			}
		}
		if targetPane == nil || targetPane.session == nil {
			_ = msg.forward.Close()
			return m, nil
		}
		targetPane.forwards = append(targetPane.forwards, msg.forward)
		if m.portForwardManager != nil && m.portForwardManager.paneID == msg.paneID {
			m.portForwardManager.starting = false
			m.portForwardManager.editing = false
			if msg.rule != nil {
				m.portForwardManager.focusSaved = true
				m.portForwardManager.focusChannels = false
				m.portForwardManager.selected = m.portForwardRuleIndex(msg.rule.Id)
			} else {
				m.portForwardManager.focusSaved = false
				m.portForwardManager.focusChannels = false
				m.portForwardManager.selected = len(m.activePortForwards()) - 1
			}
		}
		switch msg.forward.Kind() {
		case sshclient.ForwardSOCKS5:
			m.statusText = "SOCKS5 动态代理已启动：" + msg.forward.LocalAddress()
		case sshclient.ForwardRemoteTCP:
			m.statusText = "远程端口转发已启动：" + msg.forward.ListenAddress() + " → 本地 " + msg.forward.RemoteAddress()
		default:
			m.statusText = "本地端口转发已启动：" + msg.forward.LocalAddress() + " → " + msg.forward.RemoteAddress()
		}
		m.statusErr = false
		return m, nil
	case portForwardRulesListedMsg:
		if m.portForwardManager == nil || m.portForwardManager.paneID != msg.paneID {
			return m, nil
		}
		m.portForwardManager.loadingRules = false
		if msg.err != nil {
			m.portForwardManager.errorText = "读取已保存转发失败：" + msg.err.Error()
			return m, nil
		}
		m.portForwardManager.savedRules = msg.rules
		m.portForwardManager.errorText = ""
		return m, nil
	case portForwardRuleSavedMsg:
		if m.portForwardManager != nil && m.portForwardManager.paneID == msg.paneID {
			m.portForwardManager.savingRule = false
			if msg.err != nil {
				m.portForwardManager.errorText = "保存转发规则失败：" + msg.err.Error()
			} else {
				m.upsertPortForwardRule(msg.rule)
				m.portForwardManager.focusSaved = true
				m.portForwardManager.focusChannels = false
				m.portForwardManager.selected = m.portForwardRuleIndex(msg.rule.Id)
				m.portForwardManager.errorText = ""
				m.portForwardManager.noticeText = "规则已保存；下次连接此主机时会自动恢复"
			}
		}
		return m, nil
	case portForwardRuleDeletedMsg:
		if m.portForwardManager != nil && m.portForwardManager.paneID == msg.paneID {
			m.portForwardManager.deletingRule = false
			if msg.err != nil {
				m.portForwardManager.errorText = "删除转发规则失败：" + msg.err.Error()
			} else {
				m.removePortForwardRule(msg.ruleID)
				m.portForwardManager.errorText = ""
				m.portForwardManager.noticeText = "已删除自动恢复规则；当前运行中的转发不受影响"
			}
		}
		return m, nil
	case portForwardRulesRestoredMsg:
		pane := m.findPaneAcrossTabs(msg.paneID)
		if pane == nil || pane.session != msg.session {
			for _, result := range msg.results {
				if result.forward != nil {
					_ = result.forward.Close()
				}
			}
			return m, nil
		}
		if msg.err != nil {
			pane.statusText = "读取已保存端口转发失败：" + msg.err.Error()
			pane.statusErr = true
			return m, nil
		}
		var failures []string
		for _, result := range msg.results {
			if result.err != nil {
				failures = append(failures, result.rule.ListenAddress+"："+result.err.Error())
				continue
			}
			pane.forwards = append(pane.forwards, result.forward)
		}
		if m.portForwardManager != nil && m.portForwardManager.paneID == msg.paneID {
			m.portForwardManager.savedRules = msg.rules
			m.portForwardManager.loadingRules = false
			for _, result := range msg.results {
				if result.err != nil {
					m.portForwardManager.noticeText = fmt.Sprintf("自动恢复失败：%s（%s）", result.rule.ListenAddress, result.err)
					break
				}
			}
		}
		if len(failures) > 0 {
			pane.statusText = fmt.Sprintf("自动恢复端口转发：%d/%d 项失败；%s", len(failures), len(msg.rules), failures[0])
			pane.statusErr = true
		} else if len(msg.rules) > 0 {
			pane.statusText = fmt.Sprintf("已恢复 %d 条端口转发规则", len(msg.rules))
			pane.statusErr = false
		}
		return m, nil
	case portForwardStoppedMsg:
		if m.portForwardManager != nil && m.portForwardManager.paneID == msg.paneID {
			if msg.err != nil {
				m.portForwardManager.errorText = "停止失败：" + msg.err.Error()
			} else {
				m.portForwardManager.errorText = ""
			}
		}
		for _, tab := range m.tabs {
			if pane := findPane(tab.root, msg.paneID); pane != nil {
				for index, forward := range pane.forwards {
					if forward == msg.forward {
						pane.forwards = append(pane.forwards[:index], pane.forwards[index+1:]...)
						break
					}
				}
				break
			}
		}
		return m, nil
	case portForwardChannelClosedMsg:
		if m.portForwardManager != nil && m.portForwardManager.paneID == msg.paneID {
			if msg.err != nil {
				m.portForwardManager.errorText = "关闭通道失败：" + msg.err.Error()
			} else {
				m.portForwardManager.errorText = ""
				m.portForwardManager.noticeText = fmt.Sprintf("已关闭转发通道 #%d；监听器继续运行", msg.channelID)
			}
		}
		return m, nil
	case sftpOpenedMsg:
		return m, m.handleSFTPOpened(msg)
	case sftpListedMsg:
		m.handleSFTPListed(msg)
		return m, nil
	case sftpSynchronizedListedMsg:
		m.handleSFTPSynchronizedListed(msg)
		return m, nil
	case sftpSearchCompletedMsg:
		m.handleSFTPFileSearchCompleted(msg)
		return m, nil
	case sftpCompareCompletedMsg:
		return m, m.handleSFTPCompareCompleted(msg)
	case sftpMirrorDeleteCompletedMsg:
		return m, m.handleSFTPMirrorDeleteCompleted(msg)
	case sftpOperationMsg:
		return m, m.handleSFTPOperation(msg)
	case sftpOverwriteRequiredMsg:
		m.handleSFTPOverwriteRequired(msg)
		return m, nil
	case sftpQueuePreflightMsg:
		return m, m.handleSFTPQueuePreflight(msg)
	case sftpProgressMsg:
		return m, m.handleSFTPProgress(msg)
	case sftpEditorLoadedMsg:
		return m, m.handleSFTPTextLoaded(msg)
	case sftpEditorSavedMsg:
		m.handleSFTPTextSaved(msg)
		return m, nil
	case sftpExternalEditorFinishedMsg:
		return m, m.handleSFTPExternalEditorFinished(msg)
	case sftpImagePreparedMsg:
		return m, m.handleSFTPImagePrepared(msg)
	case sftpImageEditorFinishedMsg:
		return m, m.handleSFTPImageEditorFinished(msg)
	case sftpImageUploadedMsg:
		m.handleSFTPImageUploaded(msg)
		return m, nil
	case sftpEditorCommandSavedMsg:
		m.handleSFTPExternalEditorCommandSaved(msg)
		return m, nil
	case terminalClipboardCopyCompletedMsg:
		return m, m.handleTerminalClipboardCopyCompleted(msg)
	case appearanceSchemeSavedMsg:
		m.handleAppearanceSchemeSaved(msg)
		return m, nil
	case footerShortcutsPreferenceSavedMsg:
		m.handleFooterShortcutsPreferenceSaved(msg)
		return m, nil
	case tea.ClipboardMsg:
		if m.screenLock != nil || m.workspaceHelp || m.commandHistoryView != nil {
			return m, nil
		}
		return m, m.handleTerminalClipboard(msg)
	case tea.PasteMsg:
		if len(m.tabs) > 0 {
			m.sftp = m.activeSFTPState()
		}
		if m.appConfirmation != nil {
			return m, nil
		}
		if m.screenLock != nil {
			m.updateScreenLockPaste(msg.Content)
			return m, nil
		}
		if m.zmodemTransfer != nil {
			m.appendZMODEMPaste(msg.Content)
			return m, nil
		}
		if m.asciiFileSend != nil {
			m.appendASCIIFileSendPaste(msg.Content)
			return m, nil
		}
		if m.tabGroupManager != nil {
			if m.tabGroupManager.editing && len([]rune(m.tabGroupManager.name))+len([]rune(msg.Content)) <= workspaceTabGroupMaxRunes {
				m.tabGroupManager.name += msg.Content
				m.tabGroupManager.error = ""
			}
			return m, nil
		}
		if m.workspaceHelp || m.commandHistoryView != nil {
			return m, nil
		}
		if m.terminalLogViewer != nil {
			m.handleTerminalLogViewerPaste(msg.Content)
			return m, nil
		}
		if m.pluginPalette != nil {
			m.appendPluginPalettePaste(msg.Content)
			return m, nil
		}
		if m.composePane != nil {
			m.appendComposePanePaste(msg.Content)
			return m, nil
		}
		if m.quickCommands != nil && m.quickCommands.editing {
			m.appendQuickCommandPaste(msg.Content)
			return m, nil
		}
		if m.keyboardAuthPrompt != nil {
			m.appendKeyboardAuthPaste(msg.Content)
			return m, nil
		}
		if m.credentialPrompt != nil {
			if len([]rune(m.credentialPrompt.value))+len([]rune(msg.Content)) <= 512 {
				m.credentialPrompt.value += msg.Content
				m.credentialPrompt.errorText = ""
			}
			return m, nil
		}
		if m.passwordPrompt {
			if len([]rune(m.passwordValue))+len([]rune(msg.Content)) <= 512 {
				m.passwordValue += msg.Content
				m.passwordError = ""
			}
			return m, nil
		}
		if m.ftpPasswordPrompt {
			if len([]rune(m.ftpPasswordValue))+len([]rune(msg.Content)) <= 512 {
				m.ftpPasswordValue += msg.Content
				m.ftpPasswordError = ""
			}
			return m, nil
		}
		if m.scheduledTransfers != nil && m.scheduledTransfers.editing {
			m.appendScheduledTransferPaste(msg.Content)
			return m, nil
		}
		if m.sftp != nil && (m.workspaceVisible || len(m.tabs) == 0) &&
			m.sftp.prompt == nil && m.sftp.editor != nil && !m.sftp.externalEditorOnly && m.sftp.editor.mode == sftpEditMode {
			insertSFTPText(m.sftp.editor, msg.Content)
			m.sftp.editor.dirty = strings.Join(m.sftp.editor.lines, "\n") != m.sftp.editor.original
			keepSFTPTextCursorVisible(m.sftp.editor, m.height)
			return m, nil
		}
		if m.sftp != nil && (m.workspaceVisible || len(m.tabs) == 0) && m.sftp.prompt != nil &&
			m.sftp.prompt.action != sftpPromptDelete && m.sftp.prompt.action != sftpPromptOverwrite && m.sftp.prompt.action != sftpPromptDiscard {
			if len([]rune(m.sftp.prompt.value))+len([]rune(msg.Content)) <= 1024 {
				m.sftp.prompt.value += msg.Content
			}
			return m, nil
		}
		if pane := m.activeTerminalPane(); m.workspaceVisible && pane != nil && pane.search != nil && pane.search.editing {
			appendTerminalSearchText(pane, msg.Content)
			return m, nil
		}
		if pane := m.activeTerminalPane(); m.workspaceVisible && pane != nil && pane.session != nil {
			m.writeWorkspaceInput(func(target *terminalPane) []byte {
				paste := msg.Content
				if target.bracketedPaste {
					paste = "\x1b[200~" + paste + "\x1b[201~"
				}
				return []byte(paste)
			}, "发送终端粘贴内容失败：")
		}
		return m, nil
	case tea.MouseMsg:
		if len(m.tabs) > 0 {
			m.sftp = m.activeSFTPState()
		}
		if m.screenLock != nil {
			return m, nil
		}
		if m.appConfirmation != nil {
			return m, nil
		}
		if m.tabGroupManager != nil {
			return m, nil
		}
		if m.zmodemTransfer != nil || m.asciiFileSend != nil || m.composePane != nil || m.pluginPalette != nil || m.terminalLogViewer != nil || m.sftpFilters != nil || m.sftpBookmarks != nil || m.scheduledTransfers != nil || m.workspaceHelp || m.commandHistoryView != nil || m.serialPortPicker != nil || m.shortcutBarPicker != nil || m.batchEdit != nil || m.workspaceLayouts != nil {
			return m, nil
		}
		return m, m.updateMouse(msg)
	case tea.KeyPressMsg:
		if len(m.tabs) > 0 {
			m.sftp = m.activeSFTPState()
		}
		if m.screenLock != nil {
			return m, m.updateScreenLockKey(msg)
		}
		if m.zmodemTransfer != nil {
			return m.updateZMODEMKey(msg)
		}
		if m.asciiFileSend != nil {
			return m.updateASCIIFileSendKey(msg)
		}
		if m.commandHistoryView != nil {
			return m.updateTerminalCommandHistoryKey(msg)
		}
		if m.terminalLogViewer != nil {
			return m, m.updateTerminalLogViewerKey(msg)
		}
		if m.serialPortPicker != nil {
			return m.updateSerialPortPickerKey(msg)
		}
		if m.workspaceLayouts != nil {
			return m.updateWorkspaceLayoutManagerKey(msg)
		}
		return m.updateKey(msg)
	}
	return m, nil
}

func (m modelState) View() tea.View {
	if m.screenLock != nil {
		view := tea.NewView(m.screenLockView())
		view.AltScreen = true
		view.BackgroundColor = palette.canvas
		view.ForegroundColor = palette.primary
		view.WindowTitle = "SSH 终端工作台 · 已锁定"
		return view
	}
	content := m.view()
	if m.contextMenu != nil {
		menu, x, y, width, height := m.contextMenuLayout()
		content = overlayContextMenu(content, menu, x, y, width, height, m.width)
	}
	if m.asciiFileSend != nil {
		panel, x, y, width, height := m.asciiFileSendLayout()
		content = overlayContextMenu(content, panel, x, y, width, height, m.width)
	}
	if m.zmodemTransfer != nil {
		panel, x, y, width, height := m.zmodemTransferLayout()
		content = overlayContextMenu(content, panel, x, y, width, height, m.width)
	}
	if m.scheduledTransferNotice != "" && m.appConfirmation == nil && m.width > 0 && m.height > 0 &&
		m.keyboardAuthPrompt == nil && m.credentialPrompt == nil && m.trustPrompt == nil && m.hostKeyChangePrompt == nil &&
		!m.passwordPrompt && !m.ftpPasswordPrompt && !m.trustingHost {
		style := lipgloss.NewStyle().Background(palette.canvas).Foreground(palette.accent).Bold(true).Padding(0, 1)
		if m.scheduledTransferNoticeErr {
			style = lipgloss.NewStyle().Background(palette.canvas).Foreground(palette.warning).Bold(true).Padding(0, 1)
		}
		notice := style.Render("◆ " + m.scheduledTransferNotice)
		width := min(lipgloss.Width(notice), m.width)
		x := max(0, m.width-width-1)
		y := max(0, len(strings.Split(content, "\n"))-1)
		content = overlayContextMenu(content, notice, x, y, width, 1, m.width)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.BackgroundColor = palette.canvas
	view.ForegroundColor = palette.primary
	view.WindowTitle = "SSH 终端工作台"
	view.MouseMode = tea.MouseModeCellMotion
	var activeSFTP *sftpBrowserState
	if m.workspaceVisible {
		activeSFTP = m.activeSFTPState()
	}
	if activeSFTP == nil && len(m.tabs) == 0 {
		activeSFTP = m.sftp
	}
	if m.appConfirmation == nil && activeSFTP != nil && m.sftpFilters == nil && m.keyboardAuthPrompt == nil && m.trustPrompt == nil && m.hostKeyChangePrompt == nil && m.hostPicker == nil && m.connectingHost == nil && !m.trustingHost && !m.passwordPrompt {
		if activeSFTP.editor != nil && !activeSFTP.externalEditorOnly {
			if activeSFTP.prompt != nil {
				view.Cursor = sftpPromptCursor(activeSFTP, m.width, max(8, m.height-1))
			} else {
				view.Cursor = sftpTextEditorCursor(activeSFTP.editor, m.width, max(8, m.height-1))
			}
		} else {
			view.Cursor = sftpPromptCursor(activeSFTP, m.width, max(8, m.height-1))
		}
	} else if m.appConfirmation == nil && m.zmodemTransfer == nil && m.asciiFileSend == nil && m.workspaceVisible && !m.workspaceHelp && m.commandHistoryView == nil && activeSFTP == nil && m.hostPicker == nil && m.pluginPalette == nil && m.terminalLogViewer == nil && m.portForwardManager == nil && m.quickCommands == nil && m.scheduledTransfers == nil && m.composePane == nil && m.connectingHost == nil &&
		m.trustPrompt == nil && m.hostKeyChangePrompt == nil && !m.trustingHost && !m.passwordPrompt {
		view.MouseMode = m.workspaceMouseMode()
		if m.contextMenu == nil {
			view.Cursor = m.workspaceCursor()
		}
	}
	return view
}

func (m modelState) loadCatalogCmd() tea.Cmd {
	if m.catalog == nil {
		return func() tea.Msg {
			return catalogLoadedMsg{groups: []model.Group{}, hosts: []model.Host{}}
		}
	}
	catalog := m.catalog
	return func() tea.Msg {
		ctx := context.Background()
		groups, err := catalog.ListGroups(ctx)
		if err != nil {
			return catalogLoadedMsg{err: err}
		}
		hosts, err := catalog.ListHosts(ctx)
		if err != nil {
			return catalogLoadedMsg{groups: groups, err: err}
		}
		loaded := catalogLoadedMsg{groups: groups, hosts: hosts}
		if bars, ok := catalog.(shortcutBarCatalog); ok {
			loaded.shortcutBars, loaded.shortcutBarsErr = bars.ListShortcutBars(ctx)
		}
		if profiles, ok := catalog.(authProfileCatalog); ok {
			loaded.authProfiles, err = profiles.ListAuthProfiles(ctx)
			if err != nil {
				return catalogLoadedMsg{groups: groups, hosts: hosts, err: fmt.Errorf("读取认证配置档失败：%w", err)}
			}
		}
		if preferences, ok := catalog.(defaultSessionCatalog); ok {
			loaded.defaultHostID, loaded.defaultHostErr = preferences.DefaultHostID(ctx)
		}
		if lastWorkspace, ok := catalog.(lastWorkspaceCatalog); ok {
			loaded.lastWorkspaceData, loaded.lastWorkspaceErr = lastWorkspace.LastWorkspaceData(ctx)
		}
		if appearance, ok := catalog.(appearancePreferenceCatalog); ok {
			loaded.appearanceScheme, loaded.appearanceErr = appearance.AppearanceScheme(ctx)
		}
		if footerShortcuts, ok := catalog.(workspaceFooterPreferenceCatalog); ok {
			loaded.footerShortcutsVisible, loaded.footerShortcutsSet, loaded.footerShortcutsErr = footerShortcuts.FooterShortcutsVisible(ctx)
		}
		if editor, ok := catalog.(sftpEditorCommandPreferenceCatalog); ok {
			loaded.sftpEditorCommand, loaded.sftpEditorErr = editor.SFTPExternalEditorCommand(ctx)
		}
		return loaded
	}
}

func (m *modelState) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.appConfirmation != nil {
		return m.updateAppConfirmationKey(msg)
	}
	if m.batchEdit != nil {
		return m.updateBatchEditKey(msg)
	}
	if m.tabGroupManager != nil {
		return m.updateWorkspaceTabGroupManagerKey(msg)
	}
	if m.workspaceLayouts != nil {
		return m.updateWorkspaceLayoutManagerKey(msg)
	}
	if m.sshKeyGeneration != nil {
		return m.updateSSHKeyGenerationKey(msg)
	}
	if m.sshKeyManager != nil {
		return m.updateSSHKeyManagerKey(msg)
	}
	if m.sshHostKeyManager != nil {
		return m.updateSSHHostKeyManagerKey(msg)
	}
	if m.authProfileManager != nil {
		return m.updateAuthProfileManagerKey(msg)
	}
	if m.workspaceHelp {
		return m.updateWorkspaceHelpKey(msg)
	}
	if m.appearancePicker != nil {
		return m.updateAppearancePickerKey(msg)
	}
	if m.quickConnect != nil {
		return m.updateQuickConnectKey(msg)
	}
	if m.keyboardAuthPrompt != nil {
		return m.updateKeyboardAuthKey(msg)
	}
	if m.credentialPrompt != nil {
		return m.updateCredentialPromptKey(msg)
	}
	if m.trustPrompt != nil {
		switch key {
		case "enter", "y":
			if m.connectingHost == nil || m.trustingHost {
				return m, nil
			}
			host := *m.connectingHost
			publicKey := m.trustPrompt.Key
			trustHost := m.trustPrompt.Host
			if trustHost.Id == "" && trustHost.IP == "" {
				trustHost = host
			}
			m.trustPrompt = nil
			m.trustingHost = true
			return m, func() tea.Msg {
				return hostTrustedMsg{err: sshclient.TrustHost(trustHost, publicKey)}
			}
		case "esc", "escape", "n", "q", "ctrl+c":
			m.cancelSSHConnect()
			m.statusText = "已取消 SSH 连接"
			m.statusErr = false
			return m, nil
		}
		return m, nil
	}
	if m.hostKeyChangePrompt != nil {
		switch key {
		case "enter", "y":
			if m.connectingHost == nil || m.trustingHost {
				return m, nil
			}
			prompt := m.hostKeyChangePrompt
			if !prompt.CanReplace {
				m.cancelSSHConnect()
				m.statusText = prompt.ReplaceBlockReason
				m.statusErr = true
				return m, nil
			}
			host := prompt.Host
			if host.Id == "" && host.IP == "" {
				host = *m.connectingHost
			}
			m.hostKeyChangePrompt = nil
			m.trustingHost = true
			m.replacingHostKey = true
			return m, func() tea.Msg {
				return hostKeyChangedMsg{err: sshclient.ReplaceKnownHostKey(host, prompt.Key, prompt.References)}
			}
		case "esc", "escape", "n", "q", "ctrl+c":
			m.cancelSSHConnect()
			m.statusText = "已取消 SSH 连接；未更改主机指纹记录"
			m.statusErr = false
		}
		return m, nil
	}
	if m.ftpPasswordPrompt {
		form := msg.Key()
		switch key {
		case "enter":
			if m.ftpPendingHost == nil || m.ftpPendingTarget == nil {
				m.ftpPasswordPrompt = false
				m.ftpPasswordError = "连接状态已失效，请重新选择主机。"
				return m, nil
			}
			host, target, password := *m.ftpPendingHost, *m.ftpPendingTarget, m.ftpPasswordValue
			m.ftpPendingHost, m.ftpPendingTarget = nil, nil
			m.ftpPasswordValue = ""
			m.ftpPasswordPrompt = false
			return m, m.startFTPConnect(host, target, password)
		case "esc", "escape", "ctrl+c":
			target := m.ftpPendingTarget
			m.ftpPasswordPrompt = false
			m.ftpPasswordValue = ""
			m.ftpPasswordError = ""
			m.ftpPendingHost, m.ftpPendingTarget = nil, nil
			m.lastFTPPassword = ""
			if target != nil && target.kind == sftpPeerTarget && m.sftp != nil {
				m.sftp.busy = false
				m.sftp.status = "已取消" + sftpSideLabel(target.sftpSide) + "远程主机连接"
				m.sftp.statusErr = false
			} else {
				m.statusText, m.statusErr = "已取消 FTP/FTPS 连接", false
			}
			return m, nil
		case "backspace", "ctrl+h":
			password := []rune(m.ftpPasswordValue)
			if len(password) > 0 {
				m.ftpPasswordValue = string(password[:len(password)-1])
			}
			m.ftpPasswordError = ""
			return m, nil
		default:
			if form.Text != "" && len([]rune(m.ftpPasswordValue))+len([]rune(form.Text)) <= 512 {
				m.ftpPasswordValue += form.Text
				m.ftpPasswordError = ""
			}
			return m, nil
		}
	}
	if m.passwordPrompt {
		form := msg.Key()
		switch key {
		case "enter":
			if m.passwordValue == "" {
				m.passwordError = "请输入密码，或按 Esc 取消连接。"
				return m, nil
			}
			if m.connectingHost == nil {
				m.passwordPrompt = false
				return m, nil
			}
			if m.connectionTarget == nil {
				m.cancelSSHConnect()
				m.statusText = "SSH 连接状态已失效，请重试"
				m.statusErr = true
				return m, nil
			}
			host, target := *m.connectingHost, *m.connectionTarget
			authenticationHost := m.authenticationHost
			if authenticationHost == nil {
				authenticationHost = m.connectingHost
			}
			credentialKey := sshclient.HostCredentialKey(*authenticationHost)
			if m.connectionPasswords == nil {
				m.connectionPasswords = make(map[string]string)
			}
			if m.passwordAttempts == nil {
				m.passwordAttempts = make(map[string]int)
			}
			m.connectionPasswords[credentialKey] = m.passwordValue
			m.passwordAttempts[credentialKey]++
			m.authenticationHost = nil
			m.passwordValue = ""
			m.passwordPrompt = false
			return m, m.startSSHConnect(host, target)
		case "esc", "escape", "ctrl+c":
			m.passwordValue = ""
			m.passwordPrompt = false
			m.cancelSSHConnect()
			m.statusText = "已取消 SSH 连接"
			m.statusErr = false
			return m, nil
		case "backspace", "ctrl+h":
			password := []rune(m.passwordValue)
			if len(password) > 0 {
				m.passwordValue = string(password[:len(password)-1])
			}
			m.passwordError = ""
			return m, nil
		default:
			if form.Text != "" && len([]rune(m.passwordValue))+len([]rune(form.Text)) <= 512 {
				m.passwordValue += form.Text
				m.passwordError = ""
			}
			return m, nil
		}
	}
	if m.connectingHost != nil {
		switch key {
		case "esc", "escape", "ctrl+c":
			wasLocal := m.connectingHost.Kind == model.HostKindLocal
			wasSerial := m.connectingHost.Kind == model.HostKindSerial
			wasFTP := m.connectingHost.Kind == model.HostKindFTP
			wasTelnet := m.connectingHost.Kind == model.HostKindTelnet
			terminalProtocol := terminalProtocolName(*m.connectingHost)
			m.cancelSSHConnect()
			if wasLocal {
				m.statusText = "已取消本地终端启动"
			} else if wasSerial {
				m.statusText = "已取消串口连接"
			} else if wasFTP {
				m.statusText = "已取消 FTP/FTPS 连接"
			} else if wasTelnet {
				m.statusText = "已取消 " + terminalProtocol + " 连接"
			} else {
				m.statusText = "已取消 SSH 连接"
			}
			m.statusErr = false
		}
		return m, nil
	}
	if m.hostPicker != nil {
		return m.updateHostPickerKey(msg)
	}
	if m.shortcutBarPicker != nil {
		return m.updateShortcutBarPickerKey(msg)
	}
	if m.pluginPalette != nil {
		return m.updatePluginPaletteKey(msg)
	}
	if m.sftpFilters != nil {
		return m.updateSFTPFiltersKey(msg)
	}
	if m.sftpBookmarks != nil {
		return m.updateSFTPBookmarksKey(msg)
	}
	if m.scheduledTransfers != nil {
		return m.updateScheduledTransferKey(msg)
	}
	if m.composePane != nil {
		return m.updateComposePaneKey(msg)
	}
	if m.quickCommands != nil {
		return m.updateQuickCommandKey(msg)
	}
	if m.deleteConfirm != nil {
		return m.updateCatalogDeleteConfirm(key)
	}
	if m.contextMenu != nil {
		return m.updateContextMenuKey(msg)
	}
	if m.portForwardManager != nil {
		return m.updatePortForwardKey(msg)
	}
	if m.sftp != nil && m.workspaceVisible && len(m.tabs) > 0 {
		if key == "ctrl+o" {
			m.workspaceVisible = false
			m.workspaceLeaderPending = false
			return m, nil
		}
		if m.workspaceLeaderPending {
			m.workspaceLeaderPending = false
			action := workspaceLeaderAction(msg.Key(), m.config.WithDefaults().Workspace)
			switch {
			case action == "tab_next":
				return m, tea.Batch(m.activateTab(1)...)
			case action == "tab_previous":
				return m, tea.Batch(m.activateTab(-1)...)
			case action == "home":
				m.workspaceVisible = false
				return m, nil
			case strings.HasPrefix(action, "tab_"):
				index, err := strconv.Atoi(strings.TrimPrefix(action, "tab_"))
				if err == nil && index >= 1 && index <= 9 {
					return m, tea.Batch(m.activateTabIndex(index - 1)...)
				}
			}
			return m, nil
		}
		if msg.Key().Mod&tea.ModCtrl != 0 && workspaceLeaderMatches(msg.Key(), m.config.WithDefaults().Workspace) {
			m.workspaceLeaderPending = true
			return m, nil
		}
	}
	if m.sftp != nil && (m.workspaceVisible || len(m.tabs) == 0) {
		return m.updateSFTPKey(msg)
	}
	if m.workspaceVisible && len(m.tabs) > 0 {
		return m.updateWorkspaceKey(msg)
	}
	if m.form != nil {
		return m.updateFormKey(msg)
	}
	if m.showHelp {
		if key == "?" || key == "esc" || key == "escape" || key == "q" {
			m.showHelp = false
		}
		return m, nil
	}
	if m.searching {
		switch key {
		case "enter":
			m.searching = false
		case "esc", "escape":
			m.searching = false
			m.query = ""
			m.hostIndex, m.hostTop = 0, 0
		case "backspace", "ctrl+h":
			query := []rune(m.query)
			if len(query) > 0 {
				m.query = string(query[:len(query)-1])
			}
			m.hostIndex, m.hostTop = 0, 0
		case "ctrl+c":
			m.query, m.searching = "", false
			m.hostIndex, m.hostTop = 0, 0
		default:
			if text := msg.Key().Text; text != "" {
				m.query += text
				m.hostIndex, m.hostTop = 0, 0
			}
		}
		m.ensureHostSelection()
		return m, nil
	}

	switch key {
	case "q", "ctrl+c":
		m.requestQuitConfirmation()
		return m, nil
	case " ", "space":
		m.toggleBatchHostSelection()
	case "B":
		return m.openBatchEdit()
	case "esc", "escape":
		if len(m.batchSelectedHostIDs) > 0 {
			m.batchSelectedHostIDs = nil
			m.statusText, m.statusErr = "已清除批量选择", false
		}
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if command := m.connectFavoriteShortcut(key); command != nil {
			return m, command
		}
	case "[":
		if m.selectedGroup().isShortcutBar {
			return m, m.moveSelectedShortcutBarHost(-1)
		}
		return m, m.moveSelectedFavorite(-1)
	case "]":
		if m.selectedGroup().isShortcutBar {
			return m, m.moveSelectedShortcutBarHost(1)
		}
		return m, m.moveSelectedFavorite(1)
	case "ctrl+o":
		if len(m.tabs) > 0 {
			m.workspaceVisible = true
			return m, tea.Batch(m.resizeActiveWorkspace()...)
		}
	case "?":
		m.showHelp = true
	case "T":
		m.openAppearancePicker()
	case "S":
		return m, m.toggleWorkspaceFooterShortcuts()
	case "a":
		m.openQuickConnect()
	case "ctrl+k":
		return m, m.openSSHKeyManager()
	case "ctrl+shift+h":
		return m, m.openSSHHostKeyManager()
	case "ctrl+p":
		return m, m.openAuthProfileManager()
	case "tab":
		if m.activePane == groupPane {
			m.activePane = hostPane
		} else {
			m.activePane = groupPane
		}
	case "/":
		if len(m.hosts) > 0 {
			m.searching = true
		}
	case "r":
		m.loading = true
		m.statusText = "正在刷新本地主机目录…"
		m.statusErr = false
		return m, m.loadCatalogCmd()
	case "g":
		if m.loading {
			m.statusText = "请等待本地数据加载完成"
			return m, nil
		}
		parentID := ""
		selected := m.selectedGroup()
		if !selected.isAll && !selected.isFavoriteView && !selected.isRecentView && !selected.isShortcutBar {
			parentID = selected.id
		}
		m.form = newGroupForm(parentID)
	case "b":
		if m.loading {
			m.statusText = "请等待本地数据加载完成"
			return m, nil
		}
		m.form = newShortcutBarForm()
	case "n":
		if m.loading {
			m.statusText = "请等待本地数据加载完成"
			return m, nil
		}
		groupID := ""
		selected := m.selectedGroup()
		if !selected.isAll && !selected.isFavoriteView && !selected.isRecentView && !selected.isShortcutBar {
			groupID = selected.id
		}
		m.form = newHostForm(groupID)
	case "f":
		if m.loading {
			m.statusText = "请等待本地数据加载完成"
			return m, nil
		}
		if m.activePane != hostPane {
			m.statusText = "请先切换到主机列表"
			m.statusErr = true
			return m, nil
		}
		host := m.selectedHost()
		if host == nil {
			m.statusText = "请先选择 SSH/SFTP、SSH/SCP 或 FTP/FTPS 主机"
			m.statusErr = true
			return m, nil
		}
		if host.Kind != model.HostKindSSH && host.Kind != model.HostKindFTP {
			m.statusText = "文件面板仅支持 SSH 或 FTP/FTPS 主机"
			m.statusErr = true
			return m, nil
		}
		return m, m.startHostConnect(*host, m.newSFTPTarget())
	case "v":
		return m, m.toggleSelectedHostFavorite()
	case "m":
		return m, m.toggleSelectedShortcutBarMembership()
	case "s":
		return m, m.toggleDefaultSession()
	case "t":
		return m, m.openScheduledTransferManager()
	case "l":
		command := m.openWorkspaceLayoutManager()
		return m, command
	case "R":
		return m, m.restoreLastWorkspace()
	case "e":
		if m.loading {
			m.statusText = "请等待本地数据加载完成"
			return m, nil
		}
		if m.activePane == groupPane {
			selected := m.selectedGroup()
			if selected.isAll {
				m.statusText = "全部主机不是可编辑分组"
				m.statusErr = true
				return m, nil
			}
			if selected.isShortcutBar {
				if bar := m.shortcutBarByID(selected.id); bar != nil {
					m.form = newShortcutBarEditForm(*bar)
				}
			} else if selected.isFavoriteView || selected.isRecentView {
				m.statusText = "系统视图不是可编辑分组"
				m.statusErr = true
			} else if group := m.groupByID(selected.id); group != nil {
				m.form = newGroupEditForm(*group)
			}
		} else if host := m.selectedHost(); host != nil {
			m.form = newHostEditForm(*host)
			for _, profile := range m.authProfiles {
				if profile.Id == host.AuthProfileID {
					m.form.authProfileName = profile.Name
					break
				}
			}
		}
	case "c":
		m.openCredentialPrompt()
	case "d":
		if m.loading {
			m.statusText = "请等待本地数据加载完成"
			return m, nil
		}
		if m.activePane == groupPane {
			selected := m.selectedGroup()
			if selected.isShortcutBar {
				m.deleteConfirm = &catalogDeleteConfirm{kind: deleteShortcutBar, id: selected.id, name: selected.name}
			} else if selected.isAll || selected.isFavoriteView || selected.isRecentView {
				m.statusText = "系统视图不是可删除分组"
				m.statusErr = true
				return m, nil
			} else {
				m.deleteConfirm = &catalogDeleteConfirm{kind: deleteGroup, id: selected.id, name: selected.name}
			}
		} else if host := m.selectedHost(); host != nil {
			m.deleteConfirm = &catalogDeleteConfirm{kind: deleteHost, id: host.Id, name: host.Name, credentialRef: host.CredentialRef}
		}
	case "up", "k":
		m.moveSelection(-1)
	case "down", "j":
		m.moveSelection(1)
	case "enter":
		if m.activePane == hostPane && m.selectedHost() != nil {
			host := *m.selectedHost()
			return m, m.startHostConnect(host, m.newTabTarget())
		}
	}
	return m, nil
}

func (m modelState) requestSerialPorts() (modelState, tea.Cmd) {
	if m.form == nil || m.form.kind != hostForm || m.form.hostKind != model.HostKindSerial || m.form.field != 2 {
		return m, nil
	}
	m.nextSerialPortRequestID++
	requestID := m.nextSerialPortRequestID
	m.serialPortPicker = &serialPortPickerState{requestID: requestID, loading: true}
	lister := m.serialPortLister
	command := func() tea.Msg {
		if lister == nil {
			lister = serialclient.ListPorts
		}
		ports, err := lister()
		return serialPortsLoadedMsg{requestID: requestID, ports: ports, err: err}
	}
	return m, command
}

func (m modelState) updateSerialPortPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	picker := m.serialPortPicker
	if picker == nil {
		return m, nil
	}
	key := msg.String()
	switch key {
	case "esc", "escape", "ctrl+c":
		m.serialPortPicker = nil
	case "ctrl+r":
		return m.requestSerialPorts()
	case "up", "k":
		if !picker.loading && len(picker.ports) > 0 {
			picker.index = clamp(picker.index-1, 0, len(picker.ports)-1)
		}
	case "down", "j":
		if !picker.loading && len(picker.ports) > 0 {
			picker.index = clamp(picker.index+1, 0, len(picker.ports)-1)
		}
	case "enter":
		if !picker.loading && picker.errorText == "" && len(picker.ports) > 0 && m.form != nil {
			m.form.address = picker.ports[picker.index]
			m.form.errorText = ""
			m.serialPortPicker = nil
		}
	}
	if m.serialPortPicker != nil && len(picker.ports) > 0 {
		picker.top = keepVisible(picker.index, picker.top, m.serialPortPickerRows())
	}
	return m, nil
}

func (m modelState) serialPortPickerRows() int {
	return max(1, min(8, m.height-16))
}

func (m *modelState) startSSHConnect(host model.Host, target connectionTarget) tea.Cmd {
	host = m.hostWithAuthProfile(host)
	m.clearConnectCancel()
	// Network stages have their own dial/handshake timeouts. Do not put a single
	// deadline around this context: an MFA prompt may legitimately take a while.
	ctx, cancel := context.WithCancel(context.Background())
	m.connectCancel = cancel
	m.connectingHost = &host
	m.connectionTarget = &target
	width, height := target.width, target.height
	hosts := m.hostsWithAuthProfiles()
	credentials := make(map[string]string, len(m.connectionPasswords))
	for key, secret := range m.connectionPasswords {
		credentials[key] = secret
	}
	flow := newKeyboardAuthFlow(ctx)
	m.keyboardAuthFlow = flow
	options := sshConnectOptions(m.config)
	connectCmd := func() tea.Msg {
		var startupScript *startupLoginScript
		if host.Kind == model.HostKindSSH && target.kind != standaloneSFTPTarget && target.kind != sftpPeerTarget {
			var scriptErr error
			startupScript, scriptErr = loadStartupLoginScript(host.StartupScriptPath)
			if scriptErr != nil {
				return terminalConnectedMsg{host: host, target: target, err: &startupScriptConfigurationError{err: scriptErr}}
			}
		}
		var session *sshclient.Session
		var err error
		if target.kind == standaloneSFTPTarget || target.kind == sftpPeerTarget {
			session, err = sshclient.ConnectSFTPWithJumpsAndCredentialsWithOptions(ctx, host, credentials, hosts, flow.challenge, options)
		} else {
			session, err = sshclient.ConnectWithJumpsAndCredentialsWithOptions(ctx, host, credentials, hosts, width, height, flow.challenge, options)
		}
		var authenticationHost model.Host
		var authenticationErr *sshclient.AuthenticationError
		if errors.As(err, &authenticationErr) {
			authenticationHost = authenticationErr.Host
		}
		for key := range credentials {
			credentials[key] = ""
			delete(credentials, key)
		}
		return terminalConnectedMsg{host: host, authenticationHost: authenticationHost, session: session, target: target, startupScript: startupScript, err: err}
	}
	return tea.Batch(connectCmd, waitKeyboardAuthPromptCmd(flow))
}

func sshConnectOptions(config model.Config) sshclient.ConnectOptions {
	config = config.WithDefaults()
	if config.Validate() != nil {
		config = model.DefaultConfig()
	}
	return sshclient.OptionsFromSSHConfig(config.SSH)
}

func (m *modelState) startHostConnect(host model.Host, target connectionTarget) tea.Cmd {
	m.clearConnectionCredentials()
	host = m.hostWithGroupStartDirs(host)
	if host.Kind == model.HostKindSSH {
		host = m.hostWithAuthProfile(host)
	}
	if host.Kind == model.HostKindFTP {
		if target.kind == newTabTarget || target.kind == splitPaneTarget {
			target = m.newSFTPTarget()
		}
		if host.CredentialRef != "" && m.credentials != nil {
			m.connectingHost = &host
			m.connectionTarget = &target
			if target.kind == sftpPeerTarget && m.sftp != nil {
				m.sftp.busy = true
				m.sftp.status = "正在读取系统钥匙串凭据…"
				m.sftp.statusErr = false
			}
			vault := m.credentials
			return func() tea.Msg {
				password, err := vault.Get(host.CredentialRef)
				return ftpCredentialLoadedMsg{host: host, target: target, password: password, err: err}
			}
		}
		m.promptFTPPassword(host, target)
		return nil
	}
	if host.Kind == model.HostKindTelnet {
		if host.TerminalProtocol == model.TerminalProtocolRlogin {
			return m.startRloginConnect(host, target)
		}
		return m.startTelnetConnect(host, target)
	}
	if host.Kind == model.HostKindSerial {
		return m.startSerialConnect(host, target)
	}
	if host.Kind != model.HostKindLocal {
		resolvedHosts := m.hostsWithAuthProfiles()
		if host.Kind == model.HostKindSSH && hostHasSavedCredential(host, resolvedHosts) && m.credentials != nil {
			m.connectingHost = &host
			m.connectionTarget = &target
			hosts := resolvedHosts
			vault := m.credentials
			return func() tea.Msg {
				passwords := make(map[string]string)
				var warnings []string
				for _, authHost := range sshCredentialHosts(host, hosts) {
					if authHost.CredentialRef == "" {
						continue
					}
					password, err := vault.Get(authHost.CredentialRef)
					if err != nil {
						if !errors.Is(err, credentialstore.ErrNotFound) {
							warnings = append(warnings, authHost.Name+"："+err.Error())
						}
						continue
					}
					if password != "" {
						passwords[sshclient.HostCredentialKey(authHost)] = password
					}
				}
				return sshCredentialsLoadedMsg{host: host, target: target, passwords: passwords, warning: strings.Join(warnings, "；")}
			}
		}
		return m.startSSHConnect(host, target)
	}
	m.clearConnectCancel()
	m.connectingHost = &host
	m.connectionTarget = &target
	width, height := target.width, target.height
	return func() tea.Msg {
		session, err := localterminal.Start(width, height)
		return terminalConnectedMsg{host: host, session: session, target: target, err: err}
	}
}

func (m *modelState) startTelnetConnect(host model.Host, target connectionTarget) tea.Cmd {
	m.clearConnectCancel()
	ctx, cancel := context.WithCancel(context.Background())
	m.connectCancel = cancel
	m.connectingHost = &host
	m.connectionTarget = &target
	width, height := target.width, target.height
	return func() tea.Msg {
		session, err := telnetclient.Dial(ctx, telnetclient.Address(host.IP, host.Port), 0, width, height)
		return terminalConnectedMsg{host: host, session: session, target: target, err: err}
	}
}

func (m *modelState) startRloginConnect(host model.Host, target connectionTarget) tea.Cmd {
	m.clearConnectCancel()
	ctx, cancel := context.WithCancel(context.Background())
	m.connectCancel = cancel
	m.connectingHost = &host
	m.connectionTarget = &target
	width, height := target.width, target.height
	localUser, userErr := rloginclient.CurrentUsername()
	if userErr != nil {
		return func() tea.Msg {
			return terminalConnectedMsg{host: host, target: target, err: userErr}
		}
	}
	return func() tea.Msg {
		session, err := rloginclient.Dial(ctx, rloginclient.Address(host.IP, host.Port), localUser, host.User, "xterm-256color/9600", 15*time.Second, width, height)
		return terminalConnectedMsg{host: host, session: session, target: target, err: err}
	}
}

func (m *modelState) startSerialConnect(host model.Host, target connectionTarget) tea.Cmd {
	m.clearConnectCancel()
	m.connectingHost = &host
	m.connectionTarget = &target
	return func() tea.Msg {
		session, err := serialclient.Open(host)
		return terminalConnectedMsg{host: host, session: session, target: target, err: err}
	}
}

func terminalProtocolName(host model.Host) string {
	if host.TerminalProtocol == model.TerminalProtocolRlogin {
		return "Rlogin"
	}
	return "Telnet"
}

func (m *modelState) clearConnectionCredentials() {
	for key := range m.connectionPasswords {
		m.connectionPasswords[key] = ""
		delete(m.connectionPasswords, key)
	}
	m.connectionPasswords = nil
	m.passwordAttempts = nil
	m.authenticationHost = nil
}

func (m *modelState) clearConnectCancel() {
	if m.connectCancel != nil {
		m.connectCancel()
		m.connectCancel = nil
	}
}

func (m *modelState) cancelSSHConnect() {
	var target connectionTarget
	if m.connectionTarget != nil {
		target = *m.connectionTarget
	}
	if m.connectionTarget != nil && m.connectionTarget.layoutRestoreID != 0 {
		m.abortWorkspaceLayoutRestore("布局恢复已取消")
	}
	m.clearConnectCancel()
	m.connectingHost = nil
	m.trustPrompt = nil
	m.hostKeyChangePrompt = nil
	m.trustingHost = false
	m.replacingHostKey = false
	m.passwordPrompt = false
	m.passwordValue = ""
	m.passwordError = ""
	m.clearConnectionCredentials()
	m.keyboardAuthFlow = nil
	m.clearKeyboardAuthPrompt()
	m.connectionTarget = nil
	m.lastFTPPassword = ""
	if target.kind == sftpPeerTarget && m.sftp != nil && m.sftp.generation == target.sftpGeneration {
		m.sftp.busy = false
		m.sftp.status = "已取消" + sftpSideLabel(target.sftpSide) + "远程主机连接"
		m.sftp.statusErr = false
	}
}

func (m modelState) updateWorkspaceKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	code := key.Code
	if code >= 'A' && code <= 'Z' {
		code += 'a' - 'A'
	}
	if key.Code == tea.KeyEscape && m.paneMoveDrag != nil {
		m.paneMoveDrag = nil
		return m, nil
	}
	if m.workspaceLeaderPending {
		return m.updateWorkspaceLeader(msg)
	}
	if key.Mod&tea.ModCtrl != 0 && workspaceLeaderMatches(key, m.config.WithDefaults().Workspace) {
		if pane := m.activeTerminalPane(); pane != nil && pane.search != nil {
			pane.search = nil
			pane.scrollbackViewing = false
			pane.scrollbackOffset = 0
		}
		m.workspaceLeaderPending = true
		return m, nil
	}
	if pane := m.activeTerminalPane(); pane != nil && pane.search != nil {
		return m.updateTerminalSearchKey(msg)
	}
	if pane := m.activeTerminalPane(); pane != nil && pane.selection != nil && key.Code == tea.KeyEscape {
		pane.selection = nil
		return m, nil
	}
	if terminalClipboardCopyShortcut(key) {
		if pane := m.activeTerminalPane(); pane != nil && pane.selection != nil {
			return m, m.copyTerminalSelection(pane)
		}
	}
	if terminalClipboardPasteShortcut(key) {
		return m, m.beginTerminalClipboardPaste()
	}
	if pane := m.activeTerminalPane(); pane != nil && pane.scrollbackViewing {
		return m.updateWorkspaceScrollbackKey(msg)
	}
	if key.Mod&tea.ModCtrl != 0 {
		switch code {
		case ']':
			return m, m.closeActivePane()
		case 'o':
			m.workspaceVisible = false
			return m, nil
		}
	}
	pane := m.activeTerminalPane()
	if pane == nil || pane.session == nil {
		return m, nil
	}
	keyboard := m.config.Terminal.Keyboard
	m.writeWorkspaceInput(func(target *terminalPane) []byte {
		return terminalKeyBytesWithKeyboard(msg, target.applicationCursor, keyboard)
	}, "发送终端输入失败：")
	return m, nil
}

func (m *modelState) updateWorkspaceLeader(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.workspaceLeaderPending = false
	key := msg.Key()
	action := workspaceLeaderAction(key, m.config.WithDefaults().Workspace)
	switch {
	case key.Code == tea.KeyEscape:
		return m, nil
	case action == "split_vertical":
		return m, m.splitActivePane(splitVertical)
	case action == "split_horizontal":
		return m, m.splitActivePane(splitHorizontal)
	case action == "pane_grow":
		return m, tea.Batch(m.resizeActivePaneSplit(true)...)
	case action == "pane_shrink":
		return m, tea.Batch(m.resizeActivePaneSplit(false)...)
	case action == "pane_move_next":
		return m, tea.Batch(m.moveActivePaneToNext()...)
	case action == "screen_left":
		m.scrollActiveTerminalViewport(-1, 0)
	case action == "screen_right":
		m.scrollActiveTerminalViewport(1, 0)
	case action == "screen_up":
		m.scrollActiveTerminalViewport(0, -1)
	case action == "screen_down":
		m.scrollActiveTerminalViewport(0, 1)
	case action == "pane_previous":
		return m, tea.Batch(m.activatePane(-1)...)
	case action == "pane_next":
		return m, tea.Batch(m.activatePane(1)...)
	case action == "tab_next":
		return m, tea.Batch(m.activateTab(1)...)
	case action == "tab_previous":
		return m, tea.Batch(m.activateTab(-1)...)
	case strings.HasPrefix(action, "tab_") && action != "tab_next" && action != "tab_previous":
		index, err := strconv.Atoi(strings.TrimPrefix(action, "tab_"))
		if err == nil && index >= 1 && index <= 9 {
			return m, tea.Batch(m.activateTabIndex(index - 1)...)
		}
	case action == "close_pane":
		return m, m.closeActivePane()
	case action == "close_tab":
		return m, m.closeActiveTab()
	case action == "pane_menu":
		m.openActivePaneContextMenu()
	case action == "tab_menu":
		m.openActiveTabContextMenu()
	case action == "tab_groups":
		m.openWorkspaceTabGroupManager()
	case action == "sftp":
		return m, m.openActiveSFTP()
	case action == "screen_lock":
		return m, m.beginScreenLock(screenLockPurposeLock)
	case action == "screen_lock_password":
		return m, m.beginScreenLock(screenLockPurposeChange)
	case action == "quick_command":
		return m, m.openQuickCommandManager()
	case action == "scheduled_transfers":
		return m, m.openScheduledTransferManager()
	case action == "saved_layouts":
		return m, m.openWorkspaceLayoutManager()
	case action == "toggle_recording":
		return m, m.toggleActiveTerminalOutputLog()
	case action == "toggle_script_recording":
		m.toggleActiveTerminalScriptRecording()
	case action == "view_logs":
		return m, m.openTerminalLogViewer()
	case action == "plugins":
		m.openPluginPalette()
	case action == "compose":
		m.openComposePane()
	case action == "port_forward":
		if pane := m.activeTerminalPane(); pane != nil {
			return m, m.openPortForwardManager(pane.id)
		}
	case action == "reconnect":
		return m, m.reconnectActivePane()
	case action == "auto_reconnect":
		return m, m.toggleActivePaneAutoReconnect()
	case action == "sync_input":
		m.toggleWorkspaceSyncInput()
	case action == "scrollback":
		m.beginTerminalScrollback()
	case action == "search":
		beginTerminalSearch(m.activeTerminalPane())
	case action == "clear_screen":
		m.clearActiveTerminalScreen()
	case action == "command_history":
		m.openTerminalCommandHistory()
	case action == "send_ascii_file":
		m.openASCIIFileSend()
	case action == "xymodem":
		m.openXYMODEMPrompt()
	case action == "home":
		m.workspaceVisible = false
	case action == "help":
		m.workspaceHelp = true
		m.workspaceHelpTop = 0
	default:
		m.statusText = m.workspaceLeaderSummary()
		m.statusErr = false
	}
	return m, nil
}

func (m *modelState) splitActivePane(axis splitAxis) tea.Cmd {
	pane := m.activeTerminalPane()
	if pane == nil {
		return nil
	}
	target, err := m.splitTarget(axis)
	if err != nil {
		pane.statusText = err.Error()
		pane.statusErr = true
		return nil
	}
	if len(m.hosts) == 0 {
		pane.statusText = "主机列表为空，请先新增 SSH、Telnet、Rlogin 或本地终端"
		pane.statusErr = true
		return nil
	}
	index := 0
	for hostIndex, host := range m.hosts {
		if host.Id == pane.host.Id {
			index = hostIndex
			break
		}
	}
	terminalHosts := make([]model.Host, 0, len(m.hosts))
	for _, host := range m.hosts {
		if host.Kind == model.HostKindSSH || host.Kind == model.HostKindTelnet || host.Kind == model.HostKindLocal || host.Kind == model.HostKindSerial {
			terminalHosts = append(terminalHosts, host)
		}
	}
	if len(terminalHosts) == 0 {
		pane.statusText = "没有可用于终端分屏的 SSH、Telnet、Rlogin 或本地终端主机"
		pane.statusErr = true
		return nil
	}
	index = 0
	for hostIndex, host := range terminalHosts {
		if host.Id == pane.host.Id {
			index = hostIndex
			break
		}
	}
	m.hostPicker = &hostPickerState{target: target, hosts: terminalHosts, index: index}
	m.hostPicker.top = keepVisible(index, 0, m.hostPickerRows())
	return nil
}

func (m *modelState) updateHostPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	picker := m.hostPicker
	if picker == nil {
		return m, nil
	}
	switch msg.String() {
	case "esc", "escape", "ctrl+c":
		m.hostPicker = nil
		return m, nil
	case "up", "k":
		picker.index = clamp(picker.index-1, 0, len(picker.hosts)-1)
	case "down", "j":
		picker.index = clamp(picker.index+1, 0, len(picker.hosts)-1)
	case "enter":
		if len(picker.hosts) == 0 {
			return m, nil
		}
		host := picker.hosts[picker.index]
		target := picker.target
		m.hostPicker = nil
		return m, m.startHostConnect(host, target)
	}
	picker.top = keepVisible(picker.index, picker.top, m.hostPickerRows())
	return m, nil
}

func (m modelState) hostPickerRows() int {
	return max(1, min(10, m.height-14))
}

func privateModeState(sequence, mode string) (enabled, found bool) {
	for offset := 0; offset < len(sequence); {
		start := strings.Index(sequence[offset:], "\x1b[?")
		if start < 0 {
			return enabled, found
		}
		start += offset + len("\x1b[?")
		end := start
		for end < len(sequence) && sequence[end] != 'h' && sequence[end] != 'l' {
			end++
		}
		if end == len(sequence) {
			return enabled, found
		}
		if sequence[end] == 'h' || sequence[end] == 'l' {
			for _, parameter := range strings.Split(sequence[start:end], ";") {
				if parameter == mode {
					enabled = sequence[end] == 'h'
					found = true
					break
				}
			}
		}
		offset = end + 1
	}
	return enabled, found
}

func terminalKeyBytes(msg tea.KeyPressMsg, applicationCursor bool) []byte {
	return terminalKeyBytesWithKeyboard(msg, applicationCursor, model.DefaultTerminalKeyboardConfig())
}

func terminalKeyBytesWithKeyboard(msg tea.KeyPressMsg, applicationCursor bool, keyboard model.TerminalKeyboardConfig) []byte {
	backspaceSequence := keyboard.BackspaceSequence
	if backspaceSequence == "" {
		backspaceSequence = "\x7f"
	}
	deleteSequence := keyboard.DeleteSequence
	if deleteSequence == "" {
		deleteSequence = "\x1b[3~"
	}
	metaMode := strings.ToLower(strings.TrimSpace(keyboard.MetaKeyEmulation))
	if metaMode == "" {
		metaMode = model.TerminalMetaEscape
	}
	key := msg.Key()
	var data []byte
	if key.Mod&tea.ModCtrl != 0 {
		code := key.Code
		if code >= 'A' && code <= 'Z' {
			code += 'a' - 'A'
		}
		switch {
		case code >= 'a' && code <= 'z':
			data = []byte{byte(code - 'a' + 1)}
		case code == ' ' || code == '@':
			data = []byte{0}
		case code == '[':
			data = []byte{0x1b}
		case code == '\\':
			data = []byte{0x1c}
		case code == ']':
			data = []byte{0x1d}
		case code == '^':
			data = []byte{0x1e}
		case code == '_':
			data = []byte{0x1f}
		}
	} else if key.Text != "" {
		data = []byte(key.Text)
	} else {
		if key.Code == tea.KeyBackspace {
			data = []byte(backspaceSequence)
		} else if key.Code == tea.KeyDelete {
			data = []byte(deleteSequence)
		} else if name := terminalFunctionKeyName(key.Code); name != "" {
			if sequence, ok := terminalFunctionKeyOverride(keyboard.FunctionKeys, name); ok {
				data = []byte(sequence)
			} else {
				data = defaultTerminalFunctionKeyBytes(key.Code)
			}
		} else {
			switch key.Code {
			case tea.KeyEnter:
				data = []byte("\r")
			case tea.KeyTab:
				data = []byte("\t")
			case tea.KeyEscape:
				data = []byte{0x1b}
			case tea.KeyUp:
				data = arrowKeyBytes("A", applicationCursor)
			case tea.KeyDown:
				data = arrowKeyBytes("B", applicationCursor)
			case tea.KeyRight:
				data = arrowKeyBytes("C", applicationCursor)
			case tea.KeyLeft:
				data = arrowKeyBytes("D", applicationCursor)
			case tea.KeyInsert:
				data = []byte("\x1b[2~")
			case tea.KeyHome:
				data = []byte("\x1b[H")
			case tea.KeyEnd:
				data = []byte("\x1b[F")
			case tea.KeyPgUp:
				data = []byte("\x1b[5~")
			case tea.KeyPgDown:
				data = []byte("\x1b[6~")
			default:
				if terminalFunctionKeyName(key.Code) != "" {
					data = defaultTerminalFunctionKeyBytes(key.Code)
				}
			}
		}
	}
	if len(data) > 0 && key.Mod&tea.ModAlt != 0 && metaMode == model.TerminalMetaEscape {
		data = append([]byte{0x1b}, data...)
	}
	return data
}

func terminalFunctionKeyOverride(functionKeys map[string]string, name string) (string, bool) {
	if sequence, ok := functionKeys[name]; ok {
		return sequence, true
	}
	for configuredName, sequence := range functionKeys {
		if strings.EqualFold(strings.TrimSpace(configuredName), name) {
			return sequence, true
		}
	}
	return "", false
}

func terminalFunctionKeyName(code rune) string {
	functionKeys := [...]rune{
		tea.KeyF1, tea.KeyF2, tea.KeyF3, tea.KeyF4, tea.KeyF5,
		tea.KeyF6, tea.KeyF7, tea.KeyF8, tea.KeyF9, tea.KeyF10,
		tea.KeyF11, tea.KeyF12, tea.KeyF13, tea.KeyF14, tea.KeyF15,
		tea.KeyF16, tea.KeyF17, tea.KeyF18, tea.KeyF19, tea.KeyF20,
	}
	for index, functionKey := range functionKeys {
		if code == functionKey {
			return fmt.Sprintf("f%d", index+1)
		}
	}
	return ""
}

func defaultTerminalFunctionKeyBytes(code rune) []byte {
	switch code {
	case tea.KeyF1:
		return []byte("\x1bOP")
	case tea.KeyF2:
		return []byte("\x1bOQ")
	case tea.KeyF3:
		return []byte("\x1bOR")
	case tea.KeyF4:
		return []byte("\x1bOS")
	case tea.KeyF5:
		return []byte("\x1b[15~")
	case tea.KeyF6:
		return []byte("\x1b[17~")
	case tea.KeyF7:
		return []byte("\x1b[18~")
	case tea.KeyF8:
		return []byte("\x1b[19~")
	case tea.KeyF9:
		return []byte("\x1b[20~")
	case tea.KeyF10:
		return []byte("\x1b[21~")
	case tea.KeyF11:
		return []byte("\x1b[23~")
	case tea.KeyF12:
		return []byte("\x1b[24~")
	case tea.KeyF13:
		return []byte("\x1b[25~")
	case tea.KeyF14:
		return []byte("\x1b[26~")
	case tea.KeyF15:
		return []byte("\x1b[28~")
	case tea.KeyF16:
		return []byte("\x1b[29~")
	case tea.KeyF17:
		return []byte("\x1b[31~")
	case tea.KeyF18:
		return []byte("\x1b[32~")
	case tea.KeyF19:
		return []byte("\x1b[33~")
	case tea.KeyF20:
		return []byte("\x1b[34~")
	default:
		return nil
	}
}

func arrowKeyBytes(direction string, applicationCursor bool) []byte {
	if applicationCursor {
		return []byte("\x1bO" + direction)
	}
	return []byte("\x1b[" + direction)
}

func readTerminalOutputCmd(session terminalSession) tea.Cmd {
	return func() tea.Msg {
		buffer := make([]byte, 16*1024)
		for {
			n, err := session.Read(buffer)
			if n > 0 {
				return terminalOutputMsg{session: session, data: append([]byte(nil), buffer[:n]...), err: err}
			}
			if err != nil {
				return terminalOutputMsg{session: session, err: err}
			}
		}
	}
}

func resizeTerminalCmd(session terminalSession, width, height int) tea.Cmd {
	return func() tea.Msg {
		_ = session.Resize(width, height)
		return nil
	}
}

func (m modelState) updateFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.form.saving {
		return m, nil
	}
	key := msg.String()
	form := m.form
	if key == "ctrl+p" && form.kind == hostForm && form.hostKind == model.HostKindSerial && form.field == 2 {
		return m.requestSerialPorts()
	}
	if key == "ctrl+g" && form.kind == hostForm && form.hostKind == model.HostKindSSH && form.field == 5 {
		path := form.identityFile
		if path == "" {
			path = "~/.ssh/workbench_ed25519"
		}
		m.sshKeyGeneration = &sshKeyGenerationState{path: path, algorithm: "ed25519"}
		return m, nil
	}
	switch key {
	case "esc", "escape", "ctrl+c":
		m.form = nil
		return m, nil
	case "tab", "down":
		form.moveField(1)
		return m, nil
	case "shift+tab", "up":
		form.moveField(-1)
		return m, nil
	case "left", "right":
		if form.typeChoiceField() {
			delta := 1
			if key == "left" {
				delta = -1
			}
			form.cycleHostKind(delta)
			form.errorText = ""
		} else if form.tlsModeChoiceField() {
			delta := 1
			if key == "left" {
				delta = -1
			}
			form.cycleFTPTLSMode(delta)
			form.errorText = ""
		} else if form.ftpActiveModeChoiceField() {
			delta := 1
			if key == "left" {
				delta = -1
			}
			form.cycleFTPDataMode(delta)
			form.errorText = ""
		} else if form.x11ChoiceField() {
			delta := 1
			if key == "left" {
				delta = -1
			}
			form.cycleX11Forwarding(delta)
			form.errorText = ""
		} else if form.serialChoiceField() {
			delta := 1
			if key == "left" {
				delta = -1
			}
			form.cycleSerialSetting(delta)
			form.errorText = ""
		} else if form.groupChoiceField() {
			delta := 1
			if key == "left" {
				delta = -1
			}
			form.groupID = m.cycleGroupID(form.groupID, delta)
			form.errorText = ""
		} else if form.proxyJumpChoiceField() {
			delta := 1
			if key == "left" {
				delta = -1
			}
			form.proxyJumpID = m.cycleProxyJumpID(form.proxyJumpID, delta)
			form.errorText = ""
		} else if form.authProfileChoiceField() {
			delta := 1
			if key == "left" {
				delta = -1
			}
			form.authProfileID, form.authProfileName = m.cycleAuthProfile(form.authProfileID, delta)
			form.errorText = ""
		}
		return m, nil
	case "enter":
		if form.field < form.fieldCount()-1 {
			form.moveField(1)
			return m, nil
		}
		return m.submitForm()
	case "backspace", "ctrl+h":
		if !form.isChoiceField() {
			text := []rune(form.currentText())
			if len(text) > 0 {
				form.setCurrentText(string(text[:len(text)-1]))
			}
			form.errorText = ""
		}
		return m, nil
	}
	if form.isChoiceField() {
		return m, nil
	}
	text := msg.Key().Text
	if text == "" {
		return m, nil
	}
	current := form.currentText()
	if form.isNumericField() {
		maxLength := 7
		if form.isPortField() {
			maxLength = 5
		} else if form.field == 4 {
			maxLength = 1
		}
		if len(current)+len([]rune(text)) > maxLength || strings.Trim(text, "0123456789") != "" {
			return m, nil
		}
	}
	maxTextLength := 256
	if form.kind == hostForm && form.hostKind == model.HostKindSSH && form.field == 11 {
		maxTextLength = maxStartupCommandLength
	}
	if form.kind == hostForm && form.hostKind == model.HostKindSSH && form.field == 12 {
		maxTextLength = maxStartupScriptPathLength
	}
	if form.kind == groupForm && (form.field == 3 || form.field == 4) {
		maxTextLength = 1024
	}
	if form.kind == hostForm && ((form.hostKind == model.HostKindFTP && (form.field == 9 || form.field == 10)) || (form.hostKind == model.HostKindSSH && (form.field == 13 || form.field == 14))) {
		maxTextLength = 1024
	}
	if len([]rune(current))+len([]rune(text)) > maxTextLength {
		return m, nil
	}
	form.setCurrentText(current + text)
	form.errorText = ""
	return m, nil
}

func (m modelState) submitForm() (tea.Model, tea.Cmd) {
	form := m.form
	form.errorText = ""
	switch form.kind {
	case shortcutBarForm:
		bar := model.ShortcutBar{Id: form.id, Name: strings.TrimSpace(form.name)}
		if bar.Name == "" {
			form.errorText = "连接栏名称不能为空。"
			form.field = 0
			return m, nil
		}
		catalog, ok := m.catalog.(shortcutBarCatalog)
		if !ok {
			form.errorText = "当前主机目录不支持命名连接栏。"
			return m, nil
		}
		if form.editing {
			if existing := m.shortcutBarByID(form.id); existing != nil {
				bar.SortOrder, bar.HostIDs = existing.SortOrder, append([]string(nil), existing.HostIDs...)
			}
		}
		form.saving = true
		return m, func() tea.Msg {
			saved, err := catalog.SaveShortcutBar(context.Background(), bar)
			return shortcutBarSavedMsg{bar: saved, editing: form.editing, err: err}
		}
	case groupForm:
		group := model.Group{
			Id: form.id, Name: strings.TrimSpace(form.name), ParentId: form.groupID,
			AuthProfileID: form.authProfileID,
			LocalStartDir: strings.TrimSpace(form.localStartDir), RemoteStartDir: strings.TrimSpace(form.remoteStartDir),
		}
		if group.Name == "" {
			form.errorText = "分组名称不能为空。"
			form.field = 0
			return m, nil
		}
		if m.catalog == nil {
			form.errorText = "本地存储不可用。"
			return m, nil
		}
		form.saving = true
		catalog := m.catalog
		if form.editing {
			return m, func() tea.Msg {
				err := catalog.UpdateGroup(context.Background(), group)
				return groupUpdatedMsg{group: group, err: err}
			}
		}
		return m, func() tea.Msg {
			group, err := catalog.CreateGroup(context.Background(), group)
			return groupCreatedMsg{group: group, err: err}
		}
	case hostForm:
		name := strings.TrimSpace(form.name)
		if name == "" {
			form.errorText = "主机名称不能为空。"
			form.field = 0
			return m, nil
		}
		port := 0
		serialBaudRate, serialDataBits := 0, 0
		if form.hostKind == model.HostKindSerial {
			if strings.TrimSpace(form.address) == "" {
				form.errorText = "串口设备路径不能为空。"
				form.field = 2
				return m, nil
			}
			var err error
			serialBaudRate, err = strconv.Atoi(strings.TrimSpace(form.serialBaudRate))
			if err != nil || serialBaudRate < 50 || serialBaudRate > 4_000_000 {
				form.errorText = "波特率必须在 50 到 4000000 之间。"
				form.field = 3
				return m, nil
			}
			serialDataBits, err = strconv.Atoi(strings.TrimSpace(form.serialDataBits))
			if err != nil || serialDataBits < 5 || serialDataBits > 8 {
				form.errorText = "数据位必须是 5、6、7 或 8。"
				form.field = 4
				return m, nil
			}
		} else if form.hostKind == model.HostKindSSH || form.hostKind == model.HostKindFTP || form.hostKind == model.HostKindTelnet {
			if strings.TrimSpace(form.address) == "" {
				form.errorText = "主机地址不能为空。"
				form.field = 2
				return m, nil
			}
			var err error
			port, err = strconv.Atoi(strings.TrimSpace(form.port))
			if err != nil || port < 1 || port > 65535 {
				form.errorText = "端口必须是 1 到 65535 之间的数字。"
				form.field = 3
				return m, nil
			}
		}
		if m.catalog == nil {
			form.errorText = "本地存储不可用。"
			return m, nil
		}
		startup := strings.TrimSpace(form.startup)
		if form.hostKind == model.HostKindSSH && (strings.ContainsAny(startup, "\r\n") || len([]rune(startup)) > maxStartupCommandLength) {
			form.errorText = "连接后命令必须是单行，且不超过 1024 个字符。"
			form.field = 11
			return m, nil
		}
		host := model.Host{
			Id:                form.id,
			Kind:              form.hostKind,
			Name:              name,
			IP:                strings.TrimSpace(form.address),
			Port:              port,
			User:              strings.TrimSpace(form.username),
			TLSMode:           form.tlsMode,
			FTPActiveMode:     form.hostKind == model.HostKindFTP && form.ftpActiveMode,
			TerminalProtocol:  form.terminalProtocol,
			X11Forwarding:     form.hostKind == model.HostKindSSH && form.x11Forwarding,
			SerialBaudRate:    serialBaudRate,
			SerialDataBits:    serialDataBits,
			SerialParity:      form.serialParity,
			SerialStopBits:    form.serialStopBits,
			SerialFlowControl: form.serialFlowControl,
			GroupId:           form.groupID,
			Description:       strings.TrimSpace(form.description),
			LocalStartDir:     strings.TrimSpace(form.localStartDir),
			RemoteStartDir:    strings.TrimSpace(form.remoteStartDir),
		}
		if form.hostKind == model.HostKindSSH {
			host.IdentityFile = strings.TrimSpace(form.identityFile)
			host.AuthProfileID = form.authProfileID
			host.Startup = startup
			host.StartupScriptPath = strings.TrimSpace(form.startupScriptPath)
			if host.Startup != "" && host.StartupScriptPath != "" {
				form.errorText = "连接后命令与提示符登录脚本只能配置一种。"
				form.field = 12
				return m, nil
			}
		}
		if form.hostKind == model.HostKindSSH && form.proxyJumpID != "" {
			jumpHost := m.hostByID(form.proxyJumpID)
			if jumpHost == nil || jumpHost.Kind != model.HostKindSSH {
				form.errorText = "所选跳板机不可用，请重新选择。"
				form.field = 6
				return m, nil
			}
			if jumpHost.Id == form.id || m.proxyJumpCreatesCycle(form.id, jumpHost.Id) {
				form.errorText = "跳板机配置不能形成循环引用。"
				form.field = 6
				return m, nil
			}
			host.ProxyJumpList = []string{jumpHost.Id}
		}
		if form.editing {
			if existing := m.hostByID(form.id); existing != nil {
				host.CredentialRef = existing.CredentialRef
				host.Favorite = existing.Favorite
				host.LastConnectedAt = existing.LastConnectedAt
				host.TerminalEncoding = existing.TerminalEncoding
			}
		}
		form.saving = true
		catalog := m.catalog
		if form.editing {
			return m, func() tea.Msg {
				err := catalog.UpdateHost(context.Background(), host)
				return hostUpdatedMsg{host: host, err: err}
			}
		}
		return m, func() tea.Msg {
			host, err := catalog.CreateHost(context.Background(), host)
			return hostCreatedMsg{host: host, err: err}
		}
	}
	return m, nil
}

func (m modelState) cycleProxyJumpID(current string, delta int) string {
	choices := []string{""}
	for _, host := range m.hosts {
		if host.Kind != model.HostKindSSH || host.Id == "" || (m.form != nil && host.Id == m.form.id) {
			continue
		}
		if m.form != nil && m.proxyJumpCreatesCycle(m.form.id, host.Id) {
			continue
		}
		choices = append(choices, host.Id)
	}
	index := 0
	for candidateIndex, id := range choices {
		if id == current {
			index = candidateIndex
			break
		}
	}
	return choices[(index+delta+len(choices))%len(choices)]
}

func (m modelState) cycleAuthProfile(current string, delta int) (string, string) {
	type choice struct{ id, name string }
	choices := []choice{{name: ""}}
	for _, profile := range m.authProfiles {
		choices = append(choices, choice{id: profile.Id, name: profile.Name})
	}
	index := 0
	for candidate := range choices {
		if choices[candidate].id == current {
			index = candidate
			break
		}
	}
	index = (index + delta + len(choices)) % len(choices)
	return choices[index].id, choices[index].name
}

func (m modelState) proxyJumpCreatesCycle(targetID, jumpID string) bool {
	if targetID == "" || jumpID == "" {
		return false
	}
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var reachesTarget func(string) bool
	reachesTarget = func(id string) bool {
		if id == targetID {
			return true
		}
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visited[id] = true
		host := m.hostByID(id)
		if host == nil || host.Kind != model.HostKindSSH {
			return false
		}
		visiting[id] = true
		for _, parentID := range host.ProxyJumpList {
			if reachesTarget(parentID) {
				return true
			}
		}
		delete(visiting, id)
		return false
	}
	return reachesTarget(jumpID)
}

func (m modelState) cycleGroupID(current string, delta int) string {
	choices := make([]string, 1, len(m.groups)+1)
	choices[0] = ""
	excluded := map[string]bool{}
	if m.form != nil && m.form.kind == groupForm && m.form.editing {
		excluded = descendantGroupIDs(m.groups, m.form.id)
	}
	for _, entry := range buildGroupEntries(m.groups, nil) {
		if !entry.isAll && !entry.isFavoriteView && !entry.isRecentView && !excluded[entry.id] {
			choices = append(choices, entry.id)
		}
	}
	index := 0
	for i, id := range choices {
		if id == current {
			index = i
			break
		}
	}
	index = (index + delta + len(choices)) % len(choices)
	return choices[index]
}

func (m *modelState) updateCatalogDeleteConfirm(key string) (tea.Model, tea.Cmd) {
	confirm := m.deleteConfirm
	switch key {
	case "esc", "escape", "n", "q", "ctrl+c":
		m.deleteConfirm = nil
		return m, nil
	case "enter", "y":
		if confirm.deleting {
			return m, nil
		}
		if m.catalog == nil {
			m.deleteConfirm = nil
			m.statusText = "本地存储不可用"
			m.statusErr = true
			return m, nil
		}
		confirm.deleting = true
		catalog := m.catalog
		vault := m.credentials
		return m, func() tea.Msg {
			var err error
			var cleanupErr error
			if confirm.kind == deleteGroup {
				err = catalog.DeleteGroup(context.Background(), confirm.id)
			} else if confirm.kind == deleteShortcutBar {
				shortcutBars, ok := catalog.(shortcutBarCatalog)
				if !ok {
					err = errors.New("当前主机目录不支持命名连接栏")
				} else {
					err = shortcutBars.DeleteShortcutBar(context.Background(), confirm.id)
				}
			} else {
				err = catalog.DeleteHost(context.Background(), confirm.id)
				if err == nil && confirm.credentialRef != "" {
					if vault == nil {
						cleanupErr = errors.New("系统钥匙串不可用")
					} else {
						cleanupErr = vault.Delete(confirm.credentialRef)
					}
				}
			}
			return catalogDeletedMsg{kind: confirm.kind, id: confirm.id, err: err, cleanupErr: cleanupErr}
		}
	}
	return m, nil
}

func (m modelState) groupByID(id string) *model.Group {
	for index := range m.groups {
		if m.groups[index].Id == id {
			return &m.groups[index]
		}
	}
	return nil
}

func (m modelState) hostByID(id string) *model.Host {
	for index := range m.hosts {
		if m.hosts[index].Id == id {
			return &m.hosts[index]
		}
	}
	return nil
}

func (m modelState) groupIndexForID(id string) int {
	for index, entry := range m.groupEntries() {
		if entry.id == id && !entry.isAll {
			return index
		}
	}
	return 0
}

func (m modelState) groupEntries() []groupEntry {
	return buildGroupEntries(m.groups, m.hosts, m.shortcutBars)
}

func (m modelState) shortcutBarByID(id string) *model.ShortcutBar {
	for index := range m.shortcutBars {
		if m.shortcutBars[index].Id == id {
			return &m.shortcutBars[index]
		}
	}
	return nil
}

func (m *modelState) moveSelection(delta int) {
	if m.activePane == groupPane {
		entries := m.groupEntries()
		m.groupIndex = clamp(m.groupIndex+delta, 0, len(entries)-1)
		m.groupTop = keepVisible(m.groupIndex, m.groupTop, m.listRows())
		m.hostIndex, m.hostTop = 0, 0
		m.ensureHostSelection()
		return
	}
	hosts := m.visibleHosts()
	m.hostIndex = clamp(m.hostIndex+delta, 0, len(hosts)-1)
	m.hostTop = keepVisible(m.hostIndex, m.hostTop, m.hostRows())
}

func (m *modelState) ensureGroupSelection() {
	entries := m.groupEntries()
	m.groupIndex = clamp(m.groupIndex, 0, len(entries)-1)
	m.groupTop = keepVisible(m.groupIndex, m.groupTop, m.listRows())
}

func (m *modelState) ensureHostSelection() {
	hosts := m.visibleHosts()
	m.hostIndex = clamp(m.hostIndex, 0, len(hosts)-1)
	m.hostTop = keepVisible(m.hostIndex, m.hostTop, m.hostRows())
}

func (m modelState) visibleHosts() []model.Host {
	entries := m.groupEntries()
	if len(entries) == 0 {
		return nil
	}
	selected := entries[clamp(m.groupIndex, 0, len(entries)-1)]
	allowedGroups := map[string]bool(nil)
	barOrder := map[string]int(nil)
	if selected.isShortcutBar {
		barOrder = make(map[string]int)
		if bar := m.shortcutBarByID(selected.id); bar != nil {
			for index, hostID := range bar.HostIDs {
				barOrder[hostID] = index
			}
		}
	} else if !selected.isAll && !selected.isFavoriteView && !selected.isRecentView {
		allowedGroups = descendantGroupIDs(m.groups, selected.id)
	}
	query := strings.ToLower(strings.TrimSpace(m.query))
	visible := make([]model.Host, 0, len(m.hosts))
	for _, host := range m.hosts {
		if selected.isShortcutBar {
			if _, member := barOrder[host.Id]; !member {
				continue
			}
		}
		if selected.isFavoriteView && !host.Favorite {
			continue
		}
		if selected.isRecentView && host.LastConnectedAt.IsZero() {
			continue
		}
		if allowedGroups != nil && !allowedGroups[host.GroupId] {
			continue
		}
		if query != "" {
			searchable := strings.ToLower(strings.Join([]string{
				host.Name, host.IP, host.User, host.Description,
			}, " "))
			if !strings.Contains(searchable, query) {
				continue
			}
		}
		visible = append(visible, host)
	}
	if selected.isRecentView {
		sort.SliceStable(visible, func(i, j int) bool {
			if visible[i].LastConnectedAt.Equal(visible[j].LastConnectedAt) {
				return strings.ToLower(visible[i].Name) < strings.ToLower(visible[j].Name)
			}
			return visible[i].LastConnectedAt.After(visible[j].LastConnectedAt)
		})
	} else if selected.isFavoriteView {
		sort.SliceStable(visible, func(i, j int) bool { return favoriteHostLess(visible[i], visible[j]) })
	} else if selected.isShortcutBar {
		sort.SliceStable(visible, func(i, j int) bool { return barOrder[visible[i].Id] < barOrder[visible[j].Id] })
	}
	return visible
}

func (m modelState) selectedHost() *model.Host {
	hosts := m.visibleHosts()
	if len(hosts) == 0 || m.hostIndex < 0 || m.hostIndex >= len(hosts) {
		return nil
	}
	host := hosts[m.hostIndex]
	return &host
}

func (m modelState) selectedGroup() groupEntry {
	entries := m.groupEntries()
	if len(entries) == 0 {
		return groupEntry{isAll: true, name: "全部主机"}
	}
	return entries[clamp(m.groupIndex, 0, len(entries)-1)]
}

func (m modelState) listRows() int {
	return max(1, m.height-11)
}

func (m modelState) hostRows() int {
	return max(1, m.height-12)
}

func keepVisible(index, top, rows int) int {
	if rows <= 0 {
		return 0
	}
	if index < top {
		return index
	}
	if index >= top+rows {
		return index - rows + 1
	}
	return top
}

func clamp(value, low, high int) int {
	if high < low {
		return low
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
