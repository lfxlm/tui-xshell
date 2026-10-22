package ui

import (
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestParseQuickConnectAddress(t *testing.T) {
	tests := []struct {
		name             string
		addr             string
		kind             model.HostKind
		ip               string
		port             int
		user             string
		tlsMode          string
		fileProtocol     model.SSHFileProtocol
		terminalProtocol model.TerminalProtocol
	}{
		{name: "ssh user and custom port", addr: "dev@example.com:2222", kind: model.HostKindSSH, ip: "example.com", port: 2222, user: "dev"},
		{name: "ssh URI", addr: "ssh://ops@example.com", kind: model.HostKindSSH, ip: "example.com", port: 22, user: "ops"},
		{name: "SFTP URI", addr: "sftp://ops@example.com", kind: model.HostKindSSH, ip: "example.com", port: 22, user: "ops", fileProtocol: model.SSHFileProtocolSFTP},
		{name: "SCP URI", addr: "scp://ops@example.com", kind: model.HostKindSSH, ip: "example.com", port: 22, user: "ops", fileProtocol: model.SSHFileProtocolSCP},
		{name: "telnet URI", addr: "telnet://example.com:2323", kind: model.HostKindTelnet, ip: "example.com", port: 2323, terminalProtocol: model.TerminalProtocolTelnet},
		{name: "rlogin URI", addr: "rlogin://ops@example.com", kind: model.HostKindTelnet, ip: "example.com", port: 513, user: "ops", terminalProtocol: model.TerminalProtocolRlogin},
		{name: "plain FTP URI", addr: "ftp://ops@example.com", kind: model.HostKindFTP, ip: "example.com", port: 21, user: "ops", tlsMode: model.FTPTLSOff},
		{name: "explicit FTPS URI", addr: "ftps://ops@example.com", kind: model.HostKindFTP, ip: "example.com", port: 21, user: "ops", tlsMode: model.FTPTLSExplicit},
		{name: "implicit FTPS URI", addr: "ftps+implicit://ops@example.com", kind: model.HostKindFTP, ip: "example.com", port: 990, user: "ops", tlsMode: model.FTPTLSImplicit},
		{name: "bracketed IPv6", addr: "dev@[2001:db8::1]:2200", kind: model.HostKindSSH, ip: "2001:db8::1", port: 2200, user: "dev"},
		{name: "bracketed IPv6 without custom port", addr: "[2001:db8::1]", kind: model.HostKindSSH, ip: "2001:db8::1", port: 22},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host, err := parseQuickConnectAddress(test.addr)
			if err != nil {
				t.Fatalf("parse address: %v", err)
			}
			if host.Kind != test.kind || host.IP != test.ip || host.Port != test.port || host.User != test.user || host.TLSMode != test.tlsMode || host.FileProtocol != test.fileProtocol || host.TerminalProtocol != test.terminalProtocol {
				t.Fatalf("parsed host = %#v", host)
			}
			if host.Id != "" {
				t.Fatalf("quick connection should be transient, id=%q", host.Id)
			}
		})
	}
}

func TestParseQuickConnectAddressRejectsUnsafeOrUnsupportedInput(t *testing.T) {
	for _, address := range []string{
		"",
		"ftp://user:secret@example.com",
		"ssh://user:secret@example.com",
		"ssh://example.com/path",
		"ftps+implicit://example.com/path",
		"dev@example.com:0",
		"dev@example.com:70000",
		"user@2001:db8::1",
		"@example.com",
	} {
		t.Run(address, func(t *testing.T) {
			if _, err := parseQuickConnectAddress(address); err == nil {
				t.Fatalf("invalid address %q was accepted", address)
			}
		})
	}
}

func TestQuickConnectFTPStartsTransientFileConnection(t *testing.T) {
	state := modelState{width: 100, height: 30}
	state.openQuickConnect()
	updated, _ := state.updateQuickConnectKey(keyPress("ftps+implicit://ops@example.com"))
	state = unwrapModel(t, updated)
	updated, command := state.updateQuickConnectKey(keyPress("enter"))
	state = unwrapModel(t, updated)
	if command != nil || state.quickConnect != nil || !state.ftpPasswordPrompt {
		t.Fatalf("quick connect did not open FTP credentials prompt: command=%v quick=%#v prompt=%v", command != nil, state.quickConnect, state.ftpPasswordPrompt)
	}
	host := state.ftpPendingHost
	if host == nil || host.Id != "" || host.Kind != model.HostKindFTP || host.TLSMode != model.FTPTLSImplicit || host.Port != 990 || host.User != "ops" || host.Password != "" || host.CredentialRef != "" {
		t.Fatalf("FTP quick connect host is not transient or has incorrect settings: %#v", host)
	}
	if state.ftpPendingTarget == nil || state.ftpPendingTarget.kind != standaloneSFTPTarget {
		t.Fatalf("FTP quick connect target = %#v; want independent dual-pane file workspace", state.ftpPendingTarget)
	}
}

func TestQuickConnectSFTPAndSCPOpenFileWorkspace(t *testing.T) {
	tests := []struct {
		address  string
		protocol model.SSHFileProtocol
	}{
		{address: "sftp://ops@example.com", protocol: model.SSHFileProtocolSFTP},
		{address: "scp://ops@example.com", protocol: model.SSHFileProtocolSCP},
	}
	for _, test := range tests {
		t.Run(string(test.protocol), func(t *testing.T) {
			state := modelState{width: 100, height: 30}
			state.openQuickConnect()
			updated, _ := state.updateQuickConnectKey(keyPress(test.address))
			state = unwrapModel(t, updated)
			updated, command := state.updateQuickConnectKey(keyPress("enter"))
			state = unwrapModel(t, updated)
			if command == nil || state.connectingHost == nil || state.connectingHost.Id != "" || state.connectingHost.FileProtocol != test.protocol {
				t.Fatalf("quick connect did not begin transient %s connection: command=%v host=%#v", test.protocol, command != nil, state.connectingHost)
			}
			if state.connectionTarget == nil || state.connectionTarget.kind != standaloneSFTPTarget {
				t.Fatalf("%s quick connect target = %#v; want dual-pane file workspace", test.protocol, state.connectionTarget)
			}
		})
	}
}

func TestQuickConnectKeyOpensEphemeralConnection(t *testing.T) {
	state := modelState{width: 100, height: 30}
	state = updateModelWithKey(t, state, "a")
	if state.quickConnect == nil {
		t.Fatal("a did not open quick-connect dialog")
	}
	for _, key := range []string{"d", "e", "v", "@", "example.com", ":2222"} {
		updated, _ := state.updateQuickConnectKey(keyPress(key))
		state = unwrapModel(t, updated)
	}
	updated, command := state.updateQuickConnectKey(keyPress("enter"))
	state = unwrapModel(t, updated)
	if command == nil || state.quickConnect != nil || state.connectingHost == nil {
		t.Fatalf("quick connect did not begin: command=%v state=%#v", command != nil, state.quickConnect)
	}
	if state.connectingHost.Id != "" || state.connectingHost.Kind != model.HostKindSSH ||
		state.connectingHost.IP != "example.com" || state.connectingHost.Port != 2222 || state.connectingHost.User != "dev" {
		t.Fatalf("quick connect did not create the expected transient host: %#v", state.connectingHost)
	}
}
