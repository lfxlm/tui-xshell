package ui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"tui-ssh-workbench/internal/infra/sftpclient"
)

type fakeSFTPDirectoryTree map[string][]sftpclient.Entry

func (tree fakeSFTPDirectoryTree) List(directory string) ([]sftpclient.Entry, error) {
	entries, ok := tree[directory]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]sftpclient.Entry(nil), entries...), nil
}

func TestFilterRemoteTreeAppliesExistingPredicatesAcrossDirectories(t *testing.T) {
	tree := fakeSFTPDirectoryTree{
		"/root": {
			{Name: "src", IsDir: true},
			{Name: "vendor", IsDir: true, IsSymlink: true},
			{Name: "README.md", Mode: os.ModePerm, ModeKnown: true},
		},
		"/root/src": {
			{Name: "main.go", Size: 2048, Mode: os.ModePerm, ModeKnown: true},
			{Name: "main_test.go", Size: 2048, Mode: os.ModePerm, ModeKnown: true},
		},
	}
	filter, err := parseSFTPEntryFilter("*.go, !*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	results, truncated, err := filterRemoteTree(context.Background(), tree, "/root", filter, 100)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(results) != 1 || results[0].Path != "/root/src/main.go" {
		t.Fatalf("remote recursive filter = %#v, truncated=%v; want only nested main.go", results, truncated)
	}
	if _, ok := tree["/root/vendor"]; ok {
		t.Fatal("test setup must not provide symlink target listing")
	}
}

func TestRecursiveFilterGlobsMatchRelativePaths(t *testing.T) {
	tree := fakeSFTPDirectoryTree{
		"/root": {
			{Name: "src", IsDir: true},
			{Name: "root.go", Mode: os.ModePerm, ModeKnown: true},
		},
		"/root/src": {
			{Name: "main.go", Mode: os.ModePerm, ModeKnown: true},
			{Name: "main_test.go", Mode: os.ModePerm, ModeKnown: true},
			{Name: "nested", IsDir: true},
		},
		"/root/src/nested": {
			{Name: "deep.go", Mode: os.ModePerm, ModeKnown: true},
		},
	}
	filter, err := parseSFTPEntryFilter("src/*.go, !src/*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	results, truncated, err := filterRemoteTree(context.Background(), tree, "/root", filter, 100)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(results) != 1 || results[0].Path != "/root/src/main.go" {
		t.Fatalf("relative-path glob results = %#v truncated=%v; want /root/src/main.go", results, truncated)
	}

	filter, err = parseSFTPEntryFilter("expr:name:src/nested/*.go")
	if err != nil {
		t.Fatal(err)
	}
	results, truncated, err = filterRemoteTree(context.Background(), tree, "/root", filter, 100)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(results) != 1 || results[0].Path != "/root/src/nested/deep.go" {
		t.Fatalf("expression relative-path glob results = %#v truncated=%v; want nested deep.go", results, truncated)
	}
}

func TestLocalRecursiveFilterGlobsMatchRelativePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"root.go", filepath.Join("src", "main.go"), filepath.Join("src", "nested", "deep.go")} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package main"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	filter, err := parseSFTPEntryFilter("src/*.go")
	if err != nil {
		t.Fatal(err)
	}
	results, truncated, err := filterLocalTree(context.Background(), root, filter, 100)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(results) != 1 || filepath.ToSlash(results[0].Path) != filepath.ToSlash(filepath.Join(root, "src", "main.go")) {
		t.Fatalf("local relative-path glob results = %#v truncated=%v; want only src/main.go", results, truncated)
	}
}

func TestFilterRemoteTreeAppliesMetadataExpressionsAndBoundsResults(t *testing.T) {
	tree := fakeSFTPDirectoryTree{
		".": {
			{Name: "large.bin", Size: 2048, Mode: os.ModePerm, ModeKnown: true},
			{Name: "small.bin", Size: 10, Mode: os.ModePerm, ModeKnown: true},
		},
	}
	filter, err := parseSFTPEntryFilter("expr:type:file && size>=1KiB")
	if err != nil {
		t.Fatal(err)
	}
	results, truncated, err := filterRemoteTree(context.Background(), tree, ".", filter, 1)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(results) != 1 || results[0].Entry.Name != "large.bin" {
		t.Fatalf("metadata recursive filter = %#v, truncated=%v; want large.bin", results, truncated)
	}

	all, err := parseSFTPEntryFilter("*")
	if err != nil {
		t.Fatal(err)
	}
	results, truncated, err = filterRemoteTree(context.Background(), tree, ".", all, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !truncated {
		t.Fatalf("result limit = %#v, truncated=%v; want one result and truncation", results, truncated)
	}
}

func TestFilterRemoteTreeHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := filterRemoteTree(ctx, fakeSFTPDirectoryTree{"/": {}}, "/", sftpEntryFilter{raw: "*", includeNames: []string{"*"}}, 10)
	if err == nil {
		t.Fatal("expected canceled recursive filter to return an error")
	}
}

func TestFilterLocalTreeDoesNotFollowSymbolicLinkDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating directory symlinks requires elevated privileges on some Windows hosts")
	}
	root := t.TempDir()
	nested := filepath.Join(root, "src")
	outside := t.TempDir()
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.go"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	filter, err := parseSFTPEntryFilter("*.go")
	if err != nil {
		t.Fatal(err)
	}
	results, truncated, err := filterLocalTree(context.Background(), root, filter, 10)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(results) != 1 || results[0].Entry.Name != "main.go" {
		t.Fatalf("local recursive filter = %#v, truncated=%v; want main.go and no symlink target", results, truncated)
	}
}

func TestFilterPromptCanRunRecursiveFilterOverActiveLocalPane(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "src")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "main_test.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := modelState{width: 100, height: 24, sftp: &sftpBrowserState{
		standalone: true, activeSide: sftpLocalSide, localPath: root,
	}}
	state.openSFTPFilterPrompt()
	state.sftp.prompt.value = "*.go, !*_test.go, type:file"
	updated, _ := state.updateSFTPPrompt(tea.Key{Code: 'r', Mod: tea.ModCtrl})
	state = unwrapModel(t, updated)
	if !state.sftp.prompt.recursive {
		t.Fatal("Ctrl+R did not enable recursive filtering")
	}
	updated, command := state.updateSFTPPrompt(tea.Key{Code: tea.KeyEnter})
	state = unwrapModel(t, updated)
	if command == nil || !state.sftp.searchActive || !state.sftp.searchFilterMode || !state.sftp.busy {
		t.Fatalf("Enter did not start recursive filtering: state=%#v command=%v", state.sftp, command)
	}
	completed := command().(sftpSearchCompletedMsg)
	state.handleSFTPFileSearchCompleted(completed)
	if state.sftp.busy || !state.sftp.searchActive || len(state.sftp.searchResults) != 1 || state.sftp.searchResults[0].Entry.Name != "main.go" {
		t.Fatalf("recursive filter results = %#v", state.sftp.searchResults)
	}
	if view := state.sftpView(); !strings.Contains(view, "递归过滤") || !strings.Contains(view, "main.go") {
		t.Fatalf("recursive filter result view missing mode or result:\n%s", view)
	}
}
