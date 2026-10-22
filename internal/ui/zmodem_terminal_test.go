package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/model"
)

func zmodemTestState() (modelState, *terminalPane, *workspaceTestSession) {
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id: "pane-zmodem", host: model.Host{Id: "host-zmodem", Kind: model.HostKindSSH, Name: "test"},
		session: session, terminal: vt.NewSafeEmulator(80, 20),
	}
	state := modelState{
		width: 100, height: 30, workspaceVisible: true, activeTabID: "tab-zmodem",
		tabs: []*workspaceTab{{id: "tab-zmodem", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}
	return state, pane, session
}

func updateZMODEMModel(t *testing.T, state modelState, msg tea.Msg) modelState {
	t.Helper()
	updated, _ := state.Update(msg)
	return unwrapModel(t, updated)
}

func TestDetectZMODEMStartAcrossChunks(t *testing.T) {
	visible, protocol, pending, mode, found := detectZMODEMStart(nil, []byte("shell output\r\n**\x18B0"))
	if string(visible) != "shell output\r\n" || protocol != nil || string(pending) != "**\x18B0" || found {
		t.Fatalf("first detection = visible %q, protocol %q, pending %q, mode %v, found %v", visible, protocol, pending, mode, found)
	}
	visible, protocol, pending, mode, found = detectZMODEMStart(pending, []byte("0payload"))
	if len(visible) != 0 || string(protocol) != "**\x18B00payload" || len(pending) != 0 || !found || mode != zmodemReceive {
		t.Fatalf("second detection = visible %q, protocol %q, pending %q, mode %v, found %v", visible, protocol, pending, mode, found)
	}
}

func TestDetectZMODEMSendAndRetainsPossiblePrefix(t *testing.T) {
	visible, protocol, pending, mode, found := detectZMODEMStart(nil, []byte("ordinary **\x18B0"))
	if string(visible) != "ordinary " || len(protocol) != 0 || string(pending) != "**\x18B0" || found {
		t.Fatalf("possible-prefix result = visible %q protocol %q pending %q found %v", visible, protocol, pending, found)
	}
	visible, protocol, pending, mode, found = detectZMODEMStart(pending, []byte("1more"))
	if len(visible) != 0 || string(protocol) != "**\x18B01more" || len(pending) != 0 || !found || mode != zmodemSend {
		t.Fatalf("send detection = visible %q protocol %q pending %q mode %v found %v", visible, protocol, pending, mode, found)
	}
}

func TestXYMODEMChooserSelectsProtocolAndDirection(t *testing.T) {
	state, _, _ := zmodemTestState()
	state.openXYMODEMPrompt()
	if state.zmodemTransfer == nil || state.zmodemTransfer.phase != zmodemChoose {
		t.Fatal("X/YMODEM chooser did not open")
	}
	state = updateZMODEMModel(t, state, tea.KeyPressMsg(tea.Key{Text: "3", Code: '3'}))
	if state.zmodemTransfer.phase != zmodemPrompt || state.zmodemTransfer.protocol != terminalTransferYMODEM || state.zmodemTransfer.mode != zmodemReceive {
		t.Fatalf("YMODEM receive selection = %#v", state.zmodemTransfer)
	}
	state = updateZMODEMModel(t, state, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if state.zmodemTransfer != nil {
		t.Fatal("Esc did not close the X/YMODEM prompt")
	}
}

func TestXYMODEMSendRejectsMultipleXMODEMFiles(t *testing.T) {
	state, _, _ := zmodemTestState()
	state.openXYMODEMPrompt()
	state.zmodemTransfer.chooseXYMODEM(1)
	first := filepath.Join(t.TempDir(), "first.bin")
	second := filepath.Join(t.TempDir(), "second.bin")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state.zmodemTransfer.path = first + "\n" + second
	if command := state.startZMODEMTransfer(); command != nil {
		t.Fatal("XMODEM send unexpectedly started for multiple files")
	}
	if state.zmodemTransfer.errorText != "XMODEM 每次只能发送一个文件" {
		t.Fatalf("validation error = %q", state.zmodemTransfer.errorText)
	}
}

func TestZMODEMInputReadCloseAndDrain(t *testing.T) {
	input := newZMODEMInput()
	if err := input.Append([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 2)
	if n, err := input.Read(first); err != nil || n != 2 || string(first) != "ab" {
		t.Fatalf("first read = %q, %v, %d", first, err, n)
	}
	if err := input.Append([]byte("gh")); err != nil {
		t.Fatal(err)
	}
	input.Close(io.EOF)
	remaining := make([]byte, 4)
	if n, err := input.Read(remaining); err != nil || n != 4 || string(remaining) != "cdef" {
		t.Fatalf("buffered read = %q, %v, %d", remaining, err, n)
	}
	if trailing := input.Drain(); string(trailing) != "gh" {
		t.Fatalf("Drain() = %q, want gh", trailing)
	}
	if _, err := input.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("read after drain error = %v, want EOF", err)
	}
}

func TestZMODEMInputCloseUnblocksReader(t *testing.T) {
	input := newZMODEMInput()
	result := make(chan error, 1)
	go func() {
		_, err := input.Read(make([]byte, 1))
		result <- err
	}()
	input.Close(context.Canceled)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked read error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("closing ZMODEM input did not unblock its reader")
	}
}

func TestZMODEMInputRejectsOversizedQueue(t *testing.T) {
	input := newZMODEMInput()
	tooLarge := bytes.Repeat([]byte{'x'}, maxQueuedZMODEMBytes+1)
	if err := input.Append(tooLarge); !errors.Is(err, errZMODEMInputLimit) {
		t.Fatalf("Append oversized data error = %v, want limit error", err)
	}
}

func TestZMODEMTerminalOutputOpensReceivePromptWithoutRenderingProtocolBytes(t *testing.T) {
	state, pane, session := zmodemTestState()
	state = updateZMODEMModel(t, state, terminalOutputMsg{session: session, data: []byte("shell banner\r\n**\x18B0")})
	if state.zmodemTransfer != nil {
		t.Fatal("opened a prompt before receiving a complete ZMODEM marker")
	}
	if string(pane.zmodemProbe) != "**\x18B0" {
		t.Fatalf("pending marker prefix = %q", pane.zmodemProbe)
	}

	state = updateZMODEMModel(t, state, terminalOutputMsg{session: session, data: []byte("0PAYLOAD")})
	if state.zmodemTransfer == nil || state.zmodemTransfer.mode != zmodemReceive || state.zmodemTransfer.phase != zmodemPrompt {
		t.Fatalf("receive prompt state = %#v", state.zmodemTransfer)
	}
	if got := string(state.zmodemTransfer.input.Drain()); got != "**\x18B00PAYLOAD" {
		t.Fatalf("protocol input = %q", got)
	}
	rendered := pane.terminal.Render()
	if !strings.Contains(rendered, "shell banner") {
		t.Fatalf("ordinary terminal output was lost:\n%s", rendered)
	}
	if strings.Contains(rendered, "PAYLOAD") || strings.Contains(rendered, "**") {
		t.Fatalf("ZMODEM protocol bytes leaked into terminal:\n%s", rendered)
	}
}

func TestZMODEMTerminalOutputOpensSendPromptAndPasteSetsPath(t *testing.T) {
	state, _, session := zmodemTestState()
	state = updateZMODEMModel(t, state, terminalOutputMsg{session: session, data: []byte("**\x18B01")})
	if state.zmodemTransfer == nil || state.zmodemTransfer.mode != zmodemSend {
		t.Fatalf("send prompt state = %#v", state.zmodemTransfer)
	}

	path := filepath.Join(t.TempDir(), "send me.txt")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated, _ := state.Update(tea.PasteMsg{Content: path})
	state = unwrapModel(t, updated)
	if state.zmodemTransfer.path != path {
		t.Fatalf("pasted path = %q, want %q", state.zmodemTransfer.path, path)
	}
	updated, command := state.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if state.zmodemTransfer.phase != zmodemRunning || command == nil {
		t.Fatalf("confirm did not start transfer: phase=%v command=%v", state.zmodemTransfer.phase, command != nil)
	}
	if len(session.writes) != 0 {
		t.Fatalf("transfer command ran before Bubble Tea executed it: %q", session.writes)
	}
}

func TestZMODEMSendPromptAcceptsMultiplePastedPaths(t *testing.T) {
	state, _, session := zmodemTestState()
	state = updateZMODEMModel(t, state, terminalOutputMsg{session: session, data: []byte("**\x18B01")})
	first := filepath.Join(t.TempDir(), "first file.txt")
	second := filepath.Join(t.TempDir(), "second file.txt")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	updated, _ := state.Update(tea.PasteMsg{Content: first + "\n" + second + "\n"})
	state = unwrapModel(t, updated)
	if state.zmodemTransfer.path != first+"\n"+second+"\n" {
		t.Fatalf("pasted paths = %q", state.zmodemTransfer.path)
	}
	updated, command := state.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if state.zmodemTransfer.phase != zmodemRunning || command == nil {
		t.Fatalf("multi-file confirm did not start transfer: phase=%v command=%v error=%q", state.zmodemTransfer.phase, command != nil, state.zmodemTransfer.errorText)
	}
	if got, want := strings.Split(state.zmodemTransfer.path, "\n"), []string{first, second}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("normalized send paths = %q, want %q", got, want)
	}
}

func TestZMODEMSendPathsRequireRegularFiles(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "file.txt")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := zmodemSendPaths("\n" + file + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != file {
		t.Fatalf("zmodemSendPaths() = %q", paths)
	}
	if _, err := zmodemSendPaths(directory); err == nil {
		t.Fatal("directory was accepted as a send file")
	}
	if _, err := zmodemSendPaths(filepath.Join(directory, "missing")); err == nil {
		t.Fatal("missing file was accepted")
	}
}

func TestZMODEMCancelPromptSendsCancelBytes(t *testing.T) {
	state, _, session := zmodemTestState()
	state = updateZMODEMModel(t, state, terminalOutputMsg{session: session, data: []byte("**\x18B00")})
	updated, _ := state.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.zmodemTransfer != nil {
		t.Fatal("Esc did not close the ZMODEM prompt")
	}
	if len(session.writes) != 1 || !bytes.Equal(session.writes[0], bytes.Repeat([]byte{0x18}, 8)) {
		t.Fatalf("cancel writes = %q, want eight CAN bytes", session.writes)
	}
}

func TestZMODEMActiveTransferCapturesOutputInsteadOfRenderingIt(t *testing.T) {
	state, pane, session := zmodemTestState()
	state = updateZMODEMModel(t, state, terminalOutputMsg{session: session, data: []byte("**\x18B00")})
	state.zmodemTransfer.phase = zmodemRunning
	state = updateZMODEMModel(t, state, terminalOutputMsg{session: session, data: []byte("DATA-CHUNK")})
	if got := string(state.zmodemTransfer.input.Drain()); got != "**\x18B00DATA-CHUNK" {
		t.Fatalf("captured protocol bytes = %q", got)
	}
	if rendered := pane.terminal.Render(); strings.Contains(rendered, "DATA-CHUNK") {
		t.Fatalf("active transfer bytes leaked into terminal:\n%s", rendered)
	}
}
