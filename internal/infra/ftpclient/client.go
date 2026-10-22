// Package ftpclient adapts FTP and FTPS to the shared remote file interface.
package ftpclient

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ftp "github.com/jlaffaye/ftp"
	"tui-ssh-workbench/internal/infra/remotefs"
)

type TLSMode string

const (
	TLSOff      TLSMode = "off"
	TLSExplicit TLSMode = "explicit"
	TLSImplicit TLSMode = "implicit"
)

// Config contains one FTP endpoint and transient credentials. Passwords are
// not stored by this package or copied into the host model.
type Config struct {
	Address    string
	Port       int
	Username   string
	Password   string
	TLSMode    TLSMode
	ActiveMode bool
	Timeout    time.Duration
	ServerName string
	RootCAs    *x509.CertPool
}

type Client struct {
	mu              sync.Mutex
	configMu        sync.RWMutex
	conn            *ftp.ServerConn
	dialer          *trackedDialer
	closed          atomic.Bool
	address         string
	forceListHidden bool
	preferUnixList  bool
	config          Config // transient connection settings for FXP re-authentication; cleared on Close.
}

// Dial connects to an FTP server. Explicit FTPS starts in plain FTP and
// upgrades with AUTH TLS; implicit FTPS negotiates TLS immediately.
func Dial(ctx context.Context, config Config) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	address := strings.TrimSpace(config.Address)
	if address == "" {
		return nil, errors.New("FTP 地址不能为空")
	}
	if config.Port == 0 {
		switch config.TLSMode {
		case TLSImplicit:
			config.Port = 990
		default:
			config.Port = 21
		}
	}
	if config.Port < 1 || config.Port > 65535 {
		return nil, errors.New("FTP 端口必须在 1 到 65535 之间")
	}
	if config.Timeout <= 0 {
		config.Timeout = 15 * time.Second
	}
	if config.TLSMode != "" && config.TLSMode != TLSOff && config.TLSMode != TLSExplicit && config.TLSMode != TLSImplicit {
		return nil, fmt.Errorf("不支持的 FTP 安全模式：%s", config.TLSMode)
	}
	endpoint := net.JoinHostPort(address, fmt.Sprint(config.Port))
	config.Address = address
	tracker := newTrackedDialer(config.Timeout, ctx)
	stopDialAbort := context.AfterFunc(ctx, tracker.CloseAll)
	conn, err := dialFTP(config, tracker, true)
	if err != nil {
		stopDialAbort()
		tracker.CloseAll()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("连接 FTP 服务器失败：%w", err)
	}
	if err := ctx.Err(); err != nil {
		stopDialAbort()
		_ = conn.Quit()
		tracker.CloseAll()
		return nil, err
	}
	stopDialAbort()
	tracker.SetContext(context.Background())
	return &Client{
		conn: conn, dialer: tracker, address: endpoint, config: config,
		forceListHidden: true,
	}, nil
}

func dialFTP(config Config, tracker *trackedDialer, forceListHidden bool) (*ftp.ServerConn, error) {
	endpoint := net.JoinHostPort(config.Address, fmt.Sprint(config.Port))
	serverName := strings.TrimSpace(config.ServerName)
	if serverName == "" {
		serverName = config.Address
	}
	var tlsConfig *tls.Config
	if config.TLSMode != TLSOff {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName, RootCAs: config.RootCAs}
	}
	dial := func(network, target string) (net.Conn, error) {
		conn, err := tracker.Dial(network, target)
		if err != nil {
			return nil, err
		}
		// jlaffaye/ftp uses a custom DialFunc verbatim for data connections;
		// wrap implicit control connections and all FTPS data connections here.
		// Explicit FTPS starts its control connection in cleartext and upgrades
		// it itself after AUTH TLS.
		protect := config.TLSMode == TLSImplicit || (config.TLSMode == TLSExplicit && target != endpoint)
		if protect {
			return tls.Client(conn, tlsConfig.Clone()), nil
		}
		return conn, nil
	}
	options := []ftp.DialOption{
		ftp.DialWithTimeout(config.Timeout),
		ftp.DialWithDialFunc(dial),
		ftp.DialWithListenFunc(tracker.Listen),
		ftp.DialWithForceListHidden(forceListHidden),
		ftp.DialWithTrustPasvIP(false),
		ftp.DialWithActiveMode(config.ActiveMode),
	}
	if config.TLSMode != TLSOff {
		if config.TLSMode == TLSExplicit {
			options = append(options, ftp.DialWithExplicitTLS(tlsConfig))
		} else if config.TLSMode == TLSImplicit {
			options = append(options, ftp.DialWithTLS(tlsConfig))
		} else {
			return nil, fmt.Errorf("不支持的 FTPS 模式：%s", config.TLSMode)
		}
	}
	conn, err := ftp.Dial(endpoint, options...)
	if err != nil {
		return nil, err
	}
	username := config.Username
	if username == "" {
		username = "anonymous"
	}
	if err := conn.Login(username, config.Password); err != nil {
		_ = conn.Quit()
		return nil, fmt.Errorf("FTP 认证失败：%w", err)
	}
	return conn, nil
}

func (c *Client) Close() error {
	if c == nil || !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	if c.dialer != nil {
		c.dialer.CloseAll()
	}
	c.configMu.Lock()
	c.config = Config{}
	c.configMu.Unlock()
	return nil
}

func (c *Client) do(ctx context.Context, operation func(*ftp.ServerConn) error) error {
	if c == nil {
		return errors.New("FTP 连接不可用")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stopAbort := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopAbort()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() || c.conn == nil {
		return errors.New("FTP 连接已关闭")
	}
	err := operation(c.conn)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (c *Client) WorkingDirectory() (string, error) {
	var current string
	err := c.do(context.Background(), func(conn *ftp.ServerConn) error {
		var err error
		current, err = conn.CurrentDir()
		return err
	})
	return current, err
}

// RealPath canonicalizes lexically because FTP has no standard realpath command.
func (c *Client) RealPath(remotePath string) (string, error) {
	if path.IsAbs(remotePath) {
		return path.Clean(remotePath), nil
	}
	current, err := c.WorkingDirectory()
	if err != nil {
		return "", err
	}
	return path.Clean(path.Join(current, remotePath)), nil
}

func (c *Client) List(remotePath string) ([]remotefs.Entry, error) {
	var entries []remotefs.Entry
	err := c.do(context.Background(), func(conn *ftp.ServerConn) error {
		var err error
		entries, err = listEntriesWithUnixMode(conn, remotePath, c.preferUnixList)
		if err == nil || !c.forceListHidden || !isFTPCommandRejection(err) {
			return err
		}

		// LIST -a is widely supported but not part of the FTP standard. Retry
		// with a standards-compatible LIST/MLSD connection when a server rejects
		// that extension, while keeping hidden-file listings where supported.
		c.configMu.RLock()
		config := c.config
		c.configMu.RUnlock()
		_ = conn.Quit()
		c.conn = nil
		fallbackConn, dialErr := dialFTP(config, c.dialer, false)
		if dialErr != nil {
			return fmt.Errorf("FTP 服务端拒绝隐藏文件列表，兼容重连失败：%w", dialErr)
		}
		c.conn = fallbackConn
		c.forceListHidden = false
		entries, err = listEntriesWithUnixMode(fallbackConn, remotePath, c.preferUnixList)
		return err
	})
	return entries, err
}

func isFTPCommandRejection(err error) bool {
	var response *textproto.Error
	return errors.As(err, &response) && response.Code >= 500 && response.Code <= 599
}

func listEntries(conn *ftp.ServerConn, remotePath string) ([]remotefs.Entry, error) {
	return listEntriesWithUnixMode(conn, remotePath, false)
}

func listEntriesWithUnixMode(conn *ftp.ServerConn, remotePath string, preferUnix bool) ([]remotefs.Entry, error) {
	var items []*ftp.Entry
	var err error
	if preferUnix {
		items, err = conn.ListUnix(remotePath)
		if isFTPCommandRejection(err) {
			// Preserve ordinary browsing on servers where LIST is unavailable
			// while preferring its Unix permissions after SITE CHMOD.
			items, err = conn.List(remotePath)
		}
	} else {
		items, err = conn.List(remotePath)
	}
	if err != nil {
		return nil, err
	}
	entries := make([]remotefs.Entry, 0, len(items))
	for _, item := range items {
		if item == nil || !safeRemoteName(item.Name) {
			continue
		}
		entry := remotefs.Entry{
			Name: item.Name, Size: int64(item.Size), Mode: item.Mode,
			ModeKnown: item.ModeKnown, ModTime: item.Time,
		}
		switch item.Type {
		case ftp.EntryTypeFolder:
			entry.IsDir = true
			entry.Mode |= os.ModeDir
		case ftp.EntryTypeLink:
			entry.IsSymlink = true
			entry.Mode |= os.ModeSymlink
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

func (c *Client) Search(ctx context.Context, root, query string, resultLimit int) ([]remotefs.SearchMatch, bool, error) {
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
	type pendingDir struct{ path string }
	pending := []pendingDir{{path: path.Clean(root)}}
	visited := map[string]struct{}{}
	results := make([]remotefs.SearchMatch, 0)
	truncated := false
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if len(visited) >= 100000 {
			truncated = true
			break
		}
		current := path.Clean(pending[0].path)
		pending = pending[1:]
		if _, ok := visited[current]; ok {
			continue
		}
		visited[current] = struct{}{}
		entries, err := c.listContext(ctx, current)
		if err != nil {
			return nil, false, fmt.Errorf("搜索 FTP 目录 %s 失败：%w", current, err)
		}
		for _, entry := range entries {
			if !safeRemoteName(entry.Name) {
				continue
			}
			entryPath := path.Join(current, entry.Name)
			if strings.Contains(strings.ToLower(entry.Name), query) {
				if len(results) >= resultLimit {
					truncated = true
					break
				}
				results = append(results, remotefs.SearchMatch{Path: entryPath, Entry: entry})
			}
			if entry.IsDir && !entry.IsSymlink {
				pending = append(pending, pendingDir{path: entryPath})
			}
		}
		if truncated {
			break
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Path < results[j].Path })
	return results, truncated, nil
}

func (c *Client) listContext(ctx context.Context, remotePath string) ([]remotefs.Entry, error) {
	var entries []remotefs.Entry
	err := c.do(ctx, func(conn *ftp.ServerConn) error {
		var err error
		entries, err = listEntriesWithUnixMode(conn, remotePath, c.preferUnixList)
		return err
	})
	return entries, err
}

func (c *Client) Exists(remotePath string) (bool, error) {
	var exists bool
	err := c.do(context.Background(), func(conn *ftp.ServerConn) error {
		if isRemoteRoot(remotePath) {
			exists = true
			return nil
		}
		parent, base := path.Split(path.Clean(remotePath))
		if parent == "" {
			parent = "."
		}
		entries, err := conn.List(parent)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry != nil && entry.Name == base {
				exists = true
				break
			}
		}
		return nil
	})
	return exists, err
}

func (c *Client) MakeDirectory(remotePath string) error {
	return c.do(context.Background(), func(conn *ftp.ServerConn) error { return conn.MakeDir(remotePath) })
}

func (c *Client) Rename(oldPath, newPath string) error {
	return c.do(context.Background(), func(conn *ftp.ServerConn) error { return conn.Rename(oldPath, newPath) })
}

func (c *Client) SetPermissions(remotePath string, mode os.FileMode) error {
	return c.do(context.Background(), func(conn *ftp.ServerConn) error {
		if err := conn.Chmod(remotePath, mode.Perm()); err != nil {
			var response *textproto.Error
			if errors.As(err, &response) && response.Code >= 500 {
				return fmt.Errorf("FTP 服务端拒绝 SITE CHMOD（可能不支持权限修改）：%w", err)
			}
			return fmt.Errorf("FTP SITE CHMOD 失败：%w", err)
		}
		c.preferUnixList = true
		return nil
	})
}

func (c *Client) Remove(remotePath string, directory bool) error {
	return c.do(context.Background(), func(conn *ftp.ServerConn) error {
		if directory {
			return conn.RemoveDir(remotePath)
		}
		return conn.Delete(remotePath)
	})
}

func (c *Client) RemoveTree(remotePath string) error {
	return c.RemoveTreeContext(context.Background(), remotePath)
}

func (c *Client) RemoveTreeContext(ctx context.Context, remotePath string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	root := path.Clean(remotePath)
	if root == "." || root == "/" || root == ".." {
		return errors.New("拒绝递归删除 FTP 根目录")
	}
	type node struct {
		path string
		dir  bool
	}
	return c.do(ctx, func(conn *ftp.ServerConn) error {
		rootEntry, exists, err := findFTPEntry(conn, root)
		if err != nil {
			return fmt.Errorf("读取 FTP 项目失败：%w", err)
		}
		if !exists {
			return os.ErrNotExist
		}
		rootIsDir := rootEntry.Type == ftp.EntryTypeFolder
		items := make([]node, 0)
		var walk func(string) error
		walk = func(directory string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, err := listEntries(conn, directory)
			if err != nil {
				return fmt.Errorf("读取 FTP 目录 %s 失败：%w", directory, err)
			}
			for _, entry := range entries {
				if !safeRemoteName(entry.Name) {
					continue
				}
				child := path.Join(directory, entry.Name)
				if entry.IsDir && !entry.IsSymlink {
					if err := walk(child); err != nil {
						return err
					}
					items = append(items, node{path: child, dir: true})
				} else {
					items = append(items, node{path: child})
				}
			}
			return nil
		}
		if rootIsDir {
			if err := walk(root); err != nil {
				return err
			}
		}
		items = append(items, node{path: root, dir: rootIsDir})
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			if item.dir {
				err = conn.RemoveDir(item.path)
			} else {
				err = conn.Delete(item.path)
			}
			if err != nil {
				return fmt.Errorf("删除 FTP 项目 %s 失败：%w", item.path, err)
			}
		}
		return nil
	})
}

func (c *Client) Upload(ctx context.Context, localPath, remotePath string, overwrite bool, progress func(int64, int64)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	local, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开本地文件失败：%w", err)
	}
	defer local.Close()
	info, err := local.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("当前仅支持上传普通文件")
	}
	return c.do(ctx, func(conn *ftp.ServerConn) error {
		if err := ensureUploadTarget(conn, remotePath, overwrite); err != nil {
			return err
		}
		temporary := ftpUploadPartialPath(remotePath)
		offset, complete, err := findFTPUploadOffset(conn, local, info.Size(), temporary)
		if err != nil {
			return fmt.Errorf("检查 FTP 上传断点失败：%w", err)
		}
		if progress != nil {
			progress(offset, info.Size())
		}
		if !complete {
			if _, err := local.Seek(offset, io.SeekStart); err != nil {
				return err
			}
			reader := &progressReader{ctx: ctx, reader: local, total: info.Size(), current: offset, progress: progress}
			if offset > 0 {
				err = conn.StorFrom(temporary, reader, uint64(offset))
			} else {
				err = conn.Stor(temporary, reader)
			}
			if err != nil {
				return fmt.Errorf("上传 FTP 文件失败，已保留可校验的断点：%w", err)
			}
			if reader.current != info.Size() {
				return fmt.Errorf("上传 FTP 文件失败，源文件读取 %d/%d 字节：%w", reader.current, info.Size(), io.ErrUnexpectedEOF)
			}
		}
		if conn.IsSetTimeSupported() && !info.ModTime().IsZero() {
			_ = conn.SetTime(temporary, info.ModTime())
		}
		if err := replaceFTPFile(conn, temporary, remotePath, overwrite); err != nil {
			return fmt.Errorf("提交 FTP 上传文件失败，已保留完整临时文件：%w", err)
		}
		return nil
	})
}

func (c *Client) Download(ctx context.Context, remotePath, localPath string, overwrite bool, progress func(int64, int64)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	partialPath := ftpDownloadPartialPath(localPath)
	checkpointPath := ftpDownloadCheckpointPath(partialPath)
	var checkpoint ftpDownloadCheckpoint
	var checkpointEnabled bool
	var partialOwned bool
	err := c.do(ctx, func(conn *ftp.ServerConn) error {
		entry, err := getFTPEntry(conn, remotePath)
		if err != nil {
			return fmt.Errorf("读取 FTP 文件信息失败：%w", err)
		}
		if entry.Type != ftp.EntryTypeFile {
			return errors.New("当前仅支持下载普通文件")
		}
		if err := ensureLocalTarget(localPath, overwrite); err != nil {
			return err
		}
		fileSize, sizeKnown, modified, timeKnown := ftpRemoteFileIdentity(conn, remotePath, entry)
		checkpointEnabled = sizeKnown && timeKnown
		checkpoint = ftpDownloadCheckpoint{
			Endpoint: c.address, RemotePath: remotePath, Size: fileSize, ModTime: modified,
		}
		offset := int64(0)
		if checkpointEnabled {
			valid, checkpointExists, err := validateFTPDownloadCheckpoint(partialPath, checkpoint)
			if err != nil {
				return fmt.Errorf("校验本地 FTP 下载断点失败：%w", err)
			}
			if !valid && !checkpointExists {
				if _, statErr := os.Lstat(partialPath); statErr == nil {
					return errors.New("本地 FTP 下载临时文件没有工作台断点信息；为保护原文件，未覆盖它")
				} else if !errors.Is(statErr, os.ErrNotExist) {
					return statErr
				}
			}
			if valid {
				info, err := os.Stat(partialPath)
				if err != nil {
					return err
				}
				offset = info.Size()
			}
			checkpoint.Offset = offset
			checkpoint.PrefixSHA256 = fmt.Sprintf("%x", sha256.Sum256(nil))
			if offset > 0 {
				checkpoint.PrefixSHA256, err = digestLocalFile(partialPath)
				if err != nil {
					return err
				}
			}
			if err := writeFTPDownloadCheckpoint(checkpointPath, checkpoint); err != nil {
				return fmt.Errorf("保存 FTP 下载断点信息失败：%w", err)
			}
		} else {
			if err := ensureFTPDownloadPartialOwned(partialPath, checkpointPath, c.address, remotePath); err != nil {
				return err
			}
			if _, statErr := os.Lstat(partialPath); statErr == nil {
				partialOwned = true
			}
			_ = os.Remove(checkpointPath)
		}

		if sizeKnown && fileSize == 0 {
			temporary, err := openFTPDownloadPartial(partialPath, 0)
			if err != nil {
				return fmt.Errorf("创建空 FTP 下载文件失败：%w", err)
			}
			if err := temporary.Close(); err != nil {
				return err
			}
			partialOwned = true
		}
		if offset < fileSize || !sizeKnown {
			var response io.ReadCloser
			if offset > 0 {
				response, err = conn.RetrFrom(remotePath, uint64(offset))
			} else {
				response, err = conn.Retr(remotePath)
			}
			if err != nil {
				return fmt.Errorf("打开 FTP 文件失败：%w", err)
			}
			temporary, openErr := openFTPDownloadPartial(partialPath, offset)
			if openErr != nil {
				_ = response.Close()
				return fmt.Errorf("打开本地 FTP 下载断点失败：%w", openErr)
			}
			partialOwned = true
			progressTotal := fileSize
			if !sizeKnown {
				progressTotal = 0
			}
			reader := &progressReader{ctx: ctx, reader: response, total: progressTotal, current: offset, progress: progress}
			written, copyErr := io.Copy(temporary, reader)
			responseCloseErr := response.Close()
			if copyErr == nil && responseCloseErr != nil {
				copyErr = responseCloseErr
			}
			if copyErr == nil && sizeKnown && offset+written != fileSize {
				copyErr = io.ErrUnexpectedEOF
			}
			if copyErr == nil {
				copyErr = temporary.Sync()
			}
			closeErr := temporary.Close()
			if copyErr != nil {
				return fmt.Errorf("下载 FTP 文件失败，已保留可校验的断点：%w", copyErr)
			}
			if closeErr != nil {
				return closeErr
			}
		}
		if err := verifyFTPRemoteFileIdentity(conn, remotePath, fileSize, sizeKnown, modified, timeKnown); err != nil {
			return err
		}
		if timeKnown {
			_ = os.Chtimes(partialPath, modified, modified)
		}
		if err := replaceLocalFile(partialPath, localPath, overwrite); err != nil {
			return fmt.Errorf("保存下载文件失败：%w", err)
		}
		_ = os.Remove(checkpointPath)
		checkpointEnabled = false
		return nil
	})
	if err != nil && checkpointEnabled {
		if info, statErr := os.Lstat(partialPath); statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			checkpoint.Offset = info.Size()
			if checkpoint.Offset > 0 && checkpoint.Offset <= checkpoint.Size {
				if digest, digestErr := digestLocalFile(partialPath); digestErr == nil {
					checkpoint.PrefixSHA256 = digest
					if saveErr := writeFTPDownloadCheckpoint(checkpointPath, checkpoint); saveErr != nil {
						return errors.Join(err, fmt.Errorf("保存 FTP 下载断点信息失败：%w", saveErr))
					}
				}
			}
		}
	} else if err != nil && partialOwned {
		_ = os.Remove(partialPath)
		_ = os.Remove(checkpointPath)
	}
	return err
}

func (c *Client) CopyRemoteFileTo(ctx context.Context, destination remotefs.Client, sourcePath, destinationPath string, overwrite bool, progress func(int64, int64)) error {
	if destination == nil {
		return errors.New("远程目标连接不可用")
	}
	if ftpDestination, ok := destination.(*Client); ok {
		if handled, err := c.copyRemoteFileFXP(ctx, ftpDestination, sourcePath, destinationPath, overwrite, progress); handled || err != nil {
			return err
		}
	}
	temporary, err := os.CreateTemp("", "tui-ftp-remote-copy-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	_ = temporary.Close()
	_ = os.Remove(temporaryPath)
	defer os.Remove(temporaryPath)
	if err := c.Download(ctx, sourcePath, temporaryPath, false, nil); err != nil {
		return fmt.Errorf("暂存 FTP 源文件失败：%w", err)
	}
	if err := destination.Upload(ctx, temporaryPath, destinationPath, overwrite, progress); err != nil {
		return fmt.Errorf("上传远程目标文件失败：%w", err)
	}
	return nil
}

func (c *Client) CopyRemoteDirectoryTo(ctx context.Context, destination remotefs.Client, sourceRoot, destinationRoot string, overwrite bool, progress func(int64, int64)) error {
	if ftpDestination, ok := destination.(*Client); ok {
		return c.copyRemoteDirectoryToFTP(ctx, ftpDestination, sourceRoot, destinationRoot, overwrite, progress)
	}
	temporaryRoot, err := os.MkdirTemp("", "tui-ftp-remote-tree-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporaryRoot)
	if err := c.DownloadDirectory(ctx, sourceRoot, temporaryRoot, true, nil); err != nil {
		return err
	}
	return destination.UploadDirectory(ctx, temporaryRoot, destinationRoot, overwrite, progress)
}

func (c *Client) ReadTextFile(remotePath string, maxSize int64) ([]byte, remotefs.FileVersion, error) {
	var content []byte
	var version remotefs.FileVersion
	err := c.do(context.Background(), func(conn *ftp.ServerConn) error {
		entry, err := getFTPEntry(conn, remotePath)
		if err != nil {
			return fmt.Errorf("读取 FTP 文件信息失败：%w", err)
		}
		if entry.Type != ftp.EntryTypeFile {
			return errors.New("仅支持预览普通文件")
		}
		if maxSize > 0 && int64(entry.Size) > maxSize {
			return fmt.Errorf("文件大小超过限制（最大 %d 字节）", maxSize)
		}
		response, err := conn.Retr(remotePath)
		if err != nil {
			return err
		}
		defer response.Close()
		reader := io.Reader(response)
		if maxSize > 0 {
			reader = io.LimitReader(response, maxSize+1)
		}
		content, err = io.ReadAll(reader)
		if err != nil {
			return err
		}
		if maxSize > 0 && int64(len(content)) > maxSize {
			return fmt.Errorf("文件大小超过限制（最大 %d 字节）", maxSize)
		}
		version = remotefs.FileVersion{Size: int64(len(content)), ModTime: entry.Time, Mode: 0o644, Digest: sha256.Sum256(content)}
		return nil
	})
	return content, version, err
}

func (c *Client) SaveTextFile(ctx context.Context, remotePath string, content []byte, expected remotefs.FileVersion, progress func(int64, int64)) (remotefs.FileVersion, error) {
	if int64(len(content)) > remotefs.MaxEditableFileSize {
		return remotefs.FileVersion{}, fmt.Errorf("文件大小超过限制（最大 %d 字节）", remotefs.MaxEditableFileSize)
	}
	_, current, err := c.ReadTextFile(remotePath, remotefs.MaxEditableFileSize)
	if err != nil {
		return remotefs.FileVersion{}, err
	}
	if current != expected {
		return remotefs.FileVersion{}, remotefs.ErrRemoteFileChanged
	}
	local, err := os.CreateTemp("", "tui-ftp-edit-*")
	if err != nil {
		return remotefs.FileVersion{}, err
	}
	localPath := local.Name()
	defer os.Remove(localPath)
	if err := local.Chmod(0o600); err != nil {
		_ = local.Close()
		return remotefs.FileVersion{}, err
	}
	if _, err := local.Write(content); err != nil {
		_ = local.Close()
		return remotefs.FileVersion{}, err
	}
	if err := local.Sync(); err != nil {
		_ = local.Close()
		return remotefs.FileVersion{}, err
	}
	if err := local.Close(); err != nil {
		return remotefs.FileVersion{}, err
	}
	if err := c.Upload(ctx, localPath, remotePath, true, progress); err != nil {
		return remotefs.FileVersion{}, err
	}
	_, version, err := c.ReadTextFile(remotePath, remotefs.MaxEditableFileSize)
	return version, err
}

func (c *Client) UploadDirectory(ctx context.Context, localRoot, remoteRoot string, overwrite bool, progress func(int64, int64)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	rootInfo, err := os.Lstat(localRoot)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return errors.New("本地源不是目录")
	}
	if err := c.ensureDirectory(ctx, remoteRoot); err != nil {
		return err
	}
	type file struct {
		local    string
		remote   string
		relative string
		size     int64
	}
	files := make([]file, 0)
	var total int64
	err = filepath.WalkDir(localRoot, func(local string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if local == localRoot {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(localRoot, local)
		if err != nil {
			return err
		}
		remote := path.Join(remoteRoot, filepath.ToSlash(relative))
		if info.Mode()&os.ModeSymlink != 0 {
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			if err := c.ensureDirectory(ctx, remote); err != nil {
				return err
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		files = append(files, file{local: local, remote: remote, relative: filepath.ToSlash(relative), size: info.Size()})
		total += info.Size()
		return nil
	})
	if err != nil {
		return fmt.Errorf("扫描本地目录失败：%w", err)
	}
	var completed int64
	for index, item := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		reportDirectoryProgress(ctx, progress, item.relative, index, len(files), completed, total)
		err := c.Upload(ctx, item.local, item.remote, overwrite, func(current, _ int64) {
			reportDirectoryProgress(ctx, progress, item.relative, index, len(files), completed+current, total)
		})
		if err != nil {
			return fmt.Errorf("上传 %s 失败：%w", item.local, err)
		}
		completed += item.size
		reportDirectoryProgress(ctx, progress, item.relative, index+1, len(files), completed, total)
	}
	if progress != nil {
		progress(total, total)
	}
	remotefs.ReportTransferProgress(ctx, remotefs.TransferProgress{FilesCompleted: len(files), FilesTotal: len(files), Transferred: total, Total: total})
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

func (c *Client) DownloadDirectory(ctx context.Context, remoteRoot, localRoot string, overwrite bool, progress func(int64, int64)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	remoteRoot = path.Clean(remoteRoot)
	absoluteRoot, err := filepath.Abs(localRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absoluteRoot, 0o700); err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return err
	}
	type item struct {
		remotePath string
		localPath  string
		relative   string
		directory  bool
		size       int64
	}
	items := make([]item, 0)
	var total int64
	var scan func(string, string) error
	scan = func(remoteDirectory, relativeDirectory string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := c.listContext(ctx, remoteDirectory)
		if err != nil {
			return fmt.Errorf("读取远程目录 %s 失败：%w", relativeDirectory, err)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !safeRemoteName(entry.Name) || entry.IsSymlink {
				continue
			}
			relative := path.Join(relativeDirectory, entry.Name)
			remotePath := path.Join(remoteDirectory, entry.Name)
			localPath := filepath.Join(resolvedRoot, filepath.FromSlash(relative))
			items = append(items, item{
				remotePath: remotePath, localPath: localPath, relative: relative,
				directory: entry.IsDir, size: entry.Size,
			})
			if entry.IsDir {
				if err := scan(remotePath, relative); err != nil {
					return err
				}
			} else if entry.Size > 0 {
				total += entry.Size
			}
		}
		return nil
	}
	if err := scan(remoteRoot, "."); err != nil {
		return err
	}
	filesTotal := 0
	for _, item := range items {
		if item.directory {
			if err := ensureSafeLocalDirectory(resolvedRoot, item.localPath); err != nil {
				return fmt.Errorf("准备本地目录 %s 失败：%w", item.relative, err)
			}
			continue
		}
		filesTotal++
	}
	var completed int64
	filesCompleted := 0
	for _, item := range items {
		if item.directory {
			continue
		}
		if err := ensureSafeLocalDirectory(resolvedRoot, filepath.Dir(item.localPath)); err != nil {
			return fmt.Errorf("准备本地目录 %s 失败：%w", item.relative, err)
		}
		reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, completed, total)
		err := c.Download(ctx, item.remotePath, item.localPath, overwrite, func(current, _ int64) {
			reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, completed+current, total)
		})
		if err != nil {
			return fmt.Errorf("下载 %s 失败：%w", item.relative, err)
		}
		completed += item.size
		filesCompleted++
		reportDirectoryProgress(ctx, progress, item.relative, filesCompleted, filesTotal, completed, total)
	}
	if progress != nil {
		progress(total, total)
	}
	remotefs.ReportTransferProgress(ctx, remotefs.TransferProgress{FilesCompleted: filesTotal, FilesTotal: filesTotal, Transferred: total, Total: total})
	return nil
}

func (c *Client) ensureDirectory(ctx context.Context, remotePath string) error {
	if isRemoteRoot(remotePath) {
		return nil
	}
	exists, err := c.existsContext(ctx, remotePath)
	if err != nil {
		return err
	}
	if exists {
		var isDirectory bool
		if err := c.do(ctx, func(conn *ftp.ServerConn) error {
			entry, found, err := findFTPEntry(conn, remotePath)
			if err == nil && found {
				isDirectory = entry.Type == ftp.EntryTypeFolder
			}
			return err
		}); err != nil {
			return fmt.Errorf("检查 FTP 目录失败：%w", err)
		}
		if !isDirectory {
			return errors.New("FTP 目标已存在但不是目录")
		}
		return nil
	}
	return c.MakeDirectory(remotePath)
}

func (c *Client) existsContext(ctx context.Context, remotePath string) (bool, error) {
	var exists bool
	err := c.do(ctx, func(conn *ftp.ServerConn) error {
		if isRemoteRoot(remotePath) {
			exists = true
			return nil
		}
		parent, base := path.Split(path.Clean(remotePath))
		if parent == "" {
			parent = "."
		}
		entries, err := conn.List(parent)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry != nil && entry.Name == base {
				exists = true
				break
			}
		}
		return nil
	})
	return exists, err
}

func ensureUploadTarget(conn *ftp.ServerConn, remotePath string, overwrite bool) error {
	entry, exists, err := findFTPEntry(conn, remotePath)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if entry.Type == ftp.EntryTypeFolder {
		return errors.New("FTP 目标是目录")
	}
	if !overwrite {
		return os.ErrExist
	}
	return nil
}

func replaceFTPFile(conn *ftp.ServerConn, temporary, destination string, overwrite bool) error {
	_, exists, err := findFTPEntry(conn, destination)
	if err != nil {
		return err
	}
	if !exists {
		return conn.Rename(temporary, destination)
	}
	if !overwrite {
		return os.ErrExist
	}
	backup := temporaryRemoteName(destination, "backup")
	if err := conn.Rename(destination, backup); err != nil {
		return fmt.Errorf("备份 FTP 旧文件失败：%w", err)
	}
	if err := conn.Rename(temporary, destination); err != nil {
		_ = conn.Rename(backup, destination)
		return err
	}
	if err := conn.Delete(backup); err != nil {
		return fmt.Errorf("文件已提交，但删除 FTP 备份失败：%w", err)
	}
	return nil
}

func findFTPEntry(conn *ftp.ServerConn, remotePath string) (*ftp.Entry, bool, error) {
	if isRemoteRoot(remotePath) {
		return &ftp.Entry{Name: path.Clean(remotePath), Type: ftp.EntryTypeFolder}, true, nil
	}
	parent, base := path.Split(path.Clean(remotePath))
	if parent == "" {
		parent = "."
	}
	entries, err := conn.List(parent)
	if err != nil {
		return nil, false, err
	}
	for _, entry := range entries {
		if entry != nil && entry.Name == base {
			copy := *entry
			return &copy, true, nil
		}
	}
	return nil, false, nil
}

func getFTPEntry(conn *ftp.ServerConn, remotePath string) (*ftp.Entry, error) {
	entry, exists, err := findFTPEntry(conn, remotePath)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, os.ErrNotExist
	}
	return entry, nil
}

func temporaryRemoteName(remotePath, suffix string) string {
	directory, base := path.Split(path.Clean(remotePath))
	if base == "" || base == "/" || base == "." {
		base = "file"
	}
	return path.Join(directory, "."+base+"."+suffix+"-"+fmt.Sprint(time.Now().UnixNano()))
}

func ensureLocalTarget(localPath string, overwrite bool) error {
	info, err := os.Lstat(localPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("本地目标不是普通文件")
	}
	if !overwrite {
		return os.ErrExist
	}
	return nil
}

func replaceLocalFile(temporaryPath, destinationPath string, overwrite bool) error {
	if !overwrite {
		if _, err := os.Lstat(destinationPath); err == nil {
			return os.ErrExist
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Rename(temporaryPath, destinationPath)
	}
	backup := destinationPath + ".backup-" + fmt.Sprint(time.Now().UnixNano())
	if err := os.Rename(destinationPath, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err == nil {
		if err := os.Rename(temporaryPath, destinationPath); err != nil {
			_ = os.Rename(backup, destinationPath)
			return err
		}
		return os.Remove(backup)
	}
	return os.Rename(temporaryPath, destinationPath)
}

type progressReader struct {
	ctx      context.Context
	reader   io.Reader
	total    int64
	current  int64
	progress func(int64, int64)
}

func (r *progressReader) Read(buffer []byte) (int, error) {
	if r.ctx != nil {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	n, err := r.reader.Read(buffer)
	r.current += int64(n)
	if r.progress != nil {
		r.progress(r.current, r.total)
	}
	return n, err
}

func safeRemoteName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\\\x00") &&
		!filepath.IsAbs(name) && filepath.VolumeName(name) == "" && !hasDrivePrefix(name) && filepath.Base(name) == name
}

func hasDrivePrefix(name string) bool {
	return len(name) >= 2 && ((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z')) && name[1] == ':'
}

func isRemoteRoot(remotePath string) bool {
	cleaned := path.Clean(strings.TrimSpace(remotePath))
	return cleaned == "." || cleaned == "/"
}

func ensureSafeLocalDirectory(root, target string) error {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbsolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbsolute, targetAbsolute)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("远程目录项越出本地目标目录")
	}
	rootInfo, err := os.Lstat(rootAbsolute)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return errors.New("本地目标根路径不是安全目录")
	}
	current := rootAbsolute
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return errors.New("本地目标目录路径无效")
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("本地目标路径包含符号链接或非目录")
		}
	}
	return nil
}

type trackedDialer struct {
	mu        sync.Mutex
	dialer    net.Dialer
	ctx       context.Context
	conns     map[*trackedConn]struct{}
	listeners map[*trackedListener]struct{}
	closed    bool
}

func newTrackedDialer(timeout time.Duration, ctx context.Context) *trackedDialer {
	return &trackedDialer{dialer: net.Dialer{Timeout: timeout}, ctx: ctx, conns: make(map[*trackedConn]struct{}), listeners: make(map[*trackedListener]struct{})}
}

func (d *trackedDialer) SetContext(ctx context.Context) {
	d.mu.Lock()
	d.ctx = ctx
	d.mu.Unlock()
}

func (d *trackedDialer) Dial(network, address string) (net.Conn, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, net.ErrClosed
	}
	ctx := d.ctx
	d.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := d.dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return d.trackConn(conn)
}

func (d *trackedDialer) trackConn(conn net.Conn) (net.Conn, error) {
	tracked := &trackedConn{Conn: conn, owner: d}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	d.conns[tracked] = struct{}{}
	d.mu.Unlock()
	return tracked, nil
}

func (d *trackedDialer) Listen(network, address string) (net.Listener, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, net.ErrClosed
	}
	ctx := d.ctx
	d.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, network, address)
	if err != nil {
		return nil, err
	}
	tracked := &trackedListener{Listener: listener, owner: d}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		_ = listener.Close()
		return nil, net.ErrClosed
	}
	d.listeners[tracked] = struct{}{}
	d.mu.Unlock()
	return tracked, nil
}

func (d *trackedDialer) CloseAll() {
	d.mu.Lock()
	d.closed = true
	connections := make([]*trackedConn, 0, len(d.conns))
	for connection := range d.conns {
		connections = append(connections, connection)
	}
	listeners := make([]*trackedListener, 0, len(d.listeners))
	for listener := range d.listeners {
		listeners = append(listeners, listener)
	}
	d.mu.Unlock()
	for _, listener := range listeners {
		_ = listener.Close()
	}
	for _, connection := range connections {
		_ = connection.Conn.Close()
	}
}

type trackedConn struct {
	net.Conn
	owner *trackedDialer
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.owner.mu.Lock()
	delete(c.owner.conns, c)
	c.owner.mu.Unlock()
	return err
}

type trackedListener struct {
	net.Listener
	owner *trackedDialer
}

func (l *trackedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.owner.trackConn(conn)
}

func (l *trackedListener) SetDeadline(deadline time.Time) error {
	listener, ok := l.Listener.(interface{ SetDeadline(time.Time) error })
	if !ok {
		return errors.New("tracked listener does not support deadlines")
	}
	return listener.SetDeadline(deadline)
}

func (l *trackedListener) Close() error {
	err := l.Listener.Close()
	l.owner.mu.Lock()
	delete(l.owner.listeners, l)
	l.owner.mu.Unlock()
	return err
}

var _ remotefs.Client = (*Client)(nil)
