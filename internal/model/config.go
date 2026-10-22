package model

import (
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Config struct {
	SSH           SSHConfig           `toml:"ssh"`
	Terminal      TerminalConfig      `toml:"terminal"`
	Logging       LoggingConfig       `toml:"logging"`
	Security      SecurityConfig      `toml:"security"`
	Workspace     WorkspaceConfig     `toml:"workspace"`
	Notifications NotificationsConfig `toml:"notifications"`
	Appearance    AppearanceConfig    `toml:"appearance"`
}

const (
	AppearanceDefault      = "default"
	AppearanceNord         = "nord"
	AppearanceDracula      = "dracula"
	AppearanceSolarized    = "solarized-dark"
	AppearanceHighContrast = "high-contrast"
	AppearanceCatppuccin   = "catppuccin-mocha"
	AppearanceTokyoNight   = "tokyo-night"
	AppearanceGruvbox      = "gruvbox-dark"
	AppearanceOneDark      = "one-dark"
)

const (
	TerminalBellOff              = "off"
	TerminalBellVisual           = "visual"
	TerminalMetaEscape           = "escape"
	TerminalMetaOff              = "off"
	TerminalMinColumns           = 20
	TerminalMaxColumns           = 1000
	TerminalMinRows              = 5
	TerminalMaxRows              = 500
	TerminalKeySequenceMaxBytes  = 256
	TerminalHighlightMaxRules    = 32
	TerminalHighlightMaxPattern  = 512
	TerminalTriggerMaxRules      = 32
	TerminalTriggerMaxPattern    = 512
	TerminalTriggerMaxText       = 1024
	TerminalTriggerMaxScriptPath = 1024
)

// TerminalConfig controls terminal behavior provided by the TUI itself.
type TerminalConfig struct {
	Bell       string                  `toml:"bell"`
	Columns    int                     `toml:"columns"`
	Rows       int                     `toml:"rows"`
	Keyboard   TerminalKeyboardConfig  `toml:"keyboard"`
	Highlights []TerminalHighlightRule `toml:"highlights"`
	Triggers   []TerminalTriggerRule   `toml:"triggers"`
}

// TerminalHighlightRule styles text matched on one rendered terminal row.
// Rules are applied in order; later rules override only the colors they set.
type TerminalHighlightRule struct {
	Name       string `toml:"name"`
	Pattern    string `toml:"pattern"`
	IgnoreCase bool   `toml:"ignore_case"`
	Foreground string `toml:"foreground"`
	Background string `toml:"background"`
	Bold       bool   `toml:"bold"`
}

// TerminalTriggerRule runs a small TUI-safe action when output matches a
// pattern. Actions that require a desktop window or external process are not
// supported by the terminal interface.
type TerminalTriggerRule struct {
	Name       string `toml:"name"`
	Pattern    string `toml:"pattern"`
	IgnoreCase bool   `toml:"ignore_case"`
	Action     string `toml:"action"`
	Text       string `toml:"text"`
	SendEnter  bool   `toml:"send_enter"`
	ScriptPath string `toml:"script_path"`
}

const (
	TerminalTriggerSend    = "send"
	TerminalTriggerMessage = "message"
	TerminalTriggerBell    = "bell"
	TerminalTriggerScript  = "script"
)

func DefaultTerminalConfig() TerminalConfig {
	return TerminalConfig{Bell: TerminalBellVisual, Keyboard: DefaultTerminalKeyboardConfig()}
}

// TerminalKeyboardConfig controls terminal key sequences sent to remote hosts.
// FunctionKeys overrides only the named keys; omitted keys keep xterm defaults.
type TerminalKeyboardConfig struct {
	FunctionKeys      map[string]string `toml:"function_keys"`
	BackspaceSequence string            `toml:"backspace_sequence"`
	DeleteSequence    string            `toml:"delete_sequence"`
	MetaKeyEmulation  string            `toml:"meta_key_emulation"`
}

func DefaultTerminalKeyboardConfig() TerminalKeyboardConfig {
	return TerminalKeyboardConfig{
		FunctionKeys:      map[string]string{},
		BackspaceSequence: "\x7f",
		DeleteSequence:    "\x1b[3~",
		MetaKeyEmulation:  TerminalMetaEscape,
	}
}

func (c TerminalKeyboardConfig) WithDefaults() TerminalKeyboardConfig {
	defaults := DefaultTerminalKeyboardConfig()
	if c.BackspaceSequence == "" {
		c.BackspaceSequence = defaults.BackspaceSequence
	}
	if c.DeleteSequence == "" {
		c.DeleteSequence = defaults.DeleteSequence
	}
	if strings.TrimSpace(c.MetaKeyEmulation) == "" {
		c.MetaKeyEmulation = defaults.MetaKeyEmulation
	} else {
		c.MetaKeyEmulation = strings.ToLower(strings.TrimSpace(c.MetaKeyEmulation))
	}
	functionKeys := make(map[string]string, len(c.FunctionKeys))
	for name, sequence := range c.FunctionKeys {
		functionKeys[strings.ToLower(strings.TrimSpace(name))] = sequence
	}
	c.FunctionKeys = functionKeys
	return c
}

func (c TerminalKeyboardConfig) Validate() error {
	mode := strings.ToLower(strings.TrimSpace(c.MetaKeyEmulation))
	if mode == "" {
		mode = TerminalMetaEscape
	}
	if mode != TerminalMetaEscape && mode != TerminalMetaOff {
		return fmt.Errorf("terminal.keyboard.meta_key_emulation 必须是 %q 或 %q", TerminalMetaEscape, TerminalMetaOff)
	}
	for name, sequence := range map[string]string{
		"backspace_sequence": c.BackspaceSequence,
		"delete_sequence":    c.DeleteSequence,
	} {
		if len(sequence) > TerminalKeySequenceMaxBytes || !utf8.ValidString(sequence) {
			return fmt.Errorf("terminal.keyboard.%s 必须是有效 UTF-8 且不超过 %d 字节", name, TerminalKeySequenceMaxBytes)
		}
	}
	seen := make(map[string]string, len(c.FunctionKeys))
	for name, sequence := range c.FunctionKeys {
		canonical := strings.ToLower(strings.TrimSpace(name))
		if !validTerminalFunctionKey(canonical) {
			return fmt.Errorf("terminal.keyboard.function_keys 中的按键 %q 无效；仅支持 f1 到 f20", name)
		}
		if previous, exists := seen[canonical]; exists {
			return fmt.Errorf("terminal.keyboard.function_keys 中的 %q 和 %q 指向同一个按键", previous, name)
		}
		seen[canonical] = name
		if len(sequence) > TerminalKeySequenceMaxBytes || !utf8.ValidString(sequence) {
			return fmt.Errorf("terminal.keyboard.function_keys.%s 必须是有效 UTF-8 且不超过 %d 字节", canonical, TerminalKeySequenceMaxBytes)
		}
	}
	return nil
}

func validTerminalFunctionKey(name string) bool {
	if len(name) < 2 || name[0] != 'f' {
		return false
	}
	number, err := strconv.Atoi(name[1:])
	return err == nil && number >= 1 && number <= 20
}

func (c TerminalConfig) WithDefaults() TerminalConfig {
	if strings.TrimSpace(c.Bell) == "" {
		c.Bell = TerminalBellVisual
	} else {
		c.Bell = strings.ToLower(strings.TrimSpace(c.Bell))
	}
	c.Keyboard = c.Keyboard.WithDefaults()
	return c
}

func (c TerminalConfig) Validate() error {
	if err := c.Keyboard.Validate(); err != nil {
		return err
	}
	c = c.WithDefaults()
	switch c.Bell {
	case TerminalBellOff, TerminalBellVisual:
	default:
		return fmt.Errorf("terminal.bell 必须是 %q 或 %q", TerminalBellOff, TerminalBellVisual)
	}
	if c.Columns != 0 && (c.Columns < TerminalMinColumns || c.Columns > TerminalMaxColumns) {
		return fmt.Errorf("terminal.columns 必须为 0（跟随窗口）或在 %d 到 %d 之间", TerminalMinColumns, TerminalMaxColumns)
	}
	if c.Rows != 0 && (c.Rows < TerminalMinRows || c.Rows > TerminalMaxRows) {
		return fmt.Errorf("terminal.rows 必须为 0（跟随窗口）或在 %d 到 %d 之间", TerminalMinRows, TerminalMaxRows)
	}
	if len(c.Highlights) > TerminalHighlightMaxRules {
		return fmt.Errorf("terminal.highlights 最多支持 %d 条规则", TerminalHighlightMaxRules)
	}
	seenNames := make(map[string]struct{}, len(c.Highlights))
	for index, rule := range c.Highlights {
		prefix := fmt.Sprintf("terminal.highlights[%d]", index)
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			return fmt.Errorf("%s.name 不能为空", prefix)
		}
		if _, exists := seenNames[name]; exists {
			return fmt.Errorf("terminal.highlights 中规则名称 %q 重复", name)
		}
		seenNames[name] = struct{}{}
		if strings.TrimSpace(rule.Pattern) == "" {
			return fmt.Errorf("%s.pattern 不能为空", prefix)
		}
		if len(rule.Pattern) > TerminalHighlightMaxPattern {
			return fmt.Errorf("%s.pattern 不能超过 %d 字节", prefix, TerminalHighlightMaxPattern)
		}
		pattern := rule.Pattern
		if rule.IgnoreCase {
			pattern = "(?i)" + pattern
		}
		expression, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("%s.pattern 正则无效：%w", prefix, err)
		}
		if expression.MatchString("") {
			return fmt.Errorf("%s.pattern 不能匹配空文本", prefix)
		}
		if rule.Foreground == "" && rule.Background == "" && !rule.Bold {
			return fmt.Errorf("%s 至少需要设置 foreground、background 或 bold", prefix)
		}
		for _, field := range []struct{ name, value string }{
			{name: "foreground", value: rule.Foreground},
			{name: "background", value: rule.Background},
		} {
			if field.value == "" {
				continue
			}
			if len(field.value) != 7 || field.value[0] != '#' {
				return fmt.Errorf("%s.%s 必须使用 #RRGGBB 格式", prefix, field.name)
			}
			if _, err := hex.DecodeString(field.value[1:]); err != nil {
				return fmt.Errorf("%s.%s 必须使用 #RRGGBB 格式", prefix, field.name)
			}
		}
	}
	if len(c.Triggers) > TerminalTriggerMaxRules {
		return fmt.Errorf("terminal.triggers 最多支持 %d 条规则", TerminalTriggerMaxRules)
	}
	seenTriggerNames := make(map[string]struct{}, len(c.Triggers))
	for index, rule := range c.Triggers {
		prefix := fmt.Sprintf("terminal.triggers[%d]", index)
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			return fmt.Errorf("%s.name 不能为空", prefix)
		}
		if _, exists := seenTriggerNames[name]; exists {
			return fmt.Errorf("terminal.triggers 中规则名称 %q 重复", name)
		}
		seenTriggerNames[name] = struct{}{}
		if strings.TrimSpace(rule.Pattern) == "" {
			return fmt.Errorf("%s.pattern 不能为空", prefix)
		}
		if len(rule.Pattern) > TerminalTriggerMaxPattern {
			return fmt.Errorf("%s.pattern 不能超过 %d 字节", prefix, TerminalTriggerMaxPattern)
		}
		pattern := rule.Pattern
		if rule.IgnoreCase {
			pattern = "(?i)" + pattern
		}
		expression, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("%s.pattern 正则无效：%w", prefix, err)
		}
		if expression.MatchString("") {
			return fmt.Errorf("%s.pattern 不能匹配空文本", prefix)
		}
		switch rule.Action {
		case TerminalTriggerSend:
			if rule.ScriptPath != "" {
				return fmt.Errorf("%s.send 动作不接受 script_path", prefix)
			}
			if rule.Text == "" || len([]rune(rule.Text)) > TerminalTriggerMaxText || containsTriggerControlCharacter(rule.Text) {
				return fmt.Errorf("%s.text 必须填写、不超过 %d 个字符且不能包含控制字符", prefix, TerminalTriggerMaxText)
			}
		case TerminalTriggerMessage:
			if rule.ScriptPath != "" {
				return fmt.Errorf("%s.message 动作不接受 script_path", prefix)
			}
			if rule.Text == "" || len([]rune(rule.Text)) > TerminalTriggerMaxText || containsTriggerControlCharacter(rule.Text) {
				return fmt.Errorf("%s.text 必须填写、不超过 %d 个字符且不能包含控制字符", prefix, TerminalTriggerMaxText)
			}
			if rule.SendEnter {
				return fmt.Errorf("%s.send_enter 只适用于 send 动作", prefix)
			}
		case TerminalTriggerBell:
			if rule.Text != "" || rule.SendEnter || rule.ScriptPath != "" {
				return fmt.Errorf("%s.bell 动作不接受 text、send_enter 或 script_path", prefix)
			}
		case TerminalTriggerScript:
			if strings.TrimSpace(rule.ScriptPath) == "" || len(rule.ScriptPath) > TerminalTriggerMaxScriptPath || containsTriggerControlCharacter(rule.ScriptPath) || rule.Text != "" || rule.SendEnter {
				return fmt.Errorf("%s.script_path 必须填写、不超过 %d 字节且不能包含控制字符；script 动作不接受 text 或 send_enter", prefix, TerminalTriggerMaxScriptPath)
			}
		default:
			return fmt.Errorf("%s.action 必须是 %q、%q、%q 或 %q", prefix, TerminalTriggerSend, TerminalTriggerMessage, TerminalTriggerBell, TerminalTriggerScript)
		}
	}
	return nil
}

func containsTriggerControlCharacter(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

// AppearanceConfig selects a built-in TUI palette and allows per-color
// overrides without changing terminal-emulator ANSI colors.
type AppearanceConfig struct {
	Scheme string                `toml:"scheme"`
	Colors AppearanceColorConfig `toml:"colors"`
}

type AppearanceColorConfig struct {
	Canvas    string `toml:"canvas"`
	Selected  string `toml:"selected"`
	Line      string `toml:"line"`
	Primary   string `toml:"primary"`
	Secondary string `toml:"secondary"`
	Muted     string `toml:"muted"`
	Accent    string `toml:"accent"`
	Warning   string `toml:"warning"`
}

func (c AppearanceConfig) WithDefaults() AppearanceConfig {
	if strings.TrimSpace(c.Scheme) == "" {
		c.Scheme = AppearanceDefault
	} else {
		c.Scheme = strings.ToLower(strings.TrimSpace(c.Scheme))
	}
	return c
}

func (c AppearanceConfig) Validate() error {
	c = c.WithDefaults()
	switch c.Scheme {
	case AppearanceDefault, AppearanceNord, AppearanceDracula, AppearanceSolarized, AppearanceHighContrast,
		AppearanceCatppuccin, AppearanceTokyoNight, AppearanceGruvbox, AppearanceOneDark:
	default:
		return fmt.Errorf("未知的外观主题 %q；可选值：%s、%s、%s、%s、%s、%s、%s、%s、%s", c.Scheme,
			AppearanceDefault, AppearanceNord, AppearanceDracula, AppearanceSolarized, AppearanceHighContrast,
			AppearanceCatppuccin, AppearanceTokyoNight, AppearanceGruvbox, AppearanceOneDark)
	}
	for name, value := range map[string]string{
		"canvas": c.Colors.Canvas, "selected": c.Colors.Selected, "line": c.Colors.Line,
		"primary": c.Colors.Primary, "secondary": c.Colors.Secondary, "muted": c.Colors.Muted,
		"accent": c.Colors.Accent, "warning": c.Colors.Warning,
	} {
		if value == "" {
			continue
		}
		if len(value) != 7 || value[0] != '#' {
			return fmt.Errorf("appearance.colors.%s 必须使用 #RRGGBB 格式", name)
		}
		if _, err := hex.DecodeString(value[1:]); err != nil {
			return fmt.Errorf("appearance.colors.%s 必须使用 #RRGGBB 格式", name)
		}
	}
	return nil
}

// NotificationsConfig controls optional operating-system notifications.
type NotificationsConfig struct {
	Enabled bool `toml:"enabled"`
}

// SecurityConfig controls local protections for active terminal sessions.
type SecurityConfig struct {
	AutoLockMinutes int `toml:"auto_lock_minutes"`
}

// WorkspaceConfig controls application-only shortcuts in the session workspace.
// Terminal input is still passed through unless a configured leader chord is used.
type WorkspaceConfig struct {
	Leader               string            `toml:"leader"`
	RestoreLastOnStartup bool              `toml:"restore_last_on_startup"`
	ShowFooterShortcuts  bool              `toml:"show_footer_shortcuts"`
	Bindings             map[string]string `toml:"bindings"`
	StatusBarItems       []string          `toml:"status_bar_items"`
}

var defaultWorkspaceStatusBarItems = []string{
	"leader", "session", "traffic", "split", "command_history", "quick_command", "scheduled_transfers", "sftp", "help", "close_pane",
}

var defaultWorkspaceBindings = map[string]string{
	"split_vertical": "v", "split_horizontal": "h",
	"pane_previous": "left", "pane_next": "right",
	"tab_previous": "p", "tab_next": "n",
	"tab_1": "1", "tab_2": "2", "tab_3": "3", "tab_4": "4", "tab_5": "5",
	"tab_6": "6", "tab_7": "7", "tab_8": "8", "tab_9": "9",
	"close_pane": "c", "close_tab": "w", "pane_menu": "m", "tab_menu": "t",
	"tab_groups": "ctrl+t",
	"sftp":       "f", "quick_command": "q", "toggle_recording": "b", "toggle_script_recording": "ctrl+b", "view_logs": "g",
	"screen_lock": "ctrl+l", "screen_lock_password": "ctrl+p",
	"plugins": "x", "compose": "a", "port_forward": "l", "reconnect": "r",
	"auto_reconnect": "z", "sync_input": "i", "scrollback": "s", "search": "/", "home": "o", "help": "?",
	"screen_left": "[", "screen_right": "]", "screen_up": "{", "screen_down": "}",
	"pane_grow": ".", "pane_shrink": ",",
	"pane_move_next":      "ctrl+e",
	"saved_layouts":       "=",
	"clear_screen":        "u",
	"command_history":     "y",
	"external_edit":       "e",
	"scheduled_transfers": "d",
	"send_ascii_file":     "ctrl+f",
	"xymodem":             "!",
}

type LoggingConfig struct {
	SegmentBytes     int64    `toml:"segment_bytes"`
	RetainedSegments int      `toml:"retained_segments"`
	MaxTotalBytes    int64    `toml:"max_total_bytes"`
	MaxAgeDays       int      `toml:"max_age_days"`
	RedactPatterns   []string `toml:"redact_patterns"`
}

type SSHConfig struct {
	ConnectTimeoutSeconds    int            `toml:"connect_timeout_seconds"`
	KeepaliveIntervalSeconds int            `toml:"keepalive_interval_seconds"`
	KeepaliveTimeoutSeconds  int            `toml:"keepalive_timeout_seconds"`
	Proxy                    SSHProxyConfig `toml:"proxy"`
}

const (
	SSHProxyNone   = "none"
	SSHProxyHTTP   = "http"
	SSHProxySOCKS4 = "socks4"
	SSHProxySOCKS5 = "socks5"
)

// SSHProxyConfig configures an optional outbound proxy used for the first TCP
// connection in an SSH chain. Passwords are read from the named environment
// variable instead of being written to config.toml.
type SSHProxyConfig struct {
	Type        string `toml:"type"`
	Address     string `toml:"address"`
	Username    string `toml:"username"`
	PasswordEnv string `toml:"password_env"`
}

func (c SSHProxyConfig) WithDefaults() SSHProxyConfig {
	c.Type = strings.ToLower(strings.TrimSpace(c.Type))
	if c.Type == "" {
		c.Type = SSHProxyNone
	}
	c.Address = strings.TrimSpace(c.Address)
	c.Username = strings.TrimSpace(c.Username)
	c.PasswordEnv = strings.TrimSpace(c.PasswordEnv)
	return c
}

func (c SSHProxyConfig) Validate() error {
	c = c.WithDefaults()
	switch c.Type {
	case SSHProxyNone:
		if c.Address != "" || c.Username != "" || c.PasswordEnv != "" {
			return fmt.Errorf("ssh.proxy.type 为 none 时不能设置代理地址或认证字段")
		}
		return nil
	case SSHProxyHTTP, SSHProxySOCKS4, SSHProxySOCKS5:
	default:
		return fmt.Errorf("未知的 SSH 代理类型 %q；可选值：none、http、socks4、socks5", c.Type)
	}
	host, portText, err := net.SplitHostPort(c.Address)
	if err != nil || strings.TrimSpace(host) == "" || strings.ContainsAny(host, " \t\r\n/@?#") {
		return fmt.Errorf("ssh.proxy.address 必须是 host:port，例如 127.0.0.1:1080")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("ssh.proxy.address 的端口必须是 1 到 65535 之间的数字")
	}
	if c.Type == SSHProxySOCKS4 {
		if c.PasswordEnv != "" {
			return fmt.Errorf("SOCKS4 不支持密码认证；password_env 必须留空")
		}
		if len(c.Username) > 255 {
			return fmt.Errorf("SOCKS4 user_id 不能超过 255 字节")
		}
	} else if (c.Username == "") != (c.PasswordEnv == "") {
		return fmt.Errorf("SSH 代理认证需要同时设置 username 和 password_env")
	}
	if strings.ContainsAny(c.Username, "\r\n\x00") || strings.ContainsAny(c.PasswordEnv, "=\r\n\x00") {
		return fmt.Errorf("SSH 代理认证字段包含无效字符")
	}
	if c.Type == SSHProxyHTTP && strings.Contains(c.Username, ":") {
		return fmt.Errorf("HTTP 代理用户名不能包含冒号")
	}
	if c.PasswordEnv != "" && !validEnvironmentVariableName(c.PasswordEnv) {
		return fmt.Errorf("ssh.proxy.password_env 必须是有效的环境变量名称")
	}
	return nil
}

func validEnvironmentVariableName(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		valid := character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
		if index > 0 {
			valid = valid || character >= '0' && character <= '9'
		}
		if !valid {
			return false
		}
	}
	return true
}

func DefaultWorkspaceConfig() WorkspaceConfig {
	bindings := make(map[string]string, len(defaultWorkspaceBindings))
	for action, key := range defaultWorkspaceBindings {
		bindings[action] = key
	}
	return WorkspaceConfig{
		Leader: "ctrl+g", ShowFooterShortcuts: true, Bindings: bindings,
		StatusBarItems: append([]string(nil), defaultWorkspaceStatusBarItems...),
	}
}

func DefaultConfig() Config {
	return Config{SSH: SSHConfig{
		ConnectTimeoutSeconds:    15,
		KeepaliveIntervalSeconds: 30,
		KeepaliveTimeoutSeconds:  10,
		Proxy:                    SSHProxyConfig{Type: SSHProxyNone},
	}, Logging: LoggingConfig{
		SegmentBytes:     100 << 20,
		RetainedSegments: 5,
		MaxTotalBytes:    1 << 30,
		MaxAgeDays:       90,
	}, Terminal: DefaultTerminalConfig(), Workspace: DefaultWorkspaceConfig(), Appearance: AppearanceConfig{Scheme: AppearanceDefault}}
}

// WithDefaults fills omitted settings while preserving an explicit zero
// keepalive interval, which means keepalive is disabled.
func (c Config) WithDefaults() Config {
	defaults := DefaultConfig()
	c.SSH.Proxy = c.SSH.Proxy.WithDefaults()
	if c.SSH.ConnectTimeoutSeconds == 0 {
		c.SSH.ConnectTimeoutSeconds = defaults.SSH.ConnectTimeoutSeconds
		if c.SSH.KeepaliveIntervalSeconds == 0 {
			c.SSH.KeepaliveIntervalSeconds = defaults.SSH.KeepaliveIntervalSeconds
		}
	}
	if c.SSH.KeepaliveTimeoutSeconds == 0 {
		c.SSH.KeepaliveTimeoutSeconds = defaults.SSH.KeepaliveTimeoutSeconds
	}
	if c.Logging.SegmentBytes == 0 {
		c.Logging.SegmentBytes = defaults.Logging.SegmentBytes
	}
	if c.Logging.RetainedSegments == 0 {
		c.Logging.RetainedSegments = defaults.Logging.RetainedSegments
	}
	c.Workspace = c.Workspace.WithDefaults()
	c.Terminal = c.Terminal.WithDefaults()
	c.Appearance = c.Appearance.WithDefaults()
	return c
}

func (c WorkspaceConfig) WithDefaults() WorkspaceConfig {
	defaults := DefaultWorkspaceConfig()
	if strings.TrimSpace(c.Leader) == "" {
		c.Leader = defaults.Leader
	} else {
		c.Leader = strings.ToLower(strings.TrimSpace(c.Leader))
	}
	if c.Bindings == nil {
		c.Bindings = defaults.Bindings
	} else {
		bindings := make(map[string]string, len(defaults.Bindings)+len(c.Bindings))
		for action, key := range defaults.Bindings {
			bindings[action] = key
		}
		for action, key := range c.Bindings {
			bindings[strings.ToLower(strings.TrimSpace(action))] = strings.ToLower(strings.TrimSpace(key))
		}
		c.Bindings = bindings
	}
	if c.StatusBarItems == nil {
		c.StatusBarItems = append([]string(nil), defaults.StatusBarItems...)
	} else {
		items := make([]string, len(c.StatusBarItems))
		for index, item := range c.StatusBarItems {
			items[index] = strings.ToLower(strings.TrimSpace(item))
		}
		c.StatusBarItems = items
	}
	return c
}

func (c Config) Validate() error {
	if err := c.SSH.Proxy.Validate(); err != nil {
		return err
	}
	if err := c.Terminal.Validate(); err != nil {
		return err
	}
	if err := c.Appearance.Validate(); err != nil {
		return err
	}
	if c.Security.AutoLockMinutes < 0 || c.Security.AutoLockMinutes > 1440 {
		return fmt.Errorf("security.auto_lock_minutes 必须为 0（关闭）或 1 到 1440 之间")
	}
	if err := c.Workspace.WithDefaults().Validate(); err != nil {
		return err
	}
	if c.SSH.ConnectTimeoutSeconds < 1 || c.SSH.ConnectTimeoutSeconds > 600 {
		return fmt.Errorf("SSH 连接超时必须在 1 到 600 秒之间")
	}
	if c.SSH.KeepaliveIntervalSeconds < 0 || c.SSH.KeepaliveIntervalSeconds > 3600 {
		return fmt.Errorf("SSH keepalive 间隔必须在 0 到 3600 秒之间；0 表示关闭")
	}
	if c.SSH.KeepaliveTimeoutSeconds < 1 || c.SSH.KeepaliveTimeoutSeconds > 120 {
		return fmt.Errorf("SSH keepalive 响应超时必须在 1 到 120 秒之间")
	}
	if c.Logging.SegmentBytes < 1024 || c.Logging.SegmentBytes > 100<<20 {
		return fmt.Errorf("日志分段大小必须在 1 KiB 到 100 MiB 之间")
	}
	if c.Logging.RetainedSegments < 1 || c.Logging.RetainedSegments > 100 {
		return fmt.Errorf("每次日志记录保留的分段数必须在 1 到 100 之间")
	}
	if c.Logging.MaxTotalBytes < 0 || c.Logging.MaxTotalBytes > 100<<30 {
		return fmt.Errorf("日志总容量上限必须为 0（不限）或不超过 100 GiB")
	}
	if c.Logging.MaxAgeDays < 0 || c.Logging.MaxAgeDays > 3650 {
		return fmt.Errorf("日志保留天数必须为 0（不限）或不超过 3650 天")
	}
	if len(c.Logging.RedactPatterns) > 32 {
		return fmt.Errorf("日志脱敏规则最多允许 32 条")
	}
	for _, pattern := range c.Logging.RedactPatterns {
		if pattern == "" || len(pattern) > 1024 {
			return fmt.Errorf("日志脱敏规则不能为空且不能超过 1024 个字节")
		}
	}
	return nil
}

func (c WorkspaceConfig) Validate() error {
	c = c.WithDefaults()
	if !validWorkspaceLeader(c.Leader) {
		return fmt.Errorf("workspace.leader 必须是 ctrl 加一个英文字母，例如 ctrl+g")
	}
	seen := make(map[string]string, len(c.Bindings))
	for action, key := range c.Bindings {
		if _, ok := defaultWorkspaceBindings[action]; !ok {
			return fmt.Errorf("未知的工作区快捷键动作：%s", action)
		}
		if key == "" {
			continue
		}
		if key == "escape" {
			return fmt.Errorf("Esc 保留为取消键，不能绑定到工作区动作 %s", action)
		}
		if !validWorkspaceBinding(key) {
			return fmt.Errorf("工作区快捷键 %s 的值 %q 无效", action, key)
		}
		if action == "external_edit" {
			continue // This binding is used in the SFTP editor, not in the workspace.
		}
		if previous, ok := seen[key]; ok {
			return fmt.Errorf("工作区快捷键 %s 和 %s 不能绑定到同一个按键 %q", previous, action, key)
		}
		seen[key] = action
	}
	seenStatusBarItems := make(map[string]struct{}, len(c.StatusBarItems))
	for _, item := range c.StatusBarItems {
		if !validWorkspaceStatusBarItem(item) {
			return fmt.Errorf("workspace.status_bar_items 中的项目 %q 无效", item)
		}
		if _, exists := seenStatusBarItems[item]; exists {
			return fmt.Errorf("workspace.status_bar_items 中项目 %q 重复", item)
		}
		seenStatusBarItems[item] = struct{}{}
	}
	switch c.Leader {
	case "ctrl+c", "ctrl+e", "ctrl+f", "ctrl+h", "ctrl+i", "ctrl+j", "ctrl+m", "ctrl+n", "ctrl+o", "ctrl+r", "ctrl+s":
		return fmt.Errorf("workspace.leader 不能使用保留的 %s", c.Leader)
	}
	return nil
}

func validWorkspaceStatusBarItem(item string) bool {
	switch item {
	case "leader", "session", "traffic", "split", "command_history", "quick_command", "scheduled_transfers", "sftp", "help", "close_pane":
		return true
	default:
		return false
	}
}

func validWorkspaceLeader(value string) bool {
	return len(value) == len("ctrl+x") && strings.HasPrefix(value, "ctrl+") && value[len("ctrl+")] >= 'a' && value[len("ctrl+")] <= 'z'
}

func validWorkspaceBinding(value string) bool {
	switch value {
	case "left", "right", "up", "down", "escape":
		return true
	}
	if len(value) == len("ctrl+x") && strings.HasPrefix(value, "ctrl+") {
		character := value[len("ctrl+")]
		return character >= 'a' && character <= 'z'
	}
	if len(value) == 1 {
		character := value[0]
		return (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || strings.ContainsRune("/?.,;:'\"[]{}<>-_=+\\|!@#$%^&*()", rune(character))
	}
	return false
}
