package scpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
)

const maxSCPResumeCandidates = 32

type remoteUploadPartial struct {
	exists  bool
	blocked bool
	size    int64
	digest  [sha256.Size]byte
}

// selectUploadPartial resumes only a hidden partial whose complete contents
// match the corresponding source prefix. Occupied paths with unknown or
// mismatching contents are left untouched and skipped.
func (client *Client) selectUploadPartial(ctx context.Context, source *os.File, target string, sourceSize int64) (string, int64, [sha256.Size]byte, bool, error) {
	emptyDigest := sha256.Sum256(nil)
	for candidate := 0; candidate < maxSCPResumeCandidates; candidate++ {
		partial := remoteUploadPartialPath(target, candidate)
		state, err := client.inspectUploadPartial(ctx, partial)
		if err != nil {
			return "", 0, [sha256.Size]byte{}, false, fmt.Errorf("检查远程 SCP 续传文件失败：%w", err)
		}
		if !state.exists {
			if state.blocked {
				continue
			}
			return partial, 0, emptyDigest, false, nil
		}
		if state.size < 0 || state.size > sourceSize {
			continue
		}
		sourceDigest, err := digestLocalPrefix(source, state.size)
		if err != nil {
			return "", 0, [sha256.Size]byte{}, false, fmt.Errorf("读取本地源文件前缀失败：%w", err)
		}
		if sourceDigest == state.digest {
			return partial, state.size, sourceDigest, true, nil
		}
	}
	return "", 0, [sha256.Size]byte{}, false, errors.New("远程 SCP 续传暂存名均已被占用；为保护已有文件，本次上传未开始")
}

func (client *Client) inspectUploadPartial(ctx context.Context, partial string) (remoteUploadPartial, error) {
	output, err := client.runCapture(ctx, inspectUploadPartialCommand(partial), 512)
	if err != nil {
		return remoteUploadPartial{}, err
	}
	fields := strings.Fields(string(output))
	if len(fields) == 1 && fields[0] == "missing" {
		return remoteUploadPartial{}, nil
	}
	if len(fields) == 1 && fields[0] == "blocked" || len(fields) == 2 && fields[0] == "unavailable" {
		return remoteUploadPartial{blocked: true}, nil
	}
	if len(fields) != 3 || fields[0] != "file" {
		return remoteUploadPartial{}, fmt.Errorf("远程返回了无效的暂存信息：%q", strings.TrimSpace(string(output)))
	}
	size, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || size < 0 {
		return remoteUploadPartial{}, errors.New("远程 SCP 暂存文件大小无效")
	}
	digestBytes, err := hex.DecodeString(fields[2])
	if err != nil || len(digestBytes) != sha256.Size {
		// No SHA-256 utility is available or the remote response was malformed.
		// The caller will select another path instead of trusting this partial.
		return remoteUploadPartial{}, nil
	}
	var digest [sha256.Size]byte
	copy(digest[:], digestBytes)
	return remoteUploadPartial{exists: true, size: size, digest: digest}, nil
}

func remoteUploadPartialPath(target string, candidate int) string {
	digest := sha256.Sum256([]byte(path.Clean(target)))
	name := fmt.Sprintf(".tui-scp-%s-%02d.part", hex.EncodeToString(digest[:10]), candidate)
	return path.Join(path.Dir(target), name)
}

func inspectUploadPartialCommand(partial string) string {
	return "set -eu; if [ -n \"${ZSH_VERSION-}\" ]; then setopt shwordsplit; fi; partial=" + shellQuote(partial) +
		"; if [ -L \"$partial\" ] || { [ -e \"$partial\" ] && [ ! -f \"$partial\" ]; }; then printf 'blocked\\n'; exit 0; fi; " +
		"if [ ! -e \"$partial\" ]; then printf 'missing\\n'; exit 0; fi; " +
		"size=$(wc -c < \"$partial\" | tr -d '[:space:]'); " +
		"if command -v sha256sum >/dev/null 2>&1; then digest=$(sha256sum \"$partial\" | awk '{print $1}'); " +
		"elif command -v shasum >/dev/null 2>&1; then digest=$(shasum -a 256 \"$partial\" | awk '{print $1}'); " +
		"else printf 'unavailable %s\\n' \"$size\"; exit 0; fi; " +
		"printf 'file %s %s\\n' \"$size\" \"$digest\""
}

func uploadResumeStreamCommand(partial string, offset, total int64, prefixDigest [sha256.Size]byte, partialExists bool) string {
	expectedExists := "0"
	if partialExists {
		expectedExists = "1"
	}
	quotedPartial := shellQuote(partial)
	command := "set -eu; if [ -n \"${ZSH_VERSION-}\" ]; then setopt shwordsplit; fi; partial=" + quotedPartial +
		"; lock=\"$partial.lock\"; umask 077; " +
		"if ! mkdir \"$lock\" 2>/dev/null; then " +
		"if [ -L \"$lock\" ] || [ ! -f \"$lock/pid\" ]; then echo 'SCP 暂存文件正在被其他传输使用' >&2; exit 73; fi; " +
		"owner=$(cat \"$lock/pid\"); case \"$owner\" in ''|*[!0-9]*) echo 'SCP 续传锁信息无效' >&2; exit 73;; esac; " +
		"if kill -0 \"$owner\" 2>/dev/null; then echo 'SCP 暂存文件正在被其他传输使用' >&2; exit 73; fi; " +
		"rm -f \"$lock/pid\"; rmdir \"$lock\" 2>/dev/null || { echo '无法清理过期的 SCP 续传锁' >&2; exit 73; }; " +
		"mkdir \"$lock\" || { echo 'SCP 暂存文件正在被其他传输使用' >&2; exit 73; }; fi; " +
		"printf '%s\\n' \"$$\" > \"$lock/pid\"; " +
		"cleanup_lock() { rm -f \"$lock/pid\"; rmdir \"$lock\" 2>/dev/null || true; }; " +
		"trap cleanup_lock EXIT; trap 'exit 130' HUP INT TERM; " +
		"expected_exists=" + expectedExists + "; offset=" + strconv.FormatInt(offset, 10) +
		"; total=" + strconv.FormatInt(total, 10) + "; expected_digest=" + shellQuote(hex.EncodeToString(prefixDigest[:])) + "; " +
		"if [ \"$expected_exists\" = 1 ]; then " +
		"if [ -L \"$partial\" ] || [ ! -f \"$partial\" ]; then echo 'SCP 暂存路径已变化' >&2; exit 73; fi; " +
		"current_size=$(wc -c < \"$partial\" | tr -d '[:space:]'); " +
		"if [ \"$current_size\" != \"$offset\" ]; then echo 'SCP 暂存文件大小已变化' >&2; exit 73; fi; " +
		"if command -v sha256sum >/dev/null 2>&1; then current_digest=$(sha256sum \"$partial\" | awk '{print $1}'); " +
		"elif command -v shasum >/dev/null 2>&1; then current_digest=$(shasum -a 256 \"$partial\" | awk '{print $1}'); " +
		"else echo '远程缺少 SHA-256 工具，无法安全续传' >&2; exit 73; fi; " +
		"if [ \"$current_digest\" != \"$expected_digest\" ]; then echo 'SCP 暂存文件前缀已变化' >&2; exit 73; fi; " +
		"else if [ -e \"$partial\" ] || [ -L \"$partial\" ] || [ \"$offset\" != 0 ]; then echo 'SCP 暂存路径已被占用' >&2; exit 73; fi; : > \"$partial\"; fi; " +
		"cat >> \"$partial\"; actual_size=$(wc -c < \"$partial\" | tr -d '[:space:]'); " +
		"if [ \"$actual_size\" != \"$total\" ]; then echo 'SCP 上传中断，暂存文件已保留以便续传' >&2; exit 74; fi"
	return command
}

func uploadResumeCommitCommand(target, partial string, info os.FileInfo, overwrite bool) string {
	quotedTarget, quotedPartial := shellQuote(target), shellQuote(partial)
	timestamp := info.ModTime().UTC().Format("200601021504.05")
	preciseTimestamp := formatRemoteEpochTime(info.ModTime())
	seconds := strconv.FormatInt(info.ModTime().Unix(), 10)
	nanoseconds := fmt.Sprintf("%09d", info.ModTime().Nanosecond())
	pythonTimeCommand := "python3 -c 'import os,sys; ns=int(sys.argv[1])*1000000000+int(sys.argv[2]); os.utime(sys.argv[3], ns=(ns,ns))' " +
		shellQuote(seconds) + " " + shellQuote(nanoseconds) + " \"$partial\""
	touchCommand := "if touch -m -d " + shellQuote("@"+preciseTimestamp) + " \"$partial\" 2>/dev/null; then :; " +
		"elif command -v python3 >/dev/null 2>&1; then " + pythonTimeCommand + "; " +
		"else TZ=UTC touch -m -t " + shellQuote(timestamp) + " \"$partial\"; fi"
	commit := "ln \"$partial\" \"$target\" && rm -f \"$partial\""
	if overwrite {
		commit = "mv -f \"$partial\" \"$target\""
	}
	return "set -eu; target=" + quotedTarget + "; partial=" + quotedPartial +
		"; expected=" + strconv.FormatInt(info.Size(), 10) + "; " +
		"if [ -L \"$partial\" ] || [ ! -f \"$partial\" ]; then echo 'SCP 暂存文件不存在或类型已变化' >&2; exit 73; fi; " +
		"actual=$(wc -c < \"$partial\" | tr -d '[:space:]'); if [ \"$actual\" != \"$expected\" ]; then echo 'SCP 暂存文件大小不完整' >&2; exit 74; fi; " +
		"chmod " + fmt.Sprintf("%04o", info.Mode().Perm()&0o777) + " \"$partial\"; " +
		touchCommand + "; " +
		"if [ -d \"$target\" ]; then echo '目标路径是目录' >&2; exit 73; fi; " + commit
}

func digestLocalPrefix(source *os.File, size int64) ([sha256.Size]byte, error) {
	if size < 0 {
		return [sha256.Size]byte{}, errors.New("续传偏移不能小于零")
	}
	digest := sha256.New()
	if size > 0 {
		if _, err := io.CopyN(digest, io.NewSectionReader(source, 0, size), size); err != nil {
			return [sha256.Size]byte{}, err
		}
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result, nil
}
