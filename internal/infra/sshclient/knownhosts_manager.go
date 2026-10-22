package sshclient

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"tui-ssh-workbench/internal/model"
)

var ErrKnownHostEntryChanged = errors.New("SSH known_hosts entry changed; refresh before editing")
var ErrKnownHostsImportChanged = errors.New("SSH known_hosts source or destination changed; preview the import again")
var ErrKnownHostKeyChanged = errors.New("SSH known_hosts entry changed; reconnect before replacing it")

var knownHostsMutationMu sync.Mutex

// KnownHostsImportPlan is a validated, previewable merge into the application's
// trust store. Its snapshots are private so callers cannot alter the records
// that will be committed after confirmation.
type KnownHostsImportPlan struct {
	TotalRecords int
	NewRecords   int

	sourcePath          string
	sourceContents      []byte
	destinationContents []byte
	newRecords          []string
}

// SourcePath returns the validated path used to build the import preview.
func (plan KnownHostsImportPlan) SourcePath() string {
	return plan.sourcePath
}

// KnownHostEntry is a parsed line in the application's OpenSSH known_hosts
// file. Its raw line is private so callers can pass an entry back to the
// removal API without constructing arbitrary edits.
type KnownHostEntry struct {
	LineNumber  int
	Hosts       []string
	Marker      string
	KeyType     string
	Fingerprint string
	rawLine     string
}

// KnownHostKeyReference identifies a trust-store line observed during a
// failed SSH host-key check. The fingerprint prevents replacing a different
// key if the file changes before the user confirms the update.
type KnownHostKeyReference struct {
	LineNumber  int
	Fingerprint string
}

// ListKnownHostEntries lists the host keys the application currently trusts.
func ListKnownHostEntries() ([]KnownHostEntry, error) {
	path, err := ensureKnownHostsFile()
	if err != nil {
		return nil, err
	}
	return listKnownHostEntries(path)
}

// PlanKnownHostsImport validates an OpenSSH known_hosts file and prepares a
// deduplicated merge without changing the application's trust store.
func PlanKnownHostsImport(sourcePath string) (KnownHostsImportPlan, error) {
	var plan KnownHostsImportPlan
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return plan, errors.New("导入文件路径无效")
	}
	sourcePath, err := filepath.Abs(filepath.Clean(sourcePath))
	if err != nil {
		return plan, fmt.Errorf("解析导入路径失败：%w", err)
	}
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		return plan, fmt.Errorf("检查导入文件失败：%w", err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return plan, errors.New("导入文件必须是普通文件，不能是符号链接")
	}
	destinationPath, err := ensureKnownHostsFile()
	if err != nil {
		return plan, fmt.Errorf("准备 SSH 主机指纹文件失败：%w", err)
	}
	destinationInfo, err := os.Stat(destinationPath)
	if err != nil {
		return plan, fmt.Errorf("检查 SSH 主机指纹文件失败：%w", err)
	}
	if os.SameFile(sourceInfo, destinationInfo) {
		return plan, errors.New("导入文件不能是本应用自己的 known_hosts 文件")
	}

	sourceContents, err := os.ReadFile(sourcePath)
	if err != nil {
		return plan, fmt.Errorf("读取导入文件失败：%w", err)
	}
	destinationContents, err := os.ReadFile(destinationPath)
	if err != nil {
		return plan, fmt.Errorf("读取 SSH 主机指纹文件失败：%w", err)
	}
	sourceRecords, err := parseKnownHostRecords(sourceContents)
	if err != nil {
		return plan, fmt.Errorf("导入文件：%w", err)
	}
	destinationRecords, err := parseKnownHostRecords(destinationContents)
	if err != nil {
		return plan, fmt.Errorf("本应用的 SSH 主机指纹文件：%w", err)
	}

	existing := make(map[string]struct{}, len(destinationRecords))
	for _, record := range destinationRecords {
		existing[knownHostRecordIdentity(record)] = struct{}{}
	}
	newRecords := make([]string, 0, len(sourceRecords))
	for _, record := range sourceRecords {
		identity := knownHostRecordIdentity(record)
		if _, ok := existing[identity]; ok {
			continue
		}
		existing[identity] = struct{}{}
		newRecords = append(newRecords, record)
	}
	plan = KnownHostsImportPlan{
		TotalRecords:        len(sourceRecords),
		NewRecords:          len(newRecords),
		sourcePath:          sourcePath,
		sourceContents:      append([]byte(nil), sourceContents...),
		destinationContents: append([]byte(nil), destinationContents...),
		newRecords:          newRecords,
	}
	return plan, nil
}

// ApplyKnownHostsImport commits a previously previewed merge only if neither
// the source nor the destination changed since preview.
func ApplyKnownHostsImport(plan KnownHostsImportPlan) (int, error) {
	if plan.sourcePath == "" || plan.NewRecords != len(plan.newRecords) {
		return 0, errors.New("SSH 主机指纹导入预览无效")
	}
	knownHostsMutationMu.Lock()
	defer knownHostsMutationMu.Unlock()

	sourceInfo, err := os.Lstat(plan.sourcePath)
	if err != nil || !sourceInfo.Mode().IsRegular() {
		return 0, ErrKnownHostsImportChanged
	}
	sourceContents, err := os.ReadFile(plan.sourcePath)
	if err != nil || !bytes.Equal(sourceContents, plan.sourceContents) {
		return 0, ErrKnownHostsImportChanged
	}
	destinationPath, err := ensureKnownHostsFile()
	if err != nil {
		return 0, fmt.Errorf("准备 SSH 主机指纹文件失败：%w", err)
	}
	destinationContents, err := os.ReadFile(destinationPath)
	if err != nil || !bytes.Equal(destinationContents, plan.destinationContents) {
		return 0, ErrKnownHostsImportChanged
	}
	if len(plan.newRecords) == 0 {
		return 0, nil
	}
	updated := append([]byte(nil), destinationContents...)
	if len(updated) > 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, '\n')
	}
	for _, record := range plan.newRecords {
		updated = append(updated, record...)
		updated = append(updated, '\n')
	}
	if err := replaceKnownHostsFile(destinationPath, updated); err != nil {
		return 0, err
	}
	return len(plan.newRecords), nil
}

// ExportKnownHostsFile creates a private OpenSSH known_hosts copy. It never
// overwrites an existing destination.
func ExportKnownHostsFile(destinationPath string) (int, error) {
	destinationPath = strings.TrimSpace(destinationPath)
	if destinationPath == "" {
		return 0, errors.New("导出文件路径不能为空")
	}
	destinationPath, err := filepath.Abs(filepath.Clean(destinationPath))
	if err != nil {
		return 0, fmt.Errorf("解析导出路径失败：%w", err)
	}
	sourcePath, err := ensureKnownHostsFile()
	if err != nil {
		return 0, fmt.Errorf("准备 SSH 主机指纹文件失败：%w", err)
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return 0, fmt.Errorf("检查 SSH 主机指纹文件失败：%w", err)
	}
	if destinationInfo, statErr := os.Stat(destinationPath); statErr == nil && os.SameFile(sourceInfo, destinationInfo) {
		return 0, errors.New("导出目标不能是本应用自己的 known_hosts 文件")
	}
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		return 0, fmt.Errorf("读取 SSH 主机指纹文件失败：%w", err)
	}
	records, err := parseKnownHostRecords(contents)
	if err != nil {
		return 0, fmt.Errorf("本应用的 SSH 主机指纹文件：%w", err)
	}
	file, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return 0, errors.New("导出目标已存在；为避免覆盖，请选择新文件名")
		}
		return 0, fmt.Errorf("创建导出文件失败：%w", err)
	}
	writeErr := error(nil)
	if _, writeErr = file.Write(contents); writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(destinationPath)
		return 0, fmt.Errorf("写入导出文件失败：%w", writeErr)
	}
	if err := os.Chmod(destinationPath, 0o600); err != nil {
		_ = os.Remove(destinationPath)
		return 0, fmt.Errorf("保护导出文件失败：%w", err)
	}
	return len(records), nil
}

func parseKnownHostRecords(contents []byte) ([]string, error) {
	lines := bytes.Split(contents, []byte("\n"))
	records := make([]string, 0, len(lines))
	for index, line := range lines {
		line = bytes.TrimSuffix(line, []byte("\r"))
		record := strings.TrimSpace(string(line))
		if record == "" || strings.HasPrefix(record, "#") {
			continue
		}
		entry, err := parseKnownHostEntry(record)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行无效：%w", index+1, err)
		}
		if entry.Marker != "" && entry.Marker != "@cert-authority" && entry.Marker != "@revoked" {
			return nil, fmt.Errorf("第 %d 行使用不支持的标记 %q", index+1, entry.Marker)
		}
		records = append(records, record)
	}
	return records, nil
}

func knownHostRecordIdentity(record string) string {
	fields := strings.Fields(record)
	fieldCount := 3
	if len(fields) > 0 && strings.HasPrefix(fields[0], "@") {
		fieldCount++
	}
	if len(fields) < fieldCount {
		return strings.TrimSpace(record)
	}
	return strings.Join(fields[:fieldCount], " ")
}

func listKnownHostEntries(path string) ([]KnownHostEntry, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read SSH known_hosts: %w", err)
	}
	lines := bytes.Split(contents, []byte("\n"))
	entries := make([]KnownHostEntry, 0, len(lines))
	for index, line := range lines {
		line = bytes.TrimSuffix(line, []byte("\r"))
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		entry, err := parseKnownHostEntry(trimmed)
		if err != nil {
			return nil, fmt.Errorf("SSH known_hosts 第 %d 行无效：%w", index+1, err)
		}
		entry.LineNumber = index + 1
		entry.rawLine = trimmed
		entries = append(entries, entry)
	}
	return entries, nil
}

func parseKnownHostEntry(line string) (KnownHostEntry, error) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return KnownHostEntry{}, errors.New("字段不足")
	}
	entry := KnownHostEntry{}
	if strings.HasPrefix(fields[0], "@") {
		entry.Marker = fields[0]
		fields = fields[1:]
		if len(fields) < 3 {
			return KnownHostEntry{}, errors.New("标记行字段不足")
		}
	}
	entry.Hosts = strings.Split(fields[0], ",")
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields[1:], " ")))
	if err != nil {
		return KnownHostEntry{}, fmt.Errorf("解析公钥失败：%w", err)
	}
	entry.KeyType = key.Type()
	entry.Fingerprint = ssh.FingerprintSHA256(key)
	return entry, nil
}

// RemoveKnownHostEntry removes exactly the selected line. It fails if another
// process changed the file after the caller listed it.
func RemoveKnownHostEntry(entry KnownHostEntry) error {
	knownHostsMutationMu.Lock()
	defer knownHostsMutationMu.Unlock()
	path, err := ensureKnownHostsFile()
	if err != nil {
		return err
	}
	return removeKnownHostEntry(path, entry)
}

// ReplaceKnownHostKey atomically replaces the old key rows that matched one
// host with the newly confirmed key. Shared literal host rows retain their
// other aliases. Wildcard, certificate-authority, revoked, and otherwise
// ambiguous rows are rejected so this operation cannot widen the trust scope.
func ReplaceKnownHostKey(host model.Host, key ssh.PublicKey, expected []KnownHostKeyReference) error {
	if key == nil || !validHostAddress(host.IP) || host.Port < 1 || host.Port > 65535 || len(expected) == 0 {
		return errors.New("SSH 主机指纹更新参数无效")
	}
	address := knownhosts.Normalize(net.JoinHostPort(strings.TrimSpace(host.IP), strconv.Itoa(host.Port)))
	if address == "" {
		return errors.New("SSH 主机地址无效")
	}

	knownHostsMutationMu.Lock()
	defer knownHostsMutationMu.Unlock()

	path, err := ensureKnownHostsFile()
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("检查 SSH 主机指纹文件失败：%w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("SSH known_hosts 必须是普通文件")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 SSH 主机指纹文件失败：%w", err)
	}
	lines := bytes.SplitAfter(contents, []byte("\n"))
	changedLines := make(map[int][]byte, len(expected))
	seenLines := make(map[int]struct{}, len(expected))
	for _, reference := range expected {
		lineIndex := reference.LineNumber - 1
		if lineIndex < 0 || lineIndex >= len(lines) {
			return ErrKnownHostKeyChanged
		}
		if _, seen := seenLines[lineIndex]; seen {
			continue
		}
		seenLines[lineIndex] = struct{}{}

		line := lines[lineIndex]
		ending := ""
		if bytes.HasSuffix(line, []byte("\r\n")) {
			ending = "\r\n"
		} else if bytes.HasSuffix(line, []byte("\n")) {
			ending = "\n"
		}
		text := strings.TrimSuffix(string(line), "\n")
		text = strings.TrimSuffix(text, "\r")
		entry, parseErr := parseKnownHostEntry(strings.TrimSpace(text))
		if parseErr != nil || entry.Fingerprint != reference.Fingerprint || entry.Marker != "" {
			return ErrKnownHostKeyChanged
		}

		fields := strings.Fields(text)
		if len(fields) < 3 {
			return ErrKnownHostKeyChanged
		}
		hostFieldIndex := 0
		if strings.HasPrefix(fields[0], "@") {
			hostFieldIndex = 1
		}
		if hostFieldIndex >= len(fields) {
			return ErrKnownHostKeyChanged
		}

		patterns := strings.Split(fields[hostFieldIndex], ",")
		remaining := make([]string, 0, len(patterns))
		matched := false
		for _, pattern := range patterns {
			isMatch, isWildcard, matchErr := knownHostPatternMatches(pattern, address)
			if matchErr != nil {
				return fmt.Errorf("检查 SSH known_hosts 主机模式失败：%w", matchErr)
			}
			if isWildcard {
				return errors.New("此 SSH known_hosts 记录包含通配符，无法安全自动替换；请在主机指纹管理中手动检查")
			}
			if isMatch {
				matched = true
				continue
			}
			remaining = append(remaining, pattern)
		}
		if !matched {
			return ErrKnownHostKeyChanged
		}
		if len(remaining) == 0 {
			changedLines[lineIndex] = nil
			continue
		}
		fields[hostFieldIndex] = strings.Join(remaining, ",")
		changedLines[lineIndex] = []byte(strings.Join(fields, " ") + ending)
	}
	if len(changedLines) == 0 {
		return ErrKnownHostKeyChanged
	}

	updated := make([]byte, 0, len(contents)+len(key.Marshal()))
	for index, line := range lines {
		changed, ok := changedLines[index]
		if !ok {
			updated = append(updated, line...)
			continue
		}
		updated = append(updated, changed...)
	}
	if len(updated) > 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, '\n')
	}
	newRecord := knownhosts.Line([]string{address}, key)
	updated = append(updated, newRecord...)
	updated = append(updated, '\n')
	return replaceKnownHostsFile(path, updated)
}

func knownHostPatternMatches(pattern, address string) (matched, wildcard bool, err error) {
	if pattern == address {
		return true, false, nil
	}
	if strings.HasPrefix(pattern, "|1|") {
		parts := strings.Split(pattern, "|")
		if len(parts) != 4 || parts[1] != "1" {
			return false, false, errors.New("hashed SSH 主机名格式无效")
		}
		salt, decodeErr := base64.StdEncoding.DecodeString(parts[2])
		if decodeErr != nil {
			return false, false, decodeErr
		}
		want, decodeErr := base64.StdEncoding.DecodeString(parts[3])
		if decodeErr != nil {
			return false, false, decodeErr
		}
		mac := hmac.New(sha1.New, salt)
		_, _ = mac.Write([]byte(address))
		return hmac.Equal(want, mac.Sum(nil)), false, nil
	}
	if strings.ContainsAny(pattern, "*?!") {
		return false, true, nil
	}
	return false, false, nil
}

func removeKnownHostEntry(path string, entry KnownHostEntry) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect SSH known_hosts: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("SSH known_hosts 必须是普通文件")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read SSH known_hosts: %w", err)
	}
	lines := bytes.SplitAfter(contents, []byte("\n"))
	lineIndex := entry.LineNumber - 1
	if lineIndex < 0 || lineIndex >= len(lines) {
		return ErrKnownHostEntryChanged
	}
	currentLine := strings.TrimSuffix(string(lines[lineIndex]), "\n")
	currentLine = strings.TrimSuffix(currentLine, "\r")
	currentLine = strings.TrimSpace(currentLine)
	if currentLine != entry.rawLine || entry.rawLine == "" {
		return ErrKnownHostEntryChanged
	}
	updated := make([][]byte, 0, len(lines)-1)
	updated = append(updated, lines[:lineIndex]...)
	updated = append(updated, lines[lineIndex+1:]...)
	return replaceKnownHostsFile(path, bytes.Join(updated, nil))
}

func replaceKnownHostsFile(path string, contents []byte) (err error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".known_hosts-*")
	if err != nil {
		return fmt.Errorf("create temporary SSH known_hosts: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect temporary SSH known_hosts: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary SSH known_hosts: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary SSH known_hosts: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary SSH known_hosts: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace SSH known_hosts: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("protect SSH known_hosts: %w", err)
	}
	return nil
}
