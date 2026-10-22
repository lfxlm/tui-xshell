package ui

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/model"
)

func TestTerminalEncodingDecodesSplitMultibyteOutputAndEncodesInput(t *testing.T) {
	pane := &terminalPane{terminalEncoding: "GB18030"}
	want := "你好，世界"
	wire, err := encodeTerminalInput(pane, []byte(want))
	if err != nil {
		t.Fatalf("encode terminal input: %v", err)
	}
	if string(wire) == want {
		t.Fatal("GB18030 input was not converted from UTF-8")
	}
	split := 1
	first, err := decodeTerminalOutput(pane, wire[:split], false)
	if err != nil {
		t.Fatalf("decode first partial output: %v", err)
	}
	if len(first) != 0 {
		t.Fatalf("incomplete character was rendered early: %q", first)
	}
	second, err := decodeTerminalOutput(pane, wire[split:], false)
	if err != nil {
		t.Fatalf("decode remaining output: %v", err)
	}
	if string(append(first, second...)) != want {
		t.Fatalf("decoded output = %q, want %q", append(first, second...), want)
	}
}

func TestGBKTerminalCodecIsDistinctFromGB18030(t *testing.T) {
	const text = "简体中文"
	encoded, err := encodeTerminalText("GBK", []byte(text))
	if err != nil {
		t.Fatalf("encode GBK text: %v", err)
	}
	if bytes.Equal(encoded, []byte(text)) {
		t.Fatal("GBK encoding did not convert UTF-8 input")
	}
	pane := &terminalPane{terminalEncoding: "GBK"}
	decoded, err := decodeTerminalOutput(pane, encoded, true)
	if err != nil || string(decoded) != text {
		t.Fatalf("decode GBK output = %q, err=%v; want %q", decoded, err, text)
	}
	if _, err := encodeTerminalText("GBK", []byte("𠀀")); err == nil {
		t.Fatal("GBK accepted a character outside the encoding")
	}
	if _, err := encodeTerminalText("GB18030", []byte("𠀀")); err != nil {
		t.Fatalf("GB18030 rejected an extension character: %v", err)
	}
}

func TestImportedXShellOutputCodePageDoesNotChangeKeyboardInput(t *testing.T) {
	const text = "中文输入"
	pane := &terminalPane{
		terminalEncoding:      "GB18030",
		terminalInputEncoding: model.TerminalEncodingUTF8,
	}
	wire, err := encodeTerminalInput(pane, []byte(text))
	if err != nil {
		t.Fatalf("encode UTF-8 keyboard input: %v", err)
	}
	if string(wire) != text {
		t.Fatalf("keyboard input bytes = %x, want UTF-8 %x", wire, []byte(text))
	}
	if got := terminalEncodingSummary(pane); got != "显示 GB18030 / 输入 UTF-8" {
		t.Fatalf("encoding summary = %q", got)
	}
}

func TestTerminalEncodingRejectsUnrepresentableInput(t *testing.T) {
	pane := &terminalPane{terminalEncoding: "Windows-1252"}
	if _, err := encodeTerminalInput(pane, []byte("你好")); err == nil {
		t.Fatal("input not representable in Windows-1252 was accepted")
	}
}

func TestTerminalOutputUsesSelectedEncodingForTUI(t *testing.T) {
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Name: "legacy"}, session: &workspaceTestSession{},
		terminal: vt.NewSafeEmulator(40, 5), terminalEncoding: "GB18030",
	}
	wire, err := encodeTerminalInput(pane, []byte("中文终端"))
	if err != nil {
		t.Fatal(err)
	}
	state := modelState{width: 40, height: 10}
	state.renderTerminalBytes(pane, wire, false)
	if rendered := pane.terminal.Render(); !strings.Contains(rendered, "中文终端") {
		t.Fatalf("VT screen did not render decoded text:\n%s", rendered)
	}
}

func TestWorkspaceInputUsesEncodingPerPane(t *testing.T) {
	firstSession, secondSession := &workspaceTestSession{}, &workspaceTestSession{}
	first := &terminalPane{id: "first", session: firstSession, terminal: vt.NewSafeEmulator(40, 5), terminalEncoding: "GB18030"}
	second := &terminalPane{id: "second", session: secondSession, terminal: vt.NewSafeEmulator(40, 5), terminalEncoding: "Big5"}
	tab := &workspaceTab{
		id: "tab-1", activePaneID: first.id, syncInput: true,
		root: &paneNode{axis: splitVertical, ratio: 50, first: &paneNode{pane: first}, second: &paneNode{pane: second}},
	}
	state := modelState{activeTabID: tab.id, tabs: []*workspaceTab{tab}}
	if errors := state.writeWorkspaceInput(func(*terminalPane) []byte { return []byte("你好") }, "send:"); errors != 0 {
		t.Fatalf("writeWorkspaceInput errors = %d", errors)
	}
	wantGB, _ := encodeTerminalInput(first, []byte("你好"))
	wantBig5, _ := encodeTerminalInput(second, []byte("你好"))
	if len(firstSession.writes) != 1 || string(firstSession.writes[0]) != string(wantGB) {
		t.Fatalf("GB18030 pane input = %x, want %x", firstSession.writes, wantGB)
	}
	if len(secondSession.writes) != 1 || string(secondSession.writes[0]) != string(wantBig5) {
		t.Fatalf("Big5 pane input = %x, want %x", secondSession.writes, wantBig5)
	}
}

func TestTerminalClipboardPasteUsesPaneEncoding(t *testing.T) {
	session := &workspaceTestSession{}
	pane := &terminalPane{id: "pane-1", session: session, terminalEncoding: "Big5"}
	tab := &workspaceTab{id: "tab-1", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{
		workspaceVisible: true, activeTabID: tab.id, tabs: []*workspaceTab{tab},
		clipboardPasteRequest: &terminalClipboardPasteRequest{tabID: tab.id, paneIDs: []string{pane.id}},
	}
	state.handleTerminalClipboard(tea.ClipboardMsg{Content: "你好"})
	want, err := encodeTerminalInput(pane, []byte("你好"))
	if err != nil {
		t.Fatal(err)
	}
	if len(session.writes) != 1 || string(session.writes[0]) != string(want) {
		t.Fatalf("clipboard bytes = %x, want Big5 %x", session.writes, want)
	}
}

func TestTerminalEncodingCyclesAndPersistsForHost(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "terminal-encoding.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "edge", IP: "127.0.0.1", Port: 22})
	if err != nil {
		t.Fatal(err)
	}
	first := &terminalPane{id: "pane-1", host: host, terminalEncoding: model.TerminalEncodingUTF8}
	second := &terminalPane{id: "pane-2", host: host, terminalEncoding: model.TerminalEncodingUTF8}
	firstTab := &workspaceTab{id: "tab-1", activePaneID: first.id, root: &paneNode{pane: first}}
	secondTab := &workspaceTab{id: "tab-2", activePaneID: second.id, root: &paneNode{pane: second}}
	state := modelState{
		catalog: store, hosts: []model.Host{host}, activeTabID: firstTab.id,
		tabs: []*workspaceTab{firstTab, secondTab},
	}
	command := state.cycleTerminalEncoding(firstTab.id, first.id)
	if command == nil || !first.terminalEncodingSaving || !second.terminalEncodingSaving {
		t.Fatal("switch did not start a host-wide save")
	}
	updated, _ := state.Update(command())
	state = unwrapModel(t, updated)
	if first.terminalEncoding != "GB18030" || second.terminalEncoding != "GB18030" || first.terminalEncodingSaving || second.terminalEncodingSaving {
		t.Fatalf("live panes after save: first=%q second=%q saving=(%v,%v)", first.terminalEncoding, second.terminalEncoding, first.terminalEncodingSaving, second.terminalEncodingSaving)
	}
	saved, err := store.GetHost(ctx, host.Id)
	if err != nil || saved.TerminalEncoding != "GB18030" {
		t.Fatalf("persisted terminal encoding = %q, err=%v", saved.TerminalEncoding, err)
	}
	if state.hostByID(host.Id).TerminalEncoding != "GB18030" {
		t.Fatalf("host list encoding = %q, want GB18030", state.hostByID(host.Id).TerminalEncoding)
	}
}

func TestTerminalEncodingSaveFailureRestoresPreviousEncoding(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "terminal-encoding-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "edge", IP: "127.0.0.1", Port: 22})
	if err != nil {
		t.Fatal(err)
	}
	pane := &terminalPane{id: "pane-1", host: host, terminalEncoding: model.TerminalEncodingUTF8}
	tab := &workspaceTab{id: "tab-1", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{catalog: store, hosts: []model.Host{host}, activeTabID: tab.id, tabs: []*workspaceTab{tab}}
	command := state.cycleTerminalEncoding(tab.id, pane.id)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	updated, _ := state.Update(command())
	state = unwrapModel(t, updated)
	if pane.terminalEncoding != model.TerminalEncodingUTF8 || pane.terminalEncodingSaving {
		t.Fatalf("failed save left encoding=%q saving=%v", pane.terminalEncoding, pane.terminalEncodingSaving)
	}
	if !state.statusErr || !strings.Contains(state.statusText, "保存终端编码失败") {
		t.Fatalf("save failure was not reported: %q", state.statusText)
	}
}

func TestTerminalEncodingOptionsAreStableAndDistinct(t *testing.T) {
	options := model.TerminalEncodingOptions()
	if len(options) != 6 || options[0] != model.TerminalEncodingUTF8 {
		t.Fatalf("terminal encoding options = %#v", options)
	}
	seen := make(map[string]bool, len(options))
	for _, option := range options {
		if seen[option] || !model.IsTerminalEncodingSupported(option) {
			t.Fatalf("invalid or duplicate terminal encoding option %q", option)
		}
		seen[option] = true
	}
	if got := nextTerminalEncoding("Windows-1252"); got != model.TerminalEncodingUTF8 {
		t.Fatalf("encoding cycle returned %q after Windows-1252", got)
	}
}
