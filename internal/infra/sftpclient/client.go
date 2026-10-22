// Package sftpclient adapts the SFTP protocol to the workbench's file-browser
// and transfer operations.
package sftpclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tui-ssh-workbench/internal/infra/remotefs"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Protocol-neutral file metadata is shared with other remote file protocols.
type Entry = remotefs.Entry
type SearchMatch = remotefs.SearchMatch
type FileVersion = remotefs.FileVersion

const MaxSearchResults = 1000
const maxSearchDirectories = 100000

var ErrRemoteFileChanged = remotefs.ErrRemoteFileChanged

const MaxEditableFileSize = remotefs.MaxEditableFileSize

// Client owns an SFTP subsystem opened over an already authenticated SSH
// transport. Closing it does not close the SSH connection.
type Client struct {
	client *sftp.Client
}

// New opens the SFTP subsystem on an existing SSH client.
func New(sshClient *ssh.Client) (*Client, error) {
	if sshClient == nil {
		return nil, errors.New("SSH 连接不可用")
	}
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		return nil, fmt.Errorf("启动 SFTP 子系统失败：%w", err)
	}
	return &Client{client: client}, nil
}

// Close closes only the SFTP subsystem.
func (c *Client) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

// WorkingDirectory returns the server-side starting directory.
func (c *Client) WorkingDirectory() (string, error) {
	return c.client.Getwd()
}

// RealPath asks the server to canonicalize a remote path.
func (c *Client) RealPath(remotePath string) (string, error) {
	return c.client.RealPath(remotePath)
}

// List reads and returns one remote directory. Symbolic links are reported
// explicitly so the UI can avoid following them accidentally.
func (c *Client) List(remotePath string) ([]Entry, error) {
	infos, err := c.client.ReadDir(remotePath)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(infos))
	for _, info := range infos {
		if info.Name() == "." || info.Name() == ".." {
			continue
		}
		entries = append(entries, entryFromFileInfo(info))
	}
	return entries, nil
}

func entryFromFileInfo(info os.FileInfo) Entry {
	mode := info.Mode()
	entry := Entry{
		Name: info.Name(), Size: info.Size(), Mode: mode, ModeKnown: true,
		ModTime: info.ModTime(), IsDir: mode.IsDir(), IsSymlink: mode&os.ModeSymlink != 0,
	}
	if stat, ok := info.Sys().(*sftp.FileStat); ok && stat.UIDGIDKnown {
		entry.UID, entry.GID, entry.OwnershipKnown = stat.UID, stat.GID, true
	}
	return entry
}

// Search recursively finds remote entries whose names contain query,
// case-insensitively. It never follows symbolic links and bounds both the
// result count and the number of visited directories.
func (c *Client) Search(ctx context.Context, root, query string, resultLimit int) ([]SearchMatch, bool, error) {
	if c == nil || c.client == nil {
		return nil, false, errors.New("远程 SFTP 连接不可用")
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, false, errors.New("搜索内容不能为空")
	}
	if resultLimit <= 0 || resultLimit > MaxSearchResults {
		resultLimit = MaxSearchResults
	}
	root = path.Clean(root)
	if root == "" {
		root = "."
	}
	type directory struct {
		path string
	}
	pending := []directory{{path: root}}
	visited := make(map[string]struct{})
	results := make([]SearchMatch, 0)
	truncated := false
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if len(visited) >= maxSearchDirectories {
			truncated = true
			break
		}
		current := pending[0]
		pending = pending[1:]
		current.path = path.Clean(current.path)
		if _, ok := visited[current.path]; ok {
			continue
		}
		visited[current.path] = struct{}{}
		entries, err := c.List(current.path)
		if err != nil {
			return nil, false, fmt.Errorf("搜索远程目录 %s 失败：%w", current.path, err)
		}
		for _, entry := range entries {
			if entry.Name == "" || entry.Name == "." || entry.Name == ".." || strings.Contains(entry.Name, "/") {
				continue
			}
			entryPath := path.Join(current.path, entry.Name)
			if strings.Contains(strings.ToLower(entry.Name), query) {
				if len(results) == resultLimit {
					truncated = true
					break
				}
				results = append(results, SearchMatch{Path: entryPath, Entry: entry})
			}
			if entry.IsDir && !entry.IsSymlink {
				pending = append(pending, directory{path: entryPath})
			}
		}
		if truncated {
			break
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Path < results[j].Path })
	return results, truncated, nil
}

// Exists checks the exact remote path without following symbolic links.
func (c *Client) Exists(remotePath string) (bool, error) {
	_, err := c.client.Lstat(remotePath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// MakeDirectory creates one remote directory.
func (c *Client) MakeDirectory(remotePath string) error {
	return c.client.Mkdir(remotePath)
}

// Rename moves a remote file, directory, or symbolic link to a new path.
func (c *Client) Rename(oldPath, newPath string) error {
	return c.client.Rename(oldPath, newPath)
}

// SetPermissions updates the rwx permission bits of a regular file or directory.
// Symbolic links and special files are rejected so a path cannot silently
// change the mode of an unexpected target.
func (c *Client) SetPermissions(remotePath string, mode os.FileMode) error {
	if c == nil || c.client == nil {
		return errors.New("远程 SFTP 连接不可用")
	}
	if mode&^os.FileMode(0o777) != 0 {
		return errors.New("权限值必须只包含 rwx 权限位")
	}
	info, err := c.client.Lstat(remotePath)
	if err != nil {
		return fmt.Errorf("读取远程项目权限失败：%w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
		return errors.New("仅支持修改普通文件或目录的权限")
	}
	if err := c.client.Chmod(remotePath, mode.Perm()); err != nil {
		return fmt.Errorf("修改远程项目权限失败：%w", err)
	}
	updated, err := c.client.Lstat(remotePath)
	if err != nil {
		return fmt.Errorf("读取修改后的远程项目权限失败：%w", err)
	}
	if updated.Mode().Perm() != mode.Perm() {
		return errors.New("远程文件系统未能应用请求的 rwx 权限")
	}
	return nil
}

// Remove removes a file or symlink, and RemoveDirectory removes an empty
// directory.
func (c *Client) Remove(remotePath string, directory bool) error {
	if directory {
		return c.client.RemoveDirectory(remotePath)
	}
	return c.client.Remove(remotePath)
}

// RemoveTree recursively removes a remote file or directory without following
// symbolic links. Root-like paths are rejected to keep callers from removing
// the server's filesystem root by mistake.
func (c *Client) RemoveTree(remotePath string) error {
	return c.RemoveTreeContext(context.Background(), remotePath)
}

// RemoveTreeContext recursively removes a remote file or directory without
// following symbolic links. Cancellation is checked between SFTP operations;
// callers may close the client to interrupt a blocked network request.
func (c *Client) RemoveTreeContext(ctx context.Context, remotePath string) error {
	if c == nil || c.client == nil {
		return errors.New("远程 SFTP 连接不可用")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	root := path.Clean(remotePath)
	if root == "/" || root == "." || root == ".." {
		return errors.New("拒绝递归删除远程根目录")
	}
	type item struct {
		path      string
		directory bool
	}
	pending := []string{root}
	items := make([]item, 0)
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		info, err := c.client.Lstat(current)
		if err != nil {
			return fmt.Errorf("读取远程项目 %s 失败：%w", current, err)
		}
		isDirectory := info.IsDir() && info.Mode()&os.ModeSymlink == 0
		items = append(items, item{path: current, directory: isDirectory})
		if !isDirectory {
			continue
		}
		children, err := c.client.ReadDir(current)
		if err != nil {
			return fmt.Errorf("读取远程目录 %s 失败：%w", current, err)
		}
		for _, child := range children {
			pending = append(pending, path.Join(current, child.Name()))
		}
	}
	for index := len(items) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := items[index]
		var err error
		if item.directory {
			err = c.client.RemoveDirectory(item.path)
		} else {
			err = c.client.Remove(item.path)
		}
		if err != nil {
			return fmt.Errorf("删除远程项目 %s 失败：%w", item.path, err)
		}
	}
	return nil
}

// Upload streams a local file to a remote path. An interrupted transfer keeps
// its hidden partial file so a later call can resume after validating that its
// contents still match the source prefix. The completed file is renamed into
// place; when overwrite is approved, the previous remote item is moved aside
// and restored if the final rename fails.
func (c *Client) Upload(ctx context.Context, localPath, remotePath string, overwrite bool, progress func(int64, int64)) error {
	local, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开本地文件失败：%w", err)
	}
	defer local.Close()
	localInfo, err := local.Stat()
	if err != nil {
		return fmt.Errorf("读取本地文件信息失败：%w", err)
	}
	if !localInfo.Mode().IsRegular() {
		return errors.New("当前仅支持上传普通文件")
	}
	if err := c.ensureRemoteDestination(remotePath, overwrite); err != nil {
		return err
	}

	directory, base := path.Split(path.Clean(remotePath))
	if base == "" || base == "." || base == "/" {
		return errors.New("远程目标文件名无效")
	}
	temporary := path.Join(directory, "."+base+".upload.part")
	remote, partialSize, err := openRemotePartial(c.client, temporary)
	if err != nil {
		return fmt.Errorf("创建远程临时文件失败：%w", err)
	}
	offset, err := prepareResume(local, remote, partialSize, localInfo.Size())
	if err != nil {
		_ = remote.Close()
		return fmt.Errorf("校验上传续传数据失败：%w", err)
	}
	if progress != nil {
		progress(offset, localInfo.Size())
	}
	copyErr := copyWithProgressFrom(ctx, remote, local, localInfo.Size(), offset, progress)
	closeErr := remote.Close()
	if copyErr != nil || closeErr != nil {
		if copyErr != nil {
			return fmt.Errorf("上传文件失败：%w", copyErr)
		}
		return fmt.Errorf("关闭远程文件失败：%w", closeErr)
	}
	if err := applyRemoteMetadata(c.client, temporary, localInfo); err != nil {
		_ = restoreRemotePartial(c.client, temporary)
		return fmt.Errorf("应用源文件权限或修改时间失败：%w", err)
	}
	if err := c.replaceRemote(temporary, remotePath, overwrite); err != nil {
		_ = restoreRemotePartial(c.client, temporary)
		return fmt.Errorf("提交上传文件失败：%w", err)
	}
	return nil
}

// Download streams a remote file to a hidden partial file beside the target
// and renames it into place only after the copy is complete. A later call
// resumes a partial file only after validating its bytes against the source.
func (c *Client) Download(ctx context.Context, remotePath, localPath string, overwrite bool, progress func(int64, int64)) error {
	remoteInfo, err := c.client.Lstat(remotePath)
	if err != nil {
		return fmt.Errorf("读取远程文件信息失败：%w", err)
	}
	if remoteInfo.Mode()&os.ModeSymlink != 0 || !remoteInfo.Mode().IsRegular() {
		return errors.New("当前仅支持下载普通文件")
	}
	if err := ensureLocalDestination(localPath, overwrite); err != nil {
		return err
	}
	remote, err := c.client.Open(remotePath)
	if err != nil {
		return fmt.Errorf("打开远程文件失败：%w", err)
	}
	defer remote.Close()

	temporaryPath := localPartialPath(localPath, "download")
	temporary, partialSize, err := openLocalPartial(temporaryPath)
	if err != nil {
		return fmt.Errorf("创建本地临时文件失败：%w", err)
	}
	offset, err := prepareResume(remote, temporary, partialSize, remoteInfo.Size())
	if err != nil {
		_ = temporary.Close()
		return fmt.Errorf("校验下载续传数据失败：%w", err)
	}
	if progress != nil {
		progress(offset, remoteInfo.Size())
	}
	copyErr := copyWithProgressFrom(ctx, temporary, remote, remoteInfo.Size(), offset, progress)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		if copyErr != nil {
			return fmt.Errorf("下载文件失败：%w", copyErr)
		}
		if syncErr != nil {
			return fmt.Errorf("写入本地文件失败：%w", syncErr)
		}
		return fmt.Errorf("关闭本地文件失败：%w", closeErr)
	}
	if err := applyLocalMetadata(temporaryPath, remoteInfo); err != nil {
		_ = restoreLocalPartial(temporaryPath)
		return fmt.Errorf("应用源文件权限或修改时间失败：%w", err)
	}
	if err := replaceLocal(temporaryPath, localPath, overwrite); err != nil {
		_ = restoreLocalPartial(temporaryPath)
		return fmt.Errorf("保存下载文件失败：%w", err)
	}
	return nil
}

// CopyRemoteFileTo implements remotefs.Client for remote destinations. SFTP to
// SFTP streams through a bounded in-memory buffer into a resumable temporary
// file on the destination. Mixed protocols stage one regular file locally
// before uploading it to the destination.
func (c *Client) CopyRemoteFileTo(ctx context.Context, destination remotefs.Client, sourcePath, destinationPath string, overwrite bool, progress func(int64, int64)) error {
	if destination == nil {
		return errors.New("远程目标连接不可用")
	}
	if sftpDestination, ok := destination.(*Client); ok {
		return c.copyRemoteFileToSFTP(ctx, sftpDestination, sourcePath, destinationPath, overwrite, progress)
	}
	return c.copyRemoteFileToOther(ctx, destination, sourcePath, destinationPath, overwrite, progress)
}

func (c *Client) copyRemoteFileToSFTP(ctx context.Context, destination *Client, sourcePath, destinationPath string, overwrite bool, progress func(int64, int64)) error {
	if destination == nil || destination.client == nil {
		return errors.New("远程目标连接不可用")
	}
	info, err := c.client.Lstat(sourcePath)
	if err != nil {
		return fmt.Errorf("读取远程源文件信息失败：%w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("远程源不是普通文件")
	}
	if err := destination.ensureRemoteDestination(destinationPath, overwrite); err != nil {
		return err
	}
	source, err := c.client.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("打开远程源文件失败：%w", err)
	}
	defer source.Close()
	directory, base := path.Split(path.Clean(destinationPath))
	if base == "" || base == "." || base == "/" {
		return errors.New("远程目标文件名无效")
	}
	temporary := path.Join(directory, "."+base+".copy.part")
	remote, partialSize, err := openRemotePartial(destination.client, temporary)
	if err != nil {
		return fmt.Errorf("创建远程临时文件失败：%w", err)
	}
	offset, err := prepareResume(source, remote, partialSize, info.Size())
	if err != nil {
		_ = remote.Close()
		return fmt.Errorf("校验远程续传数据失败：%w", err)
	}
	if progress != nil {
		progress(offset, info.Size())
	}
	copyErr := copyRemoteToRemoteWithProgress(ctx, remote, source, info.Size(), offset, progress)
	closeErr := remote.Close()
	if copyErr != nil || closeErr != nil {
		if copyErr != nil {
			return fmt.Errorf("远程到远程传输失败：%w", copyErr)
		}
		return fmt.Errorf("关闭远程目标文件失败：%w", closeErr)
	}
	if err := applyRemoteMetadata(destination.client, temporary, info); err != nil {
		_ = restoreRemotePartial(destination.client, temporary)
		return fmt.Errorf("应用源文件权限或修改时间失败：%w", err)
	}
	if err := destination.replaceRemote(temporary, destinationPath, overwrite); err != nil {
		_ = restoreRemotePartial(destination.client, temporary)
		return fmt.Errorf("提交远程目标文件失败：%w", err)
	}
	return nil
}

// copyRemoteToRemoteWithProgress keeps source read errors separate from target
// write errors. SFTP servers frequently return the same generic SSH_FX_FAILURE
// status for unrelated causes, so identifying the endpoint is essential when
// diagnosing remote-to-remote transfers.
func copyRemoteToRemoteWithProgress(ctx context.Context, destination io.Writer, source io.Reader, total, copied int64, progress func(int64, int64)) error {
	buffer := make([]byte, 64*1024)
	for {
		if err := WaitIfTransferPaused(ctx); err != nil {
			return err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written, writeErr := destination.Write(buffer[:count])
			copied += int64(written)
			if progress != nil {
				progress(copied, total)
			}
			if writeErr != nil {
				return fmt.Errorf("远程目标写入失败：%w", writeErr)
			}
			if written != count {
				return fmt.Errorf("远程目标写入失败：%w", io.ErrShortWrite)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return fmt.Errorf("远程源读取失败：%w", readErr)
		}
	}
}

func (c *Client) copyRemoteFileToOther(ctx context.Context, destination remotefs.Client, sourcePath, destinationPath string, overwrite bool, progress func(int64, int64)) error {
	if c == nil || c.client == nil || destination == nil {
		return errors.New("远程连接不可用")
	}
	temporary, err := os.CreateTemp("", "tui-ssh-workbench-remote-copy-*")
	if err != nil {
		return fmt.Errorf("创建远程传输暂存文件失败：%w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("关闭远程传输暂存文件失败：%w", err)
	}
	_ = os.Remove(temporaryPath)
	defer os.Remove(temporaryPath)
	if err := c.Download(ctx, sourcePath, temporaryPath, false, nil); err != nil {
		return fmt.Errorf("暂存远程源文件失败：%w", err)
	}
	if err := destination.Upload(ctx, temporaryPath, destinationPath, overwrite, progress); err != nil {
		return fmt.Errorf("写入远程目标文件失败：%w", err)
	}
	return nil
}

// CopyRemoteDirectoryTo copies regular files and directories between SFTP
// transports. Symbolic links and special files are skipped; completed files
// remain at the destination if a later item fails or is cancelled.
func (c *Client) CopyRemoteDirectoryTo(ctx context.Context, destination remotefs.Client, sourceRoot, destinationRoot string, overwrite bool, progress func(int64, int64)) error {
	if destination == nil {
		return errors.New("远程目标连接不可用")
	}
	if sftpDestination, ok := destination.(*Client); ok {
		return c.copyRemoteDirectoryToSFTP(ctx, sftpDestination, sourceRoot, destinationRoot, overwrite, progress)
	}
	return c.copyRemoteDirectoryToOther(ctx, destination, sourceRoot, destinationRoot, overwrite, progress)
}

func (c *Client) copyRemoteDirectoryToSFTP(ctx context.Context, destination *Client, sourceRoot, destinationRoot string, overwrite bool, progress func(int64, int64)) error {
	if destination == nil || destination.client == nil {
		return errors.New("远程目标连接不可用")
	}
	sourceRoot = path.Clean(sourceRoot)
	rootInfo, err := c.client.Lstat(sourceRoot)
	if err != nil {
		return fmt.Errorf("读取远程源目录失败：%w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("远程源不是普通目录")
	}
	if err := destination.ensureRemoteDirectory(destinationRoot, overwrite); err != nil {
		return err
	}
	type item struct {
		remotePath string
		relative   string
		info       os.FileInfo
	}
	items := make([]item, 0)
	var total int64
	var filesTotal int
	walker := c.client.Walk(sourceRoot)
	for walker.Step() {
		if err := WaitIfTransferPaused(ctx); err != nil {
			return err
		}
		if err := walker.Err(); err != nil {
			return fmt.Errorf("扫描远程源目录失败：%w", err)
		}
		info := walker.Stat()
		if info == nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			continue
		}
		remotePath := walker.Path()
		relative, err := remoteRelativePath(sourceRoot, remotePath)
		if err != nil {
			return errors.New("远程源返回了越界路径")
		}
		items = append(items, item{remotePath: remotePath, relative: relative, info: info})
		if info.Mode().IsRegular() {
			total += info.Size()
			filesTotal++
		}
	}
	if err := walker.Err(); err != nil {
		return fmt.Errorf("扫描远程源目录失败：%w", err)
	}
	if err := destination.client.MkdirAll(destinationRoot); err != nil {
		return fmt.Errorf("创建远程目标目录失败：%w", err)
	}
	var completed int64
	filesCompleted := 0
	for _, item := range items {
		if err := WaitIfTransferPaused(ctx); err != nil {
			return err
		}
		targetPath := destinationRoot
		if item.relative != "." {
			targetPath = path.Join(destinationRoot, item.relative)
		}
		if item.info.IsDir() {
			if err := destination.client.MkdirAll(targetPath); err != nil {
				return fmt.Errorf("创建远程子目录 %s 失败：%w", item.relative, err)
			}
			continue
		}
		size := item.info.Size()
		reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, completed, total)
		err := c.CopyRemoteFileTo(ctx, destination, item.remotePath, targetPath, overwrite, func(current, _ int64) {
			reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, completed+current, total)
		})
		if err != nil {
			return fmt.Errorf("传输 %s 失败：%w", item.relative, err)
		}
		completed += size
		filesCompleted++
		reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, completed, total)
	}
	for index := len(items) - 1; index >= 0; index-- {
		if err := WaitIfTransferPaused(ctx); err != nil {
			return err
		}
		item := items[index]
		if !item.info.IsDir() {
			continue
		}
		targetPath := destinationRoot
		if item.relative != "." {
			targetPath = path.Join(destinationRoot, item.relative)
		}
		if err := applyRemoteMetadata(destination.client, targetPath, item.info); err != nil {
			return fmt.Errorf("目录已复制，但应用目录 %s 的权限或修改时间失败：%w", item.relative, err)
		}
	}
	if progress != nil {
		progress(total, total)
	}
	remotefs.ReportTransferProgress(ctx, remotefs.TransferProgress{FilesCompleted: filesTotal, FilesTotal: filesTotal, Transferred: total, Total: total})
	return nil
}

func (c *Client) copyRemoteDirectoryToOther(ctx context.Context, destination remotefs.Client, sourceRoot, destinationRoot string, overwrite bool, progress func(int64, int64)) error {
	if c == nil || c.client == nil || destination == nil {
		return errors.New("远程连接不可用")
	}
	sourceRoot = path.Clean(sourceRoot)
	rootInfo, err := c.client.Lstat(sourceRoot)
	if err != nil {
		return fmt.Errorf("读取远程源目录失败：%w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("远程源不是普通目录")
	}
	type item struct {
		remotePath string
		relative   string
		info       os.FileInfo
	}
	items := make([]item, 0)
	var total int64
	var filesTotal int
	walker := c.client.Walk(sourceRoot)
	for walker.Step() {
		if err := WaitIfTransferPaused(ctx); err != nil {
			return err
		}
		if err := walker.Err(); err != nil {
			return fmt.Errorf("扫描远程源目录失败：%w", err)
		}
		info := walker.Stat()
		if info == nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			continue
		}
		remotePath := walker.Path()
		relative, err := remoteRelativePath(sourceRoot, remotePath)
		if err != nil {
			return errors.New("远程源返回了越界路径")
		}
		items = append(items, item{remotePath: remotePath, relative: relative, info: info})
		if info.Mode().IsRegular() {
			total += info.Size()
			filesTotal++
		}
	}
	if err := walker.Err(); err != nil {
		return fmt.Errorf("扫描远程源目录失败：%w", err)
	}
	if err := ensureRemoteDirectory(destination, destinationRoot); err != nil {
		return err
	}
	var completed int64
	filesCompleted := 0
	for _, item := range items {
		if err := WaitIfTransferPaused(ctx); err != nil {
			return err
		}
		targetPath := destinationRoot
		if item.relative != "." {
			targetPath = path.Join(destinationRoot, item.relative)
		}
		if item.info.IsDir() {
			if err := ensureRemoteDirectory(destination, targetPath); err != nil {
				return fmt.Errorf("创建远程子目录 %s 失败：%w", item.relative, err)
			}
			continue
		}
		size := item.info.Size()
		reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, completed, total)
		err := c.CopyRemoteFileTo(ctx, destination, item.remotePath, targetPath, overwrite, func(current, _ int64) {
			reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, completed+current, total)
		})
		if err != nil {
			return fmt.Errorf("传输 %s 失败：%w", item.relative, err)
		}
		completed += size
		filesCompleted++
		reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, completed, total)
	}
	if progress != nil {
		progress(total, total)
	}
	remotefs.ReportTransferProgress(ctx, remotefs.TransferProgress{FilesCompleted: filesTotal, FilesTotal: filesTotal, Transferred: total, Total: total})
	return nil
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

func ensureRemoteDirectory(destination remotefs.Client, remotePath string) error {
	exists, err := destination.Exists(remotePath)
	if err != nil {
		return fmt.Errorf("检查远程目录失败：%w", err)
	}
	if exists {
		if _, err := destination.List(remotePath); err != nil {
			return fmt.Errorf("远程目标已存在但不是可读目录：%w", err)
		}
		return nil
	}
	if err := destination.MakeDirectory(remotePath); err != nil {
		return fmt.Errorf("创建远程目录失败：%w", err)
	}
	return nil
}

// ReadTextFile reads a bounded regular remote file and returns a content
// fingerprint for conflict detection when it is saved later.
func (c *Client) ReadTextFile(remotePath string, maxSize int64) ([]byte, FileVersion, error) {
	info, err := c.client.Lstat(remotePath)
	if err != nil {
		return nil, FileVersion{}, fmt.Errorf("读取远程文件信息失败：%w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, FileVersion{}, errors.New("仅支持预览普通文件")
	}
	if maxSize > 0 && info.Size() > maxSize {
		return nil, FileVersion{}, fmt.Errorf("文件大小超过限制（最大 %s）", formatSize(maxSize))
	}
	file, err := c.client.Open(remotePath)
	if err != nil {
		return nil, FileVersion{}, fmt.Errorf("打开远程文件失败：%w", err)
	}
	defer file.Close()
	reader := io.Reader(file)
	if maxSize > 0 {
		reader = io.LimitReader(file, maxSize+1)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, FileVersion{}, fmt.Errorf("读取远程文件失败：%w", err)
	}
	if maxSize > 0 && int64(len(content)) > maxSize {
		return nil, FileVersion{}, fmt.Errorf("文件大小超过限制（最大 %s）", formatSize(maxSize))
	}
	return content, FileVersion{
		Size: int64(len(content)), ModTime: info.ModTime(), Mode: info.Mode(),
		Digest: sha256.Sum256(content),
	}, nil
}

// SaveTextFile atomically replaces a remote text file only if its content
// still matches the version that was originally loaded.
func (c *Client) SaveTextFile(ctx context.Context, remotePath string, content []byte, expected FileVersion, progress func(int64, int64)) (FileVersion, error) {
	if int64(len(content)) > MaxEditableFileSize {
		return FileVersion{}, fmt.Errorf("文件大小超过限制（最大 %s）", formatSize(MaxEditableFileSize))
	}
	_, current, err := c.ReadTextFile(remotePath, MaxEditableFileSize)
	if err != nil {
		return FileVersion{}, err
	}
	if current.Digest != expected.Digest {
		return FileVersion{}, ErrRemoteFileChanged
	}
	if err := ctx.Err(); err != nil {
		return FileVersion{}, err
	}
	directory, base := path.Split(path.Clean(remotePath))
	if base == "" || base == "." || base == "/" {
		return FileVersion{}, errors.New("远程文件名无效")
	}
	temporary := path.Join(directory, "."+base+".edit-"+fmt.Sprint(time.Now().UnixNano()))
	remote, err := c.client.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return FileVersion{}, fmt.Errorf("创建远程临时文件失败：%w", err)
	}
	copyErr := copyWithProgress(ctx, remote, bytes.NewReader(content), int64(len(content)), progress)
	closeErr := remote.Close()
	if copyErr != nil || closeErr != nil {
		_ = c.client.Remove(temporary)
		if copyErr != nil {
			return FileVersion{}, fmt.Errorf("写入远程文件失败：%w", copyErr)
		}
		return FileVersion{}, fmt.Errorf("关闭远程文件失败：%w", closeErr)
	}
	if err := c.client.Chmod(temporary, expected.Mode.Perm()); err != nil {
		_ = c.client.Remove(temporary)
		return FileVersion{}, fmt.Errorf("保留远程文件权限失败：%w", err)
	}
	_, current, err = c.ReadTextFile(remotePath, MaxEditableFileSize)
	if err != nil || current.Digest != expected.Digest {
		_ = c.client.Remove(temporary)
		if err != nil {
			return FileVersion{}, err
		}
		return FileVersion{}, ErrRemoteFileChanged
	}
	if err := c.replaceRemote(temporary, remotePath, true); err != nil {
		_ = c.client.Remove(temporary)
		return FileVersion{}, fmt.Errorf("提交远程文件失败：%w", err)
	}
	info, err := c.client.Lstat(remotePath)
	if err != nil {
		return FileVersion{}, fmt.Errorf("文件已保存，但读取新版本失败：%w", err)
	}
	return FileVersion{
		Size: int64(len(content)), ModTime: info.ModTime(), Mode: info.Mode(),
		Digest: sha256.Sum256(content),
	}, nil
}

func formatSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	return fmt.Sprintf("%.1f MiB", float64(size)/(1024*1024))
}

func applyRemoteMetadata(client *sftp.Client, target string, source os.FileInfo) error {
	if err := client.Chmod(target, source.Mode().Perm()); err != nil {
		return fmt.Errorf("设置远程权限失败：%w", err)
	}
	if err := client.Chtimes(target, source.ModTime(), source.ModTime()); err != nil {
		return fmt.Errorf("设置远程修改时间失败：%w", err)
	}
	return nil
}

func applyLocalMetadata(target string, source os.FileInfo) error {
	if err := os.Chmod(target, source.Mode().Perm()); err != nil {
		return fmt.Errorf("设置本地权限失败：%w", err)
	}
	if err := os.Chtimes(target, source.ModTime(), source.ModTime()); err != nil {
		return fmt.Errorf("设置本地修改时间失败：%w", err)
	}
	return nil
}

func restoreRemotePartial(client *sftp.Client, target string) error {
	if err := client.Chmod(target, 0o600); err != nil {
		return err
	}
	now := time.Now()
	return client.Chtimes(target, now, now)
}

func restoreLocalPartial(target string) error {
	if err := os.Chmod(target, 0o600); err != nil {
		return err
	}
	now := time.Now()
	return os.Chtimes(target, now, now)
}

// UploadDirectory recursively copies regular files and empty/non-empty
// directories. Symbolic links and special files are skipped. Completed files
// remain in place if a later item fails or the context is cancelled.
func (c *Client) UploadDirectory(ctx context.Context, localRoot, remoteRoot string, overwrite bool, progress func(int64, int64)) error {
	rootInfo, err := os.Lstat(localRoot)
	if err != nil {
		return fmt.Errorf("读取本地目录失败：%w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("上传源不是普通目录")
	}
	if err := c.ensureRemoteDirectory(remoteRoot, overwrite); err != nil {
		return err
	}

	type item struct {
		localPath string
		relative  string
		info      os.FileInfo
	}
	items := make([]item, 0)
	var total int64
	var filesTotal int
	err = filepath.WalkDir(localRoot, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := WaitIfTransferPaused(ctx); err != nil {
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
		items = append(items, item{localPath: current, relative: relative, info: info})
		if info.Mode().IsRegular() {
			total += info.Size()
			filesTotal++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("扫描本地目录失败：%w", err)
	}
	if err := c.client.MkdirAll(remoteRoot); err != nil {
		return fmt.Errorf("创建远程目标目录失败：%w", err)
	}
	var completed int64
	filesCompleted := 0
	for _, item := range items {
		if err := WaitIfTransferPaused(ctx); err != nil {
			return err
		}
		remotePath := remoteRoot
		if item.relative != "." {
			remotePath = path.Join(remoteRoot, filepath.ToSlash(item.relative))
		}
		if item.info.IsDir() {
			if err := c.client.MkdirAll(remotePath); err != nil {
				return fmt.Errorf("创建远程子目录 %s 失败：%w", item.relative, err)
			}
			continue
		}
		size := item.info.Size()
		reportDirectoryProgress(ctx, progress, filepath.ToSlash(item.relative), filesCompleted, filesTotal, completed, total)
		err := c.Upload(ctx, item.localPath, remotePath, overwrite, func(current, _ int64) {
			reportDirectoryProgress(ctx, progress, filepath.ToSlash(item.relative), filesCompleted, filesTotal, completed+current, total)
		})
		if err != nil {
			return fmt.Errorf("上传 %s 失败：%w", item.relative, err)
		}
		completed += size
		filesCompleted++
		reportDirectoryProgress(ctx, progress, filepath.ToSlash(item.relative), filesCompleted, filesTotal, completed, total)
	}
	for index := len(items) - 1; index >= 0; index-- {
		if err := WaitIfTransferPaused(ctx); err != nil {
			return err
		}
		item := items[index]
		if !item.info.IsDir() {
			continue
		}
		remotePath := remoteRoot
		if item.relative != "." {
			remotePath = path.Join(remoteRoot, filepath.ToSlash(item.relative))
		}
		if err := applyRemoteMetadata(c.client, remotePath, item.info); err != nil {
			return fmt.Errorf("目录已上传，但应用目录 %s 的权限或修改时间失败：%w", item.relative, err)
		}
	}
	if progress != nil {
		progress(total, total)
	}
	remotefs.ReportTransferProgress(ctx, remotefs.TransferProgress{FilesCompleted: filesTotal, FilesTotal: filesTotal, Transferred: total, Total: total})
	return nil
}

// DownloadDirectory recursively copies regular files and directories to the
// local target. Symbolic links and special files are skipped.
func (c *Client) DownloadDirectory(ctx context.Context, remoteRoot, localRoot string, overwrite bool, progress func(int64, int64)) error {
	remoteRoot = path.Clean(remoteRoot)
	rootInfo, err := c.client.Lstat(remoteRoot)
	if err != nil {
		return fmt.Errorf("读取远程目录失败：%w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("下载源不是普通目录")
	}
	if err := ensureLocalDirectory(localRoot, overwrite); err != nil {
		return err
	}

	type item struct {
		remotePath string
		relative   string
		info       os.FileInfo
	}
	items := make([]item, 0)
	var total int64
	var filesTotal int
	walker := c.client.Walk(remoteRoot)
	for walker.Step() {
		if err := WaitIfTransferPaused(ctx); err != nil {
			return err
		}
		if err := walker.Err(); err != nil {
			return fmt.Errorf("扫描远程目录失败：%w", err)
		}
		info := walker.Stat()
		if info == nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			continue
		}
		remotePath := walker.Path()
		relative, err := remoteRelativePath(remoteRoot, remotePath)
		if err != nil {
			return errors.New("远程目录返回了越界路径")
		}
		items = append(items, item{remotePath: remotePath, relative: relative, info: info})
		if info.Mode().IsRegular() {
			total += info.Size()
			filesTotal++
		}
	}
	if err := walker.Err(); err != nil {
		return fmt.Errorf("扫描远程目录失败：%w", err)
	}
	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		return fmt.Errorf("创建本地目标目录失败：%w", err)
	}
	var completed int64
	filesCompleted := 0
	for _, item := range items {
		if err := WaitIfTransferPaused(ctx); err != nil {
			return err
		}
		localPath := localRoot
		if item.relative != "." {
			localPath = filepath.Join(localRoot, filepath.FromSlash(item.relative))
		}
		if item.info.IsDir() {
			if err := os.MkdirAll(localPath, 0o755); err != nil {
				return fmt.Errorf("创建本地子目录 %s 失败：%w", item.relative, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return fmt.Errorf("创建本地目标目录失败：%w", err)
		}
		size := item.info.Size()
		reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, completed, total)
		err := c.Download(ctx, item.remotePath, localPath, overwrite, func(current, _ int64) {
			reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, completed+current, total)
		})
		if err != nil {
			return fmt.Errorf("下载 %s 失败：%w", item.relative, err)
		}
		completed += size
		filesCompleted++
		reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, completed, total)
	}
	for index := len(items) - 1; index >= 0; index-- {
		if err := WaitIfTransferPaused(ctx); err != nil {
			return err
		}
		item := items[index]
		if !item.info.IsDir() {
			continue
		}
		localPath := localRoot
		if item.relative != "." {
			localPath = filepath.Join(localRoot, filepath.FromSlash(item.relative))
		}
		if err := applyLocalMetadata(localPath, item.info); err != nil {
			return fmt.Errorf("目录已下载，但应用目录 %s 的权限或修改时间失败：%w", item.relative, err)
		}
	}
	if progress != nil {
		progress(total, total)
	}
	remotefs.ReportTransferProgress(ctx, remotefs.TransferProgress{FilesCompleted: filesTotal, FilesTotal: filesTotal, Transferred: total, Total: total})
	return nil
}

func remoteRelativePath(root, target string) (string, error) {
	root = path.Clean(root)
	target = path.Clean(target)
	if target == root {
		return ".", nil
	}
	if root == "." {
		if path.IsAbs(target) || target == ".." || strings.HasPrefix(target, "../") {
			return "", errors.New("path is outside root")
		}
		return target, nil
	}
	prefix := strings.TrimSuffix(root, "/") + "/"
	if root == "/" {
		prefix = "/"
	}
	if !strings.HasPrefix(target, prefix) {
		return "", errors.New("path is outside root")
	}
	relative := strings.TrimPrefix(target, prefix)
	if relative == "" || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", errors.New("invalid relative path")
	}
	return relative, nil
}

func (c *Client) ensureRemoteDirectory(remotePath string, overwrite bool) error {
	exists, err := c.Exists(remotePath)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	info, err := c.client.Lstat(remotePath)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("远程目标不是普通目录")
	}
	if !overwrite {
		return os.ErrExist
	}
	return nil
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

func (c *Client) ensureRemoteDestination(remotePath string, overwrite bool) error {
	info, err := c.client.Lstat(remotePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("检查远程目标失败：%w", err)
	}
	if info.IsDir() {
		return errors.New("远程目标是目录")
	}
	if info.Mode()&os.ModeSymlink == 0 && !info.Mode().IsRegular() {
		return errors.New("远程目标不是普通文件或符号链接")
	}
	if !overwrite {
		return os.ErrExist
	}
	return nil
}

func (c *Client) replaceRemote(temporary, remotePath string, overwrite bool) error {
	exists, err := c.Exists(remotePath)
	if err != nil {
		return err
	}
	if !exists {
		return c.client.Rename(temporary, remotePath)
	}
	if !overwrite {
		return os.ErrExist
	}
	backup := remotePath + ".backup-" + fmt.Sprint(time.Now().UnixNano())
	if err := c.client.Rename(remotePath, backup); err != nil {
		return fmt.Errorf("暂存旧文件失败：%w", err)
	}
	if err := c.client.Rename(temporary, remotePath); err != nil {
		if restoreErr := c.client.Rename(backup, remotePath); restoreErr != nil {
			return fmt.Errorf("新文件替换失败：%v；恢复旧文件也失败：%w", err, restoreErr)
		}
		return err
	}
	if err := c.client.Remove(backup); err != nil {
		return fmt.Errorf("新文件已上传，但清理旧文件备份失败：%w", err)
	}
	return nil
}

func replaceLocal(temporary, localPath string, overwrite bool) error {
	_, err := os.Lstat(localPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.Rename(temporary, localPath)
		}
		return err
	}
	if !overwrite {
		return os.ErrExist
	}
	backup := localPath + ".backup-" + fmt.Sprint(time.Now().UnixNano())
	if err := os.Rename(localPath, backup); err != nil {
		return fmt.Errorf("暂存本地旧文件失败：%w", err)
	}
	if err := os.Rename(temporary, localPath); err != nil {
		if restoreErr := os.Rename(backup, localPath); restoreErr != nil {
			return fmt.Errorf("新文件替换失败：%v；恢复本地旧文件也失败：%w", err, restoreErr)
		}
		return err
	}
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("新文件已下载，但清理本地旧文件备份失败：%w", err)
	}
	return nil
}

func localPartialPath(target, operation string) string {
	return filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+"."+operation+".part")
}

func openLocalPartial(partialPath string) (*os.File, int64, error) {
	before, err := os.Lstat(partialPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, 0, err
	}
	if before != nil && (!before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0) {
		return nil, 0, errors.New("续传临时路径不是普通文件")
	}
	file, err := os.OpenFile(partialPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, 0, err
	}
	fileInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	after, err := os.Lstat(partialPath)
	if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode()&os.ModeSymlink != 0 || after == nil || !after.Mode().IsRegular() || !os.SameFile(fileInfo, after) || (before != nil && !os.SameFile(before, fileInfo)) {
		_ = file.Close()
		if err != nil {
			return nil, 0, err
		}
		return nil, 0, errors.New("续传临时路径在打开期间发生变化")
	}
	return file, fileInfo.Size(), nil
}

func openRemotePartial(client *sftp.Client, partialPath string) (*sftp.File, int64, error) {
	before, err := client.Lstat(partialPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, 0, err
	}
	if before != nil && (!before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0) {
		return nil, 0, errors.New("远程续传临时路径不是普通文件")
	}
	file, err := client.OpenFile(partialPath, os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, 0, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		_ = file.Close()
		if err != nil {
			return nil, 0, err
		}
		return nil, 0, errors.New("打开的远程续传路径不是普通文件")
	}
	if before != nil {
		after, statErr := client.Lstat(partialPath)
		if statErr != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			_ = file.Close()
			if statErr != nil {
				return nil, 0, statErr
			}
			return nil, 0, errors.New("远程续传临时路径在打开期间发生变化")
		}
	}
	return file, info.Size(), nil
}

type resumableFile interface {
	io.ReadWriteSeeker
	Truncate(size int64) error
}

func prepareResume(source io.ReadSeeker, partial resumableFile, partialSize, sourceSize int64) (int64, error) {
	if partialSize < 0 || partialSize > sourceSize {
		if err := resetPartial(source, partial); err != nil {
			return 0, err
		}
		return 0, nil
	}
	sourceDigest, err := digestPrefix(source, partialSize)
	if err != nil {
		return 0, fmt.Errorf("读取源文件前缀失败：%w", err)
	}
	partialDigest, err := digestPrefix(partial, partialSize)
	if err != nil {
		return 0, fmt.Errorf("读取临时文件前缀失败：%w", err)
	}
	if sourceDigest != partialDigest {
		if err := resetPartial(source, partial); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if _, err := source.Seek(partialSize, io.SeekStart); err != nil {
		return 0, err
	}
	if _, err := partial.Seek(partialSize, io.SeekStart); err != nil {
		return 0, err
	}
	return partialSize, nil
}

func digestPrefix(reader io.ReadSeeker, size int64) ([32]byte, error) {
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return [32]byte{}, err
	}
	digest := sha256.New()
	if size > 0 {
		if _, err := io.CopyN(digest, reader, size); err != nil {
			return [32]byte{}, err
		}
	}
	var result [32]byte
	copy(result[:], digest.Sum(nil))
	return result, nil
}

func resetPartial(source io.ReadSeeker, partial resumableFile) error {
	if err := partial.Truncate(0); err != nil {
		return err
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := partial.Seek(0, io.SeekStart)
	return err
}

func ensureLocalDestination(localPath string, overwrite bool) error {
	info, err := os.Lstat(localPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("检查本地目标失败：%w", err)
	}
	if info.IsDir() {
		return errors.New("本地目标是目录")
	}
	if info.Mode()&os.ModeSymlink == 0 && !info.Mode().IsRegular() {
		return errors.New("本地目标不是普通文件或符号链接")
	}
	if !overwrite {
		return os.ErrExist
	}
	return nil
}

func copyWithProgress(ctx context.Context, destination io.Writer, source io.Reader, total int64, progress func(int64, int64)) error {
	return copyWithProgressFrom(ctx, destination, source, total, 0, progress)
}

func copyWithProgressFrom(ctx context.Context, destination io.Writer, source io.Reader, total, copied int64, progress func(int64, int64)) error {
	buffer := make([]byte, 64*1024)
	for {
		if err := WaitIfTransferPaused(ctx); err != nil {
			return err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written, writeErr := destination.Write(buffer[:count])
			copied += int64(written)
			if progress != nil {
				progress(copied, total)
			}
			if writeErr != nil {
				return writeErr
			}
			if written != count {
				return io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// CleanRemotePath normalizes a remote path with POSIX separators.
func CleanRemotePath(value string) string {
	if strings.TrimSpace(value) == "" {
		return "."
	}
	return path.Clean(value)
}
