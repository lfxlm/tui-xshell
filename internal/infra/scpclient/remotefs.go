package scpclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"tui-ssh-workbench/internal/infra/remotefs"
)

const (
	maxWorkspaceListingBytes = 16 << 20
	maxWorkspaceEntries      = 100_000
	maxWorkspaceSearchDirs   = 100_000
)

var errWorkspaceOutputTooLarge = errors.New("远程文件操作输出超过安全上限")

// Entry and FileVersion expose SCP workspace metadata through the same
// protocol-neutral types used by the SFTP and FTP adapters.
type Entry = remotefs.Entry
type FileVersion = remotefs.FileVersion

var ErrRemoteFileChanged = remotefs.ErrRemoteFileChanged

var _ remotefs.Client = (*Client)(nil)

// Close is a no-op because each SCP operation opens and closes its own SSH
// exec channel; the authenticated SSH transport remains owned by the caller.
func (client *Client) Close() error { return nil }

// WorkingDirectory returns the server account's current directory.
func (client *Client) WorkingDirectory() (string, error) {
	output, err := client.runCapture(context.Background(), "pwd -P", 64<<10)
	if err != nil {
		return "", fmt.Errorf("读取远程工作目录失败：%w", err)
	}
	directory := strings.TrimSuffix(string(output), "\n")
	if directory == "" {
		return "", errors.New("远程 Shell 没有返回工作目录")
	}
	return cleanWorkspacePath(directory)
}

// RealPath resolves a directory using the remote shell's physical working
// directory. SCP does not define a path-canonicalization operation.
func (client *Client) RealPath(remotePath string) (string, error) {
	target, err := cleanWorkspacePath(remotePath)
	if err != nil {
		return "", err
	}
	output, err := client.runCapture(context.Background(), "cd "+shellQuote(target)+" && pwd -P", 64<<10)
	if err != nil {
		return "", fmt.Errorf("解析远程目录失败：%w", err)
	}
	directory := strings.TrimSuffix(string(output), "\n")
	if directory == "" {
		return "", errors.New("远程 Shell 没有返回解析后的目录")
	}
	return cleanWorkspacePath(directory)
}

// List obtains names and basic metadata through a portable POSIX shell loop.
// It supports GNU stat and BSD stat (including macOS), and encodes fields with
// NUL separators so spaces, tabs, and newlines cannot corrupt record parsing.
func (client *Client) List(remotePath string) ([]Entry, error) {
	directory, err := cleanWorkspacePath(remotePath)
	if err != nil {
		return nil, err
	}
	command := workspaceListCommand(directory)
	output, err := client.runCapture(context.Background(), command, maxWorkspaceListingBytes)
	if err != nil {
		return nil, fmt.Errorf("读取 SCP 远程目录失败：%w", err)
	}
	return parseWorkspaceListing(output)
}

func workspaceListCommand(directory string) string {
	return "set -eu; directory=" + shellQuote(directory) + "; cd \"$directory\"; " + `
# OpenSSH runs exec commands through the account's configured shell. zsh is a
# common default on macOS and enables NOMATCH while disabling POSIX-style word
# splitting by default; both options break the POSIX glob/metadata loop below.
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
  if [ "$stat_kind" = gnu ]; then values=$(stat -c '%a %s %Y' "$entry")
  else values=$(stat -f '%Lp %z %m' "$entry"); fi
  set -- $values
  mode=$1; size=$2; mtime=$3
  printf '%s\000%s\000%s\000%s\000%s\000' "$kind" "$mode" "$size" "$mtime" "$name"
done`
}

func parseWorkspaceListing(output []byte) ([]Entry, error) {
	if len(output) == 0 {
		return []Entry{}, nil
	}
	fields := bytes.Split(output, []byte{0})
	if len(fields) == 0 || len(fields[len(fields)-1]) != 0 {
		return nil, errors.New("远程目录元数据记录不完整")
	}
	fields = fields[:len(fields)-1]
	if len(fields)%5 != 0 {
		return nil, errors.New("远程目录元数据格式无效")
	}
	if len(fields)/5 > maxWorkspaceEntries {
		return nil, errors.New("远程目录项目数超过安全上限")
	}
	entries := make([]Entry, 0, len(fields)/5)
	for index := 0; index < len(fields); index += 5 {
		kind := string(fields[index])
		modeValue, err := strconv.ParseUint(string(fields[index+1]), 8, 32)
		if err != nil {
			return nil, fmt.Errorf("解析远程项目权限失败：%w", err)
		}
		size, err := strconv.ParseInt(string(fields[index+2]), 10, 64)
		if err != nil || size < 0 {
			return nil, errors.New("远程项目大小无效")
		}
		mtime, err := strconv.ParseInt(string(fields[index+3]), 10, 64)
		if err != nil {
			return nil, errors.New("远程项目修改时间无效")
		}
		name := string(fields[index+4])
		if name == "" || name == "." || name == ".." || strings.Contains(name, "/") || !utf8.ValidString(name) || containsControl(name) {
			// SCP's legacy file records cannot safely represent these names, and
			// the TUI cannot display control characters as one row.
			continue
		}
		mode := os.FileMode(modeValue) & 0o777
		entry := Entry{Name: name, Size: size, Mode: mode, ModeKnown: true, ModTime: time.Unix(mtime, 0)}
		switch kind {
		case "d":
			entry.IsDir = true
			entry.Mode |= os.ModeDir
		case "l":
			entry.IsSymlink = true
			entry.Mode |= os.ModeSymlink
		case "f":
			// Regular files have no additional os.FileMode type bit.
		case "s":
			entry.Mode |= os.ModeDevice
		default:
			return nil, fmt.Errorf("远程项目类型无效：%q", kind)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Search recursively searches entry names without following symbolic links.
func (client *Client) Search(ctx context.Context, root, query string, resultLimit int) ([]remotefs.SearchMatch, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, false, errors.New("搜索内容不能为空")
	}
	if resultLimit <= 0 || resultLimit > 1000 {
		resultLimit = 1000
	}
	root, err := cleanWorkspacePath(root)
	if err != nil {
		return nil, false, err
	}
	type pendingDirectory struct{ path string }
	pending := []pendingDirectory{{path: root}}
	visited := make(map[string]struct{})
	results := make([]remotefs.SearchMatch, 0)
	truncated := false
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		current := path.Clean(pending[0].path)
		pending = pending[1:]
		if _, ok := visited[current]; ok {
			continue
		}
		if len(visited) >= maxWorkspaceSearchDirs {
			truncated = true
			break
		}
		visited[current] = struct{}{}
		entries, err := client.List(current)
		if err != nil {
			return nil, false, fmt.Errorf("搜索远程目录 %s 失败：%w", current, err)
		}
		for _, entry := range entries {
			entryPath := path.Join(current, entry.Name)
			if strings.Contains(strings.ToLower(entry.Name), query) {
				if len(results) >= resultLimit {
					truncated = true
					break
				}
				results = append(results, remotefs.SearchMatch{Path: entryPath, Entry: entry})
			}
			if entry.IsDir && !entry.IsSymlink {
				pending = append(pending, pendingDirectory{path: entryPath})
			}
		}
		if truncated {
			break
		}
	}
	return results, truncated, nil
}

func (client *Client) Exists(remotePath string) (bool, error) {
	target, err := cleanWorkspacePath(remotePath)
	if err != nil {
		return false, err
	}
	output, err := client.runCapture(context.Background(), "if [ -e "+shellQuote(target)+" ] || [ -L "+shellQuote(target)+" ]; then printf 1; else printf 0; fi", 8)
	if err != nil {
		return false, err
	}
	return string(output) == "1", nil
}

func (client *Client) MakeDirectory(remotePath string) error {
	target, err := cleanWorkspacePath(remotePath)
	if err != nil {
		return err
	}
	if target == "/" || target == "." {
		return errors.New("不能把远程根目录作为新建目录")
	}
	if err := client.runRemoteCommand(context.Background(), "mkdir "+shellQuote(target)); err != nil {
		return fmt.Errorf("创建远程目录失败：%w", err)
	}
	return nil
}

func (client *Client) Rename(oldPath, newPath string) error {
	oldTarget, err := cleanWorkspacePath(oldPath)
	if err != nil {
		return err
	}
	newTarget, err := cleanWorkspacePath(newPath)
	if err != nil {
		return err
	}
	if oldTarget == "/" || oldTarget == "." || newTarget == "/" || newTarget == "." {
		return errors.New("不能重命名远程根目录")
	}
	command := "set -eu; source=" + shellQuote(oldTarget) + "; target=" + shellQuote(newTarget) +
		"; [ -e \"$source\" ] || [ -L \"$source\" ]; [ ! -e \"$target\" ] && [ ! -L \"$target\" ]; mv \"$source\" \"$target\""
	if err := client.runRemoteCommand(context.Background(), command); err != nil {
		return fmt.Errorf("重命名远程项目失败：%w", err)
	}
	return nil
}

func (client *Client) SetPermissions(remotePath string, mode os.FileMode) error {
	if mode&^os.FileMode(0o777) != 0 {
		return errors.New("权限值必须只包含 rwx 权限位")
	}
	entry, err := client.entryForPath(remotePath)
	if err != nil {
		return err
	}
	if entry.IsSymlink || (!entry.IsDir && !entry.Mode.IsRegular()) {
		return errors.New("仅支持修改普通文件或目录的权限")
	}
	target, err := cleanWorkspacePath(remotePath)
	if err != nil {
		return err
	}
	command := "set -eu; target=" + shellQuote(target) + "; " +
		"if [ -L \"$target\" ] || { [ ! -f \"$target\" ] && [ ! -d \"$target\" ]; }; then exit 73; fi; " +
		"chmod " + fmt.Sprintf("%03o", mode.Perm()) + " \"$target\""
	if err := client.runRemoteCommand(context.Background(), command); err != nil {
		return fmt.Errorf("修改远程项目权限失败：%w", err)
	}
	updated, err := client.entryForPath(target)
	if err != nil {
		return err
	}
	if updated.Mode.Perm() != mode.Perm() {
		return errors.New("远程文件系统未能应用请求的 rwx 权限")
	}
	return nil
}

func (client *Client) entryForPath(remotePath string) (Entry, error) {
	target, err := cleanWorkspacePath(remotePath)
	if err != nil {
		return Entry{}, err
	}
	parent, name := path.Dir(target), path.Base(target)
	if name == "." || name == "/" {
		return Entry{}, errors.New("远程路径必须指向一个项目")
	}
	entries, err := client.List(parent)
	if err != nil {
		return Entry{}, err
	}
	for _, entry := range entries {
		if entry.Name == name {
			return entry, nil
		}
	}
	return Entry{}, os.ErrNotExist
}

func (client *Client) Remove(remotePath string, directory bool) error {
	target, err := cleanWorkspacePath(remotePath)
	if err != nil {
		return err
	}
	if target == "/" || target == "." {
		return errors.New("不能删除远程根目录")
	}
	command := "rm -f " + shellQuote(target)
	if directory {
		command = "rmdir " + shellQuote(target)
	}
	if err := client.runRemoteCommand(context.Background(), command); err != nil {
		return fmt.Errorf("删除远程项目失败：%w", err)
	}
	return nil
}

func (client *Client) RemoveTree(remotePath string) error {
	return client.RemoveTreeContext(context.Background(), remotePath)
}

func (client *Client) RemoveTreeContext(ctx context.Context, remotePath string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	target, err := cleanWorkspacePath(remotePath)
	if err != nil {
		return err
	}
	if target == "/" || target == "." || target == ".." {
		return errors.New("拒绝递归删除远程根目录")
	}
	command := "rm -rf " + shellQuote(target)
	if err := client.runRemoteCommand(ctx, command); err != nil {
		return fmt.Errorf("递归删除远程项目失败：%w", err)
	}
	return nil
}

func (client *Client) ReadTextFile(remotePath string, maxSize int64) ([]byte, FileVersion, error) {
	target, err := cleanWorkspacePath(remotePath)
	if err != nil {
		return nil, FileVersion{}, err
	}
	entry, err := client.entryForPath(target)
	if err != nil {
		return nil, FileVersion{}, fmt.Errorf("读取远程文件信息失败：%w", err)
	}
	if entry.IsSymlink || !entry.Mode.IsRegular() {
		return nil, FileVersion{}, errors.New("仅支持预览普通文件")
	}
	if maxSize <= 0 {
		maxSize = remotefs.MaxEditableFileSize
	}
	if entry.Size > maxSize {
		return nil, FileVersion{}, fmt.Errorf("文件大小超过限制（最大 %s）", formatWorkspaceSize(maxSize))
	}
	content, err := client.runCapture(context.Background(), "cat "+shellQuote(target), maxSize)
	if err != nil {
		return nil, FileVersion{}, fmt.Errorf("读取远程文件失败：%w", err)
	}
	if int64(len(content)) > maxSize {
		return nil, FileVersion{}, fmt.Errorf("文件大小超过限制（最大 %s）", formatWorkspaceSize(maxSize))
	}
	if int64(len(content)) != entry.Size {
		return nil, FileVersion{}, remotefs.ErrRemoteFileChanged
	}
	return content, FileVersion{Size: int64(len(content)), ModTime: entry.ModTime, Mode: entry.Mode, Digest: sha256.Sum256(content)}, nil
}

func (client *Client) SaveTextFile(ctx context.Context, remotePath string, content []byte, expected FileVersion, progress func(int64, int64)) (FileVersion, error) {
	if int64(len(content)) > remotefs.MaxEditableFileSize {
		return FileVersion{}, fmt.Errorf("文件大小超过限制（最大 %s）", formatWorkspaceSize(remotefs.MaxEditableFileSize))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return FileVersion{}, err
	}
	target, err := cleanWorkspacePath(remotePath)
	if err != nil {
		return FileVersion{}, err
	}
	_, current, err := client.ReadTextFile(target, remotefs.MaxEditableFileSize)
	if err != nil {
		return FileVersion{}, err
	}
	if current.Digest != expected.Digest {
		return FileVersion{}, remotefs.ErrRemoteFileChanged
	}
	stageRoot, err := os.MkdirTemp("", "tui-scp-edit-*")
	if err != nil {
		return FileVersion{}, fmt.Errorf("创建本地保存暂存文件失败：%w", err)
	}
	defer os.RemoveAll(stageRoot)
	temporary, err := os.CreateTemp(stageRoot, "content-*")
	if err != nil {
		return FileVersion{}, fmt.Errorf("创建本地保存暂存文件失败：%w", err)
	}
	localPath := temporary.Name()
	if err := temporary.Chmod(current.Mode.Perm()); err != nil {
		_ = temporary.Close()
		return FileVersion{}, err
	}
	written, err := temporary.Write(content)
	if err != nil {
		_ = temporary.Close()
		return FileVersion{}, err
	}
	if written != len(content) {
		_ = temporary.Close()
		return FileVersion{}, io.ErrShortWrite
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return FileVersion{}, err
	}
	if err := temporary.Close(); err != nil {
		return FileVersion{}, err
	}
	_, current, err = client.ReadTextFile(target, remotefs.MaxEditableFileSize)
	if err != nil {
		return FileVersion{}, err
	}
	if current.Digest != expected.Digest {
		return FileVersion{}, remotefs.ErrRemoteFileChanged
	}
	if err := client.Upload(ctx, localPath, target, true, progress); err != nil {
		return FileVersion{}, err
	}
	_, version, err := client.ReadTextFile(target, remotefs.MaxEditableFileSize)
	return version, err
}

// CopyRemoteFileTo and CopyRemoteDirectoryTo use a private local staging area.
// SCP has no server-side copy operation, so this is the only portable route
// between separate SSH transports (and between SCP and other protocols).
func (client *Client) CopyRemoteFileTo(ctx context.Context, destination remotefs.Client, sourcePath, destinationPath string, overwrite bool, progress func(int64, int64)) error {
	if destination == nil {
		return errors.New("远程目标连接不可用")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	stageRoot, err := os.MkdirTemp("", "tui-scp-copy-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageRoot)
	localPath := filepath.Join(stageRoot, "payload")
	if err := client.Download(ctx, sourcePath, localPath, false, nil); err != nil {
		return fmt.Errorf("暂存远程源文件失败：%w", err)
	}
	if err := destination.Upload(ctx, localPath, destinationPath, overwrite, progress); err != nil {
		return fmt.Errorf("写入远程目标文件失败：%w", err)
	}
	return nil
}

func (client *Client) CopyRemoteDirectoryTo(ctx context.Context, destination remotefs.Client, sourceRoot, destinationRoot string, overwrite bool, progress func(int64, int64)) error {
	if destination == nil {
		return errors.New("远程目标连接不可用")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	stageRoot, err := os.MkdirTemp("", "tui-scp-directory-copy-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageRoot)
	localRoot := filepath.Join(stageRoot, "payload")
	if err := client.DownloadDirectory(ctx, sourceRoot, localRoot, false, nil); err != nil {
		return fmt.Errorf("暂存远程源目录失败：%w", err)
	}
	if err := destination.UploadDirectory(ctx, localRoot, destinationRoot, overwrite, progress); err != nil {
		return fmt.Errorf("写入远程目标目录失败：%w", err)
	}
	return nil
}

func (client *Client) runCapture(ctx context.Context, command string, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = maxWorkspaceListingBytes
	}
	var output []byte
	err := client.run(ctx, command, func(_ io.Writer, stdout *bufio.Reader) error {
		data, err := io.ReadAll(io.LimitReader(stdout, limit+1))
		if err != nil {
			return err
		}
		if int64(len(data)) > limit {
			return errWorkspaceOutputTooLarge
		}
		output = data
		return nil
	})
	return output, err
}

func cleanWorkspacePath(remotePath string) (string, error) {
	if remotePath == "" {
		remotePath = "."
	}
	if !utf8.ValidString(remotePath) || containsControl(remotePath) {
		return "", errors.New("远程路径包含 SCP 不支持的控制字符")
	}
	cleaned := path.Clean(remotePath)
	if strings.HasPrefix(cleaned, "-") {
		cleaned = "./" + cleaned
	}
	return cleaned, nil
}

func formatWorkspaceSize(size int64) string {
	value := float64(size)
	unit := "B"
	for _, nextUnit := range []string{"KiB", "MiB", "GiB", "TiB"} {
		if value < 1024 {
			break
		}
		value /= 1024
		unit = nextUnit
	}
	if unit == "B" {
		return fmt.Sprintf("%d %s", size, unit)
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}
