package scpclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"tui-ssh-workbench/internal/infra/remotefs"
)

const directoryDownloadMarkerVersion = 1

type remoteDownloadDirectoryInfo struct {
	mode    os.FileMode
	modTime time.Time
}

type remoteDownloadEntry struct {
	name    string
	kind    byte
	mode    os.FileMode
	size    int64
	modTime time.Time
}

type remoteDownloadDirectory struct {
	remotePath string
	localPath  string
	relative   string
	mode       os.FileMode
	modTime    time.Time
	depth      int
}

type remoteDownloadTreeFile struct {
	remotePath string
	localPath  string
	relative   string
	mode       os.FileMode
	size       int64
	modTime    time.Time
}

type directoryDownloadMarker struct {
	Version    int    `json:"version"`
	RemoteRoot string `json:"remote_root"`
	LocalRoot  string `json:"local_root"`
}

func (client *Client) inspectRemoteDownloadDirectory(ctx context.Context, source string) (remoteDownloadDirectoryInfo, error) {
	output, err := client.runCapture(ctx, inspectRemoteDownloadDirectoryCommand(source), 512)
	if err != nil {
		return remoteDownloadDirectoryInfo{}, fmt.Errorf("读取远程 SCP 目录信息失败：%w", err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 3 || fields[0] != "directory" {
		return remoteDownloadDirectoryInfo{}, fmt.Errorf("远程 SCP 目标不是可下载的普通目录：%q", strings.TrimSpace(string(output)))
	}
	modeValue, err := strconv.ParseUint(fields[1], 8, 32)
	if err != nil || modeValue > 0o7777 {
		return remoteDownloadDirectoryInfo{}, errors.New("远程 SCP 目录权限无效")
	}
	modTime, err := parseRemoteEpochTime(fields[2])
	if err != nil {
		return remoteDownloadDirectoryInfo{}, errors.New("远程 SCP 目录修改时间无效")
	}
	return remoteDownloadDirectoryInfo{mode: os.FileMode(modeValue), modTime: modTime}, nil
}

func inspectRemoteDownloadDirectoryCommand(source string) string {
	return "set -eu; LC_ALL=C; export LC_ALL; if [ -n \"${ZSH_VERSION-}\" ]; then setopt shwordsplit; fi; source=" + shellQuote(source) +
		"; if [ -L \"$source\" ] || [ ! -d \"$source\" ]; then echo 'SCP 下载目标不是普通目录' >&2; exit 73; fi; " +
		remoteStatFieldsShell() + " read_remote_stat; printf 'directory %s %s\\n' \"$mode\" \"$mtime\""
}

func (client *Client) downloadDirectoryResumable(ctx context.Context, remoteRoot, localRoot string, rootInfo remoteDownloadDirectoryInfo, overwrite bool, progress func(int64, int64)) error {
	root, err := cleanWorkspacePath(remoteRoot)
	if err != nil {
		return err
	}
	if err := ensureLocalDirectory(localRoot, true); err != nil {
		return fmt.Errorf("本地目录目标不可用：%w", err)
	}
	directories, files, total, err := client.scanRemoteDownloadTree(ctx, root, localRoot, rootInfo)
	if err != nil {
		return err
	}
	if err := validateDownloadDirectories(directories); err != nil {
		return err
	}
	markerPath, err := createDirectoryDownloadResumeMarker(localRoot, root)
	if err != nil {
		return err
	}
	rootExists, err := localPathExists(localRoot)
	if err != nil {
		return err
	}
	if !rootExists {
		if err := os.Mkdir(localRoot, 0o700); err != nil {
			return fmt.Errorf("创建本地 SCP 下载目录失败：%w", err)
		}
	}
	if err := ensureLocalDirectory(localRoot, true); err != nil {
		return fmt.Errorf("本地目录目标在传输过程中发生变化：%w", err)
	}
	for _, directory := range directories[1:] {
		if err := ensureLocalSubdirectory(directory.localPath); err != nil {
			return fmt.Errorf("创建本地目录 %s 失败：%w", directory.relative, err)
		}
	}

	var transferred int64
	filesCompleted := 0
	if len(files) == 0 {
		if progress != nil {
			progress(0, 0)
		}
		remotefs.ReportTransferProgress(ctx, remotefs.TransferProgress{FilesTotal: 0})
	}
	reporter := remotefs.TransferProgressReporter(ctx)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		reportDirectoryProgress(ctx, progress, file.relative, filesCompleted, len(files), transferred, total)
		skipped, err := client.skipMatchingDownloadedFile(ctx, file, overwrite)
		if err != nil {
			return fmt.Errorf("处理已存在的 SCP 文件 %s 失败：%w", file.relative, err)
		}
		if skipped {
			transferred += file.size
			filesCompleted++
			reportDirectoryProgress(ctx, progress, file.relative, filesCompleted, len(files), transferred, total)
			continue
		}
		fileCtx := ctx
		if reporter != nil {
			currentPath := file.relative
			completedBefore := filesCompleted
			transferredBefore := transferred
			fileCtx = remotefs.WithTransferProgressReporter(ctx, func(current remotefs.TransferProgress) {
				current.CurrentPath = currentPath
				current.FilesCompleted = completedBefore
				current.FilesTotal = len(files)
				current.Transferred += transferredBefore
				current.Total = total
				reporter(current)
			})
		}
		err = client.downloadSingleFileExpected(fileCtx, file.remotePath, file.localPath, overwrite, &file, func(current, _ int64) {
			if progress != nil {
				progress(transferred+current, total)
			}
		})
		if err != nil {
			return fmt.Errorf("下载 SCP 文件 %s 失败：%w", file.relative, err)
		}
		transferred += file.size
		filesCompleted++
		reportDirectoryProgress(ctx, progress, file.relative, filesCompleted, len(files), transferred, total)
	}
	for index := len(directories) - 1; index >= 0; index-- {
		directory := directories[index]
		if err := applyLocalMetadata(directory.localPath, directory.mode, directory.modTime); err != nil {
			return fmt.Errorf("应用目录 %s 的权限或修改时间失败：%w", directory.relative, err)
		}
	}
	if err := os.Remove(markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("移除 SCP 目录续传标记失败：%w", err)
	}
	if progress != nil {
		progress(total, total)
	}
	remotefs.ReportTransferProgress(ctx, remotefs.TransferProgress{
		FilesCompleted: len(files), FilesTotal: len(files), Transferred: total, Total: total,
	})
	return nil
}

func (client *Client) scanRemoteDownloadTree(ctx context.Context, remoteRoot, localRoot string, rootInfo remoteDownloadDirectoryInfo) ([]remoteDownloadDirectory, []remoteDownloadTreeFile, int64, error) {
	directories := []remoteDownloadDirectory{{
		remotePath: remoteRoot, localPath: localRoot, mode: rootInfo.mode, modTime: rootInfo.modTime,
	}}
	type pendingDirectory struct {
		remotePath string
		localPath  string
		relative   string
		depth      int
	}
	pending := []pendingDirectory{{remotePath: remoteRoot, localPath: localRoot}}
	files := make([]remoteDownloadTreeFile, 0)
	var total int64
	entriesSeen := 0
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, err
		}
		current := pending[0]
		pending = pending[1:]
		entries, err := client.listRemoteDirectoryForDownload(ctx, current.remotePath)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("扫描远程 SCP 目录 %s 失败：%w", current.relative, err)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
		for _, entry := range entries {
			entriesSeen++
			if entriesSeen > maxWorkspaceEntries {
				return nil, nil, 0, errors.New("远程 SCP 目录树项目数超过 100000 条安全上限")
			}
			if err := validName(entry.name); err != nil {
				return nil, nil, 0, fmt.Errorf("远程 SCP 名称无法安全映射到本地：%w", err)
			}
			localPath, err := safeLocalChild(current.localPath, entry.name)
			if err != nil {
				return nil, nil, 0, err
			}
			remotePath := path.Join(current.remotePath, entry.name)
			relative := entry.name
			if current.relative != "" {
				relative = path.Join(current.relative, entry.name)
			}
			switch entry.kind {
			case 'l':
				return nil, nil, 0, fmt.Errorf("SCP 目录下载拒绝包含远程符号链接：%s", relative)
			case 'd':
				depth := current.depth + 1
				if depth > maxDirectoryDepth {
					return nil, nil, 0, errors.New("远程 SCP 目录层级超过 256 层安全上限")
				}
				directory := remoteDownloadDirectory{
					remotePath: remotePath, localPath: localPath, relative: relative,
					mode: entry.mode, modTime: entry.modTime, depth: depth,
				}
				directories = append(directories, directory)
				pending = append(pending, pendingDirectory{remotePath: remotePath, localPath: localPath, relative: relative, depth: depth})
			case 'f':
				if entry.size > int64(^uint64(0)>>1)-total {
					return nil, nil, 0, errors.New("SCP 目录文件总大小超出支持范围")
				}
				total += entry.size
				files = append(files, remoteDownloadTreeFile{
					remotePath: remotePath, localPath: localPath, relative: relative,
					mode: entry.mode, size: entry.size, modTime: entry.modTime,
				})
			default:
				return nil, nil, 0, fmt.Errorf("SCP 目录下载不支持远程特殊文件：%s", relative)
			}
		}
	}
	return directories, files, total, nil
}

func (client *Client) listRemoteDirectoryForDownload(ctx context.Context, remotePath string) ([]remoteDownloadEntry, error) {
	directory, err := cleanWorkspacePath(remotePath)
	if err != nil {
		return nil, err
	}
	output, err := client.runCapture(ctx, workspaceDirectoryDownloadListCommand(directory), maxWorkspaceListingBytes)
	if err != nil {
		return nil, err
	}
	return parseRemoteDownloadListing(output)
}

func workspaceDirectoryDownloadListCommand(directory string) string {
	return "set -eu; LC_ALL=C; export LC_ALL; directory=" + shellQuote(directory) + "; cd \"$directory\"; " + `
if [ -n "${ZSH_VERSION-}" ]; then setopt nonomatch shwordsplit; fi
if stat -c '%a %s %Y' . >/dev/null 2>&1; then stat_kind=gnu
elif stat -f '%Lp %z %m' . >/dev/null 2>&1; then stat_kind=bsd
else echo '远端 stat 命令不受支持' >&2; exit 69; fi
for entry in ./* ./.[!.]* ./..?*; do
  [ -e "$entry" ] || [ -L "$entry" ] || continue
  name=${entry#./}
  if [ -L "$entry" ]; then kind=l
  elif [ -d "$entry" ]; then kind=d
  elif [ -f "$entry" ]; then kind=f
  else kind=s; fi
  if [ "$stat_kind" = gnu ]; then
    values=$(stat -c '%a %s %y' "$entry"); set -- $values
    mode=$1; size=$2; date_text="$3 $4 $5"
    if command -v date >/dev/null 2>&1; then mtime=$(date -u -d "$date_text" '+%s.%N' 2>/dev/null) || mtime="$(stat -c '%Y' "$entry").000000000"
    else mtime="$(stat -c '%Y' "$entry").000000000"; fi
  else
    values=$(stat -f '%Lp %z %.9Fm' "$entry"); set -- $values
    mode=$1; size=$2; mtime=$3
  fi
  printf '%s\000%s\000%s\000%s\000%s\000' "$kind" "$mode" "$size" "$mtime" "$name"
done`
}

func parseRemoteDownloadListing(output []byte) ([]remoteDownloadEntry, error) {
	if len(output) == 0 {
		return []remoteDownloadEntry{}, nil
	}
	fields := bytes.Split(output, []byte{0})
	if len(fields) == 0 || len(fields[len(fields)-1]) != 0 {
		return nil, errors.New("远程 SCP 目录记录不完整")
	}
	fields = fields[:len(fields)-1]
	if len(fields)%5 != 0 {
		return nil, errors.New("远程 SCP 目录记录格式无效")
	}
	if len(fields)/5 > maxWorkspaceEntries {
		return nil, errors.New("远程 SCP 目录项目数超过安全上限")
	}
	entries := make([]remoteDownloadEntry, 0, len(fields)/5)
	for index := 0; index < len(fields); index += 5 {
		kindField, modeField, sizeField, modTimeField, nameField := fields[index], fields[index+1], fields[index+2], fields[index+3], fields[index+4]
		if len(kindField) != 1 {
			return nil, errors.New("远程 SCP 项目类型无效")
		}
		modeValue, err := strconv.ParseUint(string(modeField), 8, 32)
		if err != nil || modeValue > 0o7777 {
			return nil, errors.New("远程 SCP 项目权限无效")
		}
		size, err := strconv.ParseInt(string(sizeField), 10, 64)
		if err != nil || size < 0 {
			return nil, errors.New("远程 SCP 项目大小无效")
		}
		modTime, err := parseRemoteEpochTime(string(modTimeField))
		if err != nil {
			return nil, errors.New("远程 SCP 项目修改时间无效")
		}
		name := string(nameField)
		if !utf8.ValidString(name) || containsControl(name) {
			return nil, errors.New("远程 SCP 文件名包含无效字符")
		}
		if err := validName(name); err != nil {
			return nil, fmt.Errorf("远程 SCP 文件名无效：%w", err)
		}
		entries = append(entries, remoteDownloadEntry{
			name: name, kind: kindField[0], mode: os.FileMode(modeValue), size: size, modTime: modTime,
		})
	}
	return entries, nil
}

func validateDownloadDirectories(directories []remoteDownloadDirectory) error {
	for _, directory := range directories {
		info, err := os.Lstat(directory.localPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("检查本地目录 %s 失败：%w", directory.relative, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("本地目录 %s 已存在但不是普通目录", directory.relative)
		}
	}
	return nil
}

func (client *Client) skipMatchingDownloadedFile(ctx context.Context, file remoteDownloadTreeFile, overwrite bool) (bool, error) {
	localInfo, err := os.Lstat(file.localPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if localInfo.Mode()&os.ModeSymlink != 0 || !localInfo.Mode().IsRegular() {
		return false, errors.New("本地同名目标不是普通文件")
	}
	remoteInfo, err := client.inspectRemoteDownloadFile(ctx, file.remotePath)
	if err != nil {
		return false, err
	}
	if remoteInfo.size != file.size || remoteInfo.mode.Perm() != file.mode.Perm() || !remoteInfo.modTime.Equal(file.modTime) {
		return false, fmt.Errorf("远程文件在目录扫描后发生变化：%w", remotefs.ErrRemoteFileChanged)
	}
	if localInfo.Size() == remoteInfo.size && remoteInfo.digestAvailable {
		localDigest, err := digestLocalFilePrefix(file.localPath, localInfo.Size())
		if err != nil {
			return false, err
		}
		if equalSHA256(localDigest[:], remoteInfo.digest[:]) {
			if err := applyLocalMetadata(file.localPath, remoteInfo.mode, remoteInfo.modTime); err != nil {
				return false, fmt.Errorf("更新已完成文件的本地元数据失败：%w", err)
			}
			return true, nil
		}
	}
	if !overwrite {
		return false, os.ErrExist
	}
	return false, nil
}

func localPathExists(localPath string) (bool, error) {
	_, err := os.Lstat(localPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func directoryDownloadResumeMarkerPath(localRoot, remoteRoot string) string {
	key := sha256.Sum256([]byte(localRoot + "\x00" + remoteRoot))
	name := ".tui-scp-directory-" + hex.EncodeToString(key[:12]) + ".resume"
	return filepath.Join(filepath.Dir(localRoot), name)
}

func directoryDownloadResumeMarkerBytes(localRoot, remoteRoot string) ([]byte, error) {
	return json.Marshal(directoryDownloadMarker{Version: directoryDownloadMarkerVersion, RemoteRoot: remoteRoot, LocalRoot: localRoot})
}

func hasDirectoryDownloadResumeMarker(localRoot, remoteRoot string) (bool, error) {
	markerPath := directoryDownloadResumeMarkerPath(localRoot, remoteRoot)
	info, err := os.Lstat(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查 SCP 目录续传标记失败：%w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 4096 {
		return false, errors.New("SCP 目录续传标记不是安全的普通文件")
	}
	marker, err := os.Open(markerPath)
	if err != nil {
		return false, fmt.Errorf("打开 SCP 目录续传标记失败：%w", err)
	}
	defer marker.Close()
	openedInfo, err := marker.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		if err != nil {
			return false, fmt.Errorf("读取 SCP 目录续传标记失败：%w", err)
		}
		return false, errors.New("SCP 目录续传标记在打开期间发生变化")
	}
	actual, err := ioReadAllBounded(marker, 4096)
	if err != nil {
		return false, err
	}
	expected, err := directoryDownloadResumeMarkerBytes(localRoot, remoteRoot)
	if err != nil {
		return false, err
	}
	return bytes.Equal(actual, expected), nil
}

func createDirectoryDownloadResumeMarker(localRoot, remoteRoot string) (string, error) {
	markerPath := directoryDownloadResumeMarkerPath(localRoot, remoteRoot)
	content, err := directoryDownloadResumeMarkerBytes(localRoot, remoteRoot)
	if err != nil {
		return "", err
	}
	marker, err := os.OpenFile(markerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		valid, checkErr := hasDirectoryDownloadResumeMarker(localRoot, remoteRoot)
		if checkErr != nil {
			return "", checkErr
		}
		if !valid {
			return "", errors.New("SCP 目录续传标记与当前传输不匹配")
		}
		return markerPath, nil
	}
	if err != nil {
		return "", fmt.Errorf("创建 SCP 目录续传标记失败：%w", err)
	}
	if _, err := marker.Write(content); err != nil {
		_ = marker.Close()
		_ = os.Remove(markerPath)
		return "", fmt.Errorf("写入 SCP 目录续传标记失败：%w", err)
	}
	if err := marker.Sync(); err != nil {
		_ = marker.Close()
		_ = os.Remove(markerPath)
		return "", fmt.Errorf("同步 SCP 目录续传标记失败：%w", err)
	}
	if err := marker.Close(); err != nil {
		_ = os.Remove(markerPath)
		return "", fmt.Errorf("关闭 SCP 目录续传标记失败：%w", err)
	}
	return markerPath, nil
}

func ioReadAllBounded(reader *os.File, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("读取 SCP 目录续传标记失败：%w", err)
	}
	if int64(len(data)) > limit {
		return nil, errors.New("SCP 目录续传标记超过安全上限")
	}
	return data, nil
}
