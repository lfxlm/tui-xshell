package ui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tui-ssh-workbench/internal/infra/sftpclient"
)

func TestParseEditorCommand(t *testing.T) {
	got, err := parseEditorCommand(`"/Applications/Code Editor.app/Contents/MacOS/code" --wait`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/Applications/Code Editor.app/Contents/MacOS/code", "--wait"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed command = %#v, want %#v", got, want)
	}
	got, err = parseEditorCommand(`nvim -f`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"nvim", "-f"}) {
		t.Fatalf("parsed simple command = %#v", got)
	}
}

func TestParseEditorCommandRejectsInvalidValues(t *testing.T) {
	for _, input := range []string{"", `"unterminated`, `''`} {
		if _, err := parseEditorCommand(input); err == nil {
			t.Errorf("parseEditorCommand(%q) succeeded, want error", input)
		}
	}
}

func TestParseEditorCommandPreservesWindowsExecutablePaths(t *testing.T) {
	got, err := parseEditorCommandForPlatform(`"C:\Program Files\Microsoft VS Code\Code.exe" --wait`, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`C:\Program Files\Microsoft VS Code\Code.exe`, "--wait"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed Windows command = %#v, want %#v", got, want)
	}
	got, err = parseEditorCommandForPlatform(`C:\tools\nvim.exe`, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{`C:\tools\nvim.exe`}) {
		t.Fatalf("parsed unquoted Windows path = %#v", got)
	}
}

func TestExternalEditorBufferRoundTrip(t *testing.T) {
	editor := &sftpEditorState{
		name: "config.yaml", lines: []string{"first", "second"},
		lineEndings: []string{"\r\n"},
	}
	path, err := writeExternalEditorBuffer(editor)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("temporary editor file mode = %o, want %o", got, want)
	}
	content, err := readExternalEditorBuffer(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), "first\r\nsecond"; got != want {
		t.Fatalf("temporary editor content = %q, want %q", got, want)
	}
	if got, want := path[len(path)-len(".edit.yaml"):], ".edit.yaml"; got != want {
		t.Fatalf("temporary editor filename suffix = %q, want %q", got, want)
	}
}

func TestSFTPExternalEditorResultUpdatesBufferOnlyAfterSuccess(t *testing.T) {
	temp, err := os.CreateTemp(t.TempDir(), "external-editor-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	path := temp.Name()
	if _, err := temp.WriteString("new\r\ncontent"); err != nil {
		t.Fatal(err)
	}
	if err := temp.Close(); err != nil {
		t.Fatal(err)
	}
	editor := &sftpEditorState{
		mode: sftpEditMode, original: "old", lines: []string{"old"},
		lineEndings: []string{}, row: 0, column: 1,
	}
	state := modelState{height: 20, sftp: &sftpBrowserState{
		generation: 4, requestID: 7, editor: editor, busy: true,
	}}
	state.handleSFTPExternalEditorFinished(sftpExternalEditorFinishedMsg{
		generation: 4, requestID: 7, tempPath: path,
	})
	if editor.original != "old" || !editor.dirty || serializeSFTPText(editor.lines, editor.lineEndings) != "new\r\ncontent" {
		t.Fatalf("external result was not loaded as a pending edit: %#v", editor)
	}
	if state.sftp.busy || state.sftp.statusErr {
		t.Fatalf("external editor completion state is incorrect: %#v", state.sftp)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("external editor temporary file remains: %v", err)
	}
}

func TestSFTPExternalEditorDirectModeSavesLocalFileOnExit(t *testing.T) {
	localRoot := t.TempDir()
	localPath := filepath.Join(localRoot, "config.txt")
	if err := os.WriteFile(localPath, []byte("before\r\nvalue"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, version, err := readLocalTextFile(localPath, sftpclient.MaxEditableFileSize)
	if err != nil {
		t.Fatal(err)
	}
	temp, err := os.CreateTemp(t.TempDir(), "external-editor-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	tempPath := temp.Name()
	if _, err := temp.WriteString("after\nvalue"); err != nil {
		t.Fatal(err)
	}
	if err := temp.Close(); err != nil {
		t.Fatal(err)
	}
	editor := &sftpEditorState{
		mode: sftpEditMode, side: sftpLocalSide, path: localPath,
		original: "before\r\nvalue", lines: []string{"before", "value"},
		lineEndings: []string{"\r\n"}, version: version,
	}
	state := modelState{height: 20, sftp: &sftpBrowserState{
		standalone: true, generation: 11, requestID: 4, localPath: localRoot,
		activeSide: sftpLocalSide, editor: editor, busy: true,
	}}
	command := state.handleSFTPExternalEditorFinished(sftpExternalEditorFinishedMsg{
		generation: 11, requestID: 4, tempPath: tempPath, autoSave: true,
	})
	if command == nil || !state.sftp.busy {
		t.Fatal("direct edit did not start the automatic save")
	}
	rawMessage := command()
	saved, ok := rawMessage.(sftpEditorSavedMsg)
	if !ok {
		t.Fatalf("automatic save returned %T, want sftpEditorSavedMsg", rawMessage)
	}
	state.handleSFTPTextSaved(saved)
	content, err := os.ReadFile(localPath)
	if err != nil || string(content) != "after\nvalue" {
		t.Fatalf("direct edit saved %q, %v; want updated content", content, err)
	}
	if state.sftp.editor.dirty || state.sftp.statusErr || state.sftp.status != "文件已保存" {
		t.Fatalf("direct edit save state = %#v", state.sftp)
	}
}

func TestSFTPExternalEditorDirectModePreservesConcurrentChanges(t *testing.T) {
	localRoot := t.TempDir()
	localPath := filepath.Join(localRoot, "config.txt")
	if err := os.WriteFile(localPath, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, version, err := readLocalTextFile(localPath, sftpclient.MaxEditableFileSize)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localPath, []byte("changed elsewhere"), 0o640); err != nil {
		t.Fatal(err)
	}
	temp, err := os.CreateTemp(t.TempDir(), "external-editor-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	tempPath := temp.Name()
	if _, err := temp.WriteString("editor result"); err != nil {
		t.Fatal(err)
	}
	if err := temp.Close(); err != nil {
		t.Fatal(err)
	}
	editor := &sftpEditorState{
		mode: sftpEditMode, side: sftpLocalSide, path: localPath,
		original: "original", lines: []string{"original"}, lineEndings: []string{}, version: version,
	}
	state := modelState{height: 20, sftp: &sftpBrowserState{
		standalone: true, generation: 12, requestID: 8, localPath: localRoot,
		activeSide: sftpLocalSide, editor: editor, busy: true,
	}}
	command := state.handleSFTPExternalEditorFinished(sftpExternalEditorFinishedMsg{
		generation: 12, requestID: 8, tempPath: tempPath, autoSave: true,
	})
	if command == nil {
		t.Fatal("direct edit did not attempt a conflict-checked save")
	}
	saved, ok := command().(sftpEditorSavedMsg)
	if !ok {
		t.Fatal("direct edit save returned an unexpected message")
	}
	state.handleSFTPTextSaved(saved)
	content, err := os.ReadFile(localPath)
	if err != nil || string(content) != "changed elsewhere" {
		t.Fatalf("direct edit overwrote a concurrent change: %q, %v", content, err)
	}
	if !state.sftp.statusErr || !strings.Contains(state.sftp.status, "保存冲突") || !state.sftp.editor.dirty {
		t.Fatalf("direct edit conflict did not retain the edited buffer: status=%q err=%v editor=%#v", state.sftp.status, state.sftp.statusErr, state.sftp.editor)
	}
}

func TestSFTPExternalEditorRejectsInvalidOutputAndPreservesBuffer(t *testing.T) {
	temp, err := os.CreateTemp(t.TempDir(), "external-editor-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	path := temp.Name()
	if _, err := temp.Write([]byte{0xff, 0xfe}); err != nil {
		t.Fatal(err)
	}
	if err := temp.Close(); err != nil {
		t.Fatal(err)
	}
	editor := &sftpEditorState{mode: sftpEditMode, original: "old", lines: []string{"old"}}
	state := modelState{height: 20, sftp: &sftpBrowserState{
		generation: 2, requestID: 3, editor: editor, busy: true,
	}}
	state.handleSFTPExternalEditorFinished(sftpExternalEditorFinishedMsg{
		generation: 2, requestID: 3, tempPath: path,
	})
	if serializeSFTPText(editor.lines, editor.lineEndings) != "old" || editor.dirty {
		t.Fatalf("invalid external output changed editor buffer: %#v", editor)
	}
	if !state.sftp.statusErr || state.sftp.busy {
		t.Fatalf("invalid output state = %#v", state.sftp)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid external output temporary file remains: %v", err)
	}
}

func TestExternalEditorRejectsOversizedBuffer(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "external-editor-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	tooLarge := make([]byte, sftpclient.MaxEditableFileSize+1)
	if _, err := file.Write(tooLarge); err != nil {
		t.Fatal(err)
	}
	if _, err := readExternalEditorBuffer(file.Name()); err == nil {
		t.Fatal("oversized file was read successfully")
	}
}
