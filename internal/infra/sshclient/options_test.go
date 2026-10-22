package sshclient

import (
	"testing"
	"time"

	"tui-ssh-workbench/internal/model"
)

func TestOptionsFromSSHConfigLoadsProxyPasswordFromEnvironment(t *testing.T) {
	t.Setenv("WORKBENCH_PROXY_PASSWORD", "transient-secret")
	options := OptionsFromSSHConfig(model.SSHConfig{
		ConnectTimeoutSeconds:    19,
		KeepaliveIntervalSeconds: 0,
		KeepaliveTimeoutSeconds:  7,
		Proxy: model.SSHProxyConfig{
			Type: model.SSHProxyHTTP, Address: "proxy.example.test:3128",
			Username: "proxy-user", PasswordEnv: "WORKBENCH_PROXY_PASSWORD",
		},
	})
	if options.ConnectTimeout != 19*time.Second || options.KeepaliveInterval != 0 || options.KeepaliveTimeout != 7*time.Second {
		t.Fatalf("connection options = %#v", options)
	}
	if options.Proxy.Type != ProxyTypeHTTP || options.Proxy.Address != "proxy.example.test:3128" || options.Proxy.Username != "proxy-user" || options.Proxy.Password != "" || options.Proxy.PasswordEnv != "WORKBENCH_PROXY_PASSWORD" {
		t.Fatalf("proxy options = %#v", options.Proxy)
	}
	resolved, err := options.Proxy.withResolvedPassword()
	if err != nil || resolved.Password != "transient-secret" || resolved.PasswordEnv != "" {
		t.Fatalf("resolve proxy password = %#v, err=%v", resolved, err)
	}
	if err := resolved.validate(); err != nil {
		t.Fatalf("resolved proxy credentials failed validation: %v", err)
	}
}

func TestOptionsFromSSHConfigMapsSOCKS4UserID(t *testing.T) {
	options := OptionsFromSSHConfig(model.SSHConfig{Proxy: model.SSHProxyConfig{
		Type: model.SSHProxySOCKS4, Address: "127.0.0.1:1080", Username: "workbench-user",
	}})
	if options.Proxy.Type != ProxyTypeSOCKS4 || options.Proxy.Address != "127.0.0.1:1080" || options.Proxy.Username != "workbench-user" {
		t.Fatalf("SOCKS4 proxy options = %#v", options.Proxy)
	}
	if err := options.Proxy.validate(); err != nil {
		t.Fatalf("SOCKS4 proxy options failed validation: %v", err)
	}
}
