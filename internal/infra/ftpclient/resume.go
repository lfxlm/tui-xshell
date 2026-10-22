package ftpclient

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	ftp "github.com/jlaffaye/ftp"
)

type ftpDownloadCheckpoint struct {
	Endpoint     string    `json:"endpoint"`
	RemotePath   string    `json:"remote_path"`
	Size         int64     `json:"size"`
	ModTime      time.Time `json:"mod_time"`
	Offset       int64     `json:"offset"`
	PrefixSHA256 string    `json:"prefix_sha256"`
}

func ftpUploadPartialPath(remotePath string) string {
	directory, base := path.Split(path.Clean(remotePath))
	return path.Join(directory, "."+base+".ftp-upload.part")
}

func ftpDownloadPartialPath(localPath string) string {
	directory := filepath.Dir(localPath)
	if directory == "." {
		directory = ""
	}
	return filepath.Join(directory, "."+filepath.Base(localPath)+".ftp-download.part")
}

func ftpDownloadCheckpointPath(partialPath string) string {
	return partialPath + ".json"
}

func ensureFTPDownloadPartialOwned(partialPath, checkpointPath, endpoint, remotePath string) error {
	info, err := os.Lstat(partialPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("FTP 下载临时路径已被非普通文件占用")
	}
	checkpoint, err := readFTPDownloadCheckpoint(checkpointPath)
	if err != nil {
		return errors.New("本地 FTP 下载临时文件已有内容，但没有有效的工作台断点信息；为保护原文件，未覆盖它")
	}
	if checkpoint.Endpoint != endpoint || checkpoint.RemotePath != remotePath {
		return errors.New("本地 FTP 下载临时文件属于其他连接或路径；为保护原文件，未覆盖它")
	}
	return nil
}

// findFTPUploadOffset resumes only when the remote temporary file is an exact
// prefix of the current local source. If an interrupted upload left unrelated
// bytes behind, the next Stor call starts from byte zero and truncates it.
func findFTPUploadOffset(conn *ftp.ServerConn, source *os.File, sourceSize int64, partialPath string) (offset int64, complete bool, err error) {
	entry, exists, err := findFTPEntry(conn, partialPath)
	if err != nil || !exists {
		return 0, false, err
	}
	if entry.Type != ftp.EntryTypeFile {
		return 0, false, errors.New("FTP 上传临时路径已被非文件项目占用")
	}
	partialSize := int64(entry.Size)
	if serverSize, sizeErr := conn.FileSize(partialPath); sizeErr == nil {
		partialSize = serverSize
	}
	if partialSize <= 0 {
		return 0, false, nil
	}
	if partialSize > sourceSize {
		return 0, false, nil
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return 0, false, err
	}
	response, err := conn.Retr(partialPath)
	if err != nil {
		return 0, false, fmt.Errorf("读取 FTP 上传断点失败：%w", err)
	}
	localHash, remoteHash := sha256.New(), sha256.New()
	_, localErr := io.CopyN(localHash, source, partialSize)
	_, remoteErr := io.CopyN(remoteHash, response, partialSize)
	closeErr := response.Close()
	if closeErr != nil {
		return 0, false, fmt.Errorf("关闭 FTP 上传断点读取失败：%w", closeErr)
	}
	if localErr != nil {
		return 0, false, fmt.Errorf("读取本地上传源文件断点失败：%w", localErr)
	}
	if remoteErr != nil {
		if isFTPShortRead(remoteErr) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("校验 FTP 上传断点失败：%w", remoteErr)
	}
	if !bytes.Equal(localHash.Sum(nil), remoteHash.Sum(nil)) {
		return 0, false, nil
	}
	if _, err := source.Seek(partialSize, io.SeekStart); err != nil {
		return 0, false, err
	}
	return partialSize, partialSize == sourceSize, nil
}

func isFTPShortRead(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func validateFTPDownloadCheckpoint(partialPath string, expected ftpDownloadCheckpoint) (valid, checkpointExists bool, err error) {
	info, err := os.Lstat(partialPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, false, errors.New("FTP 下载断点不是安全的普通文件")
	}
	checkpoint, err := readFTPDownloadCheckpoint(ftpDownloadCheckpointPath(partialPath))
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, true, err
	}
	if info.Size() <= 0 || info.Size() > expected.Size {
		return false, true, nil
	}
	if checkpoint.Endpoint != expected.Endpoint || checkpoint.RemotePath != expected.RemotePath ||
		checkpoint.Size != expected.Size || !checkpoint.ModTime.Equal(expected.ModTime) ||
		checkpoint.Offset != info.Size() || checkpoint.PrefixSHA256 == "" {
		return false, true, nil
	}
	digest, err := digestLocalFile(partialPath)
	if err != nil {
		return false, true, err
	}
	return digest == checkpoint.PrefixSHA256, true, nil
}

func openFTPDownloadPartial(partialPath string, offset int64) (*os.File, error) {
	if info, err := os.Lstat(partialPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("FTP 下载断点路径不是安全的普通文件")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(partialPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if offset == 0 {
		if err := file.Truncate(0); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func writeFTPDownloadCheckpoint(path string, checkpoint ftpDownloadCheckpoint) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("FTP 下载断点信息路径不是安全的普通文件")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".ftp-checkpoint-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceLocalFile(temporaryPath, path, true)
}

func readFTPDownloadCheckpoint(path string) (ftpDownloadCheckpoint, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ftpDownloadCheckpoint{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 16*1024 {
		return ftpDownloadCheckpoint{}, errors.New("FTP 下载断点信息不是安全的小型普通文件")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ftpDownloadCheckpoint{}, err
	}
	var checkpoint ftpDownloadCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return ftpDownloadCheckpoint{}, err
	}
	return checkpoint, nil
}

func digestLocalFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func ftpRemoteFileIdentity(conn *ftp.ServerConn, remotePath string, entry *ftp.Entry) (size int64, sizeKnown bool, modified time.Time, timeKnown bool) {
	size = int64(entry.Size)
	sizeKnown = size > 0
	if serverSize, err := conn.FileSize(remotePath); err == nil {
		size, sizeKnown = serverSize, true
	}
	modified = entry.Time
	timeKnown = !modified.IsZero()
	if conn.IsGetTimeSupported() {
		if serverTime, err := conn.GetTime(remotePath); err == nil {
			modified, timeKnown = serverTime, true
		}
	}
	return size, sizeKnown, modified, timeKnown
}

func verifyFTPRemoteFileIdentity(conn *ftp.ServerConn, remotePath string, size int64, sizeKnown bool, modified time.Time, timeKnown bool) error {
	if sizeKnown {
		currentSize, err := conn.FileSize(remotePath)
		if err == nil && currentSize != size {
			return errors.New("FTP 源文件在下载期间发生变化（大小已改变）")
		}
	}
	if timeKnown && conn.IsGetTimeSupported() {
		currentTime, err := conn.GetTime(remotePath)
		if err == nil && !currentTime.Equal(modified) {
			return errors.New("FTP 源文件在下载期间发生变化（修改时间已改变）")
		}
	}
	return nil
}
