package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/model"
)

func TestFindTerminalMatchesAcrossHistoryAndCurrentScreen(t *testing.T) {
	terminal := vt.NewSafeEmulator(24, 4)
	terminal.SetScrollbackSize(20)
	_, _ = terminal.Write([]byte("first error\r\nsecond line\r\nthird line\r\nERROR again\r\nlast line"))
	pane := &terminalPane{terminal: terminal}
	rows := terminalBufferTextRows(pane)
	matches := findTerminalMatches(rows, "error")
	if len(matches) != 2 {
		t.Fatalf("matches = %v, want two rows", matches)
	}
	if matches[0] >= terminal.ScrollbackLen() || matches[1] < terminal.ScrollbackLen() {
		t.Fatalf("expected one history and one live-screen match, got rows %v with history length %d", matches, terminal.ScrollbackLen())
	}
}

func TestTerminalSearchMatchUsesCellColumnsWithWideCharacters(t *testing.T) {
	terminal := vt.NewSafeEmulator(24, 4)
	_, _ = terminal.Write([]byte("中ERROR"))
	pane := &terminalPane{terminal: terminal}
	rows := terminalBufferSearchRows(pane)
	matches, truncated := findTerminalSearchMatches(rows, "error")
	if truncated {
		t.Fatal("unexpectedly truncated a small search result")
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %#v, want one result", matches)
	}
	want := terminalSearchMatch{row: terminal.ScrollbackLen(), startCell: 2, endCell: 7}
	if matches[0] != want {
		t.Fatalf("match = %#v, want %#v", matches[0], want)
	}
	rendered := renderTerminalSearchMatchLine(pane, matches[0])
	if !strings.Contains(ansi.Strip(rendered), "中ERROR") {
		t.Fatalf("highlighted row lost text: %q", ansi.Strip(rendered))
	}
	if !strings.Contains(rendered, "48;2;88;214;194") {
		t.Fatalf("highlighted match has no distinct background: %q", rendered)
	}
}

func TestTerminalRegexSearchMatchesCaseInsensitivelyAndMapsWideCharacters(t *testing.T) {
	terminal := vt.NewSafeEmulator(24, 2)
	_, _ = terminal.Write([]byte("中ERROR ERROR"))
	pane := &terminalPane{terminal: terminal}
	matches, truncated, err := findTerminalRegexSearchMatches(terminalBufferSearchRows(pane), `中E.ROR|E.ROR`)
	if err != nil {
		t.Fatalf("regex search: %v", err)
	}
	if truncated {
		t.Fatal("unexpectedly truncated a small regex result")
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %#v, want both occurrences on the row", matches)
	}
	want := terminalSearchMatch{row: terminal.ScrollbackLen(), startCell: 0, endCell: 7}
	if matches[0] != want {
		t.Fatalf("match = %#v, want %#v", matches[0], want)
	}
	if matches[1].startCell != 8 || matches[1].endCell != 13 {
		t.Fatalf("second match = %#v, want columns 8-13", matches[1])
	}
	if _, _, err := findTerminalRegexSearchMatches(terminalBufferSearchRows(pane), `(`); err == nil {
		t.Fatal("invalid regular expression was accepted")
	}
}

func TestTerminalSearchFindsEveryNonOverlappingOccurrence(t *testing.T) {
	text := "error error ERROR"
	row := terminalSearchRow{text: text, spans: make([]terminalSearchCellSpan, 0, len(text))}
	for index := range text {
		row.spans = append(row.spans, terminalSearchCellSpan{
			startRune: index, endRune: index + 1, startCell: index, endCell: index + 1,
		})
	}
	matches, truncated := findTerminalSearchMatches([]terminalSearchRow{row}, "error")
	if truncated || len(matches) != 3 {
		t.Fatalf("matches=%#v truncated=%v, want three occurrences", matches, truncated)
	}
	wantStarts := []int{0, 6, 12}
	for index, match := range matches {
		if match.startCell != wantStarts[index] || match.endCell != wantStarts[index]+5 {
			t.Errorf("match %d = %#v, want columns %d-%d", index, match, wantStarts[index], wantStarts[index]+5)
		}
	}
	if matches[0].row != matches[1].row || matches[1].row != matches[2].row {
		t.Fatalf("same-line occurrences have different rows: %#v", matches)
	}
	pane := &terminalPane{search: &terminalSearchState{matches: matches}}
	moveTerminalSearch(pane, 1, 5)
	if pane.search.index != 1 {
		t.Fatalf("next match index = %d, want second occurrence on the same line", pane.search.index)
	}
}

func TestTerminalSearchCapsLargeResultSets(t *testing.T) {
	const count = maxTerminalSearchResults + 1
	text := strings.Repeat("a ", count)
	spans := make([]terminalSearchCellSpan, 0, len(text))
	for index, character := range text {
		if character == 'a' {
			spans = append(spans, terminalSearchCellSpan{
				startRune: index, endRune: index + 1, startCell: index, endCell: index + 1,
			})
		}
	}
	matches, truncated := findTerminalSearchMatches([]terminalSearchRow{{text: text, spans: spans}}, "a")
	if len(matches) != maxTerminalSearchResults || !truncated {
		t.Fatalf("result count=%d truncated=%v, want cap %d and truncation", len(matches), truncated, maxTerminalSearchResults)
	}
}

func TestTerminalSearchCanSwitchToRegexMode(t *testing.T) {
	terminal := vt.NewSafeEmulator(24, 4)
	terminal.SetScrollbackSize(20)
	_, _ = terminal.Write([]byte("ERROR one\r\nfiller\r\nerror two"))
	pane := &terminalPane{id: "pane-regex", terminal: terminal, session: &workspaceTestSession{}}
	state := modelState{
		width:            80,
		height:           12,
		workspaceVisible: true,
		activeTabID:      "tab-regex",
		tabs:             []*workspaceTab{{id: "tab-regex", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	state = unwrapModel(t, updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	if !pane.search.regex {
		t.Fatal("Ctrl+R did not enable regex mode")
	}
	for _, character := range `err(or|ors)` {
		updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: character, Text: string(character)}))
		state = unwrapModel(t, updated)
	}
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if len(pane.search.matches) != 2 || pane.search.err != "" {
		t.Fatalf("regex search state = %#v, want two results", pane.search)
	}
	if len(pane.session.(*workspaceTestSession).writes) != 0 {
		t.Fatal("regex search keys were sent to the terminal")
	}
}

func TestTerminalSearchStaysLocalAndHighlightsCurrentMatch(t *testing.T) {
	terminal := vt.NewSafeEmulator(24, 4)
	terminal.SetScrollbackSize(20)
	_, _ = terminal.Write([]byte("booting\r\nERROR one\r\nfiller\r\nready\r\nerror two"))
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id:       "pane-search",
		host:     model.Host{Name: "test"},
		session:  session,
		terminal: terminal,
	}
	state := modelState{
		width:            80,
		height:           12,
		workspaceVisible: true,
		activeTabID:      "tab-search",
		tabs:             []*workspaceTab{{id: "tab-search", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	state = unwrapModel(t, updated)
	for _, character := range "error" {
		updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: character, Text: string(character)}))
		state = unwrapModel(t, updated)
	}
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if pane.search == nil || pane.search.editing || len(pane.search.matches) != 2 {
		t.Fatalf("search state = %#v, want two results and search mode", pane.search)
	}
	if !pane.scrollbackViewing {
		t.Fatal("search result did not switch to history view")
	}
	view := state.renderTerminalPane(pane, paneRect{width: 24, height: 7})
	if !strings.Contains(ansi.Strip(view), "ERROR one") {
		t.Fatalf("search view omitted selected match:\n%s", view)
	}
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "n"}))
	state = unwrapModel(t, updated)
	if pane.search.index != 1 {
		t.Fatalf("next match index = %d, want 1", pane.search.index)
	}
	view = state.renderTerminalPane(pane, paneRect{width: 24, height: 7})
	if !strings.Contains(ansi.Strip(view), "error two") {
		t.Fatalf("next match was not brought into view:\n%s", view)
	}
	if len(session.writes) != 0 {
		t.Fatalf("search input leaked to terminal: %q", session.writes)
	}
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if pane.search != nil || pane.scrollbackViewing {
		t.Fatalf("Escape did not return to live terminal: search=%v history=%v", pane.search, pane.scrollbackViewing)
	}
}

func TestTerminalSearchPasteIsKeptInSearchField(t *testing.T) {
	terminal := vt.NewSafeEmulator(24, 4)
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id:       "pane-search-paste",
		host:     model.Host{Name: "test"},
		session:  session,
		terminal: terminal,
		search:   &terminalSearchState{editing: true},
	}
	state := modelState{
		workspaceVisible: true,
		activeTabID:      "tab-search-paste",
		tabs:             []*workspaceTab{{id: "tab-search-paste", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}
	updated, _ := state.Update(tea.PasteMsg{Content: "error\nquery"})
	state = unwrapModel(t, updated)
	if pane.search.query != "errorquery" || len(session.writes) != 0 {
		t.Fatalf("search paste query=%q terminalWrites=%q", pane.search.query, session.writes)
	}
}
