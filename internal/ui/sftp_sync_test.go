package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/sftpclient"
)

func TestSFTPSyncDoesNotInventPermissionsForFTPEntries(t *testing.T) {
	modified := time.Unix(100, 0)
	ftpEntry := sftpclient.Entry{Name: "same.txt", Size: 5, ModTime: modified}
	sftpEntry := sftpclient.Entry{Name: "same.txt", Size: 5, Mode: 0o600, ModeKnown: true, ModTime: modified}
	actions, issues := planSFTPSync(map[string]sftpclient.Entry{"same.txt": ftpEntry}, map[string]sftpclient.Entry{"same.txt": sftpEntry})
	if len(actions) != 0 || len(issues) != 0 {
		t.Fatalf("unknown FTP permissions caused a sync action: actions=%#v issues=%#v", actions, issues)
	}

	ftpEntry.Mode, ftpEntry.ModeKnown = 0o644, true
	actions, issues = planSFTPSync(map[string]sftpclient.Entry{"same.txt": ftpEntry}, map[string]sftpclient.Entry{
		"same.txt": {Name: "same.txt", Size: 5, Mode: 0o600, ModeKnown: true, ModTime: modified},
	})
	if len(actions) != 1 || len(issues) != 0 {
		t.Fatalf("known permission difference did not produce an action: actions=%#v issues=%#v", actions, issues)
	}
}

func TestPlanSFTPSyncSelectsChangedRootsAndPreservesDestinationExtras(t *testing.T) {
	source := map[string]sftpclient.Entry{
		"same.txt":          {Name: "same.txt", Size: 4, Mode: 0o644, ModTime: time.Unix(10, 0)},
		"changed.txt":       {Name: "changed.txt", Size: 9, Mode: 0o600, ModTime: time.Unix(20, 0)},
		"newdir":            {Name: "newdir", IsDir: true, Mode: os.ModeDir | 0o755},
		"newdir/nested.txt": {Name: "nested.txt", Size: 2, Mode: 0o644, ModTime: time.Unix(30, 0)},
		"clash":             {Name: "clash", Size: 1, Mode: 0o644, ModTime: time.Unix(40, 0)},
		"link":              {Name: "link", IsSymlink: true, Mode: os.ModeSymlink | 0o777},
	}
	destination := map[string]sftpclient.Entry{
		"same.txt":    {Name: "same.txt", Size: 4, Mode: 0o644, ModTime: time.Unix(10, 0)},
		"changed.txt": {Name: "changed.txt", Size: 8, Mode: 0o600, ModTime: time.Unix(20, 0)},
		"clash":       {Name: "clash", IsDir: true, Mode: os.ModeDir | 0o755},
		"extra.txt":   {Name: "extra.txt", Size: 3, Mode: 0o644, ModTime: time.Unix(50, 0)},
	}
	actions, issues := planSFTPSync(source, destination)
	if len(actions) != 2 || actions[0].relative != "changed.txt" || actions[1].relative != "newdir" {
		t.Fatalf("sync actions = %#v; want changed.txt and whole newdir only", actions)
	}
	if len(issues) != 2 || issues[0].relative != "clash" || !issues[0].blocked || issues[1].relative != "link" || issues[1].blocked {
		t.Fatalf("sync issues = %#v; want one blocked type conflict and one skipped symlink", issues)
	}
	reverse, reverseIssues := planSFTPSync(destination, source)
	if len(reverse) != 2 || reverse[0].relative != "changed.txt" || reverse[1].relative != "extra.txt" {
		t.Fatalf("reverse sync actions = %#v; want changed.txt and extra.txt", reverse)
	}
	if len(reverseIssues) != 1 || !reverseIssues[0].blocked {
		t.Fatalf("reverse sync issues = %#v; want clash type conflict", reverseIssues)
	}
}

func TestPlanSFTPBidirectionalSyncUsesNewerVersionAndReportsAmbiguousConflicts(t *testing.T) {
	left := map[string]sftpclient.Entry{
		"both.txt":         {Name: "both.txt", Size: 5, Mode: 0o644, ModTime: time.Unix(20, 0)},
		"equal.txt":        {Name: "equal.txt", Size: 4, Mode: 0o644, ModTime: time.Unix(15, 0)},
		"left.txt":         {Name: "left.txt", Size: 2, Mode: 0o600, ModTime: time.Unix(10, 0)},
		"newdir":           {Name: "newdir", IsDir: true, Mode: os.ModeDir | 0o755},
		"newdir/keep.txt":  {Name: "keep.txt", Size: 2, Mode: 0o644},
		"newdir/skip.link": {Name: "skip.link", IsSymlink: true, Mode: os.ModeSymlink | 0o777},
		"same-time.txt":    {Name: "same-time.txt", Size: 1, Mode: 0o644, ModTime: time.Unix(8, 0)},
		"type-clash":       {Name: "type-clash", Size: 1, Mode: 0o644},
	}
	right := map[string]sftpclient.Entry{
		"both.txt":              {Name: "both.txt", Size: 3, Mode: 0o644, ModTime: time.Unix(11, 0)},
		"equal.txt":             {Name: "equal.txt", Size: 4, Mode: 0o644, ModTime: time.Unix(15, 0)},
		"newdir":                {Name: "newdir", IsDir: true, Mode: os.ModeDir | 0o755},
		"newdir/keep.txt":       {Name: "keep.txt", Size: 2, Mode: 0o644},
		"right-dir":             {Name: "right-dir", IsDir: true, Mode: os.ModeDir | 0o755},
		"right-dir/remote.txt":  {Name: "remote.txt", Size: 7, Mode: 0o600},
		"right.txt":             {Name: "right.txt", Size: 9, Mode: 0o644, ModTime: time.Unix(12, 0)},
		"same-time.txt":         {Name: "same-time.txt", Size: 2, Mode: 0o644, ModTime: time.Unix(8, 0)},
		"type-clash":            {Name: "type-clash", IsDir: true, Mode: os.ModeDir | 0o755},
		"type-clash/nested.txt": {Name: "nested.txt", Size: 1, Mode: 0o644},
	}
	actions, issues := planSFTPBidirectionalSync(left, right)
	wantSources := map[string]sftpSide{
		"both.txt":  sftpLocalSide,
		"left.txt":  sftpLocalSide,
		"right-dir": sftpRemoteSide,
		"right.txt": sftpRemoteSide,
	}
	if len(actions) != len(wantSources) {
		t.Fatalf("bidirectional actions = %#v, want %v", actions, wantSources)
	}
	for _, action := range actions {
		if want, ok := wantSources[action.action.relative]; !ok || want != action.sourceSide {
			t.Fatalf("action %q source = %v, want %v", action.action.relative, action.sourceSide, want)
		}
	}
	issuePaths := make(map[string]sftpSyncIssue, len(issues))
	for _, issue := range issues {
		issuePaths[issue.relative] = issue
	}
	if issue, ok := issuePaths["same-time.txt"]; !ok || !issue.blocked || !strings.Contains(issue.message, "修改时间相同") {
		t.Fatalf("same-time conflict = %#v, want blocked manual conflict", issue)
	}
	if issue, ok := issuePaths["same-time.txt"]; !ok || !issue.resolvable {
		t.Fatalf("same-time conflict = %#v, want an explicit source choice", issue)
	}
	if issue, ok := issuePaths["type-clash"]; !ok || !issue.blocked {
		t.Fatalf("type conflict = %#v, want blocked type conflict", issue)
	}
	if issue, ok := issuePaths["newdir/skip.link"]; !ok || issue.blocked {
		t.Fatalf("nested symlink = %#v, want a visible non-blocking skip", issue)
	}
	if _, exists := issuePaths["equal.txt"]; exists {
		t.Fatal("identical files should not produce a bidirectional action or issue")
	}
}

func TestSFTPBidirectionalSyncResolvesSameTimeConflictByChosenSide(t *testing.T) {
	modified := time.Unix(100, 0)
	left := map[string]sftpclient.Entry{
		"same.txt": {Name: "same.txt", Size: 4, Mode: 0o600, ModeKnown: true, ModTime: modified},
	}
	right := map[string]sftpclient.Entry{
		"same.txt": {Name: "same.txt", Size: 9, Mode: 0o644, ModeKnown: true, ModTime: modified},
	}
	actions, issues := planSFTPBidirectionalSync(left, right)
	comparison := &sftpSyncComparison{
		leftEntries: left, rightEntries: right,
		bidirectionalActions: actions, bidirectionalIssues: issues,
		bidirectionalSources: map[string]sftpSide{"same.txt": sftpLocalSide},
	}
	resolved, unresolved, manual := comparison.bidirectionalResolvedActions()
	if len(resolved) != 1 || unresolved != 0 || manual != 1 {
		t.Fatalf("resolved actions=%#v unresolved=%d manual=%d", resolved, unresolved, manual)
	}
	if resolved[0].sourceSide != sftpLocalSide || resolved[0].action.entry.Size != 4 {
		t.Fatalf("left resolution = %#v, want local entry", resolved[0])
	}
	comparison.bidirectionalSources["same.txt"] = sftpRemoteSide
	resolved, unresolved, manual = comparison.bidirectionalResolvedActions()
	if len(resolved) != 1 || unresolved != 0 || manual != 1 || resolved[0].sourceSide != sftpRemoteSide || resolved[0].action.entry.Size != 9 {
		t.Fatalf("right resolution = %#v unresolved=%d manual=%d, want remote entry", resolved, unresolved, manual)
	}
	delete(comparison.bidirectionalSources, "same.txt")
	resolved, unresolved, manual = comparison.bidirectionalResolvedActions()
	if len(resolved) != 0 || unresolved != 1 || manual != 0 {
		t.Fatalf("unresolved conflict = %#v unresolved=%d manual=%d", resolved, unresolved, manual)
	}
}

func TestSFTPComparisonLetsUserChooseSameTimeConflictSource(t *testing.T) {
	modified := time.Unix(100, 0)
	left := map[string]sftpclient.Entry{"same.txt": {Name: "same.txt", Size: 4, Mode: 0o600, ModTime: modified}}
	right := map[string]sftpclient.Entry{"same.txt": {Name: "same.txt", Size: 9, Mode: 0o644, ModTime: modified}}
	actions, issues := planSFTPBidirectionalSync(left, right)
	state := modelState{width: 100, height: 14, sftp: &sftpBrowserState{
		standalone: true, client: &sftpclient.Client{}, localPath: "/local", path: "/remote",
		comparison: &sftpSyncComparison{
			leftRoot: "/local", rightRoot: "/remote", leftEntries: left, rightEntries: right,
			bidirectional: true, bidirectionalActions: actions, bidirectionalIssues: issues,
		},
	}}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "r"}))
	state = unwrapModel(t, updated)
	if state.sftp.comparison.bidirectionalSources["same.txt"] != sftpRemoteSide {
		t.Fatal("r did not choose the right pane as the conflict source")
	}
	view := state.sftpView()
	if !strings.Contains(view, "已选右栏覆盖左栏") || !strings.Contains(view, "同步 1 项") {
		t.Fatalf("resolved conflict is not reflected in the TUI:\n%s", view)
	}
	updated, cmd := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if cmd == nil || !state.sftp.queueActive || state.sftp.queueTotal != 1 {
		t.Fatalf("manual conflict resolution did not enter the transfer queue: queue=%#v cmd=%v", state.sftp.transferQueue, cmd)
	}
	if state.sftp.queueCurrent == nil || state.sftp.queueCurrent.direction != sftpDownload || state.sftp.queueCurrent.remotePath != "/remote/same.txt" {
		t.Fatalf("right-side choice did not produce remote-to-local transfer: %#v", state.sftp.queueCurrent)
	}
}

func TestScanLocalSFTPTreeDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "item.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "outside.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	entries, truncated, err := scanLocalSFTPTree(context.Background(), root)
	if err != nil || truncated {
		t.Fatalf("tree scan = %#v, truncated=%v, err=%v", entries, truncated, err)
	}
	if len(entries) != 3 || !entries["nested"].IsDir || entries["nested/item.txt"].Size != int64(len("content")) || !entries["linked"].IsSymlink {
		t.Fatalf("tree scan returned %#v; wanted nested directory, its file and an untraversed link", entries)
	}
	if _, found := entries["linked/outside.txt"]; found {
		t.Fatal("tree scan followed a symbolic link outside the root")
	}
}

func TestSyncTransferMapsPaneDirectionAndRemotePaths(t *testing.T) {
	state := &sftpBrowserState{
		standalone: true,
		peerClient: &sftpclient.Client{},
		localPath:  "/unused-local",
		peerPath:   "/source",
		path:       "/destination",
	}
	transfer := syncTransferForAction(state, sftpLocalSide, sftpSyncAction{
		relative: "dir/file.txt",
		entry:    sftpclient.Entry{Name: "file.txt", Size: 5, Mode: 0o644},
	})
	if transfer.direction != sftpUpload || transfer.side != sftpRemoteSide || !transfer.sourceRemote || !transfer.destRemote {
		t.Fatalf("remote-to-remote left-to-right transfer flags = %#v", transfer)
	}
	if transfer.sourcePath != "/source/dir/file.txt" || transfer.destPath != "/destination/dir/file.txt" {
		t.Fatalf("remote-to-remote paths = %q -> %q", transfer.sourcePath, transfer.destPath)
	}
}

func TestSFTPTransferClientSelectionSupportsMixedQueueDirections(t *testing.T) {
	primary := &sftpclient.Client{}
	peer := &sftpclient.Client{}
	state := &sftpBrowserState{client: primary, peerClient: peer}
	leftToRight := sftpTransferRequest{direction: sftpUpload, side: sftpRemoteSide}
	source, destination := sftpClientsForTransfer(state, leftToRight)
	if source != peer || destination != primary {
		t.Fatalf("left-to-right remote clients = %p -> %p, want peer -> primary", source, destination)
	}
	rightToLeft := sftpTransferRequest{direction: sftpDownload, side: sftpLocalSide}
	source, destination = sftpClientsForTransfer(state, rightToLeft)
	if source != primary || destination != peer {
		t.Fatalf("right-to-left remote clients = %p -> %p, want primary -> peer", source, destination)
	}
}

func TestSFTPQueueCompletionRefreshesBothDestinationPanes(t *testing.T) {
	model := modelState{height: 20, sftp: &sftpBrowserState{
		requestID: 7, standalone: true, queueActive: true,
		path: "/remote-before", peerPath: "/peer-before",
		client: &sftpclient.Client{}, peerClient: &sftpclient.Client{},
	}}
	_ = model.handleSFTPOperation(sftpOperationMsg{
		requestID: 7,
		queueRefreshes: []sftpQueueRefresh{
			{side: sftpLocalSide, path: "/peer-after", entries: []sftpclient.Entry{{Name: "from-primary.txt"}}},
			{side: sftpRemoteSide, path: "/remote-after", entries: []sftpclient.Entry{{Name: "from-peer.txt"}}},
		},
	})
	if model.sftp.peerPath != "/peer-after" || model.sftp.path != "/remote-after" {
		t.Fatalf("queue refreshed paths = left %q / right %q", model.sftp.peerPath, model.sftp.path)
	}
	if len(model.sftp.localEntries) != 1 || model.sftp.localEntries[0].Name != "from-primary.txt" ||
		len(model.sftp.entries) != 1 || model.sftp.entries[0].Name != "from-peer.txt" {
		t.Fatalf("queue refreshed listings = left %#v / right %#v", model.sftp.localEntries, model.sftp.entries)
	}
}

func TestSFTPComparisonViewAndDirectionControls(t *testing.T) {
	state := modelState{width: 90, height: 12, sftp: &sftpBrowserState{
		standalone: true,
		comparison: &sftpSyncComparison{
			leftRoot: "/local/project", rightRoot: "/srv/project",
			leftToRight: []sftpSyncAction{{relative: "main.go", entry: sftpclient.Entry{Name: "main.go", Mode: 0o644}}},
			rightToLeft: []sftpSyncAction{{relative: "config.yaml", entry: sftpclient.Entry{Name: "config.yaml", Mode: 0o600}}},
			direction:   sftpLocalSide,
		},
	}}
	view := state.sftpView()
	for _, expected := range []string{"目录比较与同步", "/local/project", "/srv/project", "main.go", "保留目标多余文件"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("comparison view does not contain %q:\n%s", expected, view)
		}
	}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	state = unwrapModel(t, updated)
	if state.sftp.comparison.direction != sftpRemoteSide {
		t.Fatal("Tab did not switch the comparison direction to right-to-left")
	}
	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	state = unwrapModel(t, updated)
	if state.sftp.comparison.direction != sftpLocalSide {
		t.Fatal("Left did not select left-to-right comparison")
	}
	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.sftp.comparison != nil {
		t.Fatal("Escape did not close the comparison preview")
	}
}

func TestPlanSFTPMirrorDeletesOnlyDestinationExtrasAndSafeDirectories(t *testing.T) {
	source := map[string]sftpclient.Entry{
		"overlap":             {Name: "overlap", IsDir: true, Mode: os.ModeDir | 0o755},
		"overlap/keep.txt":    {Name: "keep.txt", Size: 4, Mode: 0o644},
		"conflict":            {Name: "conflict", Size: 1, Mode: 0o644},
		"unsafe-dir":          {Name: "unsafe-dir", IsDir: true, Mode: os.ModeDir | 0o755},
		"unsafe-dir/keep.txt": {Name: "keep.txt", Size: 3, Mode: 0o600},
	}
	destination := map[string]sftpclient.Entry{
		"extra.txt":            {Name: "extra.txt", Size: 2, Mode: 0o644},
		"extra-dir":            {Name: "extra-dir", IsDir: true, Mode: os.ModeDir | 0o755},
		"extra-dir/nested.txt": {Name: "nested.txt", Size: 4, Mode: 0o600},
		"overlap":              {Name: "overlap", IsDir: true, Mode: os.ModeDir | 0o755},
		"overlap/keep.txt":     {Name: "keep.txt", Size: 4, Mode: 0o644},
		"overlap/extra.txt":    {Name: "extra.txt", Size: 1, Mode: 0o644},
		"conflict":             {Name: "conflict", IsDir: true, Mode: os.ModeDir | 0o755},
		"unsafe-dir":           {Name: "unsafe-dir", IsDir: true, Mode: os.ModeDir | 0o755},
		"unsafe-dir/keep.txt":  {Name: "keep.txt", Size: 3, Mode: 0o600},
		"unsafe-dir/device":    {Name: "device", Mode: os.ModeDevice},
		"link":                 {Name: "link", IsSymlink: true, Mode: os.ModeSymlink | 0o777},
	}
	issues := []sftpSyncIssue{{relative: "conflict", message: "type conflict", blocked: true}}
	deletes, deleteIssues := planSFTPMirrorDeletes(source, destination, issues)
	want := []string{"extra-dir", "extra.txt", "link", "overlap/extra.txt"}
	if len(deletes) != len(want) {
		t.Fatalf("mirror deletes = %#v, want %v", deletes, want)
	}
	for index, relative := range want {
		if deletes[index].relative != relative {
			t.Fatalf("mirror delete %d = %q, want %q", index, deletes[index].relative, relative)
		}
	}
	if len(deleteIssues) != 1 || deleteIssues[0].relative != "unsafe-dir/device" {
		t.Fatalf("mirror delete issues = %#v, want unsupported device entry", deleteIssues)
	}
}

func TestMirrorDeletePlanDetectsChangedPreviewSnapshot(t *testing.T) {
	expected := sftpclient.Entry{Name: "old.log", Size: 5, Mode: 0o644, ModTime: time.Unix(10, 0)}
	plan := &sftpMirrorPlan{
		deletes:        []sftpSyncDeleteAction{{relative: "old.log", entry: expected}},
		deleteSnapshot: map[string]sftpclient.Entry{"old.log": expected},
	}
	source := map[string]sftpclient.Entry{}
	destination := map[string]sftpclient.Entry{"old.log": expected}
	deletes, _ := planSFTPMirrorDeletes(source, destination, nil)
	if !sameMirrorDeletePlan(plan, deletes, nil, source, destination) {
		t.Fatal("unchanged delete plan was rejected")
	}
	destination["old.log"] = sftpclient.Entry{Name: "old.log", Size: 9, Mode: 0o644, ModTime: time.Unix(10, 0)}
	deletes, _ = planSFTPMirrorDeletes(source, destination, nil)
	if sameMirrorDeletePlan(plan, deletes, nil, source, destination) {
		t.Fatal("changed destination content kept a stale deletion approval")
	}
	destination["old.log"] = expected
	source["old.log"] = expected
	deletes, _ = planSFTPMirrorDeletes(source, destination, nil)
	if sameMirrorDeletePlan(plan, deletes, nil, source, destination) {
		t.Fatal("new source item under a deletion path kept a stale deletion approval")
	}
}

func TestRemoveLocalTreeContextIsCancelableAndDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	deletionRoot := filepath.Join(root, "delete-me")
	if err := os.MkdirAll(filepath.Join(deletionRoot, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deletionRoot, "nested", "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	externalFile := filepath.Join(external, "outside.txt")
	if err := os.WriteFile(externalFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(deletionRoot, "linked")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := removeLocalTreeContext(canceled, deletionRoot); err != context.Canceled {
		t.Fatalf("canceled delete error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(deletionRoot, "nested", "inside.txt")); err != nil {
		t.Fatalf("canceled deletion changed the tree: %v", err)
	}
	if err := removeLocalTreeContext(context.Background(), deletionRoot); err != nil {
		t.Fatalf("remove local tree: %v", err)
	}
	if _, err := os.Stat(externalFile); err != nil {
		t.Fatalf("deletion followed a symlink outside its root: %v", err)
	}
}

func TestSFTPComparisonMirrorModeRequiresSecondConfirmation(t *testing.T) {
	source := map[string]sftpclient.Entry{}
	destination := map[string]sftpclient.Entry{"extra.txt": {Name: "extra.txt", Size: 1, Mode: 0o644}}
	deletes, _ := planSFTPMirrorDeletes(source, destination, nil)
	state := modelState{width: 90, height: 12, sftp: &sftpBrowserState{
		standalone: true,
		comparison: &sftpSyncComparison{
			leftRoot: "/local", rightRoot: "/remote", leftEntries: source, rightEntries: destination,
			leftToRightDelete: deletes, direction: sftpLocalSide,
		},
	}}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "m"}))
	state = unwrapModel(t, updated)
	if !state.sftp.comparison.mirror || !strings.Contains(state.sftpView(), "extra.txt") {
		t.Fatal("mirror mode did not display the extra destination path")
	}
	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if state.sftp.prompt == nil || state.sftp.prompt.action != sftpPromptMirrorConfirm || !strings.Contains(state.sftp.prompt.title, "永久删除") {
		t.Fatalf("Enter did not require the explicit mirror deletion confirmation: %#v", state.sftp.prompt)
	}
	updated, _ = state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.sftp.mirrorPlan != nil || state.sftp.prompt != nil || !strings.Contains(state.sftp.status, "已取消") {
		t.Fatal("Escape did not cancel mirror sync before any mutation")
	}
}

func TestSFTPComparisonBidirectionalModeShowsAndQueuesBothDirections(t *testing.T) {
	left := map[string]sftpclient.Entry{"left.txt": {Name: "left.txt", Size: 1, Mode: 0o644, ModTime: time.Unix(10, 0)}}
	right := map[string]sftpclient.Entry{"right.txt": {Name: "right.txt", Size: 2, Mode: 0o644, ModTime: time.Unix(12, 0)}}
	actions, issues := planSFTPBidirectionalSync(left, right)
	state := modelState{width: 100, height: 14, sftp: &sftpBrowserState{
		standalone: true, client: &sftpclient.Client{}, localPath: "/local", path: "/remote",
		comparison: &sftpSyncComparison{
			leftRoot: "/local", rightRoot: "/remote", leftEntries: left, rightEntries: right,
			bidirectionalActions: actions, bidirectionalIssues: issues,
		},
	}}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "b"}))
	state = unwrapModel(t, updated)
	if !state.sftp.comparison.bidirectional {
		t.Fatal("b did not switch to bidirectional mode")
	}
	view := state.sftpView()
	for _, expected := range []string{"双向合并", "左→右 文件  left.txt", "右→左 文件  right.txt"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("bidirectional view is missing %q:\n%s", expected, view)
		}
	}
	updated, cmd := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if cmd == nil || !state.sftp.queueActive || state.sftp.queueTotal != 2 {
		t.Fatalf("bidirectional action did not start a two-item queue: queue=%#v cmd=%v", state.sftp.transferQueue, cmd)
	}
	if state.sftp.queueCurrent == nil || state.sftp.queueCurrent.direction != sftpUpload || len(state.sftp.transferQueue) != 1 || state.sftp.transferQueue[0].direction != sftpDownload {
		t.Fatalf("mixed-direction queue = current %#v remaining %#v", state.sftp.queueCurrent, state.sftp.transferQueue)
	}
}

func TestSFTPMirrorDoesNotDeleteAfterTransferFailureOrSkip(t *testing.T) {
	for _, test := range []struct {
		name      string
		resultErr error
		skipped   int
	}{
		{name: "failed", resultErr: errors.New("copy failed")},
		{name: "skipped", skipped: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := modelState{sftp: &sftpBrowserState{
				queueActive: true, queueTotal: 1, queueSkipped: test.skipped,
				mirrorPlan: &sftpMirrorPlan{},
			}}
			_ = state.finishSFTPQueue([]sftpQueueResult{{transfer: sftpTransferRequest{}, err: test.resultErr}}, nil)
			if state.sftp.mirrorPlan != nil || state.sftp.busy || !strings.Contains(state.sftp.status, "未删除目标侧多余文件") {
				t.Fatalf("mirror plan/status after blocked queue = %#v / %q", state.sftp.mirrorPlan, state.sftp.status)
			}
		})
	}
}
