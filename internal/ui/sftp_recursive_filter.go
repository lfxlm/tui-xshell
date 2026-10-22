package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"tui-ssh-workbench/internal/infra/sftpclient"
)

const maxRecursiveFilterDirectories = 100000
const maxRecursiveFilterEntries = 1000000

type sftpDirectoryLister interface {
	List(string) ([]sftpclient.Entry, error)
}

func (m *modelState) startSFTPFilterSearch(side sftpSide, root string, filter sftpEntryFilter) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy {
		return nil
	}
	remote := sftpSideIsRemote(state, side)
	client := sftpClientForSide(state, side)
	if remote && client == nil {
		state.status, state.statusErr = "远程文件连接不可用", true
		return nil
	}
	if remote {
		root = sftpclient.CleanRemotePath(root)
	} else {
		root = filepath.Clean(root)
	}
	ctx, cancel := context.WithCancel(context.Background())
	state.requestID++
	requestID, generation := state.requestID, state.generation
	state.searchActive = true
	state.searchFilterMode = true
	state.searchQuery = filter.raw
	state.searchRoot = root
	state.searchSide = side
	state.searchResults = nil
	state.searchSelected, state.searchTop = 0, 0
	state.searchTruncated = false
	state.searchCancel = cancel
	state.busy = true
	state.status = "正在递归过滤文件…"
	state.statusErr = false
	return func() tea.Msg {
		var results []sftpSearchResult
		var truncated bool
		var err error
		if remote {
			results, truncated, err = filterRemoteTree(ctx, client, root, filter, sftpclient.MaxSearchResults)
		} else {
			results, truncated, err = filterLocalTree(ctx, root, filter, sftpclient.MaxSearchResults)
		}
		return sftpSearchCompletedMsg{
			requestID: requestID, generation: generation, side: side, root: root,
			query: filter.raw, filterMode: true, results: results, truncated: truncated, err: err,
		}
	}
}

func filterRemoteTree(ctx context.Context, client sftpDirectoryLister, root string, filter sftpEntryFilter, resultLimit int) ([]sftpSearchResult, bool, error) {
	if client == nil {
		return nil, false, errors.New("远程文件连接不可用")
	}
	root = path.Clean(root)
	if root == "" {
		root = "."
	}
	resultLimit = boundedSFTPFilterResultLimit(resultLimit)
	pending := []string{root}
	visited := make(map[string]struct{})
	results := make([]sftpSearchResult, 0)
	truncated := false
	visitedEntries := 0
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if len(visited) >= maxRecursiveFilterDirectories {
			truncated = true
			break
		}
		current := path.Clean(pending[len(pending)-1])
		pending = pending[:len(pending)-1]
		if _, exists := visited[current]; exists {
			continue
		}
		visited[current] = struct{}{}
		entries, err := client.List(current)
		if err != nil {
			return nil, false, fmt.Errorf("读取远程目录 %s 失败：%w", current, err)
		}
		for _, entry := range entries {
			visitedEntries++
			if visitedEntries > maxRecursiveFilterEntries {
				truncated = true
				break
			}
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			if !validSFTPWalkEntryName(entry.Name) {
				continue
			}
			entryPath := path.Join(current, entry.Name)
			relativePath := relativeSFTPFilterPath(root, entryPath, true)
			if matchesSFTPEntryFilterAtPath(entry, filter, relativePath) {
				if len(results) >= resultLimit {
					truncated = true
					break
				}
				results = append(results, sftpSearchResult{Path: entryPath, Entry: entry})
			}
			if entry.IsDir && !entry.IsSymlink {
				if len(visited)+len(pending) >= maxRecursiveFilterDirectories {
					truncated = true
					break
				}
				pending = append(pending, entryPath)
			}
		}
		if truncated {
			break
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Path < results[j].Path })
	return results, truncated, nil
}

func filterLocalTree(ctx context.Context, root string, filter sftpEntryFilter, resultLimit int) ([]sftpSearchResult, bool, error) {
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, false, fmt.Errorf("解析本地目录失败：%w", err)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, false, fmt.Errorf("读取本地搜索目录失败：%w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, false, errors.New("递归过滤起点必须是普通目录，不能是符号链接")
	}
	resultLimit = boundedSFTPFilterResultLimit(resultLimit)
	pending := []string{root}
	visited := make(map[string]struct{})
	results := make([]sftpSearchResult, 0)
	truncated := false
	visitedEntries := 0
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if len(visited) >= maxRecursiveFilterDirectories {
			truncated = true
			break
		}
		current := filepath.Clean(pending[len(pending)-1])
		pending = pending[:len(pending)-1]
		if _, exists := visited[current]; exists {
			continue
		}
		visited[current] = struct{}{}
		entries, err := os.ReadDir(current)
		if err != nil {
			return nil, false, fmt.Errorf("读取本地目录 %s 失败：%w", current, err)
		}
		for _, directoryEntry := range entries {
			visitedEntries++
			if visitedEntries > maxRecursiveFilterEntries {
				truncated = true
				break
			}
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			name := directoryEntry.Name()
			if !validSFTPWalkEntryName(name) {
				continue
			}
			entryPath := filepath.Join(current, name)
			info, err := os.Lstat(entryPath)
			if err != nil {
				return nil, false, fmt.Errorf("读取本地文件 %s 失败：%w", entryPath, err)
			}
			entry := sftpclient.Entry{
				Name: name, Size: info.Size(), Mode: info.Mode(), ModeKnown: true,
				ModTime: info.ModTime(), IsDir: info.IsDir(), IsSymlink: info.Mode()&os.ModeSymlink != 0,
			}
			relativePath, err := filepath.Rel(root, entryPath)
			if err != nil {
				return nil, false, fmt.Errorf("计算本地过滤相对路径失败：%w", err)
			}
			if matchesSFTPEntryFilterAtPath(entry, filter, filepath.ToSlash(relativePath)) {
				if len(results) >= resultLimit {
					truncated = true
					break
				}
				results = append(results, sftpSearchResult{Path: entryPath, Entry: entry})
			}
			if entry.IsDir && !entry.IsSymlink {
				if len(visited)+len(pending) >= maxRecursiveFilterDirectories {
					truncated = true
					break
				}
				pending = append(pending, entryPath)
			}
		}
		if truncated {
			break
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Path < results[j].Path })
	return results, truncated, nil
}

func validSFTPWalkEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.Contains(name, "/")
}

func relativeSFTPFilterPath(root, entryPath string, remote bool) string {
	if !remote {
		relative, err := filepath.Rel(root, entryPath)
		if err != nil {
			return filepath.ToSlash(filepath.Base(entryPath))
		}
		return filepath.ToSlash(relative)
	}
	cleanRoot, cleanEntry := path.Clean(root), path.Clean(entryPath)
	if cleanRoot == "." {
		return strings.TrimPrefix(cleanEntry, "./")
	}
	if cleanRoot == "/" {
		return strings.TrimPrefix(cleanEntry, "/")
	}
	return strings.TrimPrefix(cleanEntry, strings.TrimSuffix(cleanRoot, "/")+"/")
}

func boundedSFTPFilterResultLimit(limit int) int {
	if limit <= 0 || limit > sftpclient.MaxSearchResults {
		return sftpclient.MaxSearchResults
	}
	return limit
}
