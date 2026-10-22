package sessionfile

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"tui-ssh-workbench/internal/model"
)

const (
	maxXShellDirectoryFiles  = 4096
	maxXShellDirectoryGroups = 4096
	maxXShellDirectoryBytes  = 64 << 20
)

// ReadNativeSessionDirectory recursively imports .xsh and .xfp sessions.
// Directory names become nested groups, and relative paths are included in
// generated IDs so same-named sessions in different folders remain distinct.
func ReadNativeSessionDirectory(root string) (Catalog, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return Catalog{}, fmt.Errorf("stat Xshell session directory: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return Catalog{}, errors.New("Xshell session import path must be a directory, not a symlink")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return Catalog{}, fmt.Errorf("resolve Xshell session directory: %w", err)
	}
	var files []string
	var directories []string
	var total int64
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != root {
				directories = append(directories, path)
				if len(directories) > maxXShellDirectoryGroups {
					return fmt.Errorf("Xshell session directory contains more than %d groups", maxXShellDirectoryGroups)
				}
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".xsh" && ext != ".xfp" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() < 0 || info.Size() > MaxFileBytes {
			return fmt.Errorf("Xshell session %q exceeds %d bytes", path, MaxFileBytes)
		}
		total += info.Size()
		if total > maxXShellDirectoryBytes {
			return fmt.Errorf("Xshell session directory exceeds %d bytes", maxXShellDirectoryBytes)
		}
		files = append(files, path)
		if len(files) > maxXShellDirectoryFiles {
			return fmt.Errorf("Xshell session directory contains more than %d files", maxXShellDirectoryFiles)
		}
		return nil
	})
	if err != nil {
		return Catalog{}, fmt.Errorf("scan Xshell session directory: %w", err)
	}
	if len(files) == 0 {
		return Catalog{}, errors.New("native session directory contains no .xsh or .xfp files")
	}
	sort.Strings(files)
	groupsByPath := make(map[string]model.Group)
	var groups []model.Group
	var hosts []model.Host
	var totalRead int64
	sort.Strings(directories)
	for _, path := range directories {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return Catalog{}, fmt.Errorf("resolve Xshell group path: %w", err)
		}
		addXShellDirectoryGroups(filepath.ToSlash(rel), groupsByPath, &groups)
	}
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return Catalog{}, fmt.Errorf("resolve Xshell session path: %w", err)
		}
		rel = filepath.ToSlash(rel)
		dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))
		groupID := addXShellDirectoryGroups(dir, groupsByPath, &groups)
		before, err := os.Lstat(path)
		if err != nil {
			return Catalog{}, fmt.Errorf("stat Xshell session %q: %w", rel, err)
		}
		if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
			return Catalog{}, fmt.Errorf("Xshell session %q changed type while importing", rel)
		}
		file, err := os.Open(path)
		if err != nil {
			return Catalog{}, fmt.Errorf("open Xshell session %q: %w", rel, err)
		}
		opened, statErr := file.Stat()
		after, lstatErr := os.Lstat(path)
		if statErr != nil || lstatErr != nil || !os.SameFile(before, opened) || !os.SameFile(before, after) {
			_ = file.Close()
			return Catalog{}, fmt.Errorf("Xshell session %q changed while importing", rel)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return Catalog{}, fmt.Errorf("read Xshell session %q: %w", rel, readErr)
		}
		if closeErr != nil {
			return Catalog{}, fmt.Errorf("close Xshell session %q: %w", rel, closeErr)
		}
		if len(data) > MaxFileBytes {
			return Catalog{}, fmt.Errorf("Xshell session %q exceeds %d bytes", rel, MaxFileBytes)
		}
		totalRead += int64(len(data))
		if totalRead > maxXShellDirectoryBytes {
			return Catalog{}, fmt.Errorf("Xshell session directory exceeds %d bytes", maxXShellDirectoryBytes)
		}
		var catalog Catalog
		switch strings.ToLower(filepath.Ext(path)) {
		case ".xsh":
			catalog, err = decodeXShell(data, filepath.Base(path), rel)
		case ".xfp":
			catalog, err = decodeXFTP(data, filepath.Base(path), rel)
		}
		if err != nil {
			return Catalog{}, fmt.Errorf("import native session %q: %w", rel, err)
		}
		host := catalog.HostRecords()[0]
		host.GroupId = groupID
		hosts = append(hosts, host)
	}
	return NewCatalog(groups, hosts, nil), nil
}

// ReadXShellDirectory is retained for callers which specifically name the
// directory after Xshell; mixed Xshell/Xftp session trees are also accepted.
func ReadXShellDirectory(root string) (Catalog, error) {
	return ReadNativeSessionDirectory(root)
}

// ReadXFTPDirectory imports an Xftp session tree and uses subdirectories as
// nested groups. Mixed .xsh/.xfp trees are supported as well.
func ReadXFTPDirectory(root string) (Catalog, error) {
	return ReadNativeSessionDirectory(root)
}

func addXShellDirectoryGroups(relativeDir string, groupsByPath map[string]model.Group, groups *[]model.Group) string {
	relativeDir = filepath.ToSlash(filepath.Clean(filepath.FromSlash(relativeDir)))
	if relativeDir == "." || relativeDir == "" {
		return ""
	}
	parts := strings.Split(relativeDir, "/")
	parentPath := ""
	groupID := ""
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		groupPath := part
		if parentPath != "" {
			groupPath = parentPath + "/" + part
		}
		group, ok := groupsByPath[groupPath]
		if !ok {
			group = model.Group{Id: stableXShellID("group", groupPath), Name: part}
			if parentPath != "" {
				group.ParentId = groupsByPath[parentPath].Id
			}
			groupsByPath[groupPath] = group
			*groups = append(*groups, group)
		}
		parentPath, groupID = groupPath, group.Id
	}
	return groupID
}

// NativeSessionExportResult describes native sessions written and hosts
// intentionally skipped because their settings cannot be represented.
type NativeSessionExportResult struct {
	Exported int
	Skipped  []string
}

type XShellExportResult = NativeSessionExportResult
type XFTPExportResult = NativeSessionExportResult

// WriteXShellDirectory exports supported hosts as an Xshell session directory.
// The destination must not exist; this prevents accidental replacement of a
// user's session tree.
func WriteXShellDirectory(root string, groups []model.Group, hosts []model.Host, rules []model.PortForwardRule) (result XShellExportResult, err error) {
	rulesByHost := make(map[string]bool, len(rules))
	for _, rule := range rules {
		rulesByHost[rule.HostId] = true
	}
	return writeNativeSessionDirectory(root, groups, hosts, ".xsh", func(host model.Host) string {
		if host.Kind != model.HostKindSSH && host.Kind != model.HostKindTelnet && host.Kind != model.HostKindSerial {
			return fmt.Sprintf("协议 %s 不受 Xshell 导出支持", host.Kind)
		}
		if _, ok := encodeXShellTerminalCodePage(host.TerminalEncoding); !ok {
			return fmt.Sprintf("终端编码 %q 无法映射为 Xshell 代码页", host.TerminalEncoding)
		}
		if host.Kind == model.HostKindSerial {
			if reason := xShellSerialExportIssue(host); reason != "" {
				return reason
			}
			return ""
		}
		if host.Startup != "" || host.StartupScriptPath != "" || len(host.ProxyJumpList) != 0 || host.X11Forwarding {
			return "包含启动脚本、ProxyJump 或 X11 转发，无法无损映射"
		}
		if rulesByHost[host.Id] {
			return "包含端口转发规则，无法无损映射"
		}
		return ""
	}, encodeXShellSession)
}

// WriteXFTPDirectory exports SFTP, SCP, and plain FTP sessions as .xfp files.
// TLS-mode FTP and jump-host settings are skipped rather than converted to a
// weaker or unrelated connection.
func WriteXFTPDirectory(root string, groups []model.Group, hosts []model.Host) (result XFTPExportResult, err error) {
	return writeNativeSessionDirectory(root, groups, hosts, ".xfp", func(host model.Host) string {
		switch host.Kind {
		case model.HostKindSSH:
			if len(host.ProxyJumpList) != 0 {
				return "包含 ProxyJump，无法无损映射"
			}
			if host.FileProtocol != "" && host.FileProtocol != model.SSHFileProtocolSFTP && host.FileProtocol != model.SSHFileProtocolSCP {
				return "SSH 文件协议未知，无法无损映射"
			}
		case model.HostKindFTP:
			if host.FTPActiveMode {
				return "启用了 FTP 主动数据连接，Xftp 原生会话字段映射尚未验证"
			}
			if host.TLSMode != model.FTPTLSOff {
				return "FTP 安全模式未知或为 FTPS，无法无损映射到 Xftp FTP"
			}
		default:
			return fmt.Sprintf("协议 %s 不受 Xftp 导出支持", host.Kind)
		}
		return ""
	}, encodeXFTP)
}

func writeNativeSessionDirectory(root string, groups []model.Group, hosts []model.Host, extension string,
	skipReason func(model.Host) string, encode func(model.Host, string) []byte) (result NativeSessionExportResult, err error) {
	if strings.TrimSpace(root) == "" {
		return result, errors.New("native session export directory is required")
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return result, fmt.Errorf("create native session export directory (existing paths are not overwritten): %w", err)
	}
	createdRoot := root
	defer func() {
		if err != nil {
			_ = os.RemoveAll(createdRoot)
		}
	}()
	if err = os.Chmod(root, 0o700); err != nil {
		return result, fmt.Errorf("secure native session export directory: %w", err)
	}

	groupPaths, groupErrors := xShellGroupPaths(groups)
	ordered := append([]model.Host(nil), hosts...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].GroupId == ordered[j].GroupId {
			if ordered[i].Name == ordered[j].Name {
				return ordered[i].Id < ordered[j].Id
			}
			return ordered[i].Name < ordered[j].Name
		}
		return ordered[i].GroupId < ordered[j].GroupId
	})
	usedHostNames := make(map[string]map[string]int)
	groupDirs := make(map[string]string, len(groups))
	orderedGroups := append([]model.Group(nil), groups...)
	sort.Slice(orderedGroups, func(i, j int) bool {
		pi, pj := groupPaths[orderedGroups[i].Id], groupPaths[orderedGroups[j].Id]
		if len(pi) != len(pj) {
			return len(pi) < len(pj)
		}
		if orderedGroups[i].ParentId != orderedGroups[j].ParentId {
			return orderedGroups[i].ParentId < orderedGroups[j].ParentId
		}
		nameI, nameJ := "", ""
		if len(pi) != 0 {
			nameI = pi[len(pi)-1]
		}
		if len(pj) != 0 {
			nameJ = pj[len(pj)-1]
		}
		if nameI != nameJ {
			return strings.ToLower(nameI) < strings.ToLower(nameJ)
		}
		return orderedGroups[i].Id < orderedGroups[j].Id
	})
	usedGroupNames := make(map[string]map[string]int)
	for _, group := range orderedGroups {
		if groupErrors[group.Id] != "" {
			continue
		}
		parent := root
		if group.ParentId != "" {
			parent = groupDirs[group.ParentId]
			if parent == "" {
				groupErrors[group.Id] = "分组父级不可导出"
				continue
			}
		}
		allocated := allocateXShellName(usedGroupNames, parent, group.Name)
		dir := filepath.Join(parent, allocated)
		if err = os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return result, fmt.Errorf("create native session group directory: %w", err)
		}
		if err = os.Chmod(dir, 0o700); err != nil {
			return result, fmt.Errorf("secure native session group directory: %w", err)
		}
		groupDirs[group.Id] = dir
	}
	for _, dir := range groupDirs {
		groupName := filepath.Base(dir)
		if strings.EqualFold(filepath.Ext(groupName), extension) {
			parentKey := strings.ToLower(filepath.Dir(dir))
			if usedHostNames[parentKey] == nil {
				usedHostNames[parentKey] = make(map[string]int)
			}
			name := strings.TrimSuffix(groupName, filepath.Ext(groupName))
			usedHostNames[parentKey][strings.ToLower(name)] = 1
		}
	}
	for _, host := range ordered {
		if reason := skipReason(host); reason != "" {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s：%s", host.Name, reason))
			continue
		}
		if host.Kind == model.HostKindSerial && extension == ".xsh" {
			if strings.TrimSpace(host.IP) == "" {
				result.Skipped = append(result.Skipped, fmt.Sprintf("%s：串口设备名为空", host.Name))
				continue
			}
		} else if host.IP == "" || host.Port < 1 || host.Port > 65535 {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s：地址或端口无效", host.Name))
			continue
		}
		invalidField := ""
		for _, field := range []struct{ name, value string }{
			{"名称", host.Name}, {"地址", host.IP}, {"用户名", host.User}, {"备注", host.Description},
		} {
			if strings.ContainsAny(field.value, "\r\n\x00") {
				invalidField = field.name
				break
			}
		}
		if invalidField != "" {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s：%s包含换行或空字符", host.Name, invalidField))
			continue
		}
		dir := root
		if host.GroupId != "" {
			dir = groupDirs[host.GroupId]
		}
		if dir == "" {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s：所属分组无效", host.Name))
			continue
		}
		name := strings.TrimSpace(host.Name)
		if name == "" {
			name = host.IP
		}
		filename := allocateXShellName(usedHostNames, dir, sanitizeXShellComponent(name)) + extension
		sessionName := strings.TrimSuffix(filename, filepath.Ext(filename))
		data := encode(host, sessionName)
		file, createErr := os.OpenFile(filepath.Join(dir, filename), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			return result, fmt.Errorf("create native session %q: %w", filename, createErr)
		}
		if _, writeErr := file.Write(data); writeErr != nil {
			_ = file.Close()
			return result, fmt.Errorf("write native session %q: %w", filename, writeErr)
		}
		if syncErr := file.Sync(); syncErr != nil {
			_ = file.Close()
			return result, fmt.Errorf("sync native session %q: %w", filename, syncErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return result, fmt.Errorf("close native session %q: %w", filename, closeErr)
		}
		result.Exported++
	}
	return result, nil
}

func encodeXShellSession(host model.Host, name string) []byte {
	if host.Kind == model.HostKindSerial {
		codePage, _ := encodeXShellTerminalCodePage(host.TerminalEncoding)
		portIndex, _ := encodeXShellSerialPort(host.IP)
		baudIndex, _ := encodeXShellSerialChoice(host.SerialBaudRate, xShellBaudRates, 9600)
		dataBits := host.SerialDataBits
		if dataBits == 0 {
			dataBits = 8
		}
		parityIndex := map[string]int{
			model.SerialParityNone: 0, model.SerialParityOdd: 1, model.SerialParityEven: 2,
			model.SerialParityMark: 3, model.SerialParitySpace: 4,
		}[host.SerialParity]
		if host.SerialParity == "" {
			parityIndex = 0
		}
		stopIndex := map[string]int{
			model.SerialStopBitsOne: 0, model.SerialStopBitsOne5: 1, model.SerialStopBitsTwo: 2,
		}[host.SerialStopBits]
		if host.SerialStopBits == "" {
			stopIndex = 0
		}
		flowControlIndex := map[string]int{
			model.SerialFlowControlNone:    0,
			model.SerialFlowControlXONXOFF: 1,
			model.SerialFlowControlRTSCTS:  2,
		}[host.SerialFlowControl]
		if host.SerialFlowControl == "" {
			flowControlIndex = 0
		}
		text := "[SessionInfo]\r\nVersion=7.0\r\nSessionName=" + name + "\r\nDescription=" + host.Description + "\r\n\r\n" +
			"[CONNECTION]\r\nProtocol=SERIAL\r\n\r\n" +
			"[CONNECTION:SERIAL]\r\nBaudRate=" + fmt.Sprint(baudIndex) + "\r\nStopBits=" + fmt.Sprint(stopIndex) +
			"\r\nFlowCtrl=" + fmt.Sprint(flowControlIndex) + "\r\nParity=" + fmt.Sprint(parityIndex) + "\r\nDataBits=" + fmt.Sprint(dataBits-5) +
			"\r\nComPort=" + fmt.Sprint(portIndex) + "\r\n\r\n[TERMINAL]\r\nCodePage=" + fmt.Sprint(codePage) + "\r\n"
		return encodeXShellUTF16(text)
	}
	protocol := "SSH"
	if host.Kind == model.HostKindTelnet {
		protocol = "Telnet"
		if host.TerminalProtocol == model.TerminalProtocolRlogin {
			protocol = "Rlogin"
		}
	}
	codePage, _ := encodeXShellTerminalCodePage(host.TerminalEncoding)
	text := "[SessionInfo]\r\nVersion=7.0\r\nSessionName=" + name + "\r\nDescription=" + host.Description + "\r\n\r\n" +
		"[CONNECTION]\r\nProtocol=" + protocol + "\r\nHost=" + host.IP + "\r\nPort=" + fmt.Sprint(host.Port) + "\r\n\r\n" +
		"[CONNECTION:AUTHENTICATION]\r\nUserName=" + host.User + "\r\n\r\n[TERMINAL]\r\nCodePage=" + fmt.Sprint(codePage) + "\r\n"
	return encodeXShellUTF16(text)
}

func encodeXShellTerminalCodePage(encoding string) (int, bool) {
	if encoding == "" {
		encoding = model.TerminalEncodingUTF8
	}
	switch encoding {
	case model.TerminalEncodingUTF8:
		return 65001, true
	case "GBK":
		return 936, true
	case "GB18030":
		return 54936, true
	case "Big5":
		return 950, true
	case "Shift_JIS":
		return 932, true
	case "Windows-1252":
		return 1252, true
	default:
		return 0, false
	}
}

func encodeXShellUTF16(text string) []byte {
	units := utf16.Encode([]rune(text))
	data := []byte{0xff, 0xfe}
	for _, unit := range units {
		var encoded [2]byte
		binary.LittleEndian.PutUint16(encoded[:], unit)
		data = append(data, encoded[:]...)
	}
	return data
}

func xShellSerialExportIssue(host model.Host) string {
	if _, ok := encodeXShellSerialPort(host.IP); !ok {
		return "串口设备名无法映射为 Xshell COM 端口"
	}
	if _, ok := encodeXShellSerialChoice(host.SerialBaudRate, xShellBaudRates, 9600); !ok {
		return fmt.Sprintf("波特率 %d 不在 Xshell 可导出的选项中", host.SerialBaudRate)
	}
	dataBits := host.SerialDataBits
	if dataBits == 0 {
		dataBits = 8
	}
	if dataBits < 5 || dataBits > 8 {
		return fmt.Sprintf("数据位 %d 不受 Xshell 导出支持", dataBits)
	}
	switch host.SerialParity {
	case "", model.SerialParityNone, model.SerialParityOdd, model.SerialParityEven, model.SerialParityMark, model.SerialParitySpace:
	default:
		return fmt.Sprintf("校验位 %q 不受 Xshell 导出支持", host.SerialParity)
	}
	switch host.SerialStopBits {
	case "", model.SerialStopBitsOne, model.SerialStopBitsOne5, model.SerialStopBitsTwo:
	default:
		return fmt.Sprintf("停止位 %q 不受 Xshell 导出支持", host.SerialStopBits)
	}
	switch host.SerialFlowControl {
	case "", model.SerialFlowControlNone, model.SerialFlowControlXONXOFF, model.SerialFlowControlRTSCTS:
	default:
		return fmt.Sprintf("流控制 %q 不受 Xshell 导出支持", host.SerialFlowControl)
	}
	return ""
}

func encodeXShellSerialPort(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToUpper(value), "COM") {
		return 0, false
	}
	number, err := strconv.Atoi(value[3:])
	if err != nil || number < 1 || number > 256 {
		return 0, false
	}
	return number - 1, true
}

func encodeXShellSerialChoice(value int, choices []int, defaultValue int) (int, bool) {
	if value == 0 {
		value = defaultValue
	}
	for index, choice := range choices {
		if value == choice {
			return index, true
		}
	}
	return 0, false
}

func xShellGroupPaths(groups []model.Group) (map[string][]string, map[string]string) {
	byID := make(map[string]model.Group, len(groups))
	for _, group := range groups {
		byID[group.Id] = group
	}
	paths, errorsByID := make(map[string][]string, len(groups)), make(map[string]string)
	var visit func(string, map[string]bool) ([]string, string)
	visit = func(id string, visiting map[string]bool) ([]string, string) {
		if path, ok := paths[id]; ok {
			return path, ""
		}
		if issue := errorsByID[id]; issue != "" {
			return nil, issue
		}
		group, ok := byID[id]
		if !ok {
			return nil, "分组父级不存在"
		}
		if visiting[id] {
			return nil, "分组层级存在循环"
		}
		visiting[id] = true
		parts := []string{}
		if group.ParentId != "" {
			parent, issue := visit(group.ParentId, visiting)
			if issue != "" {
				delete(visiting, id)
				return nil, issue
			}
			parts = append(parts, parent...)
		}
		part := sanitizeXShellComponent(group.Name)
		if part == "" || part == "." || part == ".." {
			delete(visiting, id)
			return nil, "分组名称无效"
		}
		parts = append(parts, part)
		delete(visiting, id)
		paths[id] = parts
		return parts, ""
	}
	for _, group := range groups {
		if _, issue := visit(group.Id, make(map[string]bool)); issue != "" {
			errorsByID[group.Id] = issue
		}
	}
	return paths, errorsByID
}

func sanitizeXShellComponent(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if r < 0x20 || strings.ContainsRune(`/\\:*?"<>|`, r) {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	value = strings.TrimRight(strings.TrimSpace(b.String()), ". ")
	if value == "" || value == "." || value == ".." {
		value = "unnamed"
	}
	device := strings.ToUpper(strings.SplitN(value, ".", 2)[0])
	if device == "CON" || device == "PRN" || device == "AUX" || device == "NUL" ||
		(len(device) == 4 && (strings.HasPrefix(device, "COM") || strings.HasPrefix(device, "LPT")) && device[3] >= '1' && device[3] <= '9') {
		value += "_"
	}
	if len([]rune(value)) > 96 {
		value = string([]rune(value)[:96])
	}
	return value
}

func allocateXShellName(used map[string]map[string]int, parent, desired string) string {
	key := strings.ToLower(parent)
	if used[key] == nil {
		used[key] = make(map[string]int)
	}
	base := sanitizeXShellComponent(desired)
	for index := 1; ; index++ {
		candidate := base
		if index > 1 {
			candidate = fmt.Sprintf("%s (%d)", base, index)
		}
		folded := strings.ToLower(candidate)
		if used[key][folded] == 0 {
			used[key][folded] = 1
			return candidate
		}
	}
}

func stableXShellID(kind, identity string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + identity))
	return "xsh-" + hex.EncodeToString(digest[:12])
}
