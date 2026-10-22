package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSFTPTextEditorFindWrapsAndAdvancesCaseInsensitively(t *testing.T) {
	original := "Alpha beta\nbeta ALPHA\nnone"
	lines, endings := splitSFTPText(original)
	state := modelState{height: 12, sftp: &sftpBrowserState{editor: &sftpEditorState{
		mode: sftpEditMode, lines: lines, lineEndings: endings,
		original: original, newline: preferredSFTPNewline(endings),
	}}}

	updated, _ := state.updateSFTPTextEditor(tea.Key{Code: 'f', Mod: tea.ModCtrl})
	state = unwrapModel(t, updated)
	if state.sftp.prompt == nil || state.sftp.prompt.action != sftpPromptEditorFind {
		t.Fatalf("Ctrl+F did not open editor find: %#v", state.sftp.prompt)
	}
	state.sftp.prompt.value = "alpha"
	updated, _ = state.updateSFTPTextEditorPrompt(tea.Key{Code: tea.KeyEnter})
	state = unwrapModel(t, updated)
	if state.sftp.editor.row != 0 || state.sftp.editor.column != 0 {
		t.Fatalf("initial search landed at (%d,%d), want (0,0)", state.sftp.editor.row, state.sftp.editor.column)
	}

	updated, _ = state.updateSFTPTextEditor(tea.Key{Code: 'n', Mod: tea.ModCtrl})
	state = unwrapModel(t, updated)
	if state.sftp.editor.row != 1 || state.sftp.editor.column != 5 {
		t.Fatalf("next search landed at (%d,%d), want (1,5)", state.sftp.editor.row, state.sftp.editor.column)
	}
	updated, _ = state.updateSFTPTextEditor(tea.Key{Code: 'n', Mod: tea.ModCtrl})
	state = unwrapModel(t, updated)
	if state.sftp.editor.row != 0 || state.sftp.editor.column != 0 {
		t.Fatalf("wrapped search landed at (%d,%d), want (0,0)", state.sftp.editor.row, state.sftp.editor.column)
	}
}

func TestSFTPTextEditorReplaceCurrentAndMarkDirty(t *testing.T) {
	original := "cat\nkeep"
	lines, endings := splitSFTPText(original)
	state := modelState{height: 12, sftp: &sftpBrowserState{editor: &sftpEditorState{
		mode: sftpEditMode, lines: lines, lineEndings: endings,
		original: original, newline: preferredSFTPNewline(endings),
	}}}

	updated, _ := state.updateSFTPTextEditor(tea.Key{Code: 'r', Mod: tea.ModCtrl})
	state = unwrapModel(t, updated)
	state.sftp.prompt.value = "cat"
	updated, _ = state.updateSFTPTextEditorPrompt(tea.Key{Code: tea.KeyEnter})
	state = unwrapModel(t, updated)
	if state.sftp.prompt.action != sftpPromptEditorReplaceValue {
		t.Fatalf("replace query did not advance to replacement input: %#v", state.sftp.prompt)
	}
	state.sftp.prompt.value = "dog"
	updated, _ = state.updateSFTPTextEditorPrompt(tea.Key{Code: tea.KeyEnter})
	state = unwrapModel(t, updated)
	if got := serializeSFTPText(state.sftp.editor.lines, state.sftp.editor.lineEndings); got != "dog\nkeep" {
		t.Fatalf("replace current produced %q", got)
	}
	if !state.sftp.editor.dirty || state.sftp.editor.row != 0 || state.sftp.editor.column != 3 {
		t.Fatalf("replace state is incorrect: %#v", state.sftp.editor)
	}
}

func TestSFTPTextEditorReplaceAllIsCaseInsensitiveAndPreservesLineEndings(t *testing.T) {
	original := "cat Cat concatenate\r\ncat"
	lines, endings := splitSFTPText(original)
	editor := &sftpEditorState{
		mode: sftpEditMode, lines: lines, lineEndings: endings,
		original: original, newline: preferredSFTPNewline(endings),
	}
	if count := replaceAllSFTPText(editor, "cat", "x"); count != 4 {
		t.Fatalf("replace all changed %d occurrences, want 4", count)
	}
	if got, want := serializeSFTPText(editor.lines, editor.lineEndings), "x x conxenate\r\nx"; got != want {
		t.Fatalf("replace all produced %q, want %q", got, want)
	}
	if !editor.dirty || !strings.Contains(editor.searchQuery, "cat") {
		t.Fatalf("replace all did not update editor state: %#v", editor)
	}
}

func TestSFTPTextEditorReplaceAllCanRunFromPromptWithCtrlEnter(t *testing.T) {
	original := "one one"
	lines, endings := splitSFTPText(original)
	state := modelState{height: 12, sftp: &sftpBrowserState{editor: &sftpEditorState{
		mode: sftpEditMode, lines: lines, lineEndings: endings,
		original: original, newline: preferredSFTPNewline(endings),
	}, prompt: &sftpPrompt{action: sftpPromptEditorReplaceValue, value: "two"}}}
	state.sftp.editor.replaceQuery = "one"
	updated, _ := state.updateSFTPTextEditorPrompt(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	state = unwrapModel(t, updated)
	if got := serializeSFTPText(state.sftp.editor.lines, state.sftp.editor.lineEndings); got != "two two" {
		t.Fatalf("Ctrl+Enter replace all produced %q", got)
	}
	if state.sftp.prompt != nil || !strings.Contains(state.sftp.status, "2 处") {
		t.Fatalf("replace all did not finish cleanly: prompt=%#v status=%q", state.sftp.prompt, state.sftp.status)
	}
}
