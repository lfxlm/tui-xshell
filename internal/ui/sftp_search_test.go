package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/sftpclient"
)

func TestSearchLocalSFTPIsRecursiveCaseInsensitiveAndBounded(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{filepath.Join(root, "Needle.txt"), filepath.Join(nested, "needle.conf")} {
		if err := os.WriteFile(file, []byte("match"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "needle-outside.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}

	results, truncated, err := searchLocalSFTP(context.Background(), root, "NEEDLE", 10)
	if err != nil || truncated {
		t.Fatalf("search results = %#v, truncated=%v, err=%v", results, truncated, err)
	}
	if len(results) != 2 || filepath.Base(results[0].Path) != "Needle.txt" || filepath.Base(results[1].Path) != "needle.conf" {
		t.Fatalf("recursive search returned %#v; want only the two in-root matches", results)
	}
	limited, truncated, err := searchLocalSFTP(context.Background(), root, "needle", 1)
	if err != nil || !truncated || len(limited) != 1 {
		t.Fatalf("limited search = %#v, truncated=%v, err=%v; want one result and truncation", limited, truncated, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := searchLocalSFTP(cancelled, root, "needle", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search error = %v, want context.Canceled", err)
	}
}

func TestSFTPSearchPromptResultsAndLocation(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "Needle.txt"), []byte("match"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := modelState{width: 80, height: 12, sftp: &sftpBrowserState{
		standalone: true, activeSide: sftpLocalSide, localPath: root,
		localEntries: []sftpclient.Entry{{Name: "nested", IsDir: true, Mode: os.ModeDir | 0o755}},
	}}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "/", Code: '/'}))
	state = unwrapModel(t, updated)
	if state.sftp.prompt == nil || state.sftp.prompt.action != sftpPromptSearch {
		t.Fatal("slash did not open the file search prompt")
	}
	state.sftp.prompt.value = "needle"
	updated, command := state.updateSFTPPrompt(tea.Key{Code: tea.KeyEnter})
	state = unwrapModel(t, updated)
	if command == nil || !state.sftp.busy {
		t.Fatal("confirming the query did not start a search")
	}
	rawMessage := command()
	message, ok := rawMessage.(sftpSearchCompletedMsg)
	if !ok {
		t.Fatalf("search command returned %T, want sftpSearchCompletedMsg", rawMessage)
	}
	state.handleSFTPFileSearchCompleted(message)
	if !state.sftp.searchActive || len(state.sftp.searchResults) != 1 || state.sftp.searchResults[0].Path != filepath.Join(nested, "Needle.txt") {
		t.Fatalf("unexpected search results: %#v", state.sftp.searchResults)
	}
	if view := state.sftpView(); !strings.Contains(view, "Needle.txt") || !strings.Contains(view, "搜索完成") {
		t.Fatalf("search view does not show the result and completion status:\n%s", view)
	}
	listCommand := state.openSFTPSearchResult()
	if listCommand == nil || !state.sftp.busy {
		t.Fatal("opening a result did not start listing its parent directory")
	}
	rawMessage = listCommand()
	listed, ok := rawMessage.(sftpListedMsg)
	if !ok {
		t.Fatalf("list command returned %T, want sftpListedMsg", rawMessage)
	}
	state.handleSFTPListed(listed)
	if state.sftp.localPath != nested || state.sftp.localSelected != 0 || state.sftp.localEntries[state.sftp.localSelected].Name != "Needle.txt" {
		t.Fatalf("search result did not select its file: path=%q selection=%d entries=%#v", state.sftp.localPath, state.sftp.localSelected, state.sftp.localEntries)
	}
}
