package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/infra/terminalog"
	"tui-ssh-workbench/internal/model"
)

func TestTerminalOutputLogRecordsOnlyOutputAndClosesCleanly(t *testing.T) {
	logger, err := terminalog.New(filepath.Join(t.TempDir(), "logs"), "test-host")
	if err != nil {
		t.Fatal(err)
	}
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Name: "test-host"}, session: session,
		terminal: vt.NewSafeEmulator(32, 5), outputLogger: logger,
	}
	tab := &workspaceTab{id: "tab-1", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{
		width: 80, height: 20, workspaceVisible: true, activeTabID: tab.id,
		tabs: []*workspaceTab{tab}, outputLoggers: map[*terminalog.Logger]struct{}{logger: {}},
	}
	if err := session.Write([]byte("typed secret")); err != nil {
		t.Fatal(err)
	}
	remoteOutput := []byte("remote output\r\n")
	updated, _ := state.Update(terminalOutputMsg{session: session, data: remoteOutput})
	state = unwrapModel(t, updated)
	if len(session.writes) != 1 || string(session.writes[0]) != "typed secret" {
		t.Fatalf("fake shell writes = %q", session.writes)
	}
	if pane.outputLogger != logger {
		t.Fatal("incoming terminal output unexpectedly stopped the logger")
	}

	closeCmd := state.toggleActiveTerminalOutputLog()
	if closeCmd == nil || pane.outputLogger != nil || !pane.outputLogClosing {
		t.Fatal("user stop did not detach the logger and begin asynchronous close")
	}
	closed, ok := closeCmd().(terminalOutputLogClosedMsg)
	if !ok {
		t.Fatalf("close command returned %T", closeCmd())
	}
	updated, _ = state.Update(closed)
	state = unwrapModel(t, updated)
	content, err := os.ReadFile(logger.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(remoteOutput) {
		t.Fatalf("log contents = %q, want output only %q", content, remoteOutput)
	}
	if len(state.outputLoggers) != 0 || pane.outputLogClosing {
		t.Fatalf("log close state = tracked %d, closing %v", len(state.outputLoggers), pane.outputLogClosing)
	}
}

func TestOutputLogShortcutRequiresConnectedPane(t *testing.T) {
	pane := &terminalPane{id: "pane-1", host: model.Host{Name: "offline"}}
	tab := &workspaceTab{id: "tab-1", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{activeTabID: tab.id, tabs: []*workspaceTab{tab}}

	updated, cmd := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, cmd = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'b', Text: "b"}))
	state = unwrapModel(t, updated)
	if cmd != nil || pane.outputLogger != nil || !pane.statusErr {
		t.Fatalf("offline output logging state: cmd=%v logger=%v error=%v", cmd != nil, pane.outputLogger != nil, pane.statusErr)
	}
}

func TestTerminalLogOptionsUseApplicationSettings(t *testing.T) {
	config := model.DefaultConfig()
	config.Logging.SegmentBytes = 4096
	config.Logging.RetainedSegments = 7
	config.Logging.MaxTotalBytes = 3 << 20
	config.Logging.MaxAgeDays = 14
	config.Logging.RedactPatterns = []string{`token=[A-Z0-9]{1,16}`}
	options := terminalLogOptions(config)
	if options.MaxBytes != 4096 || options.RetainedFiles != 7 || options.MaxTotalBytes != 3<<20 || options.MaxAge != 14*24*time.Hour || len(options.RedactPatterns) != 1 || options.RedactPatterns[0] != config.Logging.RedactPatterns[0] {
		t.Fatalf("terminal log options = %#v", options)
	}
	options.RedactPatterns[0] = "changed"
	if config.Logging.RedactPatterns[0] != `token=[A-Z0-9]{1,16}` {
		t.Fatal("logger options share the mutable config pattern slice")
	}
}

func TestTerminalLogPreviewDrainsDeviceStatusReplies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query.log")
	if err := os.WriteFile(path, []byte("before\x1b[6nafter"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct {
		terminal *vt.SafeEmulator
		err      error
	}, 1)
	go func() {
		terminal, err := loadTerminalLog(path, 40, 5)
		done <- struct {
			terminal *vt.SafeEmulator
			err      error
		}{terminal: terminal, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		defer result.terminal.Close()
		if rendered := result.terminal.Render(); !strings.Contains(rendered, "before") || !strings.Contains(rendered, "after") {
			t.Fatalf("terminal log preview lost text around query: %q", rendered)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal log preview blocked on a device-status query")
	}
}

func TestOutputLogRedactsStoredOutputWithoutChangingLiveTerminal(t *testing.T) {
	config := model.DefaultConfig()
	config.Logging.RedactPatterns = []string{`token=[a-z0-9]{1,16}`}
	logger, err := terminalog.NewWithOptions(filepath.Join(t.TempDir(), "logs"), "redacted", terminalLogOptions(config))
	if err != nil {
		t.Fatal(err)
	}
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id: "pane-redacted", host: model.Host{Name: "redacted"}, session: session,
		terminal: vt.NewSafeEmulator(80, 5), outputLogger: logger,
	}
	tab := &workspaceTab{id: "tab-redacted", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{
		width: 80, height: 12, workspaceVisible: true, activeTabID: tab.id,
		tabs: []*workspaceTab{tab}, config: config, outputLoggers: map[*terminalog.Logger]struct{}{logger: {}},
	}
	output := []byte("token=abc123\r\n")
	updated, _ := state.Update(terminalOutputMsg{session: session, data: output})
	state = unwrapModel(t, updated)
	if live := pane.terminal.Render(); !strings.Contains(live, "token=abc123") {
		t.Fatalf("live terminal output was changed by log redaction: %q", live)
	}
	closeCmd := state.toggleActiveTerminalOutputLog()
	if closeCmd == nil {
		t.Fatal("stopping the redacted logger returned no command")
	}
	closed, ok := closeCmd().(terminalOutputLogClosedMsg)
	if !ok || closed.err != nil {
		t.Fatalf("close result = %#v", closed)
	}
	stored, err := os.ReadFile(logger.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "token=abc123") || !strings.Contains(string(stored), "[REDACTED]") {
		t.Fatalf("stored output did not redact the token: %q", stored)
	}
}
