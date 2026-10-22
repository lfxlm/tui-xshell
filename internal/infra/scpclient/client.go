// Package scpclient transfers files through authenticated SSH exec channels.
// Shell-backed streaming and hidden staging files let interrupted single-file
// and recursive downloads resume after checking remote metadata and content.
// Since SCP has no standard listing request, its workspace adapter uses
// bounded POSIX shell and GNU/BSD stat queries.
package scpclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"tui-ssh-workbench/internal/infra/remotefs"
)

const (
	maxProtocolLine   = 64 << 10
	maxStderrBytes    = 16 << 10
	copyBufferSize    = 64 << 10
	maxDirectoryDepth = 256
)

// Command is the subset of an SSH exec channel used for an SCP transfer.
// ssh.Session implements this interface.
type Command interface {
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.Reader, error)
	StderrPipe() (io.Reader, error)
	Start(command string) error
	Wait() error
	Close() error
}

// Client opens one SSH exec channel per transfer. The authenticated SSH
// transport remains owned by the caller.
type Client struct {
	openCommand func() (Command, error)
}

func New(openCommand func() (Command, error)) (*Client, error) {
	if openCommand == nil {
		return nil, errors.New("SCP command opener is required")
	}
	return &Client{openCommand: openCommand}, nil
}

// Upload copies a regular local file through a remote shell staging file. The
// remote file is committed only after the stream completes and its size is
// checked. With overwrite disabled, the final hard link is an atomic
// no-replace operation on the remote filesystem.
func (client *Client) Upload(ctx context.Context, localPath, remotePath string, overwrite bool, progress func(current, total int64)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(localPath)
	if err != nil {
		return fmt.Errorf("读取本地源文件失败：%w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("SCP 单文件上传只支持普通文件")
	}
	remotePath, err = cleanRemotePath(remotePath)
	if err != nil {
		return err
	}
	localName := filepath.Base(localPath)
	if err := validName(localName); err != nil {
		return fmt.Errorf("本地文件名无法通过 SCP 传输：%w", err)
	}
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开本地源文件失败：%w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
		if err != nil {
			return fmt.Errorf("读取打开后的本地源文件信息失败：%w", err)
		}
		return errors.New("本地源文件在打开期间发生变化")
	}
	temporary, offset, prefixDigest, partialExists, err := client.selectUploadPartial(ctx, file, remotePath, openedInfo.Size())
	if err != nil {
		return err
	}
	if progress != nil {
		progress(offset, openedInfo.Size())
	}
	streamCommand := uploadResumeStreamCommand(temporary, offset, openedInfo.Size(), prefixDigest, partialExists)
	if err := client.run(ctx, streamCommand, func(stdin io.Writer, _ *bufio.Reader) error {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("定位本地 SCP 源文件失败：%w", err)
		}
		remaining := openedInfo.Size() - offset
		if err := copyExactWithProgress(ctx, stdin, file, remaining, func(current, _ int64) {
			if progress != nil {
				progress(offset+current, openedInfo.Size())
			}
		}); err != nil {
			return fmt.Errorf("上传 SCP 文件内容失败：%w", err)
		}
		afterCopy, err := file.Stat()
		if err != nil {
			return fmt.Errorf("复核本地源文件失败：%w", err)
		}
		if afterCopy.Size() != openedInfo.Size() || !afterCopy.ModTime().Equal(openedInfo.ModTime()) {
			return errors.New("本地源文件在 SCP 传输期间发生变化")
		}
		return nil
	}); err != nil {
		return err
	}
	commitCommand := uploadResumeCommitCommand(remotePath, temporary, openedInfo, overwrite)
	if err := client.runRemoteCommand(ctx, commitCommand); err != nil {
		return fmt.Errorf("提交 SCP 上传文件失败：%w", err)
	}
	return nil
}

// Download copies a remote file or directory through bounded remote shell
// queries and streams. Files are staged beside their destination and committed
// only after validation and the remote command both finish successfully.
func (client *Client) Download(ctx context.Context, remotePath, localPath string, overwrite bool, progress func(current, total int64)) error {
	return client.download(ctx, remotePath, localPath, overwrite, false, progress)
}

// DownloadDirectory receives a remote directory into localRoot. It copies the
// directory's contents into localRoot and supports validated per-file resume.
func (client *Client) DownloadDirectory(ctx context.Context, remoteRoot, localRoot string, overwrite bool, progress func(current, total int64)) error {
	return client.download(ctx, remoteRoot, localRoot, overwrite, true, progress)
}

// UploadDirectory copies regular files and directories beneath localRoot into
// remoteRoot. Each file uses the staged single-file transfer, so a failed
// upload does not leave a truncated destination file. Symbolic links and
// special files are skipped.
func (client *Client) UploadDirectory(ctx context.Context, localRoot, remoteRoot string, overwrite bool, progress func(current, total int64)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(localRoot)
	if err != nil {
		return fmt.Errorf("读取本地 SCP 上传目录失败：%w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("SCP 上传源不是普通目录")
	}
	remoteRoot, err = cleanRemoteDirectory(remoteRoot)
	if err != nil {
		return err
	}
	type item struct {
		localPath string
		relative  string
		info      os.FileInfo
	}
	items := make([]item, 0)
	var total int64
	filesTotal := 0
	err = filepath.WalkDir(localRoot, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(localRoot, current)
		if err != nil {
			return err
		}
		relativeSlash := filepath.ToSlash(relative)
		if relativeSlash != "." {
			for _, component := range strings.Split(relativeSlash, "/") {
				if err := validName(component); err != nil {
					return fmt.Errorf("本地路径无法通过 SCP 传输：%w", err)
				}
			}
		}
		items = append(items, item{localPath: current, relative: relativeSlash, info: info})
		if info.Mode().IsRegular() {
			if info.Size() > int64(^uint64(0)>>1)-total {
				return fmt.Errorf("目录文件总大小超出 SCP 支持范围：%s", current)
			}
			total += info.Size()
			filesTotal++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("扫描本地 SCP 上传目录失败：%w", err)
	}
	if err := client.ensureRemoteDirectory(ctx, remoteRoot, overwrite); err != nil {
		return err
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.relative == "." || !item.info.IsDir() {
			continue
		}
		remotePath := path.Join(remoteRoot, item.relative)
		if err := client.makeRemoteDirectory(ctx, remotePath); err != nil {
			return fmt.Errorf("创建远程子目录 %s 失败：%w", item.relative, err)
		}
	}
	var transferred int64
	filesCompleted := 0
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !item.info.Mode().IsRegular() {
			continue
		}
		remotePath := path.Join(remoteRoot, item.relative)
		reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, transferred, total)
		err := client.Upload(ctx, item.localPath, remotePath, overwrite, func(current, _ int64) {
			reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, transferred+current, total)
		})
		if err != nil {
			return fmt.Errorf("上传 SCP 文件 %s 失败：%w", item.relative, err)
		}
		transferred += item.info.Size()
		filesCompleted++
		reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, transferred, total)
	}
	for index := len(items) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := items[index]
		if !item.info.IsDir() {
			continue
		}
		remotePath := remoteRoot
		if item.relative != "." {
			remotePath = path.Join(remoteRoot, item.relative)
		}
		if err := client.applyRemoteMetadata(ctx, remotePath, item.info); err != nil {
			return fmt.Errorf("目录已上传，但应用目录 %s 的权限或修改时间失败：%w", item.relative, err)
		}
	}
	if progress != nil {
		progress(total, total)
	}
	remotefs.ReportTransferProgress(ctx, remotefs.TransferProgress{
		FilesCompleted: filesTotal, FilesTotal: filesTotal, Transferred: total, Total: total,
	})
	return nil
}

func (client *Client) ensureRemoteDirectory(ctx context.Context, remotePath string, overwrite bool) error {
	target := shellQuote(remotePath)
	var command string
	if overwrite {
		command = "set -eu; target=" + target +
			"; if [ -L \"$target\" ]; then echo '目标目录是符号链接' >&2; exit 73; fi; " +
			"if [ -e \"$target\" ] && [ ! -d \"$target\" ]; then echo '目标路径不是目录' >&2; exit 73; fi; " +
			"mkdir -p \"$target\""
	} else {
		parent := shellQuote(path.Dir(remotePath))
		command = "set -eu; parent=" + parent + "; target=" + target +
			"; mkdir -p \"$parent\"; mkdir \"$target\""
	}
	if err := client.runRemoteCommand(ctx, command); err != nil {
		return fmt.Errorf("准备远程 SCP 目录失败：%w", err)
	}
	return nil
}

func (client *Client) makeRemoteDirectory(ctx context.Context, remotePath string) error {
	remotePath, err := cleanRemoteDirectory(remotePath)
	if err != nil {
		return err
	}
	command := "set -eu; target=" + shellQuote(remotePath) + "; " +
		"if [ -L \"$target\" ]; then echo '目标目录是符号链接' >&2; exit 73; fi; " +
		"if [ -e \"$target\" ] && [ ! -d \"$target\" ]; then echo '目标路径不是目录' >&2; exit 73; fi; " +
		"mkdir -p \"$target\""
	return client.runRemoteCommand(ctx, command)
}

func (client *Client) applyRemoteMetadata(ctx context.Context, remotePath string, info os.FileInfo) error {
	remotePath, err := cleanRemoteDirectory(remotePath)
	if err != nil {
		return err
	}
	quoted := shellQuote(remotePath)
	mode := fmt.Sprintf("%04o", info.Mode().Perm()&0o777)
	// POSIX touch -t is available on common OpenSSH server platforms. UTC
	// avoids interpreting the client's timestamp in the server's local zone.
	timestamp := info.ModTime().UTC().Format("200601021504.05")
	command := "set -eu; chmod " + mode + " " + quoted +
		"; TZ=UTC touch -m -t " + shellQuote(timestamp) + " " + quoted
	return client.runRemoteCommand(ctx, command)
}

func (client *Client) runRemoteCommand(ctx context.Context, command string) error {
	return client.run(ctx, command, func(_ io.Writer, stdout *bufio.Reader) error {
		_, err := io.Copy(io.Discard, stdout)
		return err
	})
}

func reportDirectoryProgress(ctx context.Context, progress func(int64, int64), relativePath string, filesCompleted, filesTotal int, transferred, total int64) {
	if progress != nil {
		progress(transferred, total)
	}
	remotefs.ReportTransferProgress(ctx, remotefs.TransferProgress{
		CurrentPath: relativePath, FilesCompleted: filesCompleted, FilesTotal: filesTotal,
		Transferred: transferred, Total: total,
	})
}

func ensureLocalDirectory(localPath string, overwrite bool) error {
	info, err := os.Lstat(localPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("本地目标不是普通目录")
	}
	if !overwrite {
		return os.ErrExist
	}
	return nil
}

func (client *Client) download(ctx context.Context, remotePath, localPath string, overwrite, requireDirectory bool, progress func(current, total int64)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	remotePath, err := cleanRemoteSourcePath(remotePath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(localPath) == "" {
		return errors.New("本地目标路径不能为空")
	}
	localPath, err = filepath.Abs(localPath)
	if err != nil {
		return fmt.Errorf("解析本地目标路径失败：%w", err)
	}
	destinationInfo, destinationErr := os.Lstat(localPath)
	if destinationErr != nil && !errors.Is(destinationErr, os.ErrNotExist) {
		return fmt.Errorf("检查本地目标失败：%w", destinationErr)
	}
	targetExists := destinationErr == nil
	if targetExists && (destinationInfo.Mode()&os.ModeSymlink != 0 || (!destinationInfo.IsDir() && !destinationInfo.Mode().IsRegular())) {
		return errors.New("本地目标必须是普通文件或目录，且不能是符号链接")
	}
	if requireDirectory && targetExists && !destinationInfo.IsDir() {
		return errors.New("本地目录目标已存在但不是普通目录")
	}
	if targetExists && !overwrite {
		resumeMarkerValid := false
		if destinationInfo.IsDir() {
			resumeMarkerValid, err = hasDirectoryDownloadResumeMarker(localPath, remotePath)
			if err != nil {
				return err
			}
		}
		if !resumeMarkerValid {
			return os.ErrExist
		}
	}
	parent := filepath.Dir(localPath)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		if err != nil {
			return fmt.Errorf("本地目标目录不可用：%w", err)
		}
		return errors.New("本地目标父路径不是目录")
	}
	if requireDirectory || (targetExists && destinationInfo.IsDir()) {
		directoryInfo, err := client.inspectRemoteDownloadDirectory(ctx, remotePath)
		if err != nil {
			return err
		}
		return client.downloadDirectoryResumable(ctx, remotePath, localPath, directoryInfo, overwrite, progress)
	}
	err = client.downloadSingleFile(ctx, remotePath, localPath, overwrite, progress)
	if !errors.Is(err, errRemoteDownloadIsDirectory) {
		return err
	}
	directoryInfo, err := client.inspectRemoteDownloadDirectory(ctx, remotePath)
	if err != nil {
		return err
	}
	return client.downloadDirectoryResumable(ctx, remotePath, localPath, directoryInfo, overwrite, progress)
}

func (client *Client) run(ctx context.Context, command string, transfer func(io.Writer, *bufio.Reader) error) error {
	session, err := client.openCommand()
	if err != nil {
		return fmt.Errorf("打开 SSH SCP 通道失败：%w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return fmt.Errorf("打开 SSH SCP 输入失败：%w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return fmt.Errorf("打开 SSH SCP 输出失败：%w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		_ = session.Close()
		return fmt.Errorf("打开 SSH SCP 错误输出失败：%w", err)
	}
	if err := session.Start(command); err != nil {
		_ = session.Close()
		return fmt.Errorf("启动远程 SCP 命令失败：%w", err)
	}
	stderrDone := make(chan string, 1)
	go func() {
		stderrDone <- readCapped(stderr, maxStderrBytes)
	}()
	stopCancellation := context.AfterFunc(ctx, func() { _ = session.Close() })
	transferErr := transfer(stdin, bufio.NewReader(stdout))
	if transferErr != nil {
		_ = session.Close()
	}
	_ = stdin.Close()
	waitErr := session.Wait()
	stopCancellation()
	stderrText := <-stderrDone
	_ = session.Close()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if transferErr != nil {
		if stderrText != "" {
			return fmt.Errorf("%w（远程 SCP：%s）", transferErr, stderrText)
		}
		return transferErr
	}
	if waitErr != nil {
		if stderrText != "" {
			return fmt.Errorf("远程 SCP 命令失败：%w：%s", waitErr, stderrText)
		}
		return fmt.Errorf("远程 SCP 命令失败：%w", waitErr)
	}
	return nil
}

func cleanRemotePath(remotePath string) (string, error) {
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "" || containsControl(remotePath) || !utf8.ValidString(remotePath) {
		return "", errors.New("远程路径不能为空或包含控制字符")
	}
	cleaned := path.Clean(remotePath)
	if cleaned == "." || cleaned == "/" {
		return "", errors.New("SCP 目标必须是文件路径")
	}
	if strings.HasPrefix(cleaned, "-") {
		cleaned = "./" + cleaned
	}
	return cleaned, nil
}

func cleanRemoteDirectory(remotePath string) (string, error) {
	cleaned, err := cleanRemotePath(remotePath)
	if err != nil {
		return "", err
	}
	return cleaned, nil
}

func cleanRemoteSourcePath(remotePath string) (string, error) {
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "" || containsControl(remotePath) || !utf8.ValidString(remotePath) {
		return "", errors.New("远程源路径不能为空或包含控制字符")
	}
	cleaned := path.Clean(remotePath)
	if cleaned == "." || cleaned == "/" {
		return "", errors.New("SCP 下载源必须是具体文件或目录路径")
	}
	if strings.HasPrefix(cleaned, "-") {
		cleaned = "./" + cleaned
	}
	return cleaned, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func validName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, "/\\\\\x00\r\n<>:\"|?*") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") ||
		!utf8.ValidString(name) || containsControl(name) || isWindowsReservedName(name) {
		return errors.New("文件名为空、越界或含 SCP 不支持的控制字符")
	}
	return nil
}

func containsControl(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) {
			return true
		}
	}
	return false
}

func isWindowsReservedName(name string) bool {
	name = strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch name {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}

func readResponseAfterStatus(reader *bufio.Reader, status byte) error {
	if status == 0 {
		return nil
	}
	message, err := readProtocolLine(reader)
	if err != nil {
		return fmt.Errorf("远程 SCP 状态 %d，且无法读取错误信息：%w", status, err)
	}
	return fmt.Errorf("远程 SCP 状态 %d：%s", status, strings.TrimSpace(message))
}

func readProtocolLine(reader *bufio.Reader) (string, error) {
	line := make([]byte, 0, min(reader.Size(), maxProtocolLine))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxProtocolLine {
			return "", errors.New("SCP 协议行超过长度限制")
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return "", err
		}
		break
	}
	return strings.TrimSuffix(string(line), "\n"), nil
}

func copyExactWithProgress(ctx context.Context, destination io.Writer, source io.Reader, size int64, progress func(int64, int64)) error {
	buffer := make([]byte, copyBufferSize)
	var copied int64
	for copied < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(buffer))
		if remaining := size - copied; remaining < want {
			want = remaining
		}
		read, readErr := io.ReadFull(source, buffer[:want])
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			copied += int64(written)
			if writeErr != nil {
				return writeErr
			}
			if written != read {
				return io.ErrShortWrite
			}
			if progress != nil {
				progress(copied, size)
			}
		}
		if readErr != nil {
			return readErr
		}
	}
	return nil
}

func readCapped(reader io.Reader, limit int) string {
	var buffer cappedBuffer
	buffer.limit = limit
	_, _ = io.Copy(&buffer, reader)
	return strings.TrimSpace(buffer.String())
}

type cappedBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (buffer *cappedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	originalLength := len(data)
	if len(buffer.data) < buffer.limit {
		remaining := buffer.limit - len(buffer.data)
		if len(data) > remaining {
			data = data[:remaining]
		}
		buffer.data = append(buffer.data, data...)
	}
	return originalLength, nil
}

func (buffer *cappedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return string(buffer.data)
}

func ensureLocalFileDestination(localPath string, overwrite bool) error {
	info, err := os.Lstat(localPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("检查本地目标文件失败：%w", err)
	}
	if info.IsDir() {
		return errors.New("本地目标是目录")
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return errors.New("本地目标不是普通文件")
	}
	if !overwrite {
		return os.ErrExist
	}
	return nil
}

func ensureLocalSubdirectory(localPath string) error {
	info, err := os.Lstat(localPath)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("目标已存在但不是普通目录")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Mkdir(localPath, 0o700)
}

func safeLocalChild(parent, name string) (string, error) {
	if err := validName(name); err != nil {
		return "", fmt.Errorf("远程 SCP 名称无效：%w", err)
	}
	parent, err := filepath.Abs(parent)
	if err != nil {
		return "", err
	}
	child := filepath.Join(parent, name)
	relative, err := filepath.Rel(parent, child)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("远程 SCP 名称越过本地目标目录")
	}
	return child, nil
}

func applyLocalMetadata(target string, mode os.FileMode, modTime time.Time) error {
	if err := os.Chmod(target, mode.Perm()&0o777); err != nil {
		return err
	}
	if !modTime.IsZero() {
		if err := os.Chtimes(target, modTime, modTime); err != nil {
			return err
		}
	}
	return nil
}

func installLocalFile(temporaryPath, localPath string, overwrite bool) error {
	if !overwrite {
		if err := os.Link(temporaryPath, localPath); err != nil {
			return err
		}
		return os.Remove(temporaryPath)
	}
	if _, err := os.Lstat(localPath); errors.Is(err, os.ErrNotExist) {
		return os.Rename(temporaryPath, localPath)
	} else if err != nil {
		return err
	}
	backupFile, err := os.CreateTemp(filepath.Dir(localPath), ".scp-backup-*")
	if err != nil {
		return err
	}
	backup := backupFile.Name()
	if err := backupFile.Close(); err != nil {
		_ = os.Remove(backup)
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}
	if err := os.Rename(localPath, backup); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, localPath); err != nil {
		if restoreErr := os.Rename(backup, localPath); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("恢复原文件失败：%w", restoreErr))
		}
		return err
	}
	return os.Remove(backup)
}
