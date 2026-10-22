package ui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/remotefs"
	"tui-ssh-workbench/internal/infra/sftpclient"
)

const maxSFTPSyncEntries = 100000

type sftpSyncAction struct {
	relative string
	entry    sftpclient.Entry
}

type sftpSyncDeleteAction struct {
	relative string
	entry    sftpclient.Entry
}

type sftpBidirectionalAction struct {
	sourceSide sftpSide
	action     sftpSyncAction
}

type sftpSyncIssue struct {
	relative   string
	message    string
	blocked    bool
	resolvable bool
}

type sftpSyncComparison struct {
	leftRoot             string
	rightRoot            string
	leftEntries          map[string]sftpclient.Entry
	rightEntries         map[string]sftpclient.Entry
	leftToRight          []sftpSyncAction
	rightToLeft          []sftpSyncAction
	leftToRightErr       []sftpSyncIssue
	rightToLeftErr       []sftpSyncIssue
	leftToRightDelete    []sftpSyncDeleteAction
	rightToLeftDelete    []sftpSyncDeleteAction
	leftToRightDeleteErr []sftpSyncIssue
	rightToLeftDeleteErr []sftpSyncIssue
	bidirectionalActions []sftpBidirectionalAction
	bidirectionalIssues  []sftpSyncIssue
	bidirectionalSources map[string]sftpSide
	mirror               bool
	bidirectional        bool
	direction            sftpSide
	selected             int
	top                  int
}

type sftpMirrorPlan struct {
	sourceSide      sftpSide
	destinationSide sftpSide
	sourceRoot      string
	destinationRoot string
	transfers       []sftpTransferRequest
	deletes         []sftpSyncDeleteAction
	issues          []sftpSyncIssue
	deleteSnapshot  map[string]sftpclient.Entry
	issueCount      int
	requestID       uint64
	generation      uint64
}

type sftpMirrorDeleteCompletedMsg struct {
	requestID  uint64
	generation uint64
	deleted    int
	err        error
	cancelled  bool
	refreshErr error
	entries    []sftpclient.Entry
	path       string
	side       sftpSide
}

type sftpCompareCompletedMsg struct {
	requestID  uint64
	generation uint64
	comparison *sftpSyncComparison
	err        error
}

func scanLocalSFTPTree(ctx context.Context, root string) (map[string]sftpclient.Entry, bool, error) {
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return nil, false, fmt.Errorf("读取本地比较目录失败：%w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, false, errors.New("比较起点必须是普通目录")
	}
	entries := make(map[string]sftpclient.Entry)
	truncated := false
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == root {
			return nil
		}
		if len(entries) >= maxSFTPSyncEntries {
			truncated = true
			return fs.SkipAll
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return fmt.Errorf("读取本地项目 %s 失败：%w", current, err)
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		mode := fileInfo.Mode()
		entries[filepath.ToSlash(relative)] = sftpclient.Entry{
			Name: entry.Name(), Size: fileInfo.Size(), Mode: mode, ModeKnown: true,
			ModTime: fileInfo.ModTime(), IsDir: mode.IsDir(), IsSymlink: mode&os.ModeSymlink != 0,
		}
		return nil
	})
	if errors.Is(err, fs.SkipAll) {
		err = nil
	}
	if err != nil {
		return nil, truncated, err
	}
	return entries, truncated, nil
}

func scanRemoteSFTPTree(ctx context.Context, client remotefs.Client, root string) (map[string]sftpclient.Entry, bool, error) {
	if client == nil {
		return nil, false, errors.New("远程文件连接不可用")
	}
	root = sftpclient.CleanRemotePath(root)
	type directory struct{ absolute, relative string }
	pending := []directory{{absolute: root}}
	visited := make(map[string]struct{})
	entries := make(map[string]sftpclient.Entry)
	truncated := false
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		current := pending[0]
		pending = pending[1:]
		absolute := sftpclient.CleanRemotePath(current.absolute)
		if _, ok := visited[absolute]; ok {
			continue
		}
		visited[absolute] = struct{}{}
		children, err := client.List(absolute)
		if err != nil {
			return nil, false, fmt.Errorf("读取远程比较目录 %s 失败：%w", absolute, err)
		}
		for _, entry := range children {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			if entry.Name == "" || entry.Name == "." || entry.Name == ".." || strings.Contains(entry.Name, "/") {
				continue
			}
			if len(entries) >= maxSFTPSyncEntries {
				truncated = true
				break
			}
			relative := entry.Name
			if current.relative != "" {
				relative = path.Join(current.relative, entry.Name)
			}
			entries[relative] = entry
			if entry.IsDir && !entry.IsSymlink {
				pending = append(pending, directory{
					absolute: path.Join(absolute, entry.Name), relative: relative,
				})
			}
		}
		if truncated {
			break
		}
	}
	return entries, truncated, nil
}

func planSFTPSync(source, destination map[string]sftpclient.Entry) ([]sftpSyncAction, []sftpSyncIssue) {
	all := make(map[string]struct{}, len(source)+len(destination))
	for relative := range source {
		all[relative] = struct{}{}
	}
	for relative := range destination {
		all[relative] = struct{}{}
	}
	paths := make([]string, 0, len(all))
	for relative := range all {
		paths = append(paths, relative)
	}
	sort.Strings(paths)

	actions := make([]sftpSyncAction, 0)
	issues := make([]sftpSyncIssue, 0)
	coveredDirectory := ""
	for pathIndex, relative := range paths {
		if coveredDirectory != "" && strings.HasPrefix(relative, coveredDirectory+"/") {
			continue
		}
		coveredDirectory = ""
		sourceEntry, sourceExists := source[relative]
		destinationEntry, destinationExists := destination[relative]
		if !sourceExists {
			continue // Sync is a merge: extra destination files are preserved.
		}
		if sourceEntry.IsSymlink || (!sourceEntry.IsDir && !sourceEntry.Mode.IsRegular()) {
			issues = append(issues, sftpSyncIssue{relative: relative, message: "源项目是符号链接或特殊文件，已跳过"})
			continue
		}
		if !destinationExists {
			actions = append(actions, sftpSyncAction{relative: relative, entry: sourceEntry})
			if sourceEntry.IsDir {
				coveredDirectory = relative
				for childIndex := pathIndex + 1; childIndex < len(paths) && strings.HasPrefix(paths[childIndex], relative+"/"); childIndex++ {
					childPath := paths[childIndex]
					child := source[childPath]
					if strings.HasPrefix(childPath, relative+"/") && (child.IsSymlink || (!child.IsDir && !child.Mode.IsRegular())) {
						issues = append(issues, sftpSyncIssue{relative: childPath, message: "源项目是符号链接或特殊文件，传输目录时会跳过"})
					}
				}
			}
			continue
		}
		if destinationEntry.IsSymlink || (!destinationEntry.IsDir && !destinationEntry.Mode.IsRegular()) {
			issues = append(issues, sftpSyncIssue{relative: relative, message: "目标项目是符号链接或特殊文件，不能安全覆盖", blocked: true})
			if sourceEntry.IsDir {
				coveredDirectory = relative
			}
			continue
		}
		if sourceEntry.IsDir != destinationEntry.IsDir {
			issues = append(issues, sftpSyncIssue{relative: relative, message: "源与目标的文件类型不同，需要手动处理", blocked: true})
			if sourceEntry.IsDir {
				coveredDirectory = relative
			}
			continue
		}
		if sourceEntry.IsDir {
			continue
		}
		modeDiffers := sourceEntry.ModeKnown && destinationEntry.ModeKnown && sourceEntry.Mode.Perm() != destinationEntry.Mode.Perm()
		if sourceEntry.Size != destinationEntry.Size || sourceEntry.ModTime.Unix() != destinationEntry.ModTime.Unix() || modeDiffers {
			actions = append(actions, sftpSyncAction{relative: relative, entry: sourceEntry})
		}
	}
	return actions, issues
}

func planSFTPBidirectionalSync(left, right map[string]sftpclient.Entry) ([]sftpBidirectionalAction, []sftpSyncIssue) {
	all := make(map[string]struct{}, len(left)+len(right))
	for relative := range left {
		all[relative] = struct{}{}
	}
	for relative := range right {
		all[relative] = struct{}{}
	}
	paths := make([]string, 0, len(all))
	for relative := range all {
		paths = append(paths, relative)
	}
	sort.Strings(paths)

	actions := make([]sftpBidirectionalAction, 0)
	issues := make([]sftpSyncIssue, 0)
	coveredDirectory := ""
	for pathIndex, relative := range paths {
		if coveredDirectory != "" && strings.HasPrefix(relative, coveredDirectory+"/") {
			continue
		}
		coveredDirectory = ""
		leftEntry, leftExists := left[relative]
		rightEntry, rightExists := right[relative]
		if !leftExists || !rightExists {
			sourceSide, entry := sftpLocalSide, leftEntry
			if !leftExists {
				sourceSide, entry = sftpRemoteSide, rightEntry
			}
			if entry.IsSymlink || (!entry.IsDir && !entry.Mode.IsRegular()) {
				issues = append(issues, sftpSyncIssue{relative: relative, message: "符号链接或特殊文件不会自动同步"})
				continue
			}
			actions = append(actions, sftpBidirectionalAction{
				sourceSide: sourceSide,
				action:     sftpSyncAction{relative: relative, entry: entry},
			})
			if entry.IsDir {
				coveredDirectory = relative
				sourceEntries := left
				if sourceSide == sftpRemoteSide {
					sourceEntries = right
				}
				for childIndex := pathIndex + 1; childIndex < len(paths) && strings.HasPrefix(paths[childIndex], relative+"/"); childIndex++ {
					childPath := paths[childIndex]
					child := sourceEntries[childPath]
					if child.IsSymlink || (!child.IsDir && !child.Mode.IsRegular()) {
						issues = append(issues, sftpSyncIssue{relative: childPath, message: "目录传输会跳过符号链接或特殊文件"})
					}
				}
			}
			continue
		}
		leftKind, rightKind := sftpSyncEntryKind(leftEntry), sftpSyncEntryKind(rightEntry)
		if leftKind != rightKind {
			issues = append(issues, sftpSyncIssue{relative: relative, message: "两侧文件类型不同，需要手动处理", blocked: true})
			if leftEntry.IsDir || rightEntry.IsDir {
				coveredDirectory = relative
			}
			continue
		}
		if leftKind == "link" || leftKind == "special" {
			issues = append(issues, sftpSyncIssue{relative: relative, message: "符号链接或特殊文件不会自动同步"})
			continue
		}
		if leftEntry.IsDir {
			continue
		}
		modeEqualOrUnknown := !leftEntry.ModeKnown || !rightEntry.ModeKnown || leftEntry.Mode.Perm() == rightEntry.Mode.Perm()
		if leftEntry.Size == rightEntry.Size && leftEntry.ModTime.Unix() == rightEntry.ModTime.Unix() && modeEqualOrUnknown {
			continue
		}
		switch {
		case leftEntry.ModTime.Unix() > rightEntry.ModTime.Unix():
			actions = append(actions, sftpBidirectionalAction{sourceSide: sftpLocalSide, action: sftpSyncAction{relative: relative, entry: leftEntry}})
		case rightEntry.ModTime.Unix() > leftEntry.ModTime.Unix():
			actions = append(actions, sftpBidirectionalAction{sourceSide: sftpRemoteSide, action: sftpSyncAction{relative: relative, entry: rightEntry}})
		default:
			issues = append(issues, sftpSyncIssue{relative: relative, message: "两侧修改时间相同但文件属性不同，无法判断哪侧更新", blocked: true, resolvable: true})
		}
	}
	return actions, issues
}

func (comparison *sftpSyncComparison) bidirectionalResolvedActions() ([]sftpBidirectionalAction, int, int) {
	if comparison == nil {
		return nil, 0, 0
	}
	actions := append([]sftpBidirectionalAction(nil), comparison.bidirectionalActions...)
	resolved, unresolved := 0, 0
	for _, issue := range comparison.bidirectionalIssues {
		if !issue.resolvable {
			unresolved++
			continue
		}
		sourceSide, chosen := comparison.bidirectionalSources[issue.relative]
		if !chosen {
			unresolved++
			continue
		}
		entries := comparison.leftEntries
		if sourceSide == sftpRemoteSide {
			entries = comparison.rightEntries
		}
		entry, exists := entries[issue.relative]
		if !exists {
			unresolved++
			continue
		}
		actions = append(actions, sftpBidirectionalAction{
			sourceSide: sourceSide,
			action:     sftpSyncAction{relative: issue.relative, entry: entry},
		})
		resolved++
	}
	return actions, unresolved, resolved
}

func sftpSyncEntryKind(entry sftpclient.Entry) string {
	switch {
	case entry.IsSymlink:
		return "link"
	case entry.IsDir:
		return "directory"
	case entry.Mode.IsRegular():
		return "file"
	default:
		return "special"
	}
}

func planSFTPMirrorDeletes(source, destination map[string]sftpclient.Entry, syncIssues []sftpSyncIssue) ([]sftpSyncDeleteAction, []sftpSyncIssue) {
	blocked := make(map[string]struct{})
	for _, issue := range syncIssues {
		if issue.blocked {
			blocked[issue.relative] = struct{}{}
		}
	}
	unsafeDirectories := make(map[string]bool)
	for relative, entry := range destination {
		_, inSource := source[relative]
		ineligible := inSource || unsafeSyncRelative(relative) || isBlockedSyncPath(relative, blocked) ||
			(!entry.IsDir && !entry.IsSymlink && !entry.Mode.IsRegular())
		if !ineligible {
			continue
		}
		parent := path.Dir(relative)
		for parent != "." && parent != "/" {
			unsafeDirectories[parent] = true
			parent = path.Dir(parent)
		}
	}
	paths := make([]string, 0, len(destination))
	for relative := range destination {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	deletes := make([]sftpSyncDeleteAction, 0)
	issues := make([]sftpSyncIssue, 0)
	coveredDirectory := ""
	for _, relative := range paths {
		if coveredDirectory != "" && strings.HasPrefix(relative, coveredDirectory+"/") {
			continue
		}
		coveredDirectory = ""
		if _, exists := source[relative]; exists || unsafeSyncRelative(relative) {
			continue
		}
		if isBlockedSyncPath(relative, blocked) {
			continue
		}
		entry := destination[relative]
		if entry.IsSymlink {
			deletes = append(deletes, sftpSyncDeleteAction{relative: relative, entry: entry})
			continue
		}
		if entry.IsDir {
			if !unsafeDirectories[relative] {
				deletes = append(deletes, sftpSyncDeleteAction{relative: relative, entry: entry})
				coveredDirectory = relative
			}
			continue
		}
		if !entry.Mode.IsRegular() {
			issues = append(issues, sftpSyncIssue{
				relative: relative, message: "目标是特殊文件，镜像模式不会自动删除",
			})
			continue
		}
		deletes = append(deletes, sftpSyncDeleteAction{relative: relative, entry: entry})
	}
	return deletes, issues
}

func isBlockedSyncPath(relative string, blocked map[string]struct{}) bool {
	for current := relative; current != "." && current != "/"; current = path.Dir(current) {
		if _, exists := blocked[current]; exists {
			return true
		}
	}
	return false
}

func unsafeSyncRelative(relative string) bool {
	clean := path.Clean(filepath.ToSlash(relative))
	return relative == "" || filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/")
}

func (m *modelState) startSFTPDirectoryComparison() tea.Cmd {
	state := m.sftp
	if state == nil || !state.standalone || state.busy {
		if state != nil {
			state.status, state.statusErr = "目录比较仅适用于独立双栏文件面板", true
		}
		return nil
	}
	leftRoot, rightRoot := state.localPath, state.path
	leftRemote, rightRemote := sftpSideIsRemote(state, sftpLocalSide), sftpSideIsRemote(state, sftpRemoteSide)
	leftClient, rightClient := sftpClientForSide(state, sftpLocalSide), sftpClientForSide(state, sftpRemoteSide)
	state.comparison = &sftpSyncComparison{leftRoot: leftRoot, rightRoot: rightRoot, direction: sftpLocalSide}
	state.requestID++
	requestID, generation := state.requestID, state.generation
	ctx, cancel := context.WithCancel(context.Background())
	state.compareCancel = cancel
	state.busy = true
	state.status, state.statusErr = "正在比较两个目录…", false
	return func() tea.Msg {
		stopClose := context.AfterFunc(ctx, func() { closeSFTPTransferClients(leftClient, rightClient) })
		defer func() {
			stopClose()
			cancel()
		}()
		var left, right map[string]sftpclient.Entry
		var leftTruncated, rightTruncated bool
		var err error
		if leftRemote {
			left, leftTruncated, err = scanRemoteSFTPTree(ctx, leftClient, leftRoot)
		} else {
			left, leftTruncated, err = scanLocalSFTPTree(ctx, leftRoot)
		}
		if err == nil {
			if rightRemote {
				right, rightTruncated, err = scanRemoteSFTPTree(ctx, rightClient, rightRoot)
			} else {
				right, rightTruncated, err = scanLocalSFTPTree(ctx, rightRoot)
			}
		}
		if err == nil && (leftTruncated || rightTruncated) {
			err = fmt.Errorf("目录超过 %d 项比较上限；请缩小比较范围", maxSFTPSyncEntries)
		}
		if ctx.Err() != nil {
			err = context.Canceled
		}
		if err != nil {
			return sftpCompareCompletedMsg{requestID: requestID, generation: generation, err: err}
		}
		leftToRight, leftIssues := planSFTPSync(left, right)
		rightToLeft, rightIssues := planSFTPSync(right, left)
		bidirectionalActions, bidirectionalIssues := planSFTPBidirectionalSync(left, right)
		leftDeletes, leftDeleteIssues := planSFTPMirrorDeletes(left, right, leftIssues)
		rightDeletes, rightDeleteIssues := planSFTPMirrorDeletes(right, left, rightIssues)
		return sftpCompareCompletedMsg{
			requestID: requestID, generation: generation,
			comparison: &sftpSyncComparison{
				leftRoot: leftRoot, rightRoot: rightRoot,
				leftEntries: left, rightEntries: right,
				leftToRight: leftToRight, rightToLeft: rightToLeft,
				leftToRightErr: leftIssues, rightToLeftErr: rightIssues,
				bidirectionalActions: bidirectionalActions, bidirectionalIssues: bidirectionalIssues,
				leftToRightDelete: leftDeletes, rightToLeftDelete: rightDeletes,
				leftToRightDeleteErr: leftDeleteIssues, rightToLeftDeleteErr: rightDeleteIssues,
				direction: sftpLocalSide,
			},
		}
	}
}

func (m *modelState) handleSFTPCompareCompleted(msg sftpCompareCompletedMsg) tea.Cmd {
	state := m.sftp
	if state == nil || state.requestID != msg.requestID || state.generation != msg.generation {
		return nil
	}
	if state.compareCancel != nil {
		state.compareCancel()
		state.compareCancel = nil
	}
	state.busy = false
	if msg.err != nil {
		state.comparison = nil
		if errors.Is(msg.err, context.Canceled) {
			state.client = nil
			state.peerClient = nil
			state.busy = true
			state.status, state.statusErr = "目录比较已取消，正在恢复远程文件连接…", false
			return m.reopenSFTPAfterTransferCancel()
		} else {
			state.status, state.statusErr = "目录比较失败："+msg.err.Error(), true
		}
		return nil
	}
	state.comparison = msg.comparison
	left, right := len(msg.comparison.leftToRight), len(msg.comparison.rightToLeft)
	state.status = fmt.Sprintf("比较完成：左→右 %d 项，右→左 %d 项，双向 %d 项；按 b 查看双向方案，默认保留目标多余文件", left, right, len(msg.comparison.bidirectionalActions))
	state.statusErr = false
	return nil
}

func (m *modelState) updateSFTPComparisonKey(key tea.Key) tea.Cmd {
	state := m.sftp
	comparison := state.comparison
	if comparison == nil {
		return nil
	}
	actions, issues, deletes, deleteIssues := comparison.directionPlan()
	itemCount := len(actions) + len(issues)
	bidirectionalActions := comparison.bidirectionalActions
	if comparison.bidirectional {
		actions, issues, deletes, deleteIssues = nil, comparison.bidirectionalIssues, nil, nil
		itemCount = len(bidirectionalActions) + len(issues)
	} else if comparison.mirror {
		itemCount += len(deletes) + len(deleteIssues)
	}
	visible := max(1, m.height-7)
	switch key.Code {
	case tea.KeyEscape:
		state.comparison = nil
		state.status, state.statusErr = "已返回双栏文件列表", false
		return nil
	case tea.KeyTab:
		if comparison.bidirectional {
			return nil
		}
		if comparison.direction == sftpLocalSide {
			comparison.direction = sftpRemoteSide
		} else {
			comparison.direction = sftpLocalSide
		}
		comparison.selected, comparison.top = 0, 0
		return nil
	case tea.KeyLeft:
		if comparison.bidirectional {
			return nil
		}
		comparison.direction = sftpLocalSide
		comparison.selected, comparison.top = 0, 0
		return nil
	case tea.KeyRight:
		if comparison.bidirectional {
			return nil
		}
		comparison.direction = sftpRemoteSide
		comparison.selected, comparison.top = 0, 0
		return nil
	case tea.KeyUp:
		comparison.selected = clamp(comparison.selected-1, 0, max(0, itemCount-1))
		comparison.top = keepVisible(comparison.selected, comparison.top, visible)
		return nil
	case tea.KeyDown:
		comparison.selected = clamp(comparison.selected+1, 0, max(0, itemCount-1))
		comparison.top = keepVisible(comparison.selected, comparison.top, visible)
		return nil
	case tea.KeyEnter:
		if comparison.bidirectional {
			resolvedActions, unresolvedIssues, manuallyResolved := comparison.bidirectionalResolvedActions()
			if len(resolvedActions) == 0 {
				state.status = "当前没有可自动同步的双向差异"
				state.statusErr = len(issues) > 0
				if state.statusErr {
					state.status += "；请处理下方冲突项"
				}
				return nil
			}
			transfers := make([]sftpTransferRequest, 0, len(resolvedActions))
			for _, action := range resolvedActions {
				transfers = append(transfers, syncTransferForAction(state, action.sourceSide, action.action))
			}
			state.syncIssueCount = unresolvedIssues
			state.comparison = nil
			state.status = fmt.Sprintf("已生成双向同步队列：%d 项按修改时间自动选择，%d 项按手动选择来源；目标覆盖仍会逐项确认", len(resolvedActions)-manuallyResolved, manuallyResolved)
			cmd := m.startSFTPTransferQueue(transfers)
			if cmd == nil {
				state.syncIssueCount = 0
			}
			return cmd
		}
		if len(actions) == 0 && (!comparison.mirror || len(deletes) == 0) {
			state.status = "当前方向没有可安全同步的差异"
			if len(issues)+len(deleteIssues) > 0 {
				state.status += "；请检查下方跳过或冲突项"
			}
			state.statusErr = len(issues)+len(deleteIssues) > 0
			return nil
		}
		sourceSide := comparison.direction
		transfers := make([]sftpTransferRequest, 0, len(actions))
		for _, action := range actions {
			transfers = append(transfers, syncTransferForAction(state, sourceSide, action))
		}
		if comparison.mirror {
			destinationSide := sftpRemoteSide
			if sourceSide == sftpRemoteSide {
				destinationSide = sftpLocalSide
			}
			plan := &sftpMirrorPlan{
				sourceSide: sourceSide, destinationSide: destinationSide,
				sourceRoot:      sftpSyncSideRoot(state, sourceSide),
				destinationRoot: sftpSyncSideRoot(state, destinationSide),
				transfers:       transfers, deletes: append([]sftpSyncDeleteAction(nil), deletes...),
				issues:         append(append([]sftpSyncIssue(nil), issues...), deleteIssues...),
				deleteSnapshot: mirrorDeleteSnapshot(comparison.directionEntries(destinationSide), deletes),
				issueCount:     len(issues) + len(deleteIssues),
				generation:     state.generation,
			}
			state.comparison = nil
			state.mirrorPlan = plan
			state.prompt = &sftpPrompt{
				action: sftpPromptMirrorConfirm,
				title:  fmt.Sprintf("镜像同步将传输 %d 项并永久删除目标侧 %d 项。删除前会重新核对；输入 y 继续，其他键取消", len(transfers), len(deletes)),
			}
			return nil
		}
		state.syncIssueCount = len(issues)
		state.comparison = nil
		state.status = "已生成目录同步队列；目标冲突仍会逐项确认"
		cmd := m.startSFTPTransferQueue(transfers)
		if cmd == nil {
			state.syncIssueCount = 0
		}
		return cmd
	}
	switch key.Text {
	case "b":
		comparison.bidirectional = !comparison.bidirectional
		comparison.selected, comparison.top = 0, 0
		if comparison.bidirectional {
			comparison.mirror = false
			state.status, state.statusErr = "已切换到双向同步；同时间冲突可选中后按 l/r 选择来源", false
		} else {
			state.status, state.statusErr = "已返回单向比较模式", false
		}
		return nil
	case "m":
		if comparison.bidirectional {
			state.status, state.statusErr = "双向同步会合并两侧文件，不执行镜像删除", false
			return nil
		}
		comparison.mirror = !comparison.mirror
		comparison.selected, comparison.top = 0, 0
		if comparison.mirror {
			state.status, state.statusErr = "已切换到镜像模式；执行前会显示删除清单并要求确认", false
		} else {
			state.status, state.statusErr = "已切回合并模式；目标多余文件会保留", false
		}
		return nil
	case "j":
		comparison.selected = clamp(comparison.selected+1, 0, max(0, itemCount-1))
		comparison.top = keepVisible(comparison.selected, comparison.top, visible)
	case "k":
		comparison.selected = clamp(comparison.selected-1, 0, max(0, itemCount-1))
		comparison.top = keepVisible(comparison.selected, comparison.top, visible)
	case "l", "r", "x":
		if !comparison.bidirectional {
			return nil
		}
		issueIndex := comparison.selected - len(comparison.bidirectionalActions)
		if issueIndex < 0 || issueIndex >= len(comparison.bidirectionalIssues) {
			return nil
		}
		issue := comparison.bidirectionalIssues[issueIndex]
		if !issue.resolvable {
			state.status, state.statusErr = "此冲突涉及不支持的文件类型，不能自动选择来源", true
			return nil
		}
		if comparison.bidirectionalSources == nil {
			comparison.bidirectionalSources = make(map[string]sftpSide)
		}
		if key.Text == "x" {
			delete(comparison.bidirectionalSources, issue.relative)
			state.status, state.statusErr = "已清除该项的同步来源选择", false
			return nil
		}
		sourceSide := sftpLocalSide
		label := "左栏"
		if key.Text == "r" {
			sourceSide, label = sftpRemoteSide, "右栏"
		}
		if comparison.bidirectionalSources[issue.relative] == sourceSide {
			delete(comparison.bidirectionalSources, issue.relative)
			state.status, state.statusErr = "已清除该项的同步来源选择", false
			return nil
		}
		comparison.bidirectionalSources[issue.relative] = sourceSide
		state.status = fmt.Sprintf("%s 将覆盖另一侧的同名文件；执行前仍会要求确认", label)
		state.statusErr = false
	}
	return nil
}

func (comparison *sftpSyncComparison) directionPlan() ([]sftpSyncAction, []sftpSyncIssue, []sftpSyncDeleteAction, []sftpSyncIssue) {
	if comparison == nil {
		return nil, nil, nil, nil
	}
	if comparison.direction == sftpRemoteSide {
		return comparison.rightToLeft, comparison.rightToLeftErr, comparison.rightToLeftDelete, comparison.rightToLeftDeleteErr
	}
	return comparison.leftToRight, comparison.leftToRightErr, comparison.leftToRightDelete, comparison.leftToRightDeleteErr
}

func (comparison *sftpSyncComparison) directionEntries(side sftpSide) map[string]sftpclient.Entry {
	if comparison == nil {
		return nil
	}
	if side == sftpRemoteSide {
		return comparison.rightEntries
	}
	return comparison.leftEntries
}

func mirrorDeleteSnapshot(destination map[string]sftpclient.Entry, deletes []sftpSyncDeleteAction) map[string]sftpclient.Entry {
	snapshot := make(map[string]sftpclient.Entry)
	paths := make([]string, 0, len(destination))
	for relative := range destination {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	for _, deletion := range deletes {
		if entry, exists := destination[deletion.relative]; exists {
			snapshot[deletion.relative] = entry
		}
		prefix := deletion.relative + "/"
		for index := sort.SearchStrings(paths, prefix); index < len(paths) && strings.HasPrefix(paths[index], prefix); index++ {
			snapshot[paths[index]] = destination[paths[index]]
		}
	}
	return snapshot
}

func (m *modelState) confirmSFTPMirrorPlan() tea.Cmd {
	state := m.sftp
	if state == nil || state.mirrorPlan == nil {
		return nil
	}
	plan := state.mirrorPlan
	state.syncIssueCount = plan.issueCount
	if len(plan.transfers) == 0 {
		return m.startSFTPMirrorDelete(plan)
	}
	if cmd := m.startSFTPTransferQueue(plan.transfers); cmd != nil {
		return cmd
	}
	state.mirrorPlan = nil
	state.syncIssueCount = 0
	state.status, state.statusErr = "无法启动镜像同步队列，未执行删除", true
	return nil
}

func (m *modelState) startSFTPMirrorDelete(plan *sftpMirrorPlan) tea.Cmd {
	state := m.sftp
	if state == nil || plan == nil || state.generation != plan.generation || state.busy {
		return nil
	}
	state.requestID++
	requestID, generation := state.requestID, state.generation
	plan.requestID = requestID
	ctx, cancel := context.WithCancel(context.Background())
	state.mirrorDeleteCancel = cancel
	state.mirrorPlan = plan
	state.busy = true
	state.status, state.statusErr = "正在重新核对镜像删除清单…", false
	sourceClient := sftpClientForSide(state, plan.sourceSide)
	destinationClient := sftpClientForSide(state, plan.destinationSide)
	sourceRemote := sftpSideIsRemote(state, plan.sourceSide)
	destinationRemote := sftpSideIsRemote(state, plan.destinationSide)
	return func() tea.Msg {
		stopClose := context.AfterFunc(ctx, func() { closeSFTPTransferClients(sourceClient, destinationClient) })
		defer func() {
			stopClose()
			cancel()
		}()
		sourceEntries, sourceTruncated, err := scanSFTPSideTree(ctx, sourceRemote, sourceClient, plan.sourceRoot)
		var destinationEntries map[string]sftpclient.Entry
		var destinationTruncated bool
		if err == nil {
			destinationEntries, destinationTruncated, err = scanSFTPSideTree(ctx, destinationRemote, destinationClient, plan.destinationRoot)
		}
		if err == nil && (sourceTruncated || destinationTruncated) {
			err = fmt.Errorf("目录超过 %d 项镜像校验上限", maxSFTPSyncEntries)
		}
		if err == nil {
			_, syncIssues := planSFTPSync(sourceEntries, destinationEntries)
			currentDeletes, deleteIssues := planSFTPMirrorDeletes(sourceEntries, destinationEntries, syncIssues)
			currentIssues := append(syncIssues, deleteIssues...)
			if !sameMirrorDeletePlan(plan, currentDeletes, currentIssues, sourceEntries, destinationEntries) {
				err = errors.New("删除清单自预览后发生变化；请重新比较并确认")
			}
		}
		deleted := 0
		if err == nil {
			for _, deletion := range plan.deletes {
				if ctx.Err() != nil {
					err = ctx.Err()
					break
				}
				absolute := sftpMirrorAbsolutePath(destinationRemote, plan.destinationRoot, deletion.relative)
				if destinationRemote {
					err = destinationClient.RemoveTreeContext(ctx, absolute)
				} else {
					err = removeLocalTreeContext(ctx, absolute)
				}
				if err != nil {
					err = fmt.Errorf("删除 %s 失败：%w", deletion.relative, err)
					break
				}
				deleted++
			}
		}
		cancelled := errors.Is(err, context.Canceled) || ctx.Err() != nil
		if cancelled {
			err = context.Canceled
		}
		var entries []sftpclient.Entry
		var refreshErr error
		if !cancelled {
			entries, refreshErr = listSFTPPath(destinationClient, plan.destinationSide, plan.destinationRoot)
		}
		return sftpMirrorDeleteCompletedMsg{
			requestID: requestID, generation: generation, deleted: deleted,
			err: err, cancelled: cancelled, entries: entries, refreshErr: refreshErr,
			path: plan.destinationRoot, side: plan.destinationSide,
		}
	}
}

func (m *modelState) handleSFTPMirrorDeleteCompleted(msg sftpMirrorDeleteCompletedMsg) tea.Cmd {
	state := m.sftp
	if state == nil || state.requestID != msg.requestID || state.generation != msg.generation {
		return nil
	}
	if state.mirrorDeleteCancel != nil {
		state.mirrorDeleteCancel()
		state.mirrorDeleteCancel = nil
	}
	state.busy = false
	if msg.cancelled {
		state.mirrorPlan = nil
		state.syncIssueCount = 0
		state.client, state.peerClient = nil, nil
		state.busy = true
		state.status, state.statusErr = "镜像删除已取消，正在恢复远程文件连接…", false
		return tea.Batch(m.reopenSFTPAfterTransferCancel(), m.showTransferNotification("镜像同步已取消", true))
	}
	state.mirrorPlan = nil
	if msg.entries != nil {
		if msg.side == sftpRemoteSide {
			state.path, state.entries = msg.path, msg.entries
			rebuildSFTPEntryViews(state)
		} else if sftpSideIsRemote(state, msg.side) {
			state.peerPath, state.localEntries = msg.path, msg.entries
			rebuildSFTPEntryViews(state)
		} else {
			state.localPath, state.localEntries = msg.path, msg.entries
			rebuildSFTPEntryViews(state)
		}
	}
	if msg.err != nil {
		state.status = "镜像删除未完成"
		if msg.deleted > 0 {
			state.status += fmt.Sprintf("：已删除 %d 项后遇到错误", msg.deleted)
		}
		state.status += "：" + msg.err.Error()
		state.statusErr = true
		state.syncIssueCount = 0
		return m.showTransferNotification("镜像同步未完成", true)
	}
	state.status = fmt.Sprintf("镜像同步完成，已删除目标侧 %d 项", msg.deleted)
	if state.syncIssueCount > 0 {
		state.status += fmt.Sprintf("；%d 项因冲突或不支持的类型跳过", state.syncIssueCount)
	}
	if msg.refreshErr != nil {
		state.status += "；刷新目录失败：" + msg.refreshErr.Error()
	}
	state.statusErr = msg.refreshErr != nil
	state.syncIssueCount = 0
	return m.showTransferNotification(state.status, state.statusErr)
}

func scanSFTPSideTree(ctx context.Context, remote bool, client remotefs.Client, root string) (map[string]sftpclient.Entry, bool, error) {
	if remote {
		return scanRemoteSFTPTree(ctx, client, root)
	}
	return scanLocalSFTPTree(ctx, root)
}

func sameMirrorDeletePlan(plan *sftpMirrorPlan, current []sftpSyncDeleteAction, currentIssues []sftpSyncIssue, source, destination map[string]sftpclient.Entry) bool {
	if plan == nil || len(current) != len(plan.deletes) || len(destination) == 0 && len(plan.deleteSnapshot) != 0 {
		return false
	}
	if len(currentIssues) != len(plan.issues) {
		return false
	}
	for index := range currentIssues {
		if currentIssues[index] != plan.issues[index] {
			return false
		}
	}
	for index := range current {
		if current[index].relative != plan.deletes[index].relative || !sameSyncEntry(current[index].entry, plan.deletes[index].entry) {
			return false
		}
	}
	currentSnapshot := mirrorDeleteSnapshot(destination, current)
	if len(currentSnapshot) != len(plan.deleteSnapshot) {
		return false
	}
	sourceAncestors := make(map[string]struct{}, len(source))
	for sourcePath := range source {
		for current := sourcePath; current != "." && current != "/"; current = path.Dir(current) {
			sourceAncestors[current] = struct{}{}
		}
	}
	for _, deletion := range plan.deletes {
		if _, exists := sourceAncestors[deletion.relative]; exists {
			return false
		}
	}
	for relative, expected := range plan.deleteSnapshot {
		actual, exists := currentSnapshot[relative]
		if !exists || !sameSyncEntry(actual, expected) {
			return false
		}
	}
	return true
}

func sameSyncEntry(left, right sftpclient.Entry) bool {
	modeEqual := left.ModeKnown == right.ModeKnown && (!left.ModeKnown || left.Mode == right.Mode)
	return left.Name == right.Name && left.Size == right.Size && modeEqual &&
		left.ModTime.Equal(right.ModTime) && left.IsDir == right.IsDir && left.IsSymlink == right.IsSymlink
}

func sftpMirrorAbsolutePath(remote bool, root, relative string) string {
	if remote {
		return path.Join(root, path.Clean(relative))
	}
	return filepath.Join(root, filepath.FromSlash(relative))
}

func removeLocalTreeContext(ctx context.Context, root string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	root = filepath.Clean(root)
	volumeRoot := filepath.VolumeName(root) + string(filepath.Separator)
	if root == string(filepath.Separator) || root == "." || (filepath.VolumeName(root) != "" && root == volumeRoot) {
		return errors.New("拒绝递归删除本地根目录")
	}
	type item struct {
		path string
	}
	items := make([]item, 0)
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		items = append(items, item{path: current})
		return nil
	})
	if err != nil {
		return err
	}
	for index := len(items) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Remove(items[index].path); err != nil {
			return fmt.Errorf("删除本地项目 %s 失败：%w", items[index].path, err)
		}
	}
	return nil
}

func syncTransferForAction(state *sftpBrowserState, sourceSide sftpSide, action sftpSyncAction) sftpTransferRequest {
	destinationSide := sftpRemoteSide
	direction := sftpUpload
	if sourceSide == sftpRemoteSide {
		destinationSide = sftpLocalSide
		direction = sftpDownload
	}
	sourceRoot := sftpSyncSideRoot(state, sourceSide)
	destinationRoot := sftpSyncSideRoot(state, destinationSide)
	sourcePath := sftpJoinSidePath(state, sourceSide, sourceRoot, action.relative)
	destinationPath := sftpJoinSidePath(state, destinationSide, destinationRoot, action.relative)
	transfer := sftpTransferRequest{
		direction: direction, side: destinationSide, directory: action.entry.IsDir,
		sourceRemote: sftpSideIsRemote(state, sourceSide),
		destRemote:   sftpSideIsRemote(state, destinationSide),
		sourcePath:   sourcePath, destPath: destinationPath,
		entryPath: sourcePath, localPath: sourcePath, remotePath: destinationPath,
		permissions: action.entry.Mode.Perm(),
	}
	if direction == sftpDownload {
		transfer.remotePath = sourcePath
		transfer.localPath = destinationPath
	}
	return transfer
}

func sftpSyncSideRoot(state *sftpBrowserState, side sftpSide) string {
	if side == sftpRemoteSide {
		return state.path
	}
	if sftpSideIsRemote(state, side) {
		return state.peerPath
	}
	return state.localPath
}

func sftpJoinSidePath(state *sftpBrowserState, side sftpSide, root, relative string) string {
	if sftpSideIsRemote(state, side) {
		return path.Join(root, path.Clean(relative))
	}
	return filepath.Join(root, filepath.FromSlash(relative))
}

func renderSFTPSyncComparison(comparison *sftpSyncComparison, status string, statusErr bool, width, height int, busy bool) string {
	lines := make([]string, height)
	if comparison == nil {
		return strings.Join(lines, "\n")
	}
	sourceName := "左栏 → 右栏"
	sourceRoot, destinationRoot := comparison.leftRoot, comparison.rightRoot
	if comparison.bidirectional {
		sourceName = "双向（修改时间较新的一侧优先）"
	} else if comparison.direction == sftpRemoteSide {
		sourceName = "右栏 → 左栏"
		sourceRoot, destinationRoot = comparison.rightRoot, comparison.leftRoot
	}
	lines[0] = fitLine(brandStyle.Render("目录比较与同步")+"  "+titleStyle.Render(sourceName), width)
	if comparison.bidirectional {
		lines[1] = fitLine(mutedStyle.Render("左栏  "+sanitizeRemoteText(comparison.leftRoot)), width)
		lines[2] = fitLine(mutedStyle.Render("右栏  "+sanitizeRemoteText(comparison.rightRoot)), width)
	} else {
		lines[1] = fitLine(mutedStyle.Render("来源  "+sanitizeRemoteText(sourceRoot)), width)
		lines[2] = fitLine(mutedStyle.Render("目标  "+sanitizeRemoteText(destinationRoot)), width)
	}
	actions, issues, deletes, deleteIssues := comparison.directionPlan()
	var bidirectionalActions []sftpBidirectionalAction
	var bidirectionalManualResolved int
	if comparison.bidirectional {
		bidirectionalActions = comparison.bidirectionalActions
		_, _, bidirectionalManualResolved = comparison.bidirectionalResolvedActions()
		actions, issues, deletes, deleteIssues = nil, comparison.bidirectionalIssues, nil, nil
	}
	blocked, resolvedIssues := 0, 0
	for _, issue := range issues {
		if issue.blocked {
			if comparison.bidirectional && issue.resolvable {
				if _, chosen := comparison.bidirectionalSources[issue.relative]; chosen {
					resolvedIssues++
					continue
				}
			}
			blocked++
		}
	}
	deleteCount := 0
	mode := "合并模式 · 保留目标多余文件 · m 切换镜像"
	if comparison.bidirectional {
		mode = "双向合并 · 修改时间较新的一侧优先 · 同时间冲突需手动处理"
	} else if comparison.mirror {
		deleteCount = len(deletes)
		issues = append(append([]sftpSyncIssue(nil), issues...), deleteIssues...)
		mode = "镜像模式 · 将删除目标多余文件 · m 切换合并"
	}
	lines[3] = fitLine(mutedStyle.Render(fmt.Sprintf("%s · 同步 %d 项 · 删除 %d 项 · 安全冲突 %d 项 · 跳过项 %d 项", mode, len(actions)+len(bidirectionalActions)+bidirectionalManualResolved, deleteCount, blocked, len(issues)-blocked-resolvedIssues)), width)
	lines[4] = fitLine(separatorStyle.Render(strings.Repeat("─", width)), width)
	itemCount := len(bidirectionalActions) + len(actions) + len(issues)
	if comparison.mirror {
		itemCount += len(deletes)
	}
	visible := max(0, height-7)
	top := clamp(comparison.top, 0, max(0, itemCount-visible))
	for row := 0; row < visible; row++ {
		index := top + row
		if index >= itemCount {
			break
		}
		line, bad := "", false
		if index < len(bidirectionalActions) {
			action := bidirectionalActions[index]
			flow := "左→右"
			if action.sourceSide == sftpRemoteSide {
				flow = "右→左"
			}
			kind := "文件"
			if action.action.entry.IsDir {
				kind = "目录"
			}
			line = "+ " + flow + " " + kind + "  " + action.action.relative
		} else if index < len(bidirectionalActions)+len(actions) {
			action := actions[index-len(bidirectionalActions)]
			kind := "文件"
			if action.entry.IsDir {
				kind = "目录"
			}
			line = "+ " + kind + "  " + action.relative
		} else if comparison.mirror && index < len(bidirectionalActions)+len(actions)+len(deletes) {
			deletion := deletes[index-len(bidirectionalActions)-len(actions)]
			kind := "文件"
			if deletion.entry.IsDir {
				kind = "目录"
			} else if deletion.entry.IsSymlink {
				kind = "符号链接"
			}
			line, bad = "− 删除 "+kind+"  "+deletion.relative, true
		} else {
			issueIndex := index - len(bidirectionalActions) - len(actions)
			if comparison.mirror {
				issueIndex -= len(deletes)
			}
			issue := issues[issueIndex]
			prefix := "! "
			if issue.blocked {
				prefix = "× "
			}
			line = prefix + issue.relative + "：" + issue.message
			if comparison.bidirectional && issue.resolvable {
				switch comparison.bidirectionalSources[issue.relative] {
				case sftpLocalSide:
					line += "（已选左栏覆盖右栏）"
				case sftpRemoteSide:
					line += "（已选右栏覆盖左栏）"
				default:
					line += "（按 l/r 选择来源）"
				}
			}
			bad = true
		}
		style := secondaryStyle
		if bad {
			style = warningStyle
		}
		if index == comparison.selected {
			lines[5+row] = fitLine(selectedStyle.Width(width).Render(sanitizeRemoteText(line)), width)
		} else {
			lines[5+row] = fitLine(style.Render(sanitizeRemoteText(line)), width)
		}
	}
	if statusErr {
		lines[height-2] = fitLine(statusErrorStyle.Render(sanitizeRemoteText(status)), width)
	} else {
		lines[height-2] = fitLine(secondaryStyle.Render(sanitizeRemoteText(status)), width)
	}
	help := "Tab/←→ 切方向  ↑↓/j/k 查看  b 单向/双向  m 合并/镜像  Enter 执行  Esc 返回"
	if comparison.bidirectional {
		help = "↑↓/j/k 查看  l/r 选择冲突来源  x 清除选择  b 返回单向  Enter 双向同步  Esc 返回"
	}
	if busy {
		help = "正在扫描目录；Esc 取消比较"
	}
	lines[height-1] = fitLine(mutedStyle.Render(help), width)
	return strings.Join(lines, "\n")
}
