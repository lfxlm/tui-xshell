package ui

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/remotefs"
	"tui-ssh-workbench/internal/infra/sftpclient"
)

type sftpImageEditorState struct {
	requestID      uint64
	generation     uint64
	side           sftpSide
	hostName       string
	name           string
	path           string
	tempDir        string
	tempPath       string
	remote         bool
	editable       bool
	original       sftpclient.Entry
	originalDigest [32]byte
	command        []string
	cancel         context.CancelFunc
}

type sftpImagePreparedMsg struct {
	requestID  uint64
	generation uint64
	digest     [32]byte
	err        error
}

type sftpImageEditorFinishedMsg struct {
	requestID  uint64
	generation uint64
	err        error
}

type sftpImageUploadedMsg struct {
	requestID  uint64
	generation uint64
	path       string
	entries    []sftpclient.Entry
	changed    bool
	uploaded   bool
	uploadErr  error
	listErr    error
}

func isSFTPImageName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".avif", ".heic", ".heif", ".svg", ".ico":
		return true
	default:
		return false
	}
}

func (m *modelState) openSFTPImageEditor(editable bool) tea.Cmd {
	state := m.sftp
	if state == nil || state.busy {
		return nil
	}
	side := sftpRemoteSide
	if state.standalone {
		side = state.activeSide
	}
	entry := m.selectedSFTPEntry(side)
	if entry == nil || entry.IsDir || entry.IsSymlink || !entry.Mode.IsRegular() {
		state.status, state.statusErr = "请选择普通图片文件", true
		return nil
	}
	if !isSFTPImageName(entry.Name) {
		state.status, state.statusErr = "暂不支持此图片格式", true
		return nil
	}
	remote := sftpSideIsRemote(state, side)
	client := sftpClientForSide(state, side)
	if remote && client == nil {
		state.status, state.statusErr = "远程文件连接不可用", true
		return nil
	}
	commandParts, err := sftpImageEditorCommand(m.sftpExternalEditorCommand, editable)
	if err != nil {
		state.status, state.statusErr = "请按 E 设置本地编辑器命令（例如 code --wait）："+err.Error(), true
		return nil
	}
	commandParts, err = resolveExternalEditorCommand(commandParts, editable)
	if err != nil {
		state.status, state.statusErr = "无法定位本地图片编辑器："+err.Error(), true
		return nil
	}

	filePath := m.sftpEntryPath(side, entry.Name)
	hostName := "本地"
	if side == sftpRemoteSide {
		hostName = state.host.Name
	} else if remote {
		hostName = state.peerHost.Name
	}
	state.requestID++
	requestID, generation := state.requestID, state.generation
	image := &sftpImageEditorState{
		requestID: requestID, generation: generation, side: side,
		hostName: hostName, name: entry.Name, path: filePath, remote: remote, editable: editable,
		original: *entry, command: commandParts,
	}
	state.imageEditor = image
	state.busy = true
	state.status = "正在准备图片文件…"
	if remote {
		state.status = "正在流式下载图片到本机临时目录…"
	}
	state.statusErr = false

	if !remote && editable {
		image.tempPath = filePath
		return m.launchSFTPImageEditor(image)
	}
	image.tempDir, err = os.MkdirTemp("", "tui-ssh-image-*")
	if err != nil {
		state.imageEditor, state.busy = nil, false
		state.status, state.statusErr = "创建图片临时目录失败："+err.Error(), true
		return nil
	}
	image.tempPath = filepath.Join(image.tempDir, filepath.Base(entry.Name))
	ctx, cancel := context.WithCancel(context.Background())
	image.cancel = cancel
	return func() tea.Msg {
		var copyErr error
		if remote {
			copyErr = client.Download(ctx, filePath, image.tempPath, false, nil)
		} else {
			copyErr = copySFTPImageFile(ctx, filePath, image.tempPath)
		}
		if copyErr == nil && remote {
			mode := os.FileMode(0o600)
			if entry.ModeKnown {
				mode = entry.Mode.Perm() | 0o600
			}
			copyErr = os.Chmod(image.tempPath, mode)
		}
		var digest [32]byte
		if copyErr == nil {
			digest, copyErr = hashSFTPImageFile(image.tempPath)
		}
		if copyErr != nil {
			_ = os.RemoveAll(image.tempDir)
		}
		return sftpImagePreparedMsg{requestID: requestID, generation: generation, digest: digest, err: copyErr}
	}
}

func sftpImageEditorCommand(configured string, editable bool) ([]string, error) {
	imageEditor := strings.TrimSpace(os.Getenv("SFTP_IMAGE_EDITOR"))
	if !editable {
		if imageEditor != "" {
			return parseEditorCommand(imageEditor)
		}
		if runtime.GOOS == "darwin" {
			// Use the native image viewer for preview. Editor-only flags such as
			// --wait should not be added to a preview command.
			// Do not use open -W: it waits for Preview.app itself to quit, which
			// can leave the terminal suspended after the preview window closes.
			return []string{"open", "-a", "Preview"}, nil
		}
	}
	rawCommand := strings.TrimSpace(configured)
	if rawCommand == "" {
		rawCommand = imageEditor
	}
	if rawCommand == "" {
		rawCommand = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	if rawCommand == "" && editable {
		rawCommand = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if rawCommand == "" {
		if !editable {
			return nil, errors.New("图片预览程序为空；请设置 SFTP_IMAGE_EDITOR")
		}
		rawCommand = "code --wait"
	}
	return parseEditorCommand(rawCommand)
}

func copySFTPImageFile(ctx context.Context, source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("打开本地图片失败：%w", err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return fmt.Errorf("读取本地图片信息失败：%w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("仅支持普通图片文件")
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("创建图片副本失败：%w", err)
	}
	_, copyErr := io.Copy(output, sftpImageContextReader{ctx: ctx, reader: input})
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("复制本地图片失败：%w", copyErr)
	}
	if syncErr != nil {
		return fmt.Errorf("写入图片副本失败：%w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("关闭图片副本失败：%w", closeErr)
	}
	return nil
}

type sftpImageContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader sftpImageContextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func hashSFTPImageFile(filePath string) ([32]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return [32]byte{}, fmt.Errorf("打开图片失败：%w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [32]byte{}, fmt.Errorf("计算图片校验值失败：%w", err)
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func (m *modelState) handleSFTPImagePrepared(msg sftpImagePreparedMsg) tea.Cmd {
	state := m.sftp
	if state == nil || state.generation != msg.generation || state.requestID != msg.requestID || state.imageEditor == nil {
		return nil
	}
	image := state.imageEditor
	if image.cancel != nil {
		image.cancel()
		image.cancel = nil
	}
	if msg.err != nil {
		state.busy = false
		state.imageEditor = nil
		state.status, state.statusErr = "准备图片失败："+msg.err.Error(), true
		return nil
	}
	image.originalDigest = msg.digest
	return m.launchSFTPImageEditor(image)
}

func (m *modelState) launchSFTPImageEditor(image *sftpImageEditorState) tea.Cmd {
	state := m.sftp
	if state == nil || image == nil || state.imageEditor != image {
		return nil
	}
	if len(image.command) == 0 {
		state.busy = false
		state.imageEditor = nil
		if image.tempDir != "" {
			_ = os.RemoveAll(image.tempDir)
		}
		state.status, state.statusErr = "无法启动本地图片编辑器：启动命令为空", true
		return nil
	}
	command := exec.Command(image.command[0], append(image.command[1:], image.tempPath)...)
	state.busy = true
	state.status = "已在本地编辑器中打开图片：" + image.hostName + " · " + image.name + "；关闭编辑器返回"
	if image.editable && image.remote {
		state.status = "正在本地编辑图片；关闭编辑器后会检查并上传修改"
	} else if image.editable {
		state.status = "正在本地编辑图片；关闭编辑器后返回"
	} else {
		state.status = "正在打开图片预览；TUI 会继续运行，不会回写编辑"
	}
	if isGUIEditorCommand(command.Path) && image.editable && image.remote {
		state.status = "图片已在外部编辑器中打开；TUI 保持显示，关闭文件后检查并上传修改"
	}
	state.statusErr = false
	requestID, generation := image.requestID, image.generation
	return runExternalEditor(command, func(err error) tea.Msg {
		return sftpImageEditorFinishedMsg{requestID: requestID, generation: generation, err: err}
	})
}

func (m *modelState) handleSFTPImageEditorFinished(msg sftpImageEditorFinishedMsg) tea.Cmd {
	state := m.sftp
	if state == nil || state.generation != msg.generation || state.requestID != msg.requestID || state.imageEditor == nil {
		return nil
	}
	image := state.imageEditor
	state.busy = false
	if msg.err != nil {
		state.imageEditor = nil
		state.status, state.statusErr = "本地图片编辑器退出失败："+msg.err.Error(), true
		if image.tempPath != image.path {
			state.status += "；临时副本保留在 " + image.tempPath
		}
		return nil
	}
	if !image.editable {
		state.imageEditor = nil
		if image.tempDir != "" {
			m.sftpPreviewTempDirs = append(m.sftpPreviewTempDirs, image.tempDir)
		}
		state.status, state.statusErr = "图片已在系统预览器中打开；TUI 可继续操作", false
		return nil
	}
	if !image.remote {
		state.imageEditor = nil
		state.status, state.statusErr = "本地图片编辑已结束", false
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	image.cancel = cancel
	state.busy = true
	state.status, state.statusErr = "正在检查图片改动并保存到远程主机…", false
	client := sftpClientForSide(state, image.side)
	return func() tea.Msg {
		message := sftpImageUploadedMsg{
			requestID: image.requestID, generation: image.generation,
			path: path.Dir(image.path),
		}
		updatedDigest, err := hashSFTPImageFile(image.tempPath)
		if err != nil {
			message.uploadErr = err
			return message
		}
		if updatedDigest == image.originalDigest {
			return message
		}
		message.changed = true
		if err := validateSFTPImageRemoteVersion(client, image.path, image.original); err != nil {
			message.uploadErr = err
			return message
		}
		if image.original.ModeKnown {
			if err := os.Chmod(image.tempPath, image.original.Mode.Perm()); err != nil {
				message.uploadErr = fmt.Errorf("恢复图片原权限失败：%w", err)
				return message
			}
		}
		if err := client.Upload(ctx, image.tempPath, image.path, true, nil); err != nil {
			message.uploadErr = fmt.Errorf("上传图片失败：%w", err)
			return message
		}
		message.uploaded = true
		message.entries, message.listErr = client.List(message.path)
		return message
	}
}

func cleanupSFTPPreviewTempDirs(dirs []string) {
	for _, dir := range dirs {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}
}

func validateSFTPImageRemoteVersion(client remotefs.Client, filePath string, original sftpclient.Entry) error {
	if client == nil {
		return errors.New("远程文件连接不可用，未上传修改")
	}
	entries, err := client.List(path.Dir(filePath))
	if err != nil {
		return fmt.Errorf("检查远程图片版本失败：%w", err)
	}
	base := path.Base(filePath)
	for _, current := range entries {
		if current.Name != base {
			continue
		}
		if current.IsDir || current.IsSymlink || current.Size != original.Size ||
			(!current.ModTime.IsZero() && !original.ModTime.IsZero() && !current.ModTime.Equal(original.ModTime)) ||
			(current.ModeKnown && original.ModeKnown && current.Mode.Perm() != original.Mode.Perm()) {
			return errors.New("远程图片在编辑期间发生变化，未覆盖；本地编辑副本已保留")
		}
		return nil
	}
	return errors.New("远程图片已不存在，未上传；本地编辑副本已保留")
}

func (m *modelState) handleSFTPImageUploaded(msg sftpImageUploadedMsg) {
	state := m.sftp
	if state == nil || state.generation != msg.generation || state.requestID != msg.requestID || state.imageEditor == nil {
		return
	}
	image := state.imageEditor
	if image.cancel != nil {
		image.cancel()
		image.cancel = nil
	}
	state.busy = false
	state.imageEditor = nil
	if msg.uploadErr != nil {
		state.status = "图片保存失败：" + msg.uploadErr.Error()
		if image.tempPath != image.path {
			state.status += "；编辑副本保留在 " + image.tempPath
		}
		state.statusErr = true
		return
	}
	if image.tempDir != "" {
		_ = os.RemoveAll(image.tempDir)
	}
	if !msg.changed {
		state.status, state.statusErr = "图片未修改，未上传", false
		return
	}
	if msg.uploaded {
		if msg.entries != nil {
			m.applySFTPListing(image.side, msg.path, msg.entries)
		}
		state.status, state.statusErr = "图片修改已上传到远程主机", false
		if msg.listErr != nil {
			state.status += "；刷新目录失败：" + msg.listErr.Error()
			state.statusErr = true
		}
	}
}
