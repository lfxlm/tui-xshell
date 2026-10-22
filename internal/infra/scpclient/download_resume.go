package scpclient

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tui-ssh-workbench/internal/infra/remotefs"
)

type remoteDownloadFile struct {
	mode            os.FileMode
	modeText        string
	size            int64
	modTime         time.Time
	digest          [sha256.Size]byte
	digestAvailable bool
}

var errRemoteDownloadIsDirectory = errors.New("远程 SCP 下载目标是目录")

func (client *Client) downloadSingleFile(ctx context.Context, remotePath, localPath string, overwrite bool, progress func(current, total int64)) error {
	return client.downloadSingleFileExpected(ctx, remotePath, localPath, overwrite, nil, progress)
}

func (client *Client) downloadSingleFileExpected(ctx context.Context, remotePath, localPath string, overwrite bool, expected *remoteDownloadTreeFile, progress func(current, total int64)) error {
	remoteInfo, err := client.inspectRemoteDownloadFile(ctx, remotePath)
	if err != nil {
		return fmt.Errorf("读取远程 SCP 文件信息失败：%w", err)
	}
	if expected != nil && (remoteInfo.size != expected.size || remoteInfo.mode.Perm() != expected.mode.Perm() || !remoteInfo.modTime.Equal(expected.modTime)) {
		return fmt.Errorf("远程文件在目录扫描后发生变化：%w", remotefs.ErrRemoteFileChanged)
	}
	if err := ensureLocalFileDestination(localPath, overwrite); err != nil {
		return fmt.Errorf("本地 SCP 文件目标不可用：%w", err)
	}
	partialPath, offset, prefixDigest, err := client.selectDownloadPartial(ctx, localPath, remotePath, remoteInfo)
	if err != nil {
		return err
	}
	if progress != nil {
		progress(offset, remoteInfo.size)
	}
	command := downloadResumeStreamCommand(remotePath, remoteInfo, offset, prefixDigest)
	err = client.run(ctx, command, func(_ io.Writer, stdout *bufio.Reader) error {
		partial, err := openDownloadPartial(partialPath, offset)
		if err != nil {
			return err
		}
		closeWithError := func(cause error) error {
			if closeErr := partial.Close(); closeErr != nil {
				return errors.Join(cause, fmt.Errorf("关闭 SCP 下载暂存文件失败：%w", closeErr))
			}
			return cause
		}
		remaining := remoteInfo.size - offset
		if err := copyExactWithProgress(ctx, partial, stdout, remaining, func(current, total int64) {
			if progress != nil {
				progress(offset+current, remoteInfo.size)
			}
			remotefs.ReportTransferProgress(ctx, remotefs.TransferProgress{
				CurrentPath: filepath.Base(localPath), Transferred: offset + current, Total: remoteInfo.size,
			})
		}); err != nil {
			return closeWithError(fmt.Errorf("下载 SCP 文件内容失败：%w", err))
		}
		if err := partial.Sync(); err != nil {
			return closeWithError(fmt.Errorf("同步本地 SCP 暂存文件失败：%w", err))
		}
		terminator, err := stdout.ReadByte()
		if err != nil {
			return closeWithError(fmt.Errorf("读取远程 SCP 文件结束标记失败：%w", err))
		}
		if terminator != 0 {
			return closeWithError(readResponseAfterStatus(stdout, terminator))
		}
		if remoteInfo.digestAvailable {
			if _, err := partial.Seek(0, io.SeekStart); err != nil {
				return closeWithError(fmt.Errorf("定位 SCP 下载暂存文件失败：%w", err))
			}
			digest := sha256.New()
			if _, err := io.Copy(digest, partial); err != nil {
				return closeWithError(fmt.Errorf("校验 SCP 下载暂存文件失败：%w", err))
			}
			if !equalSHA256(digest.Sum(nil), remoteInfo.digest[:]) {
				return closeWithError(errors.New("远程文件在下载期间发生变化；暂存文件已保留，但未提交到目标路径"))
			}
		}
		if err := partial.Chmod(remoteInfo.mode.Perm() & 0o777); err != nil {
			return closeWithError(fmt.Errorf("应用远程文件权限失败：%w", err))
		}
		if err := partial.Close(); err != nil {
			return fmt.Errorf("关闭本地 SCP 暂存文件失败：%w", err)
		}
		if err := os.Chtimes(partialPath, remoteInfo.modTime, remoteInfo.modTime); err != nil {
			return fmt.Errorf("应用远程文件修改时间失败：%w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := installLocalFile(partialPath, localPath, overwrite); err != nil {
		return fmt.Errorf("提交 SCP 下载文件失败：%w", err)
	}
	return nil
}

func (client *Client) inspectRemoteDownloadFile(ctx context.Context, source string) (remoteDownloadFile, error) {
	output, err := client.runCapture(ctx, inspectRemoteDownloadFileCommand(source), 512)
	if err != nil {
		return remoteDownloadFile{}, err
	}
	fields := strings.Fields(string(output))
	if len(fields) == 1 && fields[0] == "directory" {
		return remoteDownloadFile{}, errRemoteDownloadIsDirectory
	}
	if len(fields) != 5 || fields[0] != "file" {
		return remoteDownloadFile{}, fmt.Errorf("远程返回了无效的 SCP 文件信息：%q", strings.TrimSpace(string(output)))
	}
	modeValue, err := strconv.ParseUint(fields[1], 8, 32)
	if err != nil || modeValue > 0o7777 {
		return remoteDownloadFile{}, errors.New("远程 SCP 文件权限无效")
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return remoteDownloadFile{}, errors.New("远程 SCP 文件大小无效")
	}
	modifiedTime, timeErr := parseRemoteEpochTime(fields[3])
	if timeErr != nil {
		return remoteDownloadFile{}, errors.New("远程 SCP 文件修改时间无效")
	}
	result := remoteDownloadFile{
		mode: os.FileMode(modeValue), modeText: fields[1], size: size,
		modTime: modifiedTime,
	}
	if fields[4] != "unavailable" {
		digest, err := hex.DecodeString(fields[4])
		if err != nil || len(digest) != sha256.Size {
			return remoteDownloadFile{}, errors.New("远程 SCP 文件 SHA-256 信息无效")
		}
		copy(result.digest[:], digest)
		result.digestAvailable = true
	}
	return result, nil
}

func (client *Client) selectDownloadPartial(ctx context.Context, localPath, remotePath string, remoteInfo remoteDownloadFile) (string, int64, [sha256.Size]byte, error) {
	emptyDigest := sha256.Sum256(nil)
	for candidate := 0; candidate < maxSCPResumeCandidates; candidate++ {
		partialPath := localDownloadPartialPath(localPath, remotePath, candidate)
		info, err := os.Lstat(partialPath)
		if errors.Is(err, os.ErrNotExist) {
			return partialPath, 0, emptyDigest, nil
		}
		if err != nil {
			return "", 0, [sha256.Size]byte{}, fmt.Errorf("检查本地 SCP 下载暂存文件失败：%w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > remoteInfo.size {
			continue
		}
		if !remoteInfo.digestAvailable {
			continue
		}
		localDigest, err := digestLocalFilePrefix(partialPath, info.Size())
		if err != nil {
			return "", 0, [sha256.Size]byte{}, fmt.Errorf("校验本地 SCP 下载暂存文件失败：%w", err)
		}
		remoteDigest, err := client.inspectRemoteDownloadPrefix(ctx, remotePath, info.Size())
		if err != nil {
			return "", 0, [sha256.Size]byte{}, fmt.Errorf("校验远程 SCP 文件前缀失败：%w", err)
		}
		if remoteDigest == localDigest && localDigest != emptyDigest {
			return partialPath, info.Size(), remoteDigest, nil
		}
	}
	return "", 0, [sha256.Size]byte{}, errors.New("本地 SCP 下载续传暂存名均已被占用；为保护已有文件，本次下载未开始")
}

func (client *Client) inspectRemoteDownloadPrefix(ctx context.Context, source string, size int64) ([sha256.Size]byte, error) {
	if size == 0 {
		return sha256.Sum256(nil), nil
	}
	output, err := client.runCapture(ctx, inspectRemoteDownloadPrefixCommand(source, size), 128)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	digest, err := hex.DecodeString(strings.TrimSpace(string(output)))
	if err != nil || len(digest) != sha256.Size {
		return [sha256.Size]byte{}, errors.New("远程 SCP 文件前缀 SHA-256 信息无效")
	}
	var result [sha256.Size]byte
	copy(result[:], digest)
	return result, nil
}

func inspectRemoteDownloadFileCommand(source string) string {
	return "set -eu; LC_ALL=C; export LC_ALL; if [ -n \"${ZSH_VERSION-}\" ]; then setopt shwordsplit; fi; source=" + shellQuote(source) +
		"; if [ -L \"$source\" ]; then echo 'SCP 不跟随远程符号链接' >&2; exit 73; fi; " +
		"if [ -d \"$source\" ]; then printf 'directory\\n'; exit 0; fi; " +
		"if [ ! -f \"$source\" ]; then echo 'SCP 仅支持远程普通文件或目录' >&2; exit 73; fi; " +
		remoteStatFieldsShell() + " read_remote_stat; " +
		"if command -v sha256sum >/dev/null 2>&1; then digest=$(sha256sum \"$source\" | awk '{print $1}'); " +
		"elif command -v shasum >/dev/null 2>&1; then digest=$(shasum -a 256 \"$source\" | awk '{print $1}'); " +
		"else digest=unavailable; fi; printf 'file %s %s %s %s\\n' \"$mode\" \"$size\" \"$mtime\" \"$digest\""
}

func inspectRemoteDownloadPrefixCommand(source string, size int64) string {
	offset := strconv.FormatInt(size, 10)
	return "set -eu; source=" + shellQuote(source) +
		"; if [ -L \"$source\" ] || [ ! -f \"$source\" ]; then exit 73; fi; " +
		"if command -v sha256sum >/dev/null 2>&1; then head -c " + offset + " \"$source\" | sha256sum | awk '{print $1}'; " +
		"elif command -v shasum >/dev/null 2>&1; then head -c " + offset + " \"$source\" | shasum -a 256 | awk '{print $1}'; " +
		"else echo 'SHA-256 unavailable' >&2; exit 73; fi"
}

func downloadResumeStreamCommand(source string, info remoteDownloadFile, offset int64, prefixDigest [sha256.Size]byte) string {
	quotedSource := shellQuote(source)
	remaining := info.size - offset
	command := "set -eu; LC_ALL=C; export LC_ALL; if [ -n \"${ZSH_VERSION-}\" ]; then setopt shwordsplit; fi; source=" + quotedSource +
		"; offset=" + strconv.FormatInt(offset, 10) + "; remaining=" + strconv.FormatInt(remaining, 10) + "; " +
		"expected_mode=" + shellQuote(info.modeText) + "; expected_size=" + strconv.FormatInt(info.size, 10) +
		"; expected_mtime=" + shellQuote(formatRemoteEpochTime(info.modTime)) + "; expected_digest="
	if info.digestAvailable {
		command += shellQuote(hex.EncodeToString(info.digest[:]))
	} else {
		command += "''"
	}
	command += "; expected_prefix="
	if offset > 0 && info.digestAvailable {
		command += shellQuote(hex.EncodeToString(prefixDigest[:]))
	} else {
		command += "''"
	}
	command += "; " + remoteStatFieldsShell() + " " +
		"validate_source() { if [ -L \"$source\" ] || [ ! -f \"$source\" ]; then echo 'SCP 源文件类型已变化' >&2; exit 73; fi; " +
		"read_remote_stat; if [ \"$mode\" != \"$expected_mode\" ] || [ \"$size\" != \"$expected_size\" ] || [ \"$mtime\" != \"$expected_mtime\" ]; then echo 'SCP 源文件元数据已变化' >&2; exit 74; fi; " +
		"if [ -n \"$expected_digest\" ]; then if command -v sha256sum >/dev/null 2>&1; then current_digest=$(sha256sum \"$source\" | awk '{print $1}'); " +
		"elif command -v shasum >/dev/null 2>&1; then current_digest=$(shasum -a 256 \"$source\" | awk '{print $1}'); " +
		"else echo '远程缺少 SHA-256 工具' >&2; exit 73; fi; " +
		"if [ \"$current_digest\" != \"$expected_digest\" ]; then echo 'SCP 源文件内容已变化' >&2; exit 74; fi; fi; " +
		"if [ -n \"$expected_prefix\" ]; then if command -v sha256sum >/dev/null 2>&1; then current_prefix=$(head -c \"$offset\" \"$source\" | sha256sum | awk '{print $1}'); " +
		"elif command -v shasum >/dev/null 2>&1; then current_prefix=$(head -c \"$offset\" \"$source\" | shasum -a 256 | awk '{print $1}'); " +
		"else echo '远程缺少 SHA-256 工具' >&2; exit 73; fi; " +
		"if [ \"$current_prefix\" != \"$expected_prefix\" ]; then echo 'SCP 源文件前缀已变化' >&2; exit 74; fi; fi; }; " +
		"validate_source; "
	command += "tail -c +" + strconv.FormatInt(offset+1, 10) + " \"$source\" | head -c \"$remaining\"; validate_source; printf '\\000'"
	return command
}

func remoteStatFieldsShell() string {
	return "read_remote_stat() { " + remoteStatAssignmentShell() + "; };"
}

func remoteStatAssignmentShell() string {
	return "if stat -c '%a %s %Y' \"$source\" >/dev/null 2>&1; then values=$(stat -c '%a %s %y' \"$source\"); set -- $values; " +
		"if [ $# -ne 5 ]; then echo 'GNU stat 信息格式无效' >&2; exit 73; fi; mode=$1; size=$2; date_text=\"$3 $4 $5\"; " +
		"if command -v date >/dev/null 2>&1; then mtime=$(date -u -d \"$date_text\" '+%s.%N'); " +
		"else mtime=$(stat -c '%Y' \"$source\"); mtime=\"$mtime.000000000\"; fi; " +
		"elif stat -f '%Lp %z %.9Fm' \"$source\" >/dev/null 2>&1; then values=$(stat -f '%Lp %z %.9Fm' \"$source\"); set -- $values; " +
		"if [ $# -ne 3 ]; then echo 'BSD stat 信息格式无效' >&2; exit 73; fi; mode=$1; size=$2; mtime=$3; " +
		"else echo '远程 stat 不支持 GNU/BSD 格式' >&2; exit 73; fi"
}

func parseRemoteEpochTime(value string) (time.Time, error) {
	parts := strings.SplitN(value, ".", 2)
	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, errors.New("epoch 秒数无效")
	}
	var nanoseconds int64
	if len(parts) == 2 {
		fraction := parts[1]
		if fraction == "" || len(fraction) > 9 {
			return time.Time{}, errors.New("epoch 纳秒数无效")
		}
		for len(fraction) < 9 {
			fraction += "0"
		}
		nanoseconds, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return time.Time{}, errors.New("epoch 纳秒数无效")
		}
	}
	return time.Unix(seconds, nanoseconds), nil
}

func formatRemoteEpochTime(value time.Time) string {
	return fmt.Sprintf("%d.%09d", value.Unix(), value.Nanosecond())
}

func localDownloadPartialPath(localPath, remotePath string, candidate int) string {
	key := sha256.Sum256([]byte(localPath + "\x00" + remotePath))
	name := fmt.Sprintf(".tui-scp-download-%s-%02d.part", hex.EncodeToString(key[:10]), candidate)
	return filepath.Join(filepath.Dir(localPath), name)
}

func digestLocalFilePrefix(filePath string, size int64) ([sha256.Size]byte, error) {
	if size < 0 {
		return [sha256.Size]byte{}, errors.New("SCP 下载续传偏移不能小于零")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if size > 0 {
		if _, err := io.CopyN(hash, io.NewSectionReader(file, 0, size), size); err != nil {
			return [sha256.Size]byte{}, err
		}
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func openDownloadPartial(filePath string, expectedSize int64) (*os.File, error) {
	before, err := os.Lstat(filePath)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return nil, fmt.Errorf("创建本地 SCP 下载暂存文件失败：%w", createErr)
		}
		return file, nil
	}
	if err != nil {
		return nil, fmt.Errorf("检查本地 SCP 下载暂存文件失败：%w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() != expectedSize {
		return nil, errors.New("本地 SCP 下载暂存文件已变化；为保护已有文件，本次下载已停止")
	}
	file, err := os.OpenFile(filePath, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("打开本地 SCP 下载暂存文件失败：%w", err)
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != expectedSize {
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("读取打开后的 SCP 下载暂存文件失败：%w", err)
		}
		return nil, errors.New("本地 SCP 下载暂存文件在打开期间发生变化")
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("定位本地 SCP 下载暂存文件末尾失败：%w", err)
	}
	return file, nil
}

func equalSHA256(left, right []byte) bool {
	if len(left) != sha256.Size || len(right) != sha256.Size {
		return false
	}
	var leftDigest, rightDigest [sha256.Size]byte
	copy(leftDigest[:], left)
	copy(rightDigest[:], right)
	return leftDigest == rightDigest
}
