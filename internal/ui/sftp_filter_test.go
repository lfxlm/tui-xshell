package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/sftpclient"
)

func TestSFTPEntryFilterSupportsGlobsExclusionsAndTypes(t *testing.T) {
	entries := []sftpclient.Entry{
		{Name: "main.go", Mode: 0o644},
		{Name: "main_test.go", Mode: 0o644},
		{Name: "README.md", Mode: 0o644},
		{Name: "build", IsDir: true, Mode: os.ModeDir | 0o755},
		{Name: "current", IsSymlink: true, Mode: os.ModeSymlink | 0o777},
		{Name: "socket", Mode: os.ModeSocket | 0o600},
	}
	filter, err := parseSFTPEntryFilter("*.go, !*_test.go, type:file")
	if err != nil {
		t.Fatal(err)
	}
	got := filterSFTPEntries(entries, filter)
	if len(got) != 1 || got[0].Name != "main.go" {
		t.Fatalf("filtered entries = %#v; want main.go only", got)
	}

	excluding, err := parseSFTPEntryFilter("!type:link, !*.md")
	if err != nil {
		t.Fatal(err)
	}
	got = filterSFTPEntries(entries, excluding)
	if len(got) != 4 {
		t.Fatalf("exclude-only filter returned %#v; want all but the link and README", got)
	}

	directories, err := parseSFTPEntryFilter("type:dir")
	if err != nil {
		t.Fatal(err)
	}
	got = filterSFTPEntries(entries, directories)
	if len(got) != 1 || got[0].Name != "build" {
		t.Fatalf("directory filter returned %#v; want build only", got)
	}
}

func TestFTPEntryWithoutPortableModeMatchesFileTypeButNotPermissions(t *testing.T) {
	entry := sftpclient.Entry{Name: "remote.txt", Size: 12}
	fileFilter, err := parseSFTPEntryFilter("type:file")
	if err != nil {
		t.Fatal(err)
	}
	if got := filterSFTPEntries([]sftpclient.Entry{entry}, fileFilter); len(got) != 1 {
		t.Fatalf("FTP file without mode was excluded from type:file: %#v", got)
	}
	permissionFilter, err := parseSFTPEntryFilter("perm=0644")
	if err != nil {
		t.Fatal(err)
	}
	if got := filterSFTPEntries([]sftpclient.Entry{entry}, permissionFilter); len(got) != 0 {
		t.Fatalf("FTP entry without mode unexpectedly matched a permission condition: %#v", got)
	}
}

func TestSFTPEntryFilterRejectsInvalidRules(t *testing.T) {
	for _, value := range []string{
		"type:unknown", "[", "../outside", "/absolute/*", "!", "size>", "size>=-1B", "size>=1MB",
		"size>0.1B", "mtime=not-a-time", "perm=888", "perm>0644", "size>9223372036854775808B",
	} {
		if _, err := parseSFTPEntryFilter(value); err == nil {
			t.Errorf("parseSFTPEntryFilter(%q) succeeded; want validation error", value)
		}
	}
}

func TestSFTPEntryFilterSupportsMetadataExpressions(t *testing.T) {
	jan15 := time.Date(2025, time.January, 15, 12, 30, 0, 0, time.UTC)
	entries := []sftpclient.Entry{
		{Name: "small.go", Size: 1536, Mode: 0o644, ModeKnown: true, ModTime: jan15},
		{Name: "large.go", Size: 2 << 20, Mode: 0o600, ModeKnown: true, ModTime: jan15},
		{Name: "unknown-mode.go", Size: 1536, ModTime: jan15},
		{Name: "unknown-time.go", Size: 1536, Mode: 0o644, ModeKnown: true},
	}
	filter, err := parseSFTPEntryFilter("type:file, size>=1.5KiB, size<2MiB, mtime>=2025-01-01T00:00:00Z, mtime<2025-02-01T00:00:00Z, perm=0644")
	if err != nil {
		t.Fatal(err)
	}
	got := filterSFTPEntries(entries, filter)
	if len(got) != 1 || got[0].Name != "small.go" {
		t.Fatalf("metadata filter returned %#v; want small.go only", got)
	}

	excluded, err := parseSFTPEntryFilter("!size>=2MiB")
	if err != nil {
		t.Fatal(err)
	}
	got = filterSFTPEntries(entries, excluded)
	if len(got) != 3 || got[0].Name != "small.go" {
		t.Fatalf("negated metadata filter returned %#v; want all but large.go", got)
	}

	unknownPermissions, err := parseSFTPEntryFilter("!perm=0644")
	if err != nil {
		t.Fatal(err)
	}
	got = filterSFTPEntries(entries, unknownPermissions)
	if len(got) != 2 || got[0].Name != "large.go" || got[1].Name != "unknown-mode.go" {
		t.Fatalf("unknown permission metadata was treated as a match: %#v", got)
	}

	ownershipEntries := []sftpclient.Entry{
		{Name: "root-owned", OwnershipKnown: true, UID: 0, GID: 0},
		{Name: "service-owned", OwnershipKnown: true, UID: 1001, GID: 2001},
		{Name: "unknown-owner"},
	}
	rootOwners, err := parseSFTPEntryFilter("uid=0, gid=0")
	if err != nil {
		t.Fatal(err)
	}
	got = filterSFTPEntries(ownershipEntries, rootOwners)
	if len(got) != 1 || got[0].Name != "root-owned" {
		t.Fatalf("zero UID/GID filter returned %#v; want root-owned only", got)
	}
	serviceOwners, err := parseSFTPEntryFilter("uid>=1000, gid=2001")
	if err != nil {
		t.Fatal(err)
	}
	got = filterSFTPEntries(ownershipEntries, serviceOwners)
	if len(got) != 1 || got[0].Name != "service-owned" {
		t.Fatalf("UID/GID filter returned %#v; want service-owned only", got)
	}
	unknownOwners, err := parseSFTPEntryFilter("uid=1000")
	if err != nil {
		t.Fatal(err)
	}
	got = filterSFTPEntries(ownershipEntries, unknownOwners)
	if len(got) != 0 {
		t.Fatalf("unknown owner metadata matched UID filter: %#v", got)
	}
	for _, invalid := range []string{"uid=-1", "gid=4294967296", "uid=1.5", "gid>"} {
		if _, err := parseSFTPEntryFilter(invalid); err == nil {
			t.Errorf("invalid ownership filter %q was accepted", invalid)
		}
	}
}

func TestParseSFTPFilterSize(t *testing.T) {
	for _, test := range []struct {
		input string
		want  int64
	}{
		{input: "0", want: 0},
		{input: "1536B", want: 1536},
		{input: "1.5KiB", want: 1536},
		{input: "2MiB", want: 2 << 20},
		{input: "1GiB", want: 1 << 30},
	} {
		got, err := parseSFTPFilterSize(test.input)
		if err != nil || got != test.want {
			t.Errorf("parseSFTPFilterSize(%q) = %d, %v; want %d", test.input, got, err, test.want)
		}
	}
}

func TestSFTPEntryFilterSupportsBooleanExpressions(t *testing.T) {
	entries := []sftpclient.Entry{
		{Name: "large.go", Size: 2 << 20, Mode: 0o644, ModeKnown: true, ModTime: time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)},
		{Name: "small.go", Size: 1024, Mode: 0o644, ModeKnown: true, ModTime: time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)},
		{Name: "warning.log", Size: 100, Mode: 0o600, ModeKnown: true},
		{Name: "source", IsDir: true, Mode: os.ModeDir | 0o755, ModeKnown: true},
	}

	filter, err := parseSFTPEntryFilter("expr:(type:file && size>=1MiB) || name:*.log")
	if err != nil {
		t.Fatal(err)
	}
	got := filterSFTPEntries(entries, filter)
	if len(got) != 2 || got[0].Name != "large.go" || got[1].Name != "warning.log" {
		t.Fatalf("OR expression returned %#v; want large.go and warning.log", got)
	}

	filter, err = parseSFTPEntryFilter("expr:!(type:dir || name:small*)")
	if err != nil {
		t.Fatal(err)
	}
	got = filterSFTPEntries(entries, filter)
	if len(got) != 2 || got[0].Name != "large.go" || got[1].Name != "warning.log" {
		t.Fatalf("grouped NOT expression returned %#v; want large.go and warning.log", got)
	}

	filter, err = parseSFTPEntryFilter("expr:name:*.go || name:*.log && size<1KiB")
	if err != nil {
		t.Fatal(err)
	}
	got = filterSFTPEntries(entries, filter)
	if len(got) != 3 || got[0].Name != "large.go" || got[1].Name != "small.go" || got[2].Name != "warning.log" {
		t.Fatalf("operator precedence expression returned %#v", got)
	}
}

func TestSFTPEntryFilterRejectsMalformedExpressions(t *testing.T) {
	for _, value := range []string{
		"expr:", "expr:(type:file", "expr:type:file &&", "expr:size>1MiB ||", "expr:type:unknown",
		"expr:../outside", "expr:/absolute/*", "expr:(foo & bar)", "expr:foo bar", "expr:name:",
	} {
		if _, err := parseSFTPEntryFilter(value); err == nil {
			t.Errorf("parseSFTPEntryFilter(%q) succeeded; want validation error", value)
		}
	}
}

func TestSFTPFilterPromptAppliesToOnlyTheActivePane(t *testing.T) {
	state := modelState{width: 80, height: 12, sftp: &sftpBrowserState{
		standalone: true, activeSide: sftpLocalSide,
		localEntries: []sftpclient.Entry{
			{Name: "main.go", Mode: 0o644},
			{Name: "main_test.go", Mode: 0o644},
			{Name: "README.md", Mode: 0o644},
		},
		entries: []sftpclient.Entry{
			{Name: "build", IsDir: true, Mode: os.ModeDir | 0o755},
			{Name: "server.log", Mode: 0o644},
		},
	}}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "f", Code: 'f'}))
	state = unwrapModel(t, updated)
	if state.sftp.prompt == nil || state.sftp.prompt.action != sftpPromptFilter || state.sftp.prompt.transfer.side != sftpLocalSide {
		t.Fatal("f did not open a filter prompt for the focused local pane")
	}
	state.sftp.prompt.value = "*.go, !*_test.go, type:file"
	updated, _ = state.updateSFTPPrompt(tea.Key{Code: tea.KeyEnter})
	state = unwrapModel(t, updated)
	selected := state.selectedSFTPEntry(sftpLocalSide)
	if selected == nil || selected.Name != "main.go" || len(sftpPaneEntries(state.sftp, sftpLocalSide)) != 1 {
		t.Fatalf("local filter did not update the visible selection: selected=%#v visible=%#v", selected, sftpPaneEntries(state.sftp, sftpLocalSide))
	}
	if len(sftpPaneEntries(state.sftp, sftpRemoteSide)) != 2 {
		t.Fatal("applying the local filter changed the remote pane")
	}

	state.sftp.activeSide = sftpRemoteSide
	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "f", Code: 'f'}))
	state = unwrapModel(t, updated)
	if state.sftp.prompt.transfer.side != sftpRemoteSide {
		t.Fatal("filter prompt did not follow focus to the remote pane")
	}
	state.sftp.prompt.value = "type:dir"
	updated, _ = state.updateSFTPPrompt(tea.Key{Code: tea.KeyEnter})
	state = unwrapModel(t, updated)
	if got := sftpPaneEntries(state.sftp, sftpRemoteSide); len(got) != 1 || got[0].Name != "build" {
		t.Fatalf("remote filter returned %#v; want build only", got)
	}
	if got := sftpPaneEntries(state.sftp, sftpLocalSide); len(got) != 1 || got[0].Name != "main.go" {
		t.Fatalf("remote filter changed local filter: %#v", got)
	}
	if !strings.Contains(state.standaloneSFTPView(state.sftp), "type:dir") {
		t.Fatal("pane header does not show the active filter")
	}
	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "f", Code: 'f'}))
	state = unwrapModel(t, updated)
	state.sftp.prompt.value = "expr:type:dir || name:server.log"
	updated, _ = state.updateSFTPPrompt(tea.Key{Code: tea.KeyEnter})
	state = unwrapModel(t, updated)
	if got := sftpPaneEntries(state.sftp, sftpRemoteSide); len(got) != 2 {
		t.Fatalf("expression filter was not applied in the remote pane: %#v", got)
	}
}
