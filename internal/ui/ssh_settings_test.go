package ui

import (
	"testing"
	"time"

	"tui-ssh-workbench/internal/model"
)

func TestSSHConnectOptionsMapGlobalConfig(t *testing.T) {
	config := model.DefaultConfig()
	config.SSH.ConnectTimeoutSeconds = 47
	config.SSH.KeepaliveIntervalSeconds = 0
	config.SSH.KeepaliveTimeoutSeconds = 6

	got := sshConnectOptions(config)
	if got.ConnectTimeout != 47*time.Second || got.KeepaliveInterval != 0 || got.KeepaliveTimeout != 6*time.Second {
		t.Fatalf("SSH options = %#v", got)
	}
}

func TestSSHConnectOptionsUseDefaultsForUninitializedModel(t *testing.T) {
	got := sshConnectOptions(model.Config{})
	if got.ConnectTimeout != 15*time.Second || got.KeepaliveInterval != 30*time.Second || got.KeepaliveTimeout != 10*time.Second {
		t.Fatalf("default SSH options = %#v", got)
	}
}

func TestSSHConnectOptionsIncludeConfiguredOutboundProxy(t *testing.T) {
	config := model.DefaultConfig()
	config.SSH.Proxy = model.SSHProxyConfig{
		Type: model.SSHProxySOCKS5, Address: "127.0.0.1:1080",
		Username: "proxy-user", PasswordEnv: "WORKBENCH_PROXY_PASSWORD",
	}
	got := sshConnectOptions(config)
	if got.Proxy.Type != "socks5" || got.Proxy.Address != "127.0.0.1:1080" || got.Proxy.Username != "proxy-user" || got.Proxy.PasswordEnv != "WORKBENCH_PROXY_PASSWORD" || got.Proxy.Password != "" {
		t.Fatalf("configured SSH proxy options = %#v", got.Proxy)
	}
}
