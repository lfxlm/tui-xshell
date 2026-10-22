package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"tui-ssh-workbench/internal/infra/ftpclient"
	"tui-ssh-workbench/internal/infra/remotefs"
	"tui-ssh-workbench/internal/infra/sftpclient"
	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/model"
)

func TestOpenActiveSFTPUnwrapsTrackedSSHSession(t *testing.T) {
	sshSession := &sshclient.Session{}
	pane := &terminalPane{
		id:      "pane-1",
		host:    model.Host{Id: "host-1", Kind: model.HostKindSSH, Name: "build"},
		session: trackTerminalSession(sshSession),
	}
	tab := &workspaceTab{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}
	state := modelState{workspaceVisible: true, activeTabID: tab.id, tabs: []*workspaceTab{tab}}

	command := state.openActiveSFTP()
	if command == nil || state.sftp == nil || state.sftp.session != sshSession {
		t.Fatalf("opening SFTP did not reuse the wrapped SSH session: browser=%#v command=%v", state.sftp, command != nil)
	}

	state.contextMenu = &contextMenuState{target: paneMenuTarget, tabID: tab.id, paneID: pane.id}
	foundSFTPAction := false
	for _, item := range state.contextMenuItems() {
		if item.action == menuOpenSFTP {
			foundSFTPAction = true
			if item.disabled {
				t.Fatal("pane context menu disabled SFTP for a tracked SSH session")
			}
		}
	}
	if !foundSFTPAction {
		t.Fatal("pane context menu omitted SFTP for a tracked SSH session")
	}
}

type sftpNavigationFakeClient struct {
	directories map[string][]sftpclient.Entry
}

func (client *sftpNavigationFakeClient) Close() error                      { return nil }
func (client *sftpNavigationFakeClient) WorkingDirectory() (string, error) { return ".", nil }
func (client *sftpNavigationFakeClient) RealPath(remotePath string) (string, error) {
	return remotePath, nil
}
func (client *sftpNavigationFakeClient) List(remotePath string) ([]remotefs.Entry, error) {
	entries, ok := client.directories[remotePath]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]sftpclient.Entry(nil), entries...), nil
}
func (client *sftpNavigationFakeClient) Search(context.Context, string, string, int) ([]remotefs.SearchMatch, bool, error) {
	return nil, false, errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) Exists(string) (bool, error) {
	return false, errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) MakeDirectory(string) error {
	return errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) Rename(string, string) error {
	return errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) SetPermissions(string, os.FileMode) error {
	return errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) Remove(string, bool) error {
	return errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) RemoveTree(string) error {
	return errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) RemoveTreeContext(context.Context, string) error {
	return errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) Upload(context.Context, string, string, bool, func(int64, int64)) error {
	return errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) Download(context.Context, string, string, bool, func(int64, int64)) error {
	return errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) CopyRemoteFileTo(context.Context, remotefs.Client, string, string, bool, func(int64, int64)) error {
	return errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) CopyRemoteDirectoryTo(context.Context, remotefs.Client, string, string, bool, func(int64, int64)) error {
	return errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) ReadTextFile(string, int64) ([]byte, remotefs.FileVersion, error) {
	return nil, remotefs.FileVersion{}, errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) SaveTextFile(context.Context, string, []byte, remotefs.FileVersion, func(int64, int64)) (remotefs.FileVersion, error) {
	return remotefs.FileVersion{}, errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) UploadDirectory(context.Context, string, string, bool, func(int64, int64)) error {
	return errors.New("not implemented")
}
func (client *sftpNavigationFakeClient) DownloadDirectory(context.Context, string, string, bool, func(int64, int64)) error {
	return errors.New("not implemented")
}

func TestStandaloneSFTPViewRendersBothDirectories(t *testing.T) {
	state := modelState{
		width:  100,
		height: 14,
		sftp: &sftpBrowserState{
			standalone: true,
			host:       model.Host{Name: "staging"},
			localPath:  "/tmp/local-files",
			path:       "/srv/remote-files",
			localEntries: []sftpclient.Entry{
				{Name: "local.txt", Mode: 0o644},
			},
			entries: []sftpclient.Entry{
				{Name: "remote.txt", Mode: 0o644},
			},
		},
	}

	view := state.sftpView()
	for _, expected := range []string{"本地", "staging", "/tmp/local-files", "/srv/remote-files", "local.txt", "remote.txt"} {
		if !strings.Contains(view, expected) {
			t.Errorf("standalone SFTP view does not contain %q:\n%s", expected, view)
		}
	}
	if lines := strings.Split(view, "\n"); len(lines) != state.height {
		t.Fatalf("view has %d rows, want %d", len(lines), state.height)
	}
}

func TestSinglePaneSFTPUsesTheSameHeaderAsFileRows(t *testing.T) {
	const width = 120
	state := modelState{
		width: width, height: 10,
		sftp: &sftpBrowserState{host: model.Host{Name: "staging"}},
	}
	lines := strings.Split(state.sftpView(), "\n")
	if len(lines) <= 3 {
		t.Fatalf("SFTP view has no list header: %q", lines)
	}
	if got, want := lines[3], formatSFTPListHeader(width); got != want {
		t.Fatalf("single-pane SFTP header differs from the shared column layout:\n got: %q\nwant: %q", got, want)
	}
}

func TestSFTPTableColumnsStayAlignedAcrossDifferentFileNames(t *testing.T) {
	const width = 100
	modified := time.Date(2026, time.October, 21, 15, 33, 0, 0, time.Local)
	entries := []sftpclient.Entry{
		{Name: "a", Size: 128, Mode: 0o644, ModeKnown: true, ModTime: modified},
		{Name: "很长的中文文件名.md", Size: 4096, Mode: 0o644, ModeKnown: true, ModTime: modified},
	}
	dateLabel := modified.Format("01-02 15:04")
	header := ansi.Strip(formatSFTPListHeader(width))
	headerDateIndex := strings.Index(header, "修改时间")
	if headerDateIndex < 0 {
		t.Fatalf("table header is missing the modified-time column: %q", header)
	}
	headerDateColumn := lipgloss.Width(header[:headerDateIndex])
	for _, entry := range entries {
		row := ansi.Strip(formatSFTPEntry(entry, false, false, width))
		dateIndex := strings.Index(row, dateLabel)
		if dateIndex < 0 {
			t.Fatalf("row is missing its modified-time value: %q", row)
		}
		dateColumn := lipgloss.Width(row[:dateIndex])
		if dateColumn != headerDateColumn {
			t.Errorf("modified-time column for %q starts at %d, header starts at %d:\nrow:    %q\nheader: %q", entry.Name, dateColumn, headerDateColumn, row, header)
		}
	}
}

func TestStandaloneSFTPKeyboardSwitchesDirectorySide(t *testing.T) {
	state := modelState{sftp: &sftpBrowserState{standalone: true, activeSide: sftpLocalSide}}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	state = unwrapModel(t, updated)
	if state.sftp.activeSide != sftpRemoteSide {
		t.Fatal("Tab did not move SFTP focus to the remote side")
	}

	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	state = unwrapModel(t, updated)
	if state.sftp.activeSide != sftpLocalSide {
		t.Fatal("Left did not move SFTP focus to the local side")
	}
}

func TestStandaloneFTPCommandShortcutOpensPromptOnlyForFTP(t *testing.T) {
	state := modelState{sftp: &sftpBrowserState{
		standalone: true, activeSide: sftpRemoteSide, client: &ftpclient.Client{},
	}}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: ":", Code: ':'}))
	state = unwrapModel(t, updated)
	if state.sftp.prompt == nil || state.sftp.prompt.action != sftpPromptFTPCommand {
		t.Fatalf(": did not open the FTP command prompt: %#v", state.sftp.prompt)
	}
	if state.sftp.prompt.transfer == nil || state.sftp.prompt.transfer.side != sftpRemoteSide {
		t.Fatalf("FTP command prompt did not capture the active pane: %#v", state.sftp.prompt.transfer)
	}

	state = modelState{sftp: &sftpBrowserState{
		standalone: true, activeSide: sftpRemoteSide, client: &sftpNavigationFakeClient{},
	}}
	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: ":", Code: ':'}))
	state = unwrapModel(t, updated)
	if state.sftp.prompt != nil || !state.sftp.statusErr {
		t.Fatalf(": should reject a non-FTP connection, prompt=%#v status=%q", state.sftp.prompt, state.sftp.status)
	}
}

func TestStandaloneFTPCommandHelpOnlyAppearsForFTPConnection(t *testing.T) {
	ftpState := modelState{width: 120, height: 12, sftp: &sftpBrowserState{
		standalone: true, activeSide: sftpRemoteSide, client: &ftpclient.Client{},
	}}
	if view := ftpState.sftpView(); !strings.Contains(view, ": FTP 命令") {
		t.Fatalf("FTP command shortcut is missing from the FTP pane help:\n%s", view)
	}

	sftpState := modelState{width: 120, height: 12, sftp: &sftpBrowserState{
		standalone: true, activeSide: sftpRemoteSide, client: &sftpNavigationFakeClient{},
	}}
	if view := sftpState.sftpView(); strings.Contains(view, ": FTP 命令") {
		t.Fatalf("FTP command shortcut should not be shown for SFTP:\n%s", view)
	}
}

func TestSFTPGoToPathPromptNavigatesRelativeRemotePath(t *testing.T) {
	client := &sftpNavigationFakeClient{directories: map[string][]sftpclient.Entry{
		"/srv":          {{Name: "releases", IsDir: true, Mode: os.ModeDir | 0o755}},
		"/srv/releases": {{Name: "app.tar.gz", Mode: 0o644}},
	}}
	state := modelState{width: 100, height: 12, sftp: &sftpBrowserState{
		standalone: true, activeSide: sftpRemoteSide, client: client, path: "/srv",
		entries: []sftpclient.Entry{{Name: "releases", IsDir: true, Mode: os.ModeDir | 0o755}},
	}}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'}))
	state = unwrapModel(t, updated)
	if state.sftp.prompt == nil || state.sftp.prompt.action != sftpPromptGoToPath || state.sftp.prompt.transfer.side != sftpRemoteSide {
		t.Fatalf("g did not open a path prompt for the active remote pane: %#v", state.sftp.prompt)
	}
	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "releases"}))
	state = unwrapModel(t, updated)
	updated, command := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if command == nil || !state.sftp.busy {
		t.Fatal("Enter did not start listing the requested path")
	}
	rawMessage := command()
	message, ok := rawMessage.(sftpListedMsg)
	if !ok {
		t.Fatalf("path navigation returned %T, want sftpListedMsg", rawMessage)
	}
	state.handleSFTPListed(message)
	if state.sftp.path != "/srv/releases" || len(state.sftp.entries) != 1 || state.sftp.entries[0].Name != "app.tar.gz" || state.sftp.busy {
		t.Fatalf("relative path navigation state = path %q entries %#v busy=%v", state.sftp.path, state.sftp.entries, state.sftp.busy)
	}
}

func TestResolveSFTPPathValidatesAndResolvesBothPathKinds(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		current string
		input   string
		remote  bool
		want    string
		wantErr bool
	}{
		{name: "remote relative", current: "/srv/app", input: "../releases", remote: true, want: "/srv/releases"},
		{name: "remote absolute", current: "/srv/app", input: "/var/log", remote: true, want: "/var/log"},
		{name: "local relative", current: filepath.Join(string(filepath.Separator), "tmp", "work"), input: "../logs", want: filepath.Join(string(filepath.Separator), "tmp", "logs")},
		{name: "empty", current: "/srv", input: " ", remote: true, wantErr: true},
		{name: "control character", current: "/srv", input: "bad\npath", remote: true, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := resolveSFTPPath(testCase.current, testCase.input, testCase.remote)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("resolveSFTPPath() error = %v, wantErr %v", err, testCase.wantErr)
			}
			if !testCase.wantErr && got != testCase.want {
				t.Fatalf("resolveSFTPPath() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestFTPCommandResultViewScrollsReply(t *testing.T) {
	replies := make([]string, 12)
	for index := range replies {
		replies[index] = fmt.Sprintf("reply line %02d", index)
	}
	state := modelState{width: 64, height: 10, sftp: &sftpBrowserState{
		ftpCommand: &ftpCommandOutput{command: "SYST", code: 215, lines: replies},
	}}
	view := state.sftpView()
	if !strings.Contains(view, "FTP 原始命令回复") || !strings.Contains(view, "服务器响应  215") || !strings.Contains(view, "reply line 00") {
		t.Fatalf("FTP command view is missing the command response:\n%s", view)
	}
	if lines := strings.Split(view, "\n"); len(lines) != state.height {
		t.Fatalf("FTP command view has %d rows, want %d", len(lines), state.height)
	}

	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	state = unwrapModel(t, updated)
	if state.sftp.ftpCommand.top != 1 {
		t.Fatalf("Down moved reply offset to %d, want 1", state.sftp.ftpCommand.top)
	}
	view = state.sftpView()
	if !strings.Contains(view, "reply line 01") || strings.Contains(view, "reply line 00") {
		t.Fatalf("Down did not scroll the FTP reply:\n%s", view)
	}
}

func TestSFTPSynchronizedBrowsingNavigatesBothRemotePanes(t *testing.T) {
	nested := sftpclient.Entry{Name: "config", IsDir: true, Mode: os.ModeDir | 0o755, ModeKnown: true}
	left := &sftpNavigationFakeClient{directories: map[string][]sftpclient.Entry{
		"/home/alice":        {nested},
		"/home/alice/config": {{Name: "left.yaml", Mode: 0o644, ModeKnown: true}},
	}}
	right := &sftpNavigationFakeClient{directories: map[string][]sftpclient.Entry{
		"/srv/work":        {nested},
		"/srv/work/config": {{Name: "right.yaml", Mode: 0o600, ModeKnown: true}},
	}}
	state := modelState{width: 100, height: 12, sftp: &sftpBrowserState{
		standalone: true, activeSide: sftpLocalSide,
		peerClient: left, peerPath: "/home/alice", localEntries: []sftpclient.Entry{nested},
		client: right, path: "/srv/work", entries: []sftpclient.Entry{nested},
	}}

	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	state = unwrapModel(t, updated)
	if !state.sftp.synchronizedBrowsing || state.sftp.synchronizedLocalRoot != "/home/alice" || state.sftp.synchronizedRemoteRoot != "/srv/work" {
		t.Fatalf("synchronized browsing did not capture both starting directories: %#v", state.sftp)
	}
	updated, command := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if command == nil || !state.sftp.busy {
		t.Fatal("enter did not start a synchronized directory change")
	}
	message, ok := command().(sftpSynchronizedListedMsg)
	if !ok {
		t.Fatalf("synchronized navigation returned %T", command())
	}
	state.handleSFTPSynchronizedListed(message)
	if state.sftp.busy || state.sftp.peerPath != "/home/alice/config" || state.sftp.path != "/srv/work/config" {
		t.Fatalf("navigation did not update corresponding directories atomically: local=%q remote=%q busy=%v", state.sftp.peerPath, state.sftp.path, state.sftp.busy)
	}
	if len(state.sftp.localEntries) != 1 || state.sftp.localEntries[0].Name != "left.yaml" || len(state.sftp.entries) != 1 || state.sftp.entries[0].Name != "right.yaml" {
		t.Fatalf("navigation did not refresh both panes: local=%#v remote=%#v", state.sftp.localEntries, state.sftp.entries)
	}
	if view := state.sftpView(); !strings.Contains(view, "同步浏览") {
		t.Fatalf("view does not show synchronized browsing is active:\n%s", view)
	}

	updated, command = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("Backspace did not navigate both panes to their roots")
	}
	message, ok = command().(sftpSynchronizedListedMsg)
	if !ok {
		t.Fatalf("parent navigation returned %T", command())
	}
	state.handleSFTPSynchronizedListed(message)
	if state.sftp.peerPath != "/home/alice" || state.sftp.path != "/srv/work" {
		t.Fatalf("parent navigation did not restore starting directories: local=%q remote=%q", state.sftp.peerPath, state.sftp.path)
	}

	updated, command = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	state = unwrapModel(t, updated)
	if command != nil || state.sftp.busy || !strings.Contains(state.sftp.status, "起始目录") {
		t.Fatal("synchronized browsing allowed navigation above its starting directories")
	}
	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	state = unwrapModel(t, updated)
	if state.sftp.synchronizedBrowsing {
		t.Fatal("y did not turn synchronized browsing off")
	}
}

func TestSFTPSynchronizedBrowsingLeavesBothPanesUnchangedOnPeerError(t *testing.T) {
	nested := sftpclient.Entry{Name: "missing-on-right", IsDir: true, Mode: os.ModeDir | 0o755, ModeKnown: true}
	left := &sftpNavigationFakeClient{directories: map[string][]sftpclient.Entry{
		"/home/alice":                  {nested},
		"/home/alice/missing-on-right": {},
	}}
	right := &sftpNavigationFakeClient{directories: map[string][]sftpclient.Entry{
		"/srv/work": {nested},
	}}
	state := modelState{width: 100, height: 12, sftp: &sftpBrowserState{
		standalone: true, activeSide: sftpLocalSide,
		peerClient: left, peerPath: "/home/alice", localEntries: []sftpclient.Entry{nested},
		client: right, path: "/srv/work", entries: []sftpclient.Entry{nested},
	}}
	state.toggleSFTPSynchronizedBrowsing()
	command := state.enterSFTPSelection()
	if command == nil {
		t.Fatal("enter did not start synchronized navigation")
	}
	message, ok := command().(sftpSynchronizedListedMsg)
	if !ok || message.err == nil {
		t.Fatalf("missing peer directory should fail atomically, got %#v", message)
	}
	state.handleSFTPSynchronizedListed(message)
	if state.sftp.peerPath != "/home/alice" || state.sftp.path != "/srv/work" || !state.sftp.statusErr {
		t.Fatalf("failed navigation changed paths or hid its error: local=%q remote=%q status=%q", state.sftp.peerPath, state.sftp.path, state.sftp.status)
	}
}

func TestSFTPRelativeRemotePathRejectsLeavingMappingRoot(t *testing.T) {
	for _, test := range []struct {
		root, target, want string
		ok                 bool
	}{
		{root: "/srv", target: "/srv", want: ".", ok: true},
		{root: "/srv", target: "/srv/project/src", want: "project/src", ok: true},
		{root: "/", target: "/etc/ssh", want: "etc/ssh", ok: true},
		{root: ".", target: "project/src", want: "project/src", ok: true},
		{root: "/srv", target: "/srv-old/project", ok: false},
		{root: "/srv", target: "/etc", ok: false},
		{root: ".", target: "../outside", ok: false},
	} {
		got, ok := sftpRelativeRemotePath(test.root, test.target)
		if got != test.want || ok != test.ok {
			t.Errorf("sftpRelativeRemotePath(%q, %q) = (%q, %v); want (%q, %v)", test.root, test.target, got, ok, test.want, test.ok)
		}
	}
}

func TestSFTPTransferSpacePausesAndResumes(t *testing.T) {
	gate := sftpclient.NewTransferPauseGate()
	state := modelState{width: 80, height: 12, sftp: &sftpBrowserState{
		standalone: true, busy: true, status: "正在上传 archive.tar",
		transferPauseGate: gate,
	}}

	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))
	state = unwrapModel(t, updated)
	if !state.sftp.transferPaused || !gate.IsPaused() {
		t.Fatal("Space did not pause the active SFTP transfer")
	}
	if view := state.sftpView(); !strings.Contains(view, "已暂停") || !strings.Contains(view, "Space 暂停/恢复传输") {
		t.Fatalf("paused SFTP transfer state is not visible:\n%s", view)
	}

	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))
	state = unwrapModel(t, updated)
	if state.sftp.transferPaused || gate.IsPaused() {
		t.Fatal("Space did not resume the active SFTP transfer")
	}
}

func TestSFTPFileSelectionShowsCurrentActions(t *testing.T) {
	state := modelState{sftp: &sftpBrowserState{
		standalone: true, activeSide: sftpRemoteSide,
		entries: []sftpclient.Entry{{Name: "config.yaml", Mode: 0o644}},
	}}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if strings.Contains(state.sftp.status, "后续加入") || !strings.Contains(state.sftp.status, "v 预览") || !strings.Contains(state.sftp.status, "e 编辑") || !strings.Contains(state.sftp.status, "i 属性") {
		t.Fatalf("file selection status does not show implemented actions: %q", state.sftp.status)
	}
}

func TestSFTPPropertiesViewShowsAvailableMetadataAndReturnsToList(t *testing.T) {
	state := modelState{width: 100, height: 12, sftp: &sftpBrowserState{
		standalone: true,
		host:       model.Host{Kind: model.HostKindSSH, Name: "staging"},
		activeSide: sftpRemoteSide,
		path:       "/srv/releases",
		entries: []sftpclient.Entry{{
			Name: "release.tar", Size: 4096, Mode: 0o640, ModeKnown: true,
			ModTime: time.Date(2025, time.January, 15, 12, 30, 0, 0, time.Local),
			UID:     1001, GID: 2001, OwnershipKnown: true,
		}},
	}}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "i", Code: 'i'}))
	state = unwrapModel(t, updated)
	if state.sftp.properties == nil {
		t.Fatal("i did not open the properties view")
	}
	view := state.sftpView()
	for _, expected := range []string{"文件属性", "release.tar", "SFTP · staging", "/srv/releases/release.tar", "4096 字节", "640", "UID 1001 · GID 2001"} {
		if !strings.Contains(view, expected) {
			t.Errorf("properties view is missing %q:\n%s", expected, view)
		}
	}
	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.sftp.properties != nil || !strings.Contains(state.sftpView(), "release.tar") {
		t.Fatal("Esc did not return from properties to the file list")
	}
}

func TestFTPPropertiesDoNotInventOwnership(t *testing.T) {
	state := modelState{width: 90, height: 10, sftp: &sftpBrowserState{
		standalone: true,
		host:       model.Host{Kind: model.HostKindFTP, Name: "legacy"},
		activeSide: sftpRemoteSide,
		path:       "/incoming",
		entries:    []sftpclient.Entry{{Name: "report.csv", Size: 128}},
	}}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "i", Code: 'i'}))
	state = unwrapModel(t, updated)
	view := state.sftpView()
	if !strings.Contains(view, "FTP · legacy") || !strings.Contains(view, "属主/属组  未提供") || strings.Contains(view, "UID 0") {
		t.Fatalf("FTP properties view invented unsupported metadata:\n%s", view)
	}
}

func TestFTPSideOffersSiteChmodWithoutInventingPermissions(t *testing.T) {
	state := modelState{width: 80, height: 12, sftp: &sftpBrowserState{
		standalone: true,
		host:       model.Host{Kind: model.HostKindFTP, Name: "archive"},
		activeSide: sftpRemoteSide,
		entries:    []sftpclient.Entry{{Name: "report.csv", Size: 10}},
	}}
	if view := state.sftpView(); !strings.Contains(view, "report.csv") || strings.Contains(view, "---") {
		t.Fatalf("compact FTP file list should omit unavailable permissions:\n%s", view)
	}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "p", Code: 'p'}))
	state = unwrapModel(t, updated)
	if state.sftp.prompt == nil || state.sftp.prompt.action != sftpPromptChmod || state.sftp.prompt.value != "644" {
		t.Fatalf("FTP permission action should open SITE CHMOD prompt with a safe file default: %#v", state.sftp.prompt)
	}
}

func TestSFTPEntryViewShowsNumericOwnershipWithoutInventingIt(t *testing.T) {
	known := formatSFTPEntry(sftpclient.Entry{Name: "root-file", Mode: 0o600, ModeKnown: true, OwnershipKnown: true, UID: 0, GID: 0}, false, false, 130)
	if !strings.Contains(known, "0:0") {
		t.Fatalf("SFTP entry did not show root ownership: %q", known)
	}
	unknown := formatSFTPEntry(sftpclient.Entry{Name: "ftp-file", Mode: 0o600, ModeKnown: true}, false, false, 130)
	if strings.Contains(unknown, "0:0") || strings.Contains(unknown, "1000:1000") {
		t.Fatalf("entry without ownership metadata displayed invented IDs: %q", unknown)
	}
}

func TestSFTPChmodPromptChangesLocalFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX rwx permissions through os.FileMode")
	}
	localRoot := t.TempDir()
	filePath := filepath.Join(localRoot, "private.txt")
	if err := os.WriteFile(filePath, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := modelState{
		width: 80, height: 12,
		sftp: &sftpBrowserState{
			standalone: true, activeSide: sftpLocalSide, localPath: localRoot,
			localEntries: []sftpclient.Entry{{Name: "private.txt", Mode: 0o600, ModeKnown: true}},
		},
	}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "p", Code: 'p'}))
	state = unwrapModel(t, updated)
	if state.sftp.prompt == nil || state.sftp.prompt.action != sftpPromptChmod || state.sftp.prompt.value != "600" {
		t.Fatalf("permission prompt was not initialized from file mode: %#v", state.sftp.prompt)
	}
	state.sftp.prompt.value = "640"
	updated, command := state.updateSFTPPrompt(tea.Key{Code: tea.KeyEnter})
	state = unwrapModel(t, updated)
	if command == nil || !state.sftp.busy {
		t.Fatal("confirming a valid permission change did not start an operation")
	}
	rawMessage := command()
	message, ok := rawMessage.(sftpOperationMsg)
	if !ok {
		t.Fatalf("permission operation returned %T, want sftpOperationMsg", rawMessage)
	}
	state.handleSFTPOperation(message)
	info, err := os.Stat(filePath)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("local file mode = %v, %v; want 0640", info, err)
	}
	if state.sftp.statusErr || state.sftp.status != "本地权限已修改为 640" {
		t.Fatalf("permission update status = %q (error=%v)", state.sftp.status, state.sftp.statusErr)
	}
}

func TestParseSFTPPermissions(t *testing.T) {
	for _, test := range []struct {
		input string
		want  os.FileMode
	}{
		{input: "644", want: 0o644},
		{input: "0755", want: 0o755},
		{input: "000", want: 0},
	} {
		got, err := parseSFTPPermissions(test.input)
		if err != nil || got != test.want {
			t.Errorf("parseSFTPPermissions(%q) = %04o, %v; want %04o", test.input, got, err, test.want)
		}
	}
	for _, input := range []string{"", "64", "0788", "1000", "7555", "-64", "0o644", " 644"} {
		if _, err := parseSFTPPermissions(input); err == nil {
			t.Errorf("parseSFTPPermissions(%q) succeeded; want validation error", input)
		}
	}
}

func TestStandaloneSFTPPeerRemoteCanOpenAndSaveEditor(t *testing.T) {
	state := modelState{
		width:  80,
		height: 14,
		sftp: &sftpBrowserState{
			standalone: true,
			generation: 3,
			host:       model.Host{Name: "destination"},
			client:     &sftpclient.Client{},
			peerHost:   model.Host{Name: "source"},
			peerClient: &sftpclient.Client{},
			peerPath:   "/srv/source",
			activeSide: sftpLocalSide,
			localEntries: []sftpclient.Entry{
				{Name: "notes.txt", Mode: 0o644},
			},
		},
	}

	updated, command := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "v", Code: 'v'}))
	state = unwrapModel(t, updated)
	if command == nil || !state.sftp.busy {
		t.Fatal("preview from the peer remote pane did not start")
	}
	if state.sftp.status == "远程文件预览仅适用于右侧远程栏" {
		t.Fatal("peer remote pane was rejected as a non-remote side")
	}

	state.handleSFTPTextLoaded(sftpEditorLoadedMsg{
		requestID: state.sftp.requestID, generation: state.sftp.generation,
		side: sftpLocalSide, hostName: "source", path: "/srv/source/notes.txt",
		mode: sftpEditMode, content: []byte("original"),
	})
	if state.sftp.editor == nil || state.sftp.editor.side != sftpLocalSide || state.sftp.editor.hostName != "source" {
		t.Fatalf("editor lost peer connection identity: %#v", state.sftp.editor)
	}
	if got := state.sftpView(); !strings.Contains(got, "source") || !strings.Contains(got, "/srv/source/notes.txt") {
		t.Fatalf("editor view does not identify the peer host and path:\n%s", got)
	}

	state.sftp.editor.lines[0] = "updated"
	state.sftp.editor.dirty = true
	command = state.saveSFTPTextEditor()
	if command == nil || !state.sftp.busy || state.sftp.status != "正在保存文件…" {
		t.Fatal("saving from the peer remote editor did not start")
	}
}

func TestStandaloneSFTPLocalPaneCanPreviewEditAndDetectConflicts(t *testing.T) {
	localPath := t.TempDir()
	filePath := filepath.Join(localPath, "local.txt")
	if err := os.WriteFile(filePath, []byte("original\r\nsecond line\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	state := modelState{
		width:  80,
		height: 14,
		sftp: &sftpBrowserState{
			standalone: true,
			generation: 4,
			localPath:  localPath,
			activeSide: sftpLocalSide,
			localEntries: []sftpclient.Entry{
				{Name: "local.txt", Mode: 0o640},
			},
		},
	}

	updated, command := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "v", Code: 'v'}))
	state = unwrapModel(t, updated)
	if command == nil || !state.sftp.busy {
		t.Fatal("preview from the local pane did not start")
	}
	loaded, ok := command().(sftpEditorLoadedMsg)
	if !ok {
		t.Fatal("preview command returned an unexpected message")
	}
	state.handleSFTPTextLoaded(loaded)
	if state.sftp.editor == nil || state.sftp.editor.mode != sftpPreviewMode || state.sftp.editor.hostName != "本地" {
		t.Fatalf("local preview state is incorrect: %#v", state.sftp.editor)
	}

	updated, _ = state.updateSFTPTextEditor(tea.Key{Code: tea.KeyEscape})
	state = unwrapModel(t, updated)
	updated, command = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "e", Code: 'e'}))
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("editing from the local pane did not start")
	}
	loaded, ok = command().(sftpEditorLoadedMsg)
	if !ok {
		t.Fatal("edit command returned an unexpected message")
	}
	state.handleSFTPTextLoaded(loaded)
	state.sftp.editor.lines[0] = "updated"
	state.sftp.editor.dirty = true
	command = state.saveSFTPTextEditor()
	if command == nil {
		t.Fatal("saving local changes did not start")
	}
	saved, ok := command().(sftpEditorSavedMsg)
	if !ok {
		t.Fatal("local save returned an unexpected message")
	}
	state.handleSFTPTextSaved(saved)
	if content, err := os.ReadFile(filePath); err != nil || string(content) != "updated\r\nsecond line\n" {
		t.Fatalf("saved local content = %q, %v", content, err)
	}
	if info, err := os.Stat(filePath); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("local file permissions = %v, %v; want 0640", info, err)
	}

	state.sftp.editor.lines[0] = "user edit"
	state.sftp.editor.dirty = true
	if err := os.WriteFile(filePath, []byte("external edit"), 0o640); err != nil {
		t.Fatal(err)
	}
	command = state.saveSFTPTextEditor()
	if command == nil {
		t.Fatal("saving after external change did not start")
	}
	saved, ok = command().(sftpEditorSavedMsg)
	if !ok {
		t.Fatal("conflicted local save returned an unexpected message")
	}
	state.handleSFTPTextSaved(saved)
	if !state.sftp.statusErr || !strings.Contains(state.sftp.status, "保存冲突") || state.sftp.editor.lines[0] != "user edit" {
		t.Fatalf("local conflict did not preserve the buffer: status=%q lines=%v", state.sftp.status, state.sftp.editor.lines)
	}
	if content, err := os.ReadFile(filePath); err != nil || string(content) != "external edit" {
		t.Fatalf("conflicted save overwrote external content: %q, %v", content, err)
	}
}

func TestSFTPTextEditorPreservesMixedLineEndingsWhenEditingLines(t *testing.T) {
	original := "first\r\nsecond\nthird\r\n"
	lines, endings := splitSFTPText(original)
	editor := &sftpEditorState{
		mode: sftpEditMode, original: original, lines: lines, lineEndings: endings,
		newline: preferredSFTPNewline(endings), row: 1, column: len([]rune(lines[1])),
	}
	state := modelState{height: 20, sftp: &sftpBrowserState{editor: editor}}
	updated, _ := state.updateSFTPTextEditor(tea.Key{Code: tea.KeyEnter})
	state = unwrapModel(t, updated)
	if got, want := serializeSFTPText(editor.lines, editor.lineEndings), "first\r\nsecond\r\n\nthird\r\n"; got != want {
		t.Fatalf("inserting a line did not preserve the existing separators: %q, want %q", got, want)
	}
	updated, _ = state.updateSFTPTextEditor(tea.Key{Code: tea.KeyBackspace})
	state = unwrapModel(t, updated)
	if got := serializeSFTPText(state.sftp.editor.lines, state.sftp.editor.lineEndings); got != original {
		t.Fatalf("merging the inserted line did not restore original content: %q, want %q", got, original)
	}
}

func TestStandaloneSFTPDeletionRecursivelyRemovesLocalTree(t *testing.T) {
	localRoot, externalRoot := t.TempDir(), t.TempDir()
	tree := filepath.Join(localRoot, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "nested", "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalFile := filepath.Join(externalRoot, "keep.txt")
	if err := os.WriteFile(externalFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalRoot, filepath.Join(tree, "external-link")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	state := modelState{
		sftp: &sftpBrowserState{
			standalone: true, localPath: localRoot, activeSide: sftpLocalSide,
			localEntries: []sftpclient.Entry{{Name: "tree", IsDir: true, Mode: os.ModeDir | 0o755}},
		},
	}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))
	state = unwrapModel(t, updated)
	if state.sftp.prompt == nil || !strings.Contains(state.sftp.prompt.title, "递归删除") {
		t.Fatal("directory deletion did not explain its recursive behavior in the confirmation")
	}
	updated, command := state.updateSFTPPrompt(tea.Key{Text: "y", Code: 'y'})
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("confirmed directory deletion did not start")
	}
	result, ok := command().(sftpOperationMsg)
	if !ok {
		t.Fatal("directory deletion returned an unexpected message")
	}
	state.handleSFTPOperation(result)
	if result.err != nil {
		t.Fatalf("recursive local deletion failed: %v", result.err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local tree remains after delete: %v", err)
	}
	if content, err := os.ReadFile(externalFile); err != nil || string(content) != "keep" {
		t.Fatalf("local recursive delete followed a symlink: content=%q err=%v", content, err)
	}
}

func TestStandaloneSFTPMarksAndQueuesTransfers(t *testing.T) {
	localPath := t.TempDir()
	state := modelState{
		width:  100,
		height: 14,
		sftp: &sftpBrowserState{
			standalone: true,
			client:     &sftpclient.Client{},
			activeSide: sftpLocalSide,
			localPath:  localPath,
			path:       "/remote",
			localEntries: []sftpclient.Entry{
				{Name: "zeta.txt", Mode: 0o644},
				{Name: "alpha.txt", Mode: 0o644},
			},
		},
	}

	state.sftp.localSelected = 0
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))
	state = unwrapModel(t, updated)
	state.sftp.localSelected = 1
	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))
	state = unwrapModel(t, updated)
	if len(state.sftp.localMarks) != 2 {
		t.Fatalf("marked %d local entries, want 2", len(state.sftp.localMarks))
	}
	if view := state.sftpView(); !strings.Contains(view, "✓") || !strings.Contains(view, "已选 2") {
		t.Fatalf("view does not show marked entries and count:\n%s", view)
	}

	if command := state.beginSFTPTransfer(sftpUpload); command != nil || !state.sftp.queueReviewActive {
		t.Fatalf("multi-select should open a queue review before starting: cmd=%v review=%v", command != nil, state.sftp.queueReviewActive)
	}
	if view := state.sftpView(); !strings.Contains(view, "传输队列") || !strings.Contains(view, "+ 提前") || !strings.Contains(view, "alpha.txt") {
		t.Fatalf("queue review does not show order controls and items:\n%s", view)
	}
	if len(state.sftp.localMarks) != 2 {
		t.Fatal("source marks were cleared before the user confirmed the queue")
	}
	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	state = unwrapModel(t, updated)
	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "+", Code: '+'}))
	state = unwrapModel(t, updated)
	if got, want := filepath.Base(state.sftp.transferQueue[0].localPath), "zeta.txt"; got != want {
		t.Fatalf("raising selected item priority moved %q to the front, want %q", got, want)
	}
	updated, command := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if command == nil || !state.sftp.queueActive || state.sftp.queueTotal != 2 || len(state.sftp.transferQueue) != 1 {
		t.Fatalf("confirming queue did not start transfers: cmd=%v active=%v total=%d remaining=%d", command != nil, state.sftp.queueActive, state.sftp.queueTotal, len(state.sftp.transferQueue))
	}
	if got, want := filepath.Base(state.sftp.queueCurrent.localPath), "zeta.txt"; got != want {
		t.Fatalf("queue order starts at %q, want prioritized item %q", got, want)
	}
	if len(state.sftp.localMarks) != 0 {
		t.Fatal("source marks were not cleared after confirming the queue")
	}
}

func TestStandaloneSFTPQueueSkipsConflictsAndContinuesAfterFailure(t *testing.T) {
	localPath := t.TempDir()
	state := modelState{
		width:  100,
		height: 14,
		sftp: &sftpBrowserState{
			standalone: true,
			client:     &sftpclient.Client{},
			activeSide: sftpLocalSide,
			localPath:  localPath,
			path:       "/remote",
			localEntries: []sftpclient.Entry{
				{Name: "a.txt", Mode: 0o644},
				{Name: "b.txt", Mode: 0o644},
				{Name: "c.txt", Mode: 0o644},
			},
		},
	}
	for index := range state.sftp.localEntries {
		state.sftp.localSelected = index
		state.toggleSFTPMark()
	}
	if command := state.beginSFTPTransfer(sftpUpload); command != nil || !state.sftp.queueReviewActive {
		t.Fatal("multi-select did not open the transfer queue review")
	}
	updated, command := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("confirming transfer queue returned no command")
	}

	state.handleSFTPOverwriteRequired(sftpOverwriteRequiredMsg{
		requestID: state.sftp.requestID,
		transfer:  *state.sftp.queueCurrent,
	})
	updated, command = state.updateSFTPPrompt(tea.Key{Text: "n", Code: 'n'})
	state = unwrapModel(t, updated)
	if command == nil || state.sftp.queueSkipped != 1 || state.sftp.queueDone != 1 {
		t.Fatalf("conflict was not skipped: command=%v skipped=%d done=%d", command != nil, state.sftp.queueSkipped, state.sftp.queueDone)
	}
	if got, want := filepath.Base(state.sftp.queueCurrent.localPath), "b.txt"; got != want {
		t.Fatalf("queue did not advance after conflict: current=%q want=%q", got, want)
	}

	command = state.handleSFTPQueuePreflight(sftpQueuePreflightMsg{
		requestID: state.sftp.requestID,
		transfer:  *state.sftp.queueCurrent,
		err:       errors.New("permission denied"),
	})
	if command == nil || len(state.sftp.queueFailures) != 1 || state.sftp.queueDone != 2 {
		t.Fatalf("failed item did not allow queue to continue: command=%v failures=%v done=%d", command != nil, state.sftp.queueFailures, state.sftp.queueDone)
	}
	if got, want := filepath.Base(state.sftp.queueCurrent.localPath), "c.txt"; got != want {
		t.Fatalf("queue did not advance after failure: current=%q want=%q", got, want)
	}
}

func TestSFTPQueueReviewCancellationKeepsMarkedSelection(t *testing.T) {
	localPath := t.TempDir()
	state := modelState{
		sftp: &sftpBrowserState{
			standalone: true,
			client:     &sftpclient.Client{},
			activeSide: sftpLocalSide,
			localPath:  localPath,
			localEntries: []sftpclient.Entry{
				{Name: "first.txt", Mode: 0o644},
				{Name: "second.txt", Mode: 0o644},
			},
		},
	}
	for index := range state.sftp.localEntries {
		state.sftp.localSelected = index
		state.toggleSFTPMark()
	}
	if command := state.beginSFTPTransfer(sftpUpload); command != nil || !state.sftp.queueReviewActive {
		t.Fatal("multi-select did not open the transfer queue review")
	}
	updated, command := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if command != nil || state.sftp.queueReviewActive || len(state.sftp.transferQueue) != 0 || state.sftp.queueActive {
		t.Fatalf("cancel did not close the preview without starting: cmd=%v state=%#v", command != nil, state.sftp)
	}
	if len(state.sftp.localMarks) != 2 {
		t.Fatalf("cancel preserved %d marked items, want 2", len(state.sftp.localMarks))
	}
}

func TestSFTPDirectoryProgressShowsFileCountAndCurrentPath(t *testing.T) {
	state := modelState{
		width:  100,
		height: 12,
		sftp: &sftpBrowserState{
			host:      model.Host{Name: "test-host"},
			busy:      true,
			requestID: 17,
			status:    "正在传输目录 tree",
		},
	}
	progressChannel := make(chan sftpProgress, 1)
	progressContext := withSFTPTransferProgressReporter(context.Background(), progressChannel, 0)
	remotefs.ReportTransferProgress(progressContext, remotefs.TransferProgress{
		CurrentPath: "nested/config.yaml", FilesCompleted: 2, FilesTotal: 5, Transferred: 12, Total: 100,
	})
	progress := <-progressChannel
	state.handleSFTPProgress(sftpProgressMsg{requestID: state.sftp.requestID, progress: progress})
	view := state.sftpView()
	if !strings.Contains(view, "文件 2/5") || !strings.Contains(view, "nested/config.yaml") {
		t.Fatalf("directory progress is missing file count or current path:\n%s", view)
	}
	if got := sftpQueueProgressSuffix(map[int]sftpProgress{
		0: {filesDone: 1, filesTotal: 3, currentPath: "a/file.txt"},
		1: {filesDone: 2, filesTotal: 4, currentPath: "b/file.txt"},
	}); !strings.Contains(got, "文件 3/7") || !strings.Contains(got, "a/file.txt") || !strings.Contains(got, "b/file.txt") {
		t.Fatalf("queue progress summary = %q; want aggregate counts and active paths", got)
	}
}

func TestSFTPQueueWorkersRespectConcurrencyLimit(t *testing.T) {
	jobs := make([]sftpQueueJob, 9)
	for index := range jobs {
		jobs[index].transfer.localPath = filepath.Join(t.TempDir(), "item")
	}
	var active, maximum int32
	results := runSFTPQueueJobs(context.Background(), jobs, func(_ int, _ sftpQueueJob) error {
		current := atomic.AddInt32(&active, 1)
		for observed := atomic.LoadInt32(&maximum); current > observed; observed = atomic.LoadInt32(&maximum) {
			if atomic.CompareAndSwapInt32(&maximum, observed, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return nil
	})
	if len(results) != len(jobs) {
		t.Fatalf("got %d queue results, want %d", len(results), len(jobs))
	}
	if maximum < 2 || maximum > sftpQueueConcurrency {
		t.Fatalf("observed maximum concurrency %d, want 2..%d", maximum, sftpQueueConcurrency)
	}
}

func TestSFTPQueueOverwriteApprovalContinuesPreflight(t *testing.T) {
	localPath := t.TempDir()
	state := modelState{
		sftp: &sftpBrowserState{
			standalone: true,
			client:     &sftpclient.Client{},
			localPath:  localPath,
			path:       "/remote",
			transferQueue: []sftpTransferRequest{
				{direction: sftpUpload, side: sftpRemoteSide, localPath: filepath.Join(localPath, "a.txt"), destPath: "/remote/a.txt", destRemote: true},
				{direction: sftpUpload, side: sftpRemoteSide, localPath: filepath.Join(localPath, "b.txt"), destPath: "/remote/b.txt", destRemote: true},
			},
		},
	}
	state.sftp.queueActive = true
	state.sftp.queueTotal = 2
	state.sftp.queueContext, state.sftp.transferCancel = context.WithCancel(context.Background())
	state.sftp.requestID = 7
	state.sftp.queueCurrent = &state.sftp.transferQueue[0]
	state.sftp.transferQueue = state.sftp.transferQueue[1:]
	state.handleSFTPQueuePreflight(sftpQueuePreflightMsg{
		requestID: 7, transfer: *state.sftp.queueCurrent, exists: true,
	})
	if state.sftp.prompt == nil {
		t.Fatal("existing target did not open overwrite prompt")
	}
	updated, command := state.updateSFTPPrompt(tea.Key{Text: "y", Code: 'y'})
	state = unwrapModel(t, updated)
	if command == nil || len(state.sftp.queueApproved) != 1 || !state.sftp.queueApproved[0].overwrite {
		t.Fatalf("overwrite approval was not queued: command=%v approved=%#v", command != nil, state.sftp.queueApproved)
	}
	if state.sftp.queueCurrent == nil || filepath.Base(state.sftp.queueCurrent.localPath) != "b.txt" {
		t.Fatalf("preflight did not advance to next item: %#v", state.sftp.queueCurrent)
	}
}

func TestSFTPTransferRoutesForLocalAndRemotePaneCombinations(t *testing.T) {
	state := &sftpBrowserState{
		standalone: true,
		localPath:  "/local/left",
		path:       "/remote/right",
		peerPath:   "/remote/peer",
		client:     &sftpclient.Client{},
		peerClient: &sftpclient.Client{},
	}
	m := modelState{sftp: state}
	entry := sftpclient.Entry{Name: "file.txt"}

	transfer := m.sftpTransferForEntry(sftpUpload, "/remote/peer/file.txt", entry)
	if !transfer.sourceRemote || !transfer.destRemote {
		t.Fatalf("remote-to-remote transfer route = sourceRemote:%v destRemote:%v", transfer.sourceRemote, transfer.destRemote)
	}

	state.peerClient = nil
	transfer = m.sftpTransferForEntry(sftpUpload, "/local/left/file.txt", entry)
	if transfer.sourceRemote || !transfer.destRemote {
		t.Fatalf("local-to-remote transfer route = sourceRemote:%v destRemote:%v", transfer.sourceRemote, transfer.destRemote)
	}

	state.primaryLocal = true
	state.client = nil
	transfer = m.sftpTransferForEntry(sftpDownload, "/local/right/file.txt", entry)
	if transfer.sourceRemote || transfer.destRemote {
		t.Fatalf("local-to-local transfer route = sourceRemote:%v destRemote:%v", transfer.sourceRemote, transfer.destRemote)
	}
}

func TestCopyLocalSFTPDirectoryCopiesAndRejectsNestedDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "file.txt"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "destination")
	if err := copyLocalSFTPPath(context.Background(), source, destination, false, true, nil); err != nil {
		t.Fatalf("copy local directory: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "nested", "file.txt"))
	if err != nil || string(data) != "payload" {
		t.Fatalf("copied file = %q, err=%v", data, err)
	}
	if err := copyLocalSFTPPath(context.Background(), source, filepath.Join(source, "inside"), true, true, nil); err == nil {
		t.Fatal("copy into a descendant directory should be rejected")
	}
}

func TestStandaloneSFTPEscapeCancelsCurrentAndQueuedTransfers(t *testing.T) {
	localPath := t.TempDir()
	state := modelState{
		sftp: &sftpBrowserState{
			standalone: true,
			client:     &sftpclient.Client{},
			activeSide: sftpLocalSide,
			localPath:  localPath,
			path:       "/remote",
			localEntries: []sftpclient.Entry{
				{Name: "a.txt", Mode: 0o644},
				{Name: "b.txt", Mode: 0o644},
			},
		},
	}
	for index := range state.sftp.localEntries {
		state.sftp.localSelected = index
		state.toggleSFTPMark()
	}
	if command := state.beginSFTPTransfer(sftpUpload); command != nil || !state.sftp.queueReviewActive {
		t.Fatal("multi-select did not open the transfer queue review")
	}
	updated, command := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("confirming transfer queue returned no command")
	}

	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.sftp.queueActive || !state.sftp.queueCancelled || len(state.sftp.transferQueue) != 0 || state.sftp.queueCurrent != nil {
		t.Fatalf("Escape did not stop the active queue: %#v", state.sftp)
	}
	if state.sftp.status != "正在取消传输并恢复远程文件连接…" {
		t.Fatalf("unexpected cancellation status: %q", state.sftp.status)
	}
}
