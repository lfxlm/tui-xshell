package ui

import (
	"fmt"
	"net"
	"net/url"
	"os/user"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/model"
)

const maxQuickConnectAddressRunes = 512

func (m *modelState) openQuickConnect() {
	m.quickConnect = &quickConnectState{}
}

func (m *modelState) updateQuickConnectKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.quickConnect
	if state == nil {
		return m, nil
	}
	switch msg.String() {
	case "esc", "escape", "ctrl+c":
		m.quickConnect = nil
		return m, nil
	case "enter":
		host, err := parseQuickConnectAddress(state.address)
		if err != nil {
			state.error = err.Error()
			return m, nil
		}
		if (host.Kind == model.HostKindSSH || host.TerminalProtocol == model.TerminalProtocolRlogin) && host.User == "" {
			currentUser, err := user.Current()
			if err != nil || currentUser == nil || strings.TrimSpace(currentUser.Username) == "" {
				state.error = "地址没有用户名，且无法读取当前系统用户名；请使用 user@host 格式。"
				return m, nil
			}
			host.User = currentUser.Username
			host.Name = quickConnectDisplayName(host)
		}
		if host.Kind == model.HostKindFTP && host.User == "" {
			host.User = "anonymous"
			host.Name = quickConnectDisplayName(host)
		}
		m.quickConnect = nil
		m.statusText = "正在快速连接 " + host.Name + "…"
		m.statusErr = false
		target := m.newTabTarget()
		if host.Kind == model.HostKindSSH && host.FileProtocol != "" {
			target = m.newSFTPTarget()
		}
		return m, m.startHostConnect(host, target)
	case "backspace", "ctrl+h":
		text := []rune(state.address)
		if len(text) > 0 {
			state.address = string(text[:len(text)-1])
		}
		state.error = ""
	case "ctrl+u":
		state.address = ""
		state.error = ""
	default:
		text := msg.Key().Text
		if text != "" && len([]rune(state.address))+len([]rune(text)) <= maxQuickConnectAddressRunes {
			state.address += text
			state.error = ""
		}
	}
	return m, nil
}

func (m modelState) quickConnectView() string {
	state := m.quickConnect
	if state == nil {
		return ""
	}
	lines := []string{
		"输入连接地址。未指定协议时按 SSH 终端连接；sftp/scp、FTP/FTPS 地址会打开双栏文件面板。",
		"",
		selectedStyle.Render(state.address + "▏"),
		"",
		mutedStyle.Render("示例：dev@example.com:2222   ssh://user@host   telnet://host:23   rlogin://user@host"),
		mutedStyle.Render("SSH 文件面板：sftp://user@host   scp://user@host"),
		mutedStyle.Render("FTP：ftp://user@host   FTPS：ftps://user@host   隐式 FTPS：ftps+implicit://user@host"),
		warningStyle.Render("ftp:// 使用明文传输账号与文件；公网连接请选择 FTPS。未写用户名时使用 anonymous。"),
		mutedStyle.Render("临时连接不会保存到主机目录；需要复用时请按 n 添加主机。"),
	}
	if state.error != "" {
		lines = append(lines, "", warningStyle.Render("! "+state.error))
	}
	lines = append(lines, "", mutedStyle.Render("Enter 连接   ·   Ctrl+U 清空   ·   Esc 取消"))
	return m.modalView("快速连接", lines, 90)
}

// parseQuickConnectAddress accepts SSH user@host[:port] and SSH, Telnet,
// Rlogin, FTP and FTPS URIs. Passwords in URLs are rejected to avoid exposing
// secrets in terminal history or process state.
func parseQuickConnectAddress(value string) (model.Host, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return model.Host{}, fmt.Errorf("请输入连接地址")
	}
	kind, port := model.HostKindSSH, 22
	tlsMode := ""
	var fileProtocol model.SSHFileProtocol
	var terminalProtocol model.TerminalProtocol
	var hostname, username string
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return model.Host{}, fmt.Errorf("地址格式无效：%w", err)
		}
		switch strings.ToLower(parsed.Scheme) {
		case "ssh":
			kind, port = model.HostKindSSH, 22
		case "sftp":
			kind, port, fileProtocol = model.HostKindSSH, 22, model.SSHFileProtocolSFTP
		case "scp":
			kind, port, fileProtocol = model.HostKindSSH, 22, model.SSHFileProtocolSCP
		case "telnet":
			kind, port = model.HostKindTelnet, 23
		case "rlogin":
			kind, port = model.HostKindTelnet, 513
		case "ftp":
			kind, port, tlsMode = model.HostKindFTP, 21, model.FTPTLSOff
		case "ftps":
			kind, port, tlsMode = model.HostKindFTP, 21, model.FTPTLSExplicit
		case "ftps+implicit":
			kind, port, tlsMode = model.HostKindFTP, 990, model.FTPTLSImplicit
		default:
			return model.Host{}, fmt.Errorf("快速连接暂不支持 %q 协议", parsed.Scheme)
		}
		if strings.EqualFold(parsed.Scheme, "telnet") {
			terminalProtocol = model.TerminalProtocolTelnet
		} else if strings.EqualFold(parsed.Scheme, "rlogin") {
			terminalProtocol = model.TerminalProtocolRlogin
		}
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword {
				return model.Host{}, fmt.Errorf("地址中不能包含密码，请使用系统钥匙串或交互式认证")
			}
			username = parsed.User.Username()
		}
		if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return model.Host{}, fmt.Errorf("快速连接地址不能包含路径、查询参数或片段")
		}
		hostname = parsed.Hostname()
		if parsed.Port() != "" {
			port, err = parseQuickConnectPort(parsed.Port())
			if err != nil {
				return model.Host{}, err
			}
		}
		if parsed.Host == "" || parsed.Hostname() == "" {
			return model.Host{}, fmt.Errorf("地址缺少主机名")
		}
	} else {
		hostPart := value
		if strings.Count(value, "@") > 1 {
			return model.Host{}, fmt.Errorf("SSH 地址中只能包含一个 @")
		}
		if at := strings.IndexByte(value, '@'); at >= 0 {
			username, hostPart = value[:at], value[at+1:]
			if username == "" {
				return model.Host{}, fmt.Errorf("@ 前需要填写 SSH 用户名")
			}
		}
		if strings.HasPrefix(hostPart, "[") {
			if parsedHost, parsedPort, err := net.SplitHostPort(hostPart); err == nil {
				hostname = parsedHost
				port, err = parseQuickConnectPort(parsedPort)
				if err != nil {
					return model.Host{}, err
				}
			} else if strings.HasSuffix(hostPart, "]") {
				hostname = strings.TrimSuffix(strings.TrimPrefix(hostPart, "["), "]")
			} else {
				return model.Host{}, fmt.Errorf("IPv6 地址请使用 [地址] 或 [地址]:端口 格式")
			}
		} else if strings.Count(hostPart, ":") == 1 {
			parsedHost, parsedPort, err := net.SplitHostPort(hostPart)
			if err != nil {
				return model.Host{}, fmt.Errorf("地址格式无效：%w", err)
			}
			hostname = parsedHost
			port, err = parseQuickConnectPort(parsedPort)
			if err != nil {
				return model.Host{}, err
			}
		} else if strings.Contains(hostPart, ":") {
			return model.Host{}, fmt.Errorf("IPv6 地址请使用方括号，例如 [2001:db8::1]:22")
		} else {
			hostname = hostPart
		}
	}
	if err := validateQuickConnectPart("主机名", hostname); err != nil {
		return model.Host{}, err
	}
	if username != "" {
		if err := validateQuickConnectPart("用户名", username); err != nil {
			return model.Host{}, err
		}
	}
	host := model.Host{
		Kind: kind, IP: hostname, Port: port, User: username, TLSMode: tlsMode,
		FileProtocol: fileProtocol, TerminalProtocol: terminalProtocol,
	}
	host.Name = quickConnectDisplayName(host)
	return host, nil
}

func parseQuickConnectPort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("端口必须是 1 到 65535 之间的整数")
	}
	return port, nil
}

func validateQuickConnectPart(label, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s不能为空", label)
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) || character == '/' || character == '\\' {
			return fmt.Errorf("%s包含无效字符", label)
		}
	}
	return nil
}

func quickConnectDisplayName(host model.Host) string {
	address := host.IP
	defaultPort := 22
	if host.Kind == model.HostKindFTP {
		defaultPort = 21
		if host.TLSMode == model.FTPTLSImplicit {
			defaultPort = 990
		}
	} else if host.Kind == model.HostKindTelnet {
		defaultPort = 23
		if host.TerminalProtocol == model.TerminalProtocolRlogin {
			defaultPort = 513
		}
	}
	if host.Port != defaultPort {
		address = net.JoinHostPort(host.IP, strconv.Itoa(host.Port))
	} else if strings.Contains(host.IP, ":") {
		address = "[" + host.IP + "]"
	}
	if host.User != "" {
		return host.User + "@" + address
	}
	return address
}
