package ftpclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	ftp "github.com/jlaffaye/ftp"
	"tui-ssh-workbench/internal/infra/remotefs"
)

var errFXPUnsupported = errors.New("FTP 服务器不支持安全的 FXP 协商")

type fxpControl struct {
	conn    net.Conn
	text    *textproto.Conn
	timeout time.Duration
}

type fxpFile struct {
	sourcePath      string
	destinationPath string
	relativePath    string
	size            int64
}

func (c *Client) fxpConfig() (Config, bool) {
	if c == nil {
		return Config{}, false
	}
	c.configMu.RLock()
	defer c.configMu.RUnlock()
	if c.closed.Load() || c.config.Address == "" {
		return Config{}, false
	}
	return c.config, true
}

// copyRemoteFileFXP uses the FTP server-to-server data path only when both
// endpoints use plain FTP and both servers accept EPSV/PASV plus PORT/EPRT.
// A false handled result means no transfer command started and the caller may
// use the regular local-staging path instead.
func (c *Client) copyRemoteFileFXP(ctx context.Context, destination *Client, sourcePath, destinationPath string, overwrite bool, progress func(int64, int64)) (handled bool, runErr error) {
	sourceConfig, sourceOK := c.fxpConfig()
	destinationConfig, destinationOK := destination.fxpConfig()
	if !sourceOK || !destinationOK {
		return true, errors.New("FTP 连接不可用")
	}
	if sourceConfig.TLSMode != "" && sourceConfig.TLSMode != TLSOff ||
		destinationConfig.TLSMode != "" && destinationConfig.TLSMode != TLSOff {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if err := validateFXPPath(sourcePath); err != nil {
		return true, err
	}
	if err := validateFXPPath(destinationPath); err != nil {
		return true, err
	}

	var sourceSize int64
	var sourceModTime time.Time
	if err := c.do(ctx, func(conn *ftp.ServerConn) error {
		entry, err := getFTPEntry(conn, sourcePath)
		if err != nil {
			return fmt.Errorf("读取 FXP 源文件失败：%w", err)
		}
		if entry.Type != ftp.EntryTypeFile {
			return errors.New("FXP 仅支持普通文件；目录由文件管理器递归处理")
		}
		sourceSize = int64(entry.Size)
		if size, sizeErr := conn.FileSize(sourcePath); sizeErr == nil {
			sourceSize = size
		}
		sourceModTime = entry.Time
		return nil
	}); err != nil {
		return true, err
	}
	if err := destination.do(ctx, func(conn *ftp.ServerConn) error {
		return ensureUploadTarget(conn, destinationPath, overwrite)
	}); err != nil {
		return true, err
	}
	temporaryPath, err := newFXPTemporaryName(destinationPath)
	if err != nil {
		return true, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		exists, err := destination.existsContext(ctx, temporaryPath)
		if err != nil {
			return true, fmt.Errorf("检查 FXP 临时目标失败：%w", err)
		}
		if !exists {
			break
		}
		temporaryPath, err = newFXPTemporaryName(destinationPath)
		if err != nil {
			return true, err
		}
		if attempt == 2 {
			return true, errors.New("无法生成唯一的 FXP 临时文件名")
		}
	}

	createdTemporary := false
	committed := false
	defer func() {
		if createdTemporary && !committed {
			_ = destination.Remove(temporaryPath, false)
		}
	}()

	sourceControl, err := dialFXPControl(ctx, sourceConfig)
	if err != nil {
		return true, fmt.Errorf("连接 FXP 源 FTP 控制通道失败：%w", err)
	}
	defer sourceControl.Close()
	destinationControl, err := dialFXPControl(ctx, destinationConfig)
	if err != nil {
		return true, fmt.Errorf("连接 FXP 目标 FTP 控制通道失败：%w", err)
	}
	defer destinationControl.Close()
	stopCancel := context.AfterFunc(ctx, func() {
		_ = sourceControl.Close()
		_ = destinationControl.Close()
	})
	defer stopCancel()

	started, err := transferFTPFXP(ctx, sourceControl, destinationControl, sourcePath, temporaryPath)
	createdTemporary = started
	if errors.Is(err, errFXPUnsupported) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if !started {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if err := destination.do(ctx, func(conn *ftp.ServerConn) error {
		if conn.IsSetTimeSupported() && !sourceModTime.IsZero() {
			_ = conn.SetTime(temporaryPath, sourceModTime)
		}
		return replaceFTPFile(conn, temporaryPath, destinationPath, overwrite)
	}); err != nil {
		return true, fmt.Errorf("提交 FXP 文件失败：%w", err)
	}
	committed = true
	if progress != nil {
		progress(sourceSize, sourceSize)
	}
	return true, nil
}

func transferFTPFXP(ctx context.Context, source, destination *fxpControl, sourcePath, destinationPath string) (bool, error) {
	if err := source.commandOK("TYPE", "I"); err != nil {
		return true, fmt.Errorf("设置 FXP 源文件类型失败：%w", err)
	}
	if err := destination.commandOK("TYPE", "I"); err != nil {
		return true, fmt.Errorf("设置 FXP 目标文件类型失败：%w", err)
	}
	passiveIP, passivePort, err := destination.passiveEndpoint()
	if errors.Is(err, errFXPUnsupported) {
		return false, errFXPUnsupported
	}
	if err != nil {
		return false, fmt.Errorf("请求 FXP 目标被动端口失败：%w", err)
	}
	if err := source.commandPORT(passiveIP, passivePort); errors.Is(err, errFXPUnsupported) {
		return false, errFXPUnsupported
	} else if err != nil {
		return false, fmt.Errorf("设置 FXP 源数据端点失败：%w", err)
	}

	code, message, err := destination.command("STOR", destinationPath)
	if err != nil {
		return false, fmt.Errorf("启动 FXP 目标写入失败：%w", err)
	}
	if !ftpPreliminaryTransferReply(code) {
		return false, fxpReplyError("STOR", code, message)
	}
	code, message, err = source.command("RETR", sourcePath)
	if err != nil {
		return true, fmt.Errorf("启动 FXP 源读取失败：%w", err)
	}
	if !ftpPreliminaryTransferReply(code) {
		return true, fxpReplyError("RETR", code, message)
	}
	if err := awaitFXPCompletion(ctx, source, destination); err != nil {
		return true, err
	}
	return true, nil
}

func (c *Client) copyRemoteDirectoryToFTP(ctx context.Context, destination *Client, sourceRoot, destinationRoot string, overwrite bool, progress func(int64, int64)) error {
	if destination == nil {
		return errors.New("远程目标连接不可用")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sourceRoot = path.Clean(sourceRoot)
	destinationRoot = path.Clean(destinationRoot)
	if err := c.do(ctx, func(conn *ftp.ServerConn) error {
		entry, err := getFTPEntry(conn, sourceRoot)
		if err != nil {
			return fmt.Errorf("读取 FTP 源目录失败：%w", err)
		}
		if entry.Type != ftp.EntryTypeFolder {
			return errors.New("FTP 源不是目录")
		}
		return nil
	}); err != nil {
		return err
	}
	type directory struct {
		source      string
		destination string
		relative    string
	}
	directories := []directory{{source: sourceRoot, destination: destinationRoot}}
	files := make([]fxpFile, 0)
	var total int64
	var scan func(directory) error
	scan = func(current directory) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := c.listContext(ctx, current.source)
		if err != nil {
			return fmt.Errorf("读取远程目录 %s 失败：%w", current.relative, err)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !safeRemoteName(entry.Name) || entry.IsSymlink {
				continue
			}
			sourcePath := path.Join(current.source, entry.Name)
			destinationPath := path.Join(current.destination, entry.Name)
			relative := path.Join(current.relative, entry.Name)
			if entry.IsDir {
				next := directory{source: sourcePath, destination: destinationPath, relative: relative}
				directories = append(directories, next)
				if err := scan(next); err != nil {
					return err
				}
				continue
			}
			files = append(files, fxpFile{
				sourcePath: sourcePath, destinationPath: destinationPath,
				relativePath: relative, size: max(0, entry.Size),
			})
			total += max(0, entry.Size)
		}
		return nil
	}
	if err := scan(directories[0]); err != nil {
		return err
	}
	for _, directory := range directories {
		if err := destination.ensureDirectory(ctx, directory.destination); err != nil {
			return fmt.Errorf("创建远程目录 %s 失败：%w", directory.relative, err)
		}
	}
	var completed int64
	for index, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		reportDirectoryProgress(ctx, progress, file.relativePath, index, len(files), completed, total)
		err := c.CopyRemoteFileTo(ctx, destination, file.sourcePath, file.destinationPath, overwrite, func(current, _ int64) {
			reportDirectoryProgress(ctx, progress, file.relativePath, index, len(files), completed+current, total)
		})
		if err != nil {
			return fmt.Errorf("复制远程文件 %s 失败：%w", file.relativePath, err)
		}
		completed += file.size
		reportDirectoryProgress(ctx, progress, file.relativePath, index+1, len(files), completed, total)
	}
	if progress != nil {
		progress(total, total)
	}
	remotefs.ReportTransferProgress(ctx, remotefs.TransferProgress{FilesCompleted: len(files), FilesTotal: len(files), Transferred: total, Total: total})
	return nil
}

func dialFXPControl(ctx context.Context, config Config) (*fxpControl, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	port := config.Port
	if port == 0 {
		port = 21
	}
	address := strings.TrimSpace(config.Address)
	if address == "" || port < 1 || port > 65535 {
		return nil, errors.New("无效的 FXP FTP 地址")
	}
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", net.JoinHostPort(address, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	control := &fxpControl{conn: conn, text: textproto.NewConn(conn), timeout: timeout}
	if err := control.setCommandDeadline(ctx); err != nil {
		_ = control.Close()
		return nil, err
	}
	code, _, err := control.text.ReadResponse(220)
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	if code != 220 {
		_ = control.Close()
		return nil, fmt.Errorf("FTP 欢迎响应状态码为 %d", code)
	}
	username := config.Username
	if username == "" {
		username = "anonymous"
	}
	code, message, err := control.commandContext(ctx, "USER", username)
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	if code == 230 {
		return control, nil
	}
	if code != 331 {
		_ = control.Close()
		return nil, fxpReplyError("USER", code, message)
	}
	code, message, err = control.commandContext(ctx, "PASS", config.Password)
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	if code != 230 {
		_ = control.Close()
		return nil, fmt.Errorf("FTP 认证失败（状态码 %d）", code)
	}
	return control, nil
}

func (c *fxpControl) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *fxpControl) commandOK(command, argument string) error {
	code, message, err := c.command(command, argument)
	if err != nil {
		return err
	}
	if code != 200 {
		return fxpReplyError(command, code, message)
	}
	return nil
}

func (c *fxpControl) command(command, argument string) (int, string, error) {
	return c.commandContext(context.Background(), command, argument)
}

func (c *fxpControl) commandContext(ctx context.Context, command, argument string) (int, string, error) {
	if strings.ContainsAny(command+argument, "\r\n") {
		return 0, "", errors.New("FTP 命令包含非法换行符")
	}
	if err := c.setCommandDeadline(ctx); err != nil {
		return 0, "", err
	}
	line := command
	if argument != "" {
		line += " " + argument
	}
	if err := c.text.PrintfLine("%s", line); err != nil {
		return 0, "", err
	}
	return c.text.ReadResponse(-1)
}

func (c *fxpControl) setCommandDeadline(ctx context.Context) error {
	deadline := time.Now().Add(c.timeout)
	if ctx != nil {
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
	}
	return c.conn.SetDeadline(deadline)
}

func (c *fxpControl) passiveEndpoint() (net.IP, int, error) {
	controlIP, err := fxpControlIP(c.conn.RemoteAddr())
	if err != nil {
		return nil, 0, err
	}
	code, message, err := c.command("EPSV", "")
	if err != nil {
		return nil, 0, err
	}
	if code == 229 {
		port, parseErr := parseFXPEPSVPort(message)
		if parseErr == nil {
			return controlIP, port, nil
		}
	}
	code, message, err = c.command("PASV", "")
	if err != nil {
		return nil, 0, err
	}
	if code != 227 {
		return nil, 0, errFXPUnsupported
	}
	ip, port, err := parseFXPPASV(message)
	if err != nil {
		return nil, 0, errFXPUnsupported
	}
	if !ip.Equal(controlIP) {
		return nil, 0, errFXPUnsupported
	}
	return controlIP, port, nil
}

func (c *fxpControl) commandPORT(ip net.IP, port int) error {
	if ip == nil || port < 1 || port > 65535 {
		return errors.New("FXP 被动端点无效")
	}
	var command, argument string
	if ipv4 := ip.To4(); ipv4 != nil {
		command = "PORT"
		argument = fmt.Sprintf("%d,%d,%d,%d,%d,%d", ipv4[0], ipv4[1], ipv4[2], ipv4[3], port/256, port%256)
	} else {
		command = "EPRT"
		argument = fmt.Sprintf("|2|%s|%d|", ip.String(), port)
	}
	code, _, err := c.command(command, argument)
	if err != nil {
		return err
	}
	if code != 200 {
		return errFXPUnsupported
	}
	return nil
}

func fxpControlIP(address net.Addr) (net.IP, error) {
	tcpAddress, ok := address.(*net.TCPAddr)
	if !ok || tcpAddress.IP == nil {
		return nil, errors.New("FTP 控制连接没有 TCP 地址")
	}
	return tcpAddress.IP, nil
}

func parseFXPEPSVPort(message string) (int, error) {
	start, end := strings.Index(message, "("), strings.LastIndex(message, ")")
	if start < 0 || end <= start+1 {
		return 0, errors.New("invalid EPSV response")
	}
	payload := message[start+1 : end]
	delimiter := payload[0]
	fields := strings.Split(payload, string(delimiter))
	if len(fields) < 5 {
		return 0, errors.New("invalid EPSV port")
	}
	port, err := strconv.Atoi(fields[len(fields)-2])
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("invalid EPSV port")
	}
	return port, nil
}

func parseFXPPASV(message string) (net.IP, int, error) {
	start, end := strings.Index(message, "("), strings.LastIndex(message, ")")
	if start < 0 || end <= start+1 {
		return nil, 0, errors.New("invalid PASV response")
	}
	fields := strings.Split(message[start+1:end], ",")
	if len(fields) != 6 {
		return nil, 0, errors.New("invalid PASV address")
	}
	parts := [6]int{}
	for index, field := range fields {
		value, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || value < 0 || value > 255 {
			return nil, 0, errors.New("invalid PASV address")
		}
		parts[index] = value
	}
	port := parts[4]*256 + parts[5]
	if port < 1 || port > 65535 {
		return nil, 0, errors.New("invalid PASV port")
	}
	return net.IPv4(byte(parts[0]), byte(parts[1]), byte(parts[2]), byte(parts[3])), port, nil
}

func awaitFXPCompletion(ctx context.Context, source, destination *fxpControl) error {
	results := make(chan error, 2)
	for _, control := range []*fxpControl{source, destination} {
		go func(control *fxpControl) {
			if err := control.setCommandDeadline(ctx); err != nil {
				results <- err
				return
			}
			code, message, err := control.text.ReadResponse(-1)
			if err == nil && code != 226 && code != 250 {
				err = fxpReplyError("文件传输", code, message)
			}
			results <- err
		}(control)
	}
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				_ = source.Close()
				_ = destination.Close()
				return fmt.Errorf("FXP 服务器间传输失败：%w", err)
			}
		case <-ctx.Done():
			_ = source.Close()
			_ = destination.Close()
			return ctx.Err()
		}
	}
	return nil
}

func ftpPreliminaryTransferReply(code int) bool {
	return code == 125 || code == 150
}

func fxpReplyError(command string, code int, message string) error {
	message = strings.TrimSpace(strings.Map(func(value rune) rune {
		if unicode.IsControl(value) {
			return ' '
		}
		return value
	}, message))
	if message == "" {
		return fmt.Errorf("FTP %s 命令被拒绝（状态码 %d）", command, code)
	}
	return fmt.Errorf("FTP %s 命令被拒绝（状态码 %d）：%s", command, code, message)
}

func validateFXPPath(remotePath string) error {
	if strings.TrimSpace(remotePath) == "" {
		return errors.New("FXP 远程路径不能为空或包含控制字符")
	}
	for _, value := range remotePath {
		if unicode.IsControl(value) {
			return errors.New("FXP 远程路径不能为空或包含控制字符")
		}
	}
	return nil
}

func newFXPTemporaryName(destinationPath string) (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("生成 FXP 临时文件名失败：%w", err)
	}
	directory, base := path.Split(path.Clean(destinationPath))
	return path.Join(directory, "."+base+".fxp-"+hex.EncodeToString(token[:])+".part"), nil
}
