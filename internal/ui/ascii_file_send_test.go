package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/model"
)

type asciiSendTestSession struct {
	mu     sync.Mutex
	writes [][]byte
	err    error
}

func (*asciiSendTestSession) Read([]byte) (int, error) { return 0, nil }
func (s *asciiSendTestSession) Write(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.writes = append(s.writes, append([]byte(nil), data...))
	return nil
}
func (*asciiSendTestSession) Resize(int, int) error { return nil }
func (*asciiSendTestSession) Close() error          { return nil }

func (s *asciiSendTestSession) captured() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []byte
	for _, write := range s.writes {
		result = append(result, write...)
	}
	return result
}

func TestNormalizeAndSplitASCIIFileLineEndings(t *testing.T) {
	input := []byte("one\r\ntwo\nthree\rfour\n\n")
	got := normalizeASCIIFileLineEndings(input)
	want := []byte("one\rtwo\rthree\rfour\r\r")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized bytes = %q, want %q", got, want)
	}
	lines := splitASCIIFileSendLines(got)
	var flattened []byte
	for _, line := range lines {
		flattened = append(flattened, line...)
	}
	if !reflect.DeepEqual(flattened, want) {
		t.Fatalf("sent line bytes = %q, want %q", flattened, want)
	}
}

func TestPrepareASCIIFileSendContentConvertsSourceAndTerminalEncodings(t *testing.T) {
	utf8Content := []byte("show 版本\r\nshow 接口\n")
	preview, gb18030, err := prepareASCIIFileSendContent(utf8Content, "自动检测", "GB18030")
	if err != nil {
		t.Fatalf("convert UTF-8 source to GB18030: %v", err)
	}
	if got, want := string(preview), "show 版本\rshow 接口\r"; got != want {
		t.Fatalf("Unicode preview = %q, want %q", got, want)
	}
	decoded, err := decodeSFTPTextWithEncoding(gb18030, "GB18030")
	if err != nil || decoded != string(preview) {
		t.Fatalf("GB18030 send content decodes to %q, err=%v", decoded, err)
	}

	gbSource, err := encodeSFTPText("状态\n", "GB18030")
	if err != nil {
		t.Fatal(err)
	}
	_, utf8Output, err := prepareASCIIFileSendContent(gbSource, "GB18030", "UTF-8")
	if err != nil || string(utf8Output) != "状态\r" {
		t.Fatalf("GB18030 to UTF-8 output = %q, err=%v", utf8Output, err)
	}
	if _, _, err := prepareASCIIFileSendContent([]byte("中文"), "UTF-8", "Windows-1252"); err == nil {
		t.Fatal("unrepresentable text unexpectedly converted to Windows-1252")
	}
}

func TestSendASCIIFileContentWaitsForPromptBeforeNextLine(t *testing.T) {
	session := &asciiSendTestSession{}
	feed := newTerminalPromptFeed()
	progress := make(chan asciiSendProgress, 1)
	options := asciiFileSendOptions{prompt: "ready>", maxWait: time.Second}
	result := make(chan error, 1)
	go func() {
		_, err := sendASCIIFileContent(context.Background(), session, []byte("first\rsecond"), options, feed, progress)
		result <- err
	}()

	deadline := time.Now().Add(time.Second)
	for string(session.captured()) != "first\r" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := string(session.captured()); got != "first\r" {
		t.Fatalf("before prompt, sent %q; want first line only", got)
	}
	feed.Append([]byte("\x1b[32mrea"))
	feed.Append([]byte("dy>\x1b[0m"))
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("send failed after receiving prompt: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("send did not continue after prompt")
	}
	if got := string(session.captured()); got != "first\rsecond" {
		t.Fatalf("sent %q, want %q", got, "first\rsecond")
	}
}

func TestSendASCIIFileContentCancellationInterruptsDelayAndPromptWait(t *testing.T) {
	t.Run("character delay", func(t *testing.T) {
		session := &asciiSendTestSession{}
		ctx, cancel := context.WithCancel(context.Background())
		progress := make(chan asciiSendProgress, 1)
		done := make(chan error, 1)
		go func() {
			_, err := sendASCIIFileContent(ctx, session, []byte("ab"), asciiFileSendOptions{characterDelay: time.Second}, newTerminalPromptFeed(), progress)
			done <- err
		}()
		deadline := time.Now().Add(time.Second)
		for string(session.captured()) != "a" && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("send error = %v, want context.Canceled", err)
			}
		case <-time.After(250 * time.Millisecond):
			t.Fatal("cancel did not interrupt the character delay")
		}
	})

	t.Run("prompt wait", func(t *testing.T) {
		session := &asciiSendTestSession{}
		ctx, cancel := context.WithCancel(context.Background())
		progress := make(chan asciiSendProgress, 1)
		done := make(chan error, 1)
		go func() {
			_, err := sendASCIIFileContent(ctx, session, []byte("first\rsecond"), asciiFileSendOptions{prompt: ">", maxWait: time.Minute}, newTerminalPromptFeed(), progress)
			done <- err
		}()
		deadline := time.Now().Add(time.Second)
		for string(session.captured()) != "first\r" && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("send error = %v, want context.Canceled", err)
			}
		case <-time.After(250 * time.Millisecond):
			t.Fatal("cancel did not interrupt the prompt wait")
		}
		if got := string(session.captured()); got != "first\r" {
			t.Fatalf("bytes sent after cancellation = %q", got)
		}
	})
}

func TestSendASCIIFileContentStopsAfterPromptTimeout(t *testing.T) {
	session := &asciiSendTestSession{}
	progress := make(chan asciiSendProgress, 1)
	_, err := sendASCIIFileContent(
		context.Background(), session, []byte("first\rsecond"),
		asciiFileSendOptions{prompt: "ready>", maxWait: 10 * time.Millisecond},
		newTerminalPromptFeed(), progress,
	)
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("prompt wait error = %v, want timeout", err)
	}
	if got := string(session.captured()); got != "first\r" {
		t.Fatalf("bytes sent after prompt timeout = %q, want first line only", got)
	}
}

func TestASCIIFileSendReadRejectsEmptyBinaryAndOversizedFiles(t *testing.T) {
	directory := t.TempDir()
	for name, content := range map[string][]byte{"empty": {}, "binary": {'a', 0, 'b'}} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readASCIIFileSend(path); err == nil {
			t.Errorf("accepted %s file", name)
		}
	}
	oversized := filepath.Join(directory, "oversized")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxASCIIFileSendBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readASCIIFileSend(oversized); err == nil || !strings.Contains(err.Error(), "限制") {
		t.Fatalf("oversized file error = %v, want size limit", err)
	}
}

func TestASCIIFileSendOptionsValidateDelaysAndPromptTimeout(t *testing.T) {
	state := &asciiFileSendState{characterDelayText: "1001", lineDelayText: "0", maxWaitText: "30"}
	if _, err := parseASCIIFileSendOptions(state); err == nil {
		t.Fatal("accepted character delay above limit")
	}
	state.characterDelayText = "0"
	state.prompt = ">"
	state.maxWaitText = "0"
	if _, err := parseASCIIFileSendOptions(state); err == nil {
		t.Fatal("accepted prompt without a bounded timeout")
	}
	state.maxWaitText = "30"
	if _, err := parseASCIIFileSendOptions(state); err != nil {
		t.Fatalf("valid send delay options rejected: %v", err)
	}
}

func TestASCIIFileSendOptionsEncodePromptInTerminalEncoding(t *testing.T) {
	state := &asciiFileSendState{
		sourceEncoding: "自动检测", terminalEncoding: "GB18030",
		characterDelayText: "0", lineDelayText: "0", prompt: "就绪>", maxWaitText: "30",
	}
	options, err := parseASCIIFileSendOptions(state)
	if err != nil {
		t.Fatalf("parse options: %v", err)
	}
	want, err := encodeSFTPText("就绪>", "GB18030")
	if err != nil {
		t.Fatal(err)
	}
	if options.prompt != string(want) || options.terminalEncoding != "GB18030" {
		t.Fatalf("encoded prompt/options = %x / %#v, want prompt %x", options.prompt, options, want)
	}
}

func TestASCIIFileSendEncodingChoicesCycleWithArrowKeys(t *testing.T) {
	state := modelState{asciiFileSend: &asciiFileSendState{
		phase: asciiFileSendEdit, sourceEncoding: "自动检测", terminalEncoding: "UTF-8",
	}}
	state.asciiFileSend.field = 1
	updated, _ := state.updateASCIIFileSendKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	state = unwrapModel(t, updated)
	if state.asciiFileSend.sourceEncoding != "UTF-8" {
		t.Fatalf("source encoding after Right = %q, want UTF-8", state.asciiFileSend.sourceEncoding)
	}
	state.asciiFileSend.field = 2
	updated, _ = state.updateASCIIFileSendKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	state = unwrapModel(t, updated)
	if state.asciiFileSend.terminalEncoding != "GB18030" {
		t.Fatalf("terminal encoding after Right = %q, want GB18030", state.asciiFileSend.terminalEncoding)
	}
	state.appendASCIIFileSendPaste("must-not-replace-choice")
	if state.asciiFileSend.terminalEncoding != "GB18030" {
		t.Fatalf("paste changed a choice field to %q", state.asciiFileSend.terminalEncoding)
	}
}

func TestSplitASCIIFileSendUnitsRespectsLegacyEncodingCharacters(t *testing.T) {
	data, err := encodeSFTPText("中文A", "GB18030")
	if err != nil {
		t.Fatal(err)
	}
	units := splitASCIIFileSendUnitsWithEncoding(data, "GB18030")
	if len(units) != 3 {
		t.Fatalf("GB18030 units = %x, want two double-byte characters and ASCII", units)
	}
	if string(append(append([]byte(nil), units[0]...), units[1]...)) != string(data[:4]) || string(units[2]) != "A" {
		t.Fatalf("GB18030 units = %x, want intact encoded characters", units)
	}
}

func TestWorkspaceShortcutOpensASCIIFileSendDialogWithoutWritingToTerminal(t *testing.T) {
	session := &workspaceTestSession{}
	pane := &terminalPane{id: "pane-ascii", host: model.Host{Name: "test"}, session: session, terminal: vt.NewSafeEmulator(40, 10)}
	state := modelState{
		config: model.DefaultConfig(), width: 100, height: 28, workspaceVisible: true, activeTabID: "tab-ascii",
		tabs: []*workspaceTab{{id: "tab-ascii", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'f', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	if state.asciiFileSend == nil || state.asciiFileSend.paneID != pane.id {
		t.Fatal("Ctrl+G then Ctrl+F did not open the ASCII file send dialog for the active pane")
	}
	if len(session.writes) != 0 {
		t.Fatalf("opening the dialog sent bytes to the terminal: %q", session.writes)
	}
	panel, _, _, _, _ := state.asciiFileSendLayout()
	if !strings.Contains(panel, "字符间隔") || !strings.Contains(panel, "等待提示符") || !strings.Contains(panel, "源文件编码") || !strings.Contains(panel, "终端编码") {
		t.Fatalf("send-delay controls missing from the dialog:\n%s", panel)
	}
}

func TestASCIIFileSendDialogPreviewsNormalizedTextBeforeSending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.txt")
	if err := os.WriteFile(path, []byte("show version\r\nshow interfaces\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &workspaceTestSession{}
	pane := &terminalPane{id: "pane-preview", host: model.Host{Name: "router"}, session: session, terminal: vt.NewSafeEmulator(80, 20)}
	state := modelState{
		width: 100, height: 30, workspaceVisible: true, activeTabID: "tab-preview",
		tabs: []*workspaceTab{{id: "tab-preview", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}
	state.openASCIIFileSend()
	state.asciiFileSend.path = path
	load := state.loadASCIIFileSend()
	if load == nil {
		t.Fatal("load command is nil")
	}
	updated, _ := state.Update(load())
	state = unwrapModel(t, updated)
	if state.asciiFileSend == nil || state.asciiFileSend.phase != asciiFileSendReview {
		t.Fatalf("file did not enter review phase: %#v", state.asciiFileSend)
	}
	if got, want := string(state.asciiFileSend.output), "show version\rshow interfaces\r"; got != want {
		t.Fatalf("normalized preview bytes = %q, want %q", got, want)
	}
	if len(session.writes) != 0 {
		t.Fatalf("file was sent before explicit confirmation: %q", session.writes)
	}
	panel, _, _, _, _ := state.asciiFileSendLayout()
	if !strings.Contains(panel, "show version") || !strings.Contains(panel, "Enter 开始发送") {
		t.Fatalf("review panel is missing preview or confirmation:\n%s", panel)
	}
}
