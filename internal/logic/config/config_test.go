package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gogf/gf/v2/os/gcmd"
	"tui-ssh-workbench/internal/model"
)

func TestLoadConfigAppliesDefaultsAndOverridesSSHSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ssh]\nconnect_timeout_seconds = 42\nkeepalive_interval_seconds = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.SSH.ConnectTimeoutSeconds != 42 || config.SSH.KeepaliveIntervalSeconds != 0 || config.SSH.KeepaliveTimeoutSeconds != 10 {
		t.Fatalf("loaded SSH configuration = %#v", config.SSH)
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("loaded configuration is invalid: %v", err)
	}
}

func TestLoadConfigReadsSSHProxyWithoutPersistingProxyPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "[ssh.proxy]\ntype = 'socks5'\naddress = '127.0.0.1:1080'\nusername = 'proxy-user'\npassword_env = 'WORKBENCH_PROXY_PASSWORD'\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.SSH.Proxy.Type != model.SSHProxySOCKS5 || config.SSH.Proxy.Address != "127.0.0.1:1080" || config.SSH.Proxy.Username != "proxy-user" || config.SSH.Proxy.PasswordEnv != "WORKBENCH_PROXY_PASSWORD" {
		t.Fatalf("loaded SSH proxy = %#v", config.SSH.Proxy)
	}
}

func TestLoadConfigReadsSOCKS4ProxyUserID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "[ssh.proxy]\ntype = 'socks4'\naddress = '127.0.0.1:1080'\nusername = 'workbench-user'\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("loaded SOCKS4 configuration is invalid: %v", err)
	}
	if config.SSH.Proxy.Type != model.SSHProxySOCKS4 || config.SSH.Proxy.Username != "workbench-user" || config.SSH.Proxy.PasswordEnv != "" {
		t.Fatalf("loaded SSH proxy = %#v", config.SSH.Proxy)
	}
}

func TestLoadConfigAppliesLogRotationAndRedactionSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "[logging]\nsegment_bytes = 4096\nretained_segments = 3\nmax_total_bytes = 2097152\nmax_age_days = 14\nredact_patterns = ['token=[A-Z0-9]{1,32}']\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Logging.SegmentBytes != 4096 || config.Logging.RetainedSegments != 3 || config.Logging.MaxTotalBytes != 2097152 || config.Logging.MaxAgeDays != 14 || len(config.Logging.RedactPatterns) != 1 {
		t.Fatalf("loaded logging configuration = %#v", config.Logging)
	}
}

func TestLoadConfigEnablesDesktopNotificationsOnlyWhenRequested(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[notifications]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Notifications.Enabled {
		t.Fatal("desktop notifications were not enabled from config")
	}
	if model.DefaultConfig().Notifications.Enabled {
		t.Fatal("desktop notifications should be opt-in by default")
	}
}

func TestLoadConfigReadsScreenLockIdleTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[security]\nauto_lock_minutes = 15\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Security.AutoLockMinutes != 15 {
		t.Fatalf("auto-lock duration = %d, want 15", config.Security.AutoLockMinutes)
	}
}

func TestLoadConfigReadsTerminalBellMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[terminal]\nbell = 'off'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Terminal.Bell != model.TerminalBellOff {
		t.Fatalf("loaded terminal bell = %q, want %q", config.Terminal.Bell, model.TerminalBellOff)
	}
}

func TestLoadConfigReadsFixedTerminalDimensions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[terminal]\ncolumns = 160\nrows = 48\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Terminal.Columns != 160 || config.Terminal.Rows != 48 {
		t.Fatalf("loaded terminal size = %dx%d, want 160x48", config.Terminal.Columns, config.Terminal.Rows)
	}
}

func TestLoadConfigReadsTerminalKeyboardMappings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := `[terminal.keyboard]
backspace_sequence = "\u0015"
delete_sequence = "\u001b[3~"
meta_key_emulation = "off"

[terminal.keyboard.function_keys]
f1 = "\u001b[99~"
f13 = "custom-f13"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	keyboard := config.Terminal.Keyboard
	if keyboard.BackspaceSequence != "\x15" || keyboard.DeleteSequence != "\x1b[3~" || keyboard.MetaKeyEmulation != model.TerminalMetaOff {
		t.Fatalf("loaded terminal keyboard config = %#v", keyboard)
	}
	if keyboard.FunctionKeys["f1"] != "\x1b[99~" || keyboard.FunctionKeys["f13"] != "custom-f13" {
		t.Fatalf("loaded function key overrides = %#v", keyboard.FunctionKeys)
	}
}

func TestLoadTerminalHighlightRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := `[terminal]
[[terminal.highlights]]
name = "errors"
pattern = "\\b(error|failed)\\b"
ignore_case = true
foreground = "#FFFFFF"
background = "#7F1D1D"
bold = true

[[terminal.highlights]]
name = "warning"
pattern = "WARN"
foreground = "#FFD166"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Terminal.Highlights) != 2 {
		t.Fatalf("loaded terminal highlight rules = %#v, want two", config.Terminal.Highlights)
	}
	first := config.Terminal.Highlights[0]
	if first.Name != "errors" || first.Pattern != `\b(error|failed)\b` || !first.IgnoreCase || first.Foreground != "#FFFFFF" || first.Background != "#7F1D1D" || !first.Bold {
		t.Fatalf("loaded first highlight rule = %#v", first)
	}
}

func TestLoadTerminalTriggerRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := `[terminal]
[[terminal.triggers]]
name = "confirm"
pattern = "(?i)continue\\?"
action = "send"
text = "yes"
send_enter = true

[[terminal.triggers]]
name = "warning"
pattern = "\\bwarning\\b"
action = "message"
text = "远端报告警告"

[[terminal.triggers]]
name = "login-step"
pattern = "Authentication required"
action = "script"
script_path = "./scripts/login.json"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Terminal.Triggers) != 3 {
		t.Fatalf("loaded terminal triggers = %#v, want three rules", config.Terminal.Triggers)
	}
	first := config.Terminal.Triggers[0]
	if first.Name != "confirm" || first.Pattern != `(?i)continue\?` || first.Action != model.TerminalTriggerSend || first.Text != "yes" || !first.SendEnter {
		t.Fatalf("loaded send trigger = %#v", first)
	}
	third := config.Terminal.Triggers[2]
	if third.Action != model.TerminalTriggerScript || third.ScriptPath != "./scripts/login.json" {
		t.Fatalf("loaded script trigger = %#v", third)
	}
}

func TestLoadRejectsInvalidTerminalHighlightRules(t *testing.T) {
	for _, contents := range []string{
		"[[terminal.highlights]]\nname='bad'\npattern='('\\nforeground='#FFFFFF'\n",
		"[[terminal.highlights]]\nname='empty'\npattern='a*'\nforeground='#FFFFFF'\n",
		"[[terminal.highlights]]\nname='color'\npattern='error'\nforeground='red'\n",
	} {
		t.Run(contents, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid terminal highlight rule was accepted")
			}
		})
	}
}

func TestLoadAppearanceSchemeAndColorOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "[appearance]\nscheme = 'nord'\n\n[appearance.colors]\naccent = '#FF00AA'\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Appearance.Scheme != model.AppearanceNord || config.Appearance.Colors.Accent != "#FF00AA" {
		t.Fatalf("loaded appearance = %#v", config.Appearance)
	}
	if config.Appearance.Colors.Canvas != "" {
		t.Fatalf("unspecified color override should inherit the scheme: %#v", config.Appearance.Colors)
	}
}

func TestLoadRejectsUnknownAppearanceSchemeAndMalformedColor(t *testing.T) {
	for _, contents := range []string{
		"[appearance]\nscheme = 'unknown'\n",
		"[appearance.colors]\naccent = 'teal'\n",
	} {
		t.Run(contents, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid appearance configuration was accepted")
			}
		})
	}
}

func TestLoadWorkspaceLeaderAndBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "[workspace]\nleader = 'ctrl+u'\nrestore_last_on_startup = true\nstatus_bar_items = ['help', 'quick_command']\n\n[workspace.bindings]\nsplit_vertical = 'j'\nquick_command = 'k'\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Workspace.Leader != "ctrl+u" || !config.Workspace.RestoreLastOnStartup || config.Workspace.Bindings["split_vertical"] != "j" || config.Workspace.Bindings["quick_command"] != "k" {
		t.Fatalf("workspace keymap = %#v", config.Workspace)
	}
	if config.Workspace.Bindings["split_horizontal"] != "h" || config.Workspace.Bindings["tab_next"] != "n" {
		t.Fatalf("overriding a few bindings removed defaults: %#v", config.Workspace.Bindings)
	}
	if len(config.Workspace.StatusBarItems) != 2 || config.Workspace.StatusBarItems[0] != "help" || config.Workspace.StatusBarItems[1] != "quick_command" {
		t.Fatalf("workspace status bar items = %#v", config.Workspace.StatusBarItems)
	}
}

func TestLoadWorkspaceStatusBarItemsCanBeHidden(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[workspace]\nstatus_bar_items = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Workspace.StatusBarItems) != 0 {
		t.Fatalf("explicitly empty status bar item list became %#v", config.Workspace.StatusBarItems)
	}
}

func TestLoadRejectsConflictingWorkspaceBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "[workspace.bindings]\nsplit_vertical = 'j'\nquick_command = 'j'\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("duplicate workspace key bindings were accepted")
	}
}

func TestLoadRejectsReservedWorkspaceLeader(t *testing.T) {
	for _, leader := range []string{"ctrl+o", "ctrl+c", "ctrl+f", "ctrl+s"} {
		t.Run(leader, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			contents := "[workspace]\nleader = '" + leader + "'\n"
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("reserved %s leader was accepted", leader)
			}
		})
	}
}

func TestLoadConfigRejectsInvalidSSHTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ssh]\nconnect_timeout_seconds = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("invalid connect timeout was accepted")
	}
}

func TestLoadConfigRejectsUnboundedLogRedactionPattern(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[logging]\nredact_patterns = ['secret=.*']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unbounded log redaction pattern was accepted")
	}
}

func TestDefaultConfigMatchesSSHClientOptions(t *testing.T) {
	config := model.DefaultConfig()
	if config.SSH.ConnectTimeoutSeconds != 15 ||
		config.SSH.KeepaliveIntervalSeconds != 30 || config.SSH.KeepaliveTimeoutSeconds != 10 {
		t.Fatalf("default config = %#v", config.SSH)
	}
}

func TestConfigManagerLoadsExplicitConfigPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh.toml")
	if err := os.WriteFile(path, []byte("[ssh]\nconnect_timeout_seconds = 27\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parser, err := gcmd.ParseArgs([]string{"workbench", "--config", path}, map[string]bool{"config": true})
	if err != nil {
		t.Fatal(err)
	}
	manager := &sConfigManager{}
	if err := manager.ParseConfig(context.Background(), parser); err != nil {
		t.Fatal(err)
	}
	if got := manager.Config().SSH.ConnectTimeoutSeconds; got != 27 {
		t.Fatalf("configured connect timeout = %d, want 27", got)
	}
}
