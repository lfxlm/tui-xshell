package sshclient

import (
	"time"

	"tui-ssh-workbench/internal/model"
)

// OptionsFromSSHConfig maps user settings into transport settings. Proxy
// passwords are read from the environment only for the lifetime of a connect
// attempt; callers must not persist the resulting ConnectOptions.
func OptionsFromSSHConfig(config model.SSHConfig) ConnectOptions {
	config = (model.Config{SSH: config}).WithDefaults().SSH
	proxy := config.Proxy.WithDefaults()
	return ConnectOptions{
		ConnectTimeout:    time.Duration(config.ConnectTimeoutSeconds) * time.Second,
		KeepaliveInterval: time.Duration(config.KeepaliveIntervalSeconds) * time.Second,
		KeepaliveTimeout:  time.Duration(config.KeepaliveTimeoutSeconds) * time.Second,
		Proxy: ProxyOptions{
			Type: proxy.Type, Address: proxy.Address, Username: proxy.Username,
			PasswordEnv: proxy.PasswordEnv,
		},
	}
}
