package model

import "testing"

func TestDefaultConfigSupportsASCIIFileSendChord(t *testing.T) {
	config := DefaultConfig()
	if got := config.Workspace.Bindings["send_ascii_file"]; got != "ctrl+f" {
		t.Fatalf("send_ascii_file binding = %q, want ctrl+f", got)
	}
	if got := config.Workspace.Bindings["xymodem"]; got != "!" {
		t.Fatalf("xymodem binding = %q, want !", got)
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("default config failed validation: %v", err)
	}
}

func TestLoggingRetentionDefaultsAndValidation(t *testing.T) {
	defaults := DefaultConfig()
	if defaults.Logging.MaxTotalBytes != 1<<30 || defaults.Logging.MaxAgeDays != 90 {
		t.Fatalf("default logging retention = %#v, want 1 GiB and 90 days", defaults.Logging)
	}
	if err := defaults.Validate(); err != nil {
		t.Fatalf("default logging retention failed validation: %v", err)
	}

	unlimited := DefaultConfig()
	unlimited.Logging.MaxTotalBytes = 0
	unlimited.Logging.MaxAgeDays = 0
	if err := unlimited.Validate(); err != nil {
		t.Fatalf("disabled global retention limits failed validation: %v", err)
	}

	for _, test := range []struct {
		name   string
		update func(*LoggingConfig)
	}{
		{name: "negative capacity", update: func(config *LoggingConfig) { config.MaxTotalBytes = -1 }},
		{name: "capacity above maximum", update: func(config *LoggingConfig) { config.MaxTotalBytes = 100<<30 + 1 }},
		{name: "negative age", update: func(config *LoggingConfig) { config.MaxAgeDays = -1 }},
		{name: "age above maximum", update: func(config *LoggingConfig) { config.MaxAgeDays = 3651 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultConfig()
			test.update(&config.Logging)
			if err := config.Validate(); err == nil {
				t.Fatal("invalid logging retention setting was accepted")
			}
		})
	}
}

func TestWorkspaceStatusBarItemsDefaultNormalizeAndValidate(t *testing.T) {
	defaults := DefaultWorkspaceConfig()
	if len(defaults.StatusBarItems) == 0 || defaults.StatusBarItems[0] != "leader" {
		t.Fatalf("default status bar items = %#v, want the leader item first", defaults.StatusBarItems)
	}
	configured := (WorkspaceConfig{StatusBarItems: []string{" HELP ", "Quick_Command"}}).WithDefaults()
	if len(configured.StatusBarItems) != 2 || configured.StatusBarItems[0] != "help" || configured.StatusBarItems[1] != "quick_command" {
		t.Fatalf("status bar item normalization = %#v", configured.StatusBarItems)
	}
	if err := configured.Validate(); err != nil {
		t.Fatalf("valid custom status bar layout failed validation: %v", err)
	}

	empty := (WorkspaceConfig{StatusBarItems: []string{}}).WithDefaults()
	if len(empty.StatusBarItems) != 0 {
		t.Fatalf("explicitly empty status bar layout was replaced by defaults: %#v", empty.StatusBarItems)
	}
	if got := (WorkspaceConfig{}).WithDefaults().StatusBarItems; len(got) == 0 {
		t.Fatal("omitted status bar layout did not receive defaults")
	}

	for _, items := range [][]string{{"unknown"}, {"help", "help"}} {
		if err := (WorkspaceConfig{StatusBarItems: items}).Validate(); err == nil {
			t.Fatalf("invalid status bar layout %#v was accepted", items)
		}
	}
}

func TestSecurityAutoLockConfigurationValidates(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("default security configuration failed validation: %v", err)
	}
	config := DefaultConfig()
	config.Security.AutoLockMinutes = 1440
	if err := config.Validate(); err != nil {
		t.Fatalf("maximum auto-lock duration failed validation: %v", err)
	}
	for _, minutes := range []int{-1, 1441} {
		config = DefaultConfig()
		config.Security.AutoLockMinutes = minutes
		if err := config.Validate(); err == nil {
			t.Fatalf("invalid auto-lock duration %d was accepted", minutes)
		}
	}
}

func TestTerminalBellConfigurationDefaultsAndValidates(t *testing.T) {
	if got := DefaultConfig().Terminal.Bell; got != TerminalBellVisual {
		t.Fatalf("default terminal bell = %q, want %q", got, TerminalBellVisual)
	}
	if err := (TerminalConfig{Bell: " OFF "}).Validate(); err != nil {
		t.Fatalf("off bell mode failed validation: %v", err)
	}
	if err := (TerminalConfig{Bell: "beep"}).Validate(); err == nil {
		t.Fatal("unsupported bell mode was accepted")
	}
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("default config failed validation: %v", err)
	}
}

func TestTerminalKeyboardConfigDefaultsOverridesAndValidation(t *testing.T) {
	defaults := DefaultTerminalConfig().Keyboard
	if defaults.BackspaceSequence != "\x7f" || defaults.DeleteSequence != "\x1b[3~" || defaults.MetaKeyEmulation != TerminalMetaEscape {
		t.Fatalf("default keyboard mapping = %#v", defaults)
	}
	configured := (TerminalKeyboardConfig{
		FunctionKeys:     map[string]string{" F13 ": "\x1b[99~"},
		MetaKeyEmulation: " OFF ",
	}).WithDefaults()
	if configured.FunctionKeys["f13"] != "\x1b[99~" || configured.FunctionKeys[" F13 "] != "" {
		t.Fatalf("function key names were not normalized: %#v", configured.FunctionKeys)
	}
	if configured.BackspaceSequence != "\x7f" || configured.DeleteSequence != "\x1b[3~" || configured.MetaKeyEmulation != TerminalMetaOff {
		t.Fatalf("keyboard defaults were not preserved: %#v", configured)
	}
	if err := configured.Validate(); err != nil {
		t.Fatalf("valid keyboard mapping failed validation: %v", err)
	}
}

func TestTerminalKeyboardConfigRejectsInvalidMappings(t *testing.T) {
	tests := []struct {
		name   string
		config TerminalKeyboardConfig
	}{
		{name: "unsupported function key", config: TerminalKeyboardConfig{FunctionKeys: map[string]string{"f21": "sequence"}}},
		{name: "duplicate normalized key", config: TerminalKeyboardConfig{FunctionKeys: map[string]string{"f1": "one", " F1 ": "two"}}},
		{name: "sequence too long", config: TerminalKeyboardConfig{FunctionKeys: map[string]string{"f1": string(make([]byte, TerminalKeySequenceMaxBytes+1))}}},
		{name: "invalid meta mode", config: TerminalKeyboardConfig{MetaKeyEmulation: "prefix"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.config.Validate(); err == nil {
				t.Fatal("invalid keyboard mapping was accepted")
			}
		})
	}
}

func TestTerminalDimensionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  TerminalConfig
		wantErr bool
	}{
		{name: "automatic size", config: TerminalConfig{}},
		{name: "fixed size", config: TerminalConfig{Columns: 160, Rows: 48}},
		{name: "minimum size", config: TerminalConfig{Columns: TerminalMinColumns, Rows: TerminalMinRows}},
		{name: "columns too small", config: TerminalConfig{Columns: TerminalMinColumns - 1}, wantErr: true},
		{name: "columns too large", config: TerminalConfig{Columns: TerminalMaxColumns + 1}, wantErr: true},
		{name: "rows too small", config: TerminalConfig{Rows: TerminalMinRows - 1}, wantErr: true},
		{name: "rows too large", config: TerminalConfig{Rows: TerminalMaxRows + 1}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestTerminalHighlightRulesValidate(t *testing.T) {
	valid := TerminalHighlightRule{
		Name: "errors", Pattern: `\b(error|failed)\b`, IgnoreCase: true,
		Foreground: "#FFFFFF", Background: "#7F1D1D", Bold: true,
	}
	if err := (TerminalConfig{Highlights: []TerminalHighlightRule{valid}}).Validate(); err != nil {
		t.Fatalf("valid highlight rule failed validation: %v", err)
	}
	tests := []struct {
		name      string
		rules     []TerminalHighlightRule
		wantError bool
	}{
		{name: "unnamed", rules: []TerminalHighlightRule{{Pattern: "error", Foreground: "#FFFFFF"}}, wantError: true},
		{name: "empty pattern", rules: []TerminalHighlightRule{{Name: "empty", Foreground: "#FFFFFF"}}, wantError: true},
		{name: "invalid regex", rules: []TerminalHighlightRule{{Name: "invalid", Pattern: "(", Foreground: "#FFFFFF"}}, wantError: true},
		{name: "zero width regex", rules: []TerminalHighlightRule{{Name: "zero", Pattern: `a*`, Foreground: "#FFFFFF"}}, wantError: true},
		{name: "invalid color", rules: []TerminalHighlightRule{{Name: "color", Pattern: "error", Foreground: "red"}}, wantError: true},
		{name: "style required", rules: []TerminalHighlightRule{{Name: "unstyled", Pattern: "error"}}, wantError: true},
		{name: "duplicate names", rules: []TerminalHighlightRule{
			{Name: "same", Pattern: "error", Foreground: "#FFFFFF"},
			{Name: "same", Pattern: "warn", Foreground: "#FFFF00"},
		}, wantError: true},
		{name: "too many rules", rules: make([]TerminalHighlightRule, TerminalHighlightMaxRules+1), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (TerminalConfig{Highlights: test.rules}).Validate()
			if (err != nil) != test.wantError {
				t.Fatalf("Validate() error = %v, wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestTerminalTriggerRulesValidate(t *testing.T) {
	valid := []TerminalTriggerRule{
		{Name: "confirm", Pattern: `(?i)continue\?`, Action: TerminalTriggerSend, Text: "yes", SendEnter: true},
		{Name: "warning", Pattern: `\bwarning\b`, Action: TerminalTriggerMessage, Text: "远端报告警告"},
		{Name: "failed", Pattern: `\bfailed\b`, Action: TerminalTriggerBell},
		{Name: "login", Pattern: `authentication required`, Action: TerminalTriggerScript, ScriptPath: "./scripts/login.json"},
	}
	if err := (TerminalConfig{Triggers: valid}).Validate(); err != nil {
		t.Fatalf("valid trigger rules failed validation: %v", err)
	}
	tests := []struct {
		name  string
		rules []TerminalTriggerRule
	}{
		{name: "unnamed", rules: []TerminalTriggerRule{{Pattern: "error", Action: TerminalTriggerBell}}},
		{name: "invalid regex", rules: []TerminalTriggerRule{{Name: "invalid", Pattern: "(", Action: TerminalTriggerBell}}},
		{name: "zero width regex", rules: []TerminalTriggerRule{{Name: "empty", Pattern: `a*`, Action: TerminalTriggerBell}}},
		{name: "unknown action", rules: []TerminalTriggerRule{{Name: "unknown", Pattern: "error", Action: "launch"}}},
		{name: "send requires text", rules: []TerminalTriggerRule{{Name: "send", Pattern: "prompt", Action: TerminalTriggerSend}}},
		{name: "send rejects control characters", rules: []TerminalTriggerRule{{Name: "send", Pattern: "prompt", Action: TerminalTriggerSend, Text: "yes\r"}}},
		{name: "message rejects send enter", rules: []TerminalTriggerRule{{Name: "message", Pattern: "error", Action: TerminalTriggerMessage, Text: "error", SendEnter: true}}},
		{name: "bell rejects text", rules: []TerminalTriggerRule{{Name: "bell", Pattern: "error", Action: TerminalTriggerBell, Text: "beep"}}},
		{name: "script requires path", rules: []TerminalTriggerRule{{Name: "script", Pattern: "prompt", Action: TerminalTriggerScript}}},
		{name: "script rejects text", rules: []TerminalTriggerRule{{Name: "script", Pattern: "prompt", Action: TerminalTriggerScript, ScriptPath: "script.json", Text: "yes"}}},
		{name: "script rejects control characters in path", rules: []TerminalTriggerRule{{Name: "script", Pattern: "prompt", Action: TerminalTriggerScript, ScriptPath: "script\n.json"}}},
		{name: "duplicate names", rules: []TerminalTriggerRule{
			{Name: "same", Pattern: "first", Action: TerminalTriggerBell},
			{Name: "same", Pattern: "second", Action: TerminalTriggerBell},
		}},
		{name: "too many rules", rules: make([]TerminalTriggerRule, TerminalTriggerMaxRules+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := (TerminalConfig{Triggers: test.rules}).Validate(); err == nil {
				t.Fatal("invalid trigger rules were accepted")
			}
		})
	}
}

func TestSSHProxyConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		proxy   SSHProxyConfig
		wantErr bool
	}{
		{name: "disabled default", proxy: SSHProxyConfig{Type: SSHProxyNone}},
		{name: "HTTP", proxy: SSHProxyConfig{Type: SSHProxyHTTP, Address: "proxy.example.test:3128"}},
		{name: "SOCKS4", proxy: SSHProxyConfig{Type: SSHProxySOCKS4, Address: "127.0.0.1:1080", Username: "user-id"}},
		{name: "SOCKS4 without user ID", proxy: SSHProxyConfig{Type: SSHProxySOCKS4, Address: "127.0.0.1:1080"}},
		{name: "SOCKS5 with environment credential", proxy: SSHProxyConfig{
			Type: SSHProxySOCKS5, Address: "[::1]:1080", Username: "proxy-user", PasswordEnv: "WORKBENCH_PROXY_PASSWORD",
		}},
		{name: "SOCKS4 password rejected", proxy: SSHProxyConfig{Type: SSHProxySOCKS4, Address: "127.0.0.1:1080", PasswordEnv: "PROXY_PASSWORD"}, wantErr: true},
		{name: "unsupported type", proxy: SSHProxyConfig{Type: "socks6", Address: "127.0.0.1:1080"}, wantErr: true},
		{name: "invalid address", proxy: SSHProxyConfig{Type: SSHProxyHTTP, Address: "http://proxy.example.test:3128"}, wantErr: true},
		{name: "userinfo is not an address", proxy: SSHProxyConfig{Type: SSHProxyHTTP, Address: "user:pass@proxy.example.test:3128"}, wantErr: true},
		{name: "incomplete authentication", proxy: SSHProxyConfig{Type: SSHProxyHTTP, Address: "proxy.example.test:3128", Username: "user"}, wantErr: true},
		{name: "HTTP username delimiter", proxy: SSHProxyConfig{Type: SSHProxyHTTP, Address: "proxy.example.test:3128", Username: "user:extra", PasswordEnv: "PROXY_PASSWORD"}, wantErr: true},
		{name: "invalid environment variable", proxy: SSHProxyConfig{Type: SSHProxyHTTP, Address: "proxy.example.test:3128", Username: "user", PasswordEnv: "BAD-NAME"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.proxy.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}
