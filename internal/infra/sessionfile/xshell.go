package sessionfile

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"tui-ssh-workbench/internal/model"
)

var nativeSessionKeys = map[string]struct{}{
	"version": {}, "protocol": {}, "host": {}, "hostname": {}, "port": {},
	"username": {}, "sessionname": {}, "description": {},
	"comport": {}, "baudrate": {}, "databits": {}, "parity": {}, "stopbits": {}, "flowctrl": {},
	"codepage": {},
}

// xShellBaudRates maps the zero-based option IDs used in native SERIAL
// sessions to baud rates. The observed Xshell v6 fixture uses ID 6 for 9600;
// real-client import/export acceptance remains tracked in PRODUCT_PARITY.md.
var xShellBaudRates = []int{110, 300, 600, 1200, 2400, 4800, 9600, 14400, 19200, 28800, 38400, 56000, 57600, 115200, 128000, 256000}

// DecodeXShell imports the connection settings supported by this application
// from one Xshell .xsh session. Passwords, scripts, proxy settings, and other
// Xshell-only options are intentionally ignored instead of being silently
// mapped to unrelated fields.
func DecodeXShell(data []byte, filename string) (Catalog, error) {
	return decodeXShell(data, filename, "")
}

func decodeXShell(data []byte, filename, identity string) (Catalog, error) {
	if len(data) > MaxFileBytes {
		return Catalog{}, fmt.Errorf("Xshell session exceeds %d bytes", MaxFileBytes)
	}
	text, err := decodeXShellText(data)
	if err != nil {
		return Catalog{}, fmt.Errorf("decode Xshell session: %w", err)
	}
	sections, err := parseNativeSessionINI(text)
	if err != nil {
		return Catalog{}, err
	}
	if sections["sessioninfo"] == nil {
		return Catalog{}, errors.New("文件缺少 Xshell SessionInfo 分节")
	}
	connection := sections["connection"]
	protocol := strings.ToLower(strings.TrimSpace(connection["protocol"]))
	var kind model.HostKind
	switch {
	case strings.HasPrefix(protocol, "ssh"):
		kind = model.HostKindSSH
	case protocol == "telnet" || protocol == "rlogin":
		kind = model.HostKindTelnet
	case protocol == "serial":
		kind = model.HostKindSerial
	default:
		return Catalog{}, fmt.Errorf("Xshell 会话协议 %q 暂不支持导入", connection["protocol"])
	}
	var hostname string
	port := 0
	if kind == model.HostKindSerial {
		serial := sections["connection:serial"]
		var ok bool
		hostname, ok = decodeXShellSerialPort(serial["comport"])
		if !ok {
			return Catalog{}, errors.New("Xshell 串口会话缺少有效的 ComPort")
		}
	} else {
		hostname = strings.TrimSpace(connection["host"])
		if hostname == "" {
			hostname = strings.TrimSpace(connection["hostname"])
		}
		if hostname == "" {
			return Catalog{}, errors.New("Xshell 会话缺少连接主机")
		}
		port = 22
		if kind == model.HostKindTelnet {
			port = 23
			if protocol == "rlogin" {
				port = 513
			}
		}
		if value := strings.TrimSpace(connection["port"]); value != "" {
			port, err = strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return Catalog{}, errors.New("Xshell 会话端口必须是 1 到 65535 之间的整数")
			}
		}
	}
	authentication := sections["connection:authentication"]
	username := strings.TrimSpace(authentication["username"])
	if username == "" {
		username = strings.TrimSpace(connection["username"])
	}
	if username == "" {
		username = strings.TrimSpace(sections["connection:ssh"]["username"])
	}
	if !utf8.ValidString(hostname) || !utf8.ValidString(username) {
		return Catalog{}, errors.New("Xshell 会话的主机名或用户名不是有效 UTF-8")
	}
	name := strings.TrimSpace(sections["sessioninfo"]["sessionname"])
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	}
	if name == "" || name == "." {
		name = hostname
	}
	description := strings.TrimSpace(sections["sessioninfo"]["description"])
	identityKey := identity
	if protocol == "rlogin" {
		identityKey += "\x00rlogin"
	}
	id := xShellHostID(kind, name, hostname, port, username, identityKey)
	host := model.Host{
		Id: id, Kind: kind, Name: name, IP: hostname, Port: port,
		User: username, Description: description,
	}
	terminalEncoding, hasTerminalCodePage, err := decodeXShellTerminalEncoding(sections["terminal"])
	if err != nil {
		return Catalog{}, err
	}
	host.TerminalEncoding = terminalEncoding
	if hasTerminalCodePage {
		// Xshell's output CodePage setting does not change keyboard input bytes.
		host.TerminalInputEncoding = model.TerminalEncodingUTF8
	}
	if kind == model.HostKindTelnet {
		host.TerminalProtocol = model.TerminalProtocolTelnet
		if protocol == "rlogin" {
			host.TerminalProtocol = model.TerminalProtocolRlogin
		}
	} else if kind == model.HostKindSerial {
		serial := sections["connection:serial"]
		var parseErr error
		host.SerialFlowControl, parseErr = decodeXShellSerialFlowControl(serial["flowctrl"])
		if parseErr != nil {
			return Catalog{}, fmt.Errorf("Xshell 串口流控制无效：%w", parseErr)
		}
		host.SerialBaudRate, parseErr = decodeXShellSerialEnum(serial["baudrate"], xShellBaudRates, 9600)
		if parseErr != nil {
			return Catalog{}, fmt.Errorf("Xshell 串口波特率无效：%w", parseErr)
		}
		host.SerialDataBits, parseErr = decodeXShellSerialDataBits(serial["databits"])
		if parseErr != nil {
			return Catalog{}, fmt.Errorf("Xshell 串口数据位无效：%w", parseErr)
		}
		host.SerialParity, parseErr = decodeXShellSerialParity(serial["parity"])
		if parseErr != nil {
			return Catalog{}, fmt.Errorf("Xshell 串口校验位无效：%w", parseErr)
		}
		host.SerialStopBits, parseErr = decodeXShellSerialStopBits(serial["stopbits"])
		if parseErr != nil {
			return Catalog{}, fmt.Errorf("Xshell 串口停止位无效：%w", parseErr)
		}
	}
	return NewCatalog(nil, []model.Host{host}, nil), nil
}

func decodeXShellTerminalEncoding(terminal map[string]string) (encoding string, explicit bool, err error) {
	value := strings.TrimSpace(terminal["codepage"])
	if value == "" {
		return model.TerminalEncodingUTF8, false, nil
	}
	codePage, parseErr := strconv.Atoi(value)
	if parseErr != nil || codePage < 1 {
		return "", false, fmt.Errorf("Xshell 终端代码页无效：%q", value)
	}
	switch codePage {
	case 65001:
		encoding = model.TerminalEncodingUTF8
	case 936:
		encoding = "GBK"
	case 54936:
		encoding = "GB18030"
	case 950:
		encoding = "Big5"
	case 932:
		encoding = "Shift_JIS"
	case 1252:
		encoding = "Windows-1252"
	default:
		return "", false, fmt.Errorf("Xshell 终端代码页 %d 暂不支持", codePage)
	}
	return encoding, true, nil
}

func decodeXShellSerialFlowControl(value string) (string, error) {
	// Xshell stores this choice as an option ID. Real-client fixtures are still
	// required to confirm that the IDs match the UI order used here.
	choices := []string{model.SerialFlowControlNone, model.SerialFlowControlXONXOFF, model.SerialFlowControlRTSCTS}
	if strings.TrimSpace(value) == "" {
		return model.SerialFlowControlNone, nil
	}
	index, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || index < 0 || index >= len(choices) {
		return "", fmt.Errorf("未知的选项编码 %q", value)
	}
	return choices[index], nil
}

func decodeXShellSerialPort(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if index, err := strconv.Atoi(value); err == nil {
		if index < 0 || index > 255 {
			return "", false
		}
		return fmt.Sprintf("COM%d", index+1), true
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", false
	}
	return value, true
}

func decodeXShellSerialEnum(value string, choices []int, defaultValue int) (int, error) {
	if strings.TrimSpace(value) == "" {
		return defaultValue, nil
	}
	index, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || index < 0 || index >= len(choices) {
		return 0, fmt.Errorf("未知的选项编码 %q", value)
	}
	return choices[index], nil
}

func decodeXShellSerialDataBits(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 8, nil
	}
	index, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || index < 0 || index > 3 {
		return 0, fmt.Errorf("未知的数据位编码 %q", value)
	}
	return index + 5, nil
}

func decodeXShellSerialParity(value string) (string, error) {
	choices := []string{model.SerialParityNone, model.SerialParityOdd, model.SerialParityEven, model.SerialParityMark, model.SerialParitySpace}
	if strings.TrimSpace(value) == "" {
		return model.SerialParityNone, nil
	}
	index, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || index < 0 || index >= len(choices) {
		return "", fmt.Errorf("未知的校验位编码 %q", value)
	}
	return choices[index], nil
}

func decodeXShellSerialStopBits(value string) (string, error) {
	choices := []string{model.SerialStopBitsOne, model.SerialStopBitsOne5, model.SerialStopBitsTwo}
	if strings.TrimSpace(value) == "" {
		return model.SerialStopBitsOne, nil
	}
	index, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || index < 0 || index >= len(choices) {
		return "", fmt.Errorf("未知的停止位编码 %q", value)
	}
	return choices[index], nil
}

func decodeXShellText(data []byte) (string, error) {
	switch {
	case len(data) >= 2 && data[0] == 0xff && data[1] == 0xfe:
		return decodeXShellUTF16(data[2:], binary.LittleEndian)
	case len(data) >= 2 && data[0] == 0xfe && data[1] == 0xff:
		return decodeXShellUTF16(data[2:], binary.BigEndian)
	case len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf:
		data = data[3:]
	}
	if !utf8.Valid(data) {
		return "", errors.New("session text must be UTF-8 or BOM-marked UTF-16")
	}
	return string(data), nil
}

func decodeXShellUTF16(data []byte, order binary.ByteOrder) (string, error) {
	if len(data)%2 != 0 {
		return "", errors.New("UTF-16 session has an incomplete code unit")
	}
	units := make([]uint16, len(data)/2)
	for index := range units {
		units[index] = order.Uint16(data[index*2 : index*2+2])
	}
	for index := 0; index < len(units); index++ {
		unit := units[index]
		switch {
		case unit >= 0xd800 && unit <= 0xdbff:
			if index+1 >= len(units) || units[index+1] < 0xdc00 || units[index+1] > 0xdfff {
				return "", errors.New("UTF-16 session contains an invalid surrogate pair")
			}
			index++
		case unit >= 0xdc00 && unit <= 0xdfff:
			return "", errors.New("UTF-16 session contains an invalid surrogate pair")
		}
	}
	return string(utf16.Decode(units)), nil
}

func parseNativeSessionINI(text string) (map[string]map[string]string, error) {
	sections := make(map[string]map[string]string)
	section := ""
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 4096), MaxFileBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.TrimSuffix(scanner.Text(), "\r"))
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") || len(line) < 3 {
				return nil, fmt.Errorf("Xshell 会话第 %d 行的分节标题无效", lineNumber)
			}
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			if section == "" {
				return nil, fmt.Errorf("Xshell 会话第 %d 行的分节名称为空", lineNumber)
			}
			if sections[section] == nil {
				sections[section] = make(map[string]string)
			}
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if _, allowed := nativeSessionKeys[key]; !allowed {
			continue
		}
		if section == "" {
			return nil, fmt.Errorf("Xshell 会话第 %d 行的键值对位于分节之外", lineNumber)
		}
		if sections[section] == nil {
			sections[section] = make(map[string]string)
		}
		sections[section][key] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取 Xshell 会话文本失败：%w", err)
	}
	if len(sections) == 0 {
		return nil, errors.New("文件不是受支持的 NetSarang 会话")
	}
	return sections, nil
}

func xShellHostID(kind model.HostKind, name, host string, port int, username, identity string) string {
	key := strings.Join([]string{string(kind), name, host, strconv.Itoa(port), username, identity}, "\x00")
	digest := sha256.Sum256([]byte(key))
	return "xsh-" + hex.EncodeToString(digest[:12])
}
