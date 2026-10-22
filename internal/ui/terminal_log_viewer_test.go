package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/terminalog"
	"tui-ssh-workbench/internal/model"
)

func TestTerminalLogPreviewParsesVTAndKeepsOutputSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terminal.log")
	raw := []byte("\x1b[31mred text\x1b[0m\r\nsecond line\r\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	entry := terminalog.Entry{Path: path, Name: "terminal.log", Size: int64(len(raw))}
	state := modelState{
		width: 60, height: 16,
		terminalLogViewer: &terminalLogViewerState{generation: 7, loadingPreviewPath: path, loadingPreviewEncoding: model.TerminalEncodingUTF8},
	}
	msg, ok := terminalLogPreviewCmd(entry, 60, 13, 7)().(terminalLogPreviewLoadedMsg)
	if !ok {
		t.Fatal("preview command returned an unexpected message")
	}
	state.handleTerminalLogPreviewLoaded(msg)
	if state.terminalLogViewer.preview == nil {
		t.Fatalf("preview was not loaded: %s", state.terminalLogViewer.errText)
	}
	if strings.Contains(state.terminalLogViewer.preview.String(), "\x1b[") {
		t.Fatal("preview left raw terminal escape sequences in screen text")
	}
	view := state.terminalLogViewerView()
	if !strings.Contains(view, "red text") || !strings.Contains(view, "second line") {
		t.Fatalf("preview did not render recorded text:\n%s", view)
	}
}

func TestTerminalLogPreviewSwitchesToSavedHostEncodingChoice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.log")
	encoded, err := encodeTerminalText("GB18030", []byte("你好"))
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte("\x1b[32m"), encoded...)
	raw = append(raw, []byte("\x1b[0m\r\n")...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	entry := terminalog.Entry{Path: path, Name: "legacy.log", Size: int64(len(raw))}
	viewer := &terminalLogViewerState{
		generation: 3, loadingPreviewPath: path,
		loadingPreviewEncoding: model.TerminalEncodingUTF8,
		encoding:               model.TerminalEncodingUTF8,
	}
	state := modelState{width: 60, height: 16, terminalLogViewer: viewer}
	initialMessage, ok := terminalLogPreviewCmd(entry, 60, 13, viewer.generation)().(terminalLogPreviewLoadedMsg)
	if !ok {
		t.Fatal("UTF-8 preview command returned an unexpected message")
	}
	state.handleTerminalLogPreviewLoaded(initialMessage)
	if viewer.preview == nil {
		t.Fatalf("initial preview did not load: %s", viewer.errText)
	}

	command := state.updateTerminalLogViewerKey(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	if command == nil || viewer.loadingPreviewEncoding != "GB18030" {
		t.Fatalf("Ctrl+U did not request GB18030 preview: command=%v encoding=%q", command != nil, viewer.loadingPreviewEncoding)
	}
	message, ok := command().(terminalLogPreviewLoadedMsg)
	if !ok {
		t.Fatal("GB18030 preview command returned an unexpected message")
	}
	state.handleTerminalLogPreviewLoaded(message)
	if viewer.preview == nil {
		t.Fatalf("GB18030 preview failed: %s", viewer.errText)
	}
	if screen := viewer.preview.String(); !strings.Contains(screen, "你好") {
		t.Fatalf("GB18030 preview did not decode Chinese output: screen=%q", screen)
	}
	if !strings.Contains(state.terminalLogViewerView(), "编码：GB18030") {
		t.Fatal("preview did not display selected encoding")
	}
}

func TestTerminalLogPreviewRejectsSymlink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target.log")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := loadTerminalLog(link, 40, 10); err == nil {
		t.Fatal("log preview followed a symlink")
	}
}

func TestSafeLogDisplayTextRemovesControlCharacters(t *testing.T) {
	if got, want := safeLogDisplayText("host\x1b[31m\nname"), "host�[31m�name"; got != want {
		t.Fatalf("safe log label = %q, want %q", got, want)
	}
}

func TestTerminalLogViewerFiltersNamesAndEscapes(t *testing.T) {
	viewer := &terminalLogViewerState{
		entries: []terminalog.Entry{
			{Name: "20261020-database.log"},
			{Name: "20261020-web.log"},
		},
	}
	state := modelState{width: 80, height: 20, terminalLogViewer: viewer}
	_ = state.updateTerminalLogViewerKey(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	_ = state.updateTerminalLogViewerKey(tea.KeyPressMsg(tea.Key{Code: 'w', Text: "w"}))
	_ = state.updateTerminalLogViewerKey(tea.KeyPressMsg(tea.Key{Code: 'e', Text: "e"}))
	_ = state.updateTerminalLogViewerKey(tea.KeyPressMsg(tea.Key{Code: 'b', Text: "b"}))
	if got := len(viewer.visibleEntries()); got != 1 || viewer.visibleEntries()[0].Name != "20261020-web.log" {
		t.Fatalf("filtered log entries = %#v, want only web log", viewer.visibleEntries())
	}
	_ = state.updateTerminalLogViewerKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if viewer.filtering || viewer.query != "" {
		t.Fatalf("Escape did not clear active filter: filtering=%v query=%q", viewer.filtering, viewer.query)
	}
}

func TestTerminalLogViewerDeletesOnlyAfterConfirmation(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDirectory)
	t.Setenv("HOME", configDirectory)
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	logDirectory := filepath.Join(userConfigDir, "tui-ssh-workbench", "logs")
	logger, err := terminalog.New(logDirectory, "delete-me")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Write([]byte("recorded output")); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	entry := terminalog.Entry{Path: logger.Path(), Name: filepath.Base(logger.Path())}
	viewer := &terminalLogViewerState{generation: 4, entries: []terminalog.Entry{entry}}
	state := modelState{width: 80, height: 20, terminalLogViewer: viewer}

	_ = state.updateTerminalLogViewerKey(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	if viewer.deleteTarget == nil || viewer.deleteTarget.Name != entry.Name {
		t.Fatal("d did not request deletion confirmation")
	}
	if _, err := os.Lstat(entry.Path); err != nil {
		t.Fatalf("log was removed before confirmation: %v", err)
	}
	if !strings.Contains(state.terminalLogViewerView(), "确定删除日志文件") {
		t.Fatal("delete confirmation was not displayed")
	}

	_ = state.updateTerminalLogViewerKey(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "n"}))
	if viewer.deleteTarget != nil {
		t.Fatal("n did not cancel deletion confirmation")
	}
	if _, err := os.Lstat(entry.Path); err != nil {
		t.Fatalf("cancelled deletion removed log: %v", err)
	}

	_ = state.updateTerminalLogViewerKey(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	command := state.updateTerminalLogViewerKey(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	if command == nil || !viewer.deleting {
		t.Fatal("y did not start the asynchronous delete")
	}
	message, ok := command().(terminalLogDeletedMsg)
	if !ok {
		t.Fatalf("delete command returned %T, want terminalLogDeletedMsg", command())
	}
	updated, _ := state.Update(message)
	state = unwrapModel(t, updated)
	if _, err := os.Lstat(entry.Path); !os.IsNotExist(err) {
		t.Fatalf("confirmed log still exists: %v", err)
	}
	if len(viewer.entries) != 0 || !strings.Contains(viewer.noticeText, "已删除") {
		t.Fatalf("viewer did not reflect deletion: entries=%#v notice=%q", viewer.entries, viewer.noticeText)
	}
}

func TestTerminalLogViewerRefusesDeletingActiveRecording(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDirectory)
	t.Setenv("HOME", configDirectory)
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	logDirectory := filepath.Join(userConfigDir, "tui-ssh-workbench", "logs")
	logger, err := terminalog.New(logDirectory, "active")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if err := logger.Write([]byte("active output")); err != nil {
		t.Fatal(err)
	}
	entry := terminalog.Entry{Path: logger.Path(), Name: filepath.Base(logger.Path())}
	viewer := &terminalLogViewerState{generation: 8, entries: []terminalog.Entry{entry}}
	state := modelState{terminalLogViewer: viewer, outputLoggers: map[*terminalog.Logger]struct{}{logger: {}}}
	_ = state.updateTerminalLogViewerKey(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	command := state.updateTerminalLogViewerKey(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	if command != nil || !strings.Contains(viewer.errText, "仍属于正在记录") {
		t.Fatalf("active recording was not protected: command=%v error=%q", command != nil, viewer.errText)
	}
	if viewer.deleteTarget != nil {
		t.Fatal("active-log rejection left the delete prompt open")
	}
}

func TestWorkspaceShortcutOpensLogBrowserAndPreviewsSelectedFile(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("HOME", configDir)
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(userConfigDir, "tui-ssh-workbench", "logs")
	logger, err := terminalog.New(logDir, "api-server")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Write([]byte("\x1b[32mconnected\x1b[0m\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	pane := &terminalPane{id: "pane-1", host: model.Host{Kind: model.HostKindSSH, Name: "api-server"}}
	tab := &workspaceTab{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}
	state := modelState{width: 80, height: 20, workspaceVisible: true, activeTabID: tab.id, tabs: []*workspaceTab{tab}}
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, command := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Text: "g"}))
	state = unwrapModel(t, updated)
	if command == nil || state.terminalLogViewer == nil {
		t.Fatal("Ctrl+G then G did not open the log browser")
	}
	listMessage, ok := command().(terminalLogsLoadedMsg)
	if !ok {
		t.Fatalf("log list command returned %T", command())
	}
	updated, _ = state.Update(listMessage)
	state = unwrapModel(t, updated)
	if !strings.Contains(state.view(), "api-server") {
		t.Fatalf("log browser did not list the recorded host:\n%s", state.view())
	}
	previewCommand := state.updateTerminalLogViewerKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if previewCommand == nil {
		t.Fatal("Enter did not start log preview")
	}
	previewMessage, ok := previewCommand().(terminalLogPreviewLoadedMsg)
	if !ok {
		t.Fatalf("log preview command returned %T", previewCommand())
	}
	state.handleTerminalLogPreviewLoaded(previewMessage)
	if state.terminalLogViewer.preview == nil || !strings.Contains(state.terminalLogViewer.preview.String(), "connected") {
		t.Fatal("selected output log was not previewed")
	}
}
