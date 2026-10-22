package sessionfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestDecodeXFTPImportsSFTPAndDropsCredentials(t *testing.T) {
	contents := `[SessionInfo]
Version=8.0
SessionName=production-sftp
Description=from Xftp

[Connection]
Protocol=SFTP
Host=sftp.example.test
Port=2202

[Connection:Authentication]
UserName=deploy
Password=must-not-import
`
	catalog, err := DecodeXFTP(encodeUTF16LE(contents), "prod.xfp")
	if err != nil {
		t.Fatalf("decode Xftp SFTP session: %v", err)
	}
	host := catalog.HostRecords()[0]
	if host.Kind != model.HostKindSSH || host.Name != "production-sftp" || host.IP != "sftp.example.test" ||
		host.Port != 2202 || host.User != "deploy" || host.Description != "from Xftp" {
		t.Fatalf("imported Xftp SFTP host = %#v", host)
	}
	if host.Password != "" || host.CredentialRef != "" || host.Startup != "" {
		t.Fatalf("Xftp import retained credentials or executable settings: %#v", host)
	}
	text, err := decodeXShellText(encodeXFTP(host, host.Name))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Password=") || strings.Contains(text, "must-not-import") {
		t.Fatalf("portable session retained a password: %s", text)
	}
}

func TestDecodeXFTPImportsSCPAsAnSSHFileProtocol(t *testing.T) {
	contents := `[SessionInfo]
SessionName=legacy-files
[Connection]
Protocol=SCP
Host=scp.example.test
Port=2222
[Connection:Authentication]
UserName=deploy
Password=must-not-import
`
	catalog, err := DecodeXFTP(encodeUTF16LE(contents), "legacy.xfp")
	if err != nil {
		t.Fatalf("decode Xftp SCP session: %v", err)
	}
	host := catalog.HostRecords()[0]
	if host.Kind != model.HostKindSSH || host.FileProtocol != model.SSHFileProtocolSCP || host.Port != 2222 {
		t.Fatalf("imported Xftp SCP session = %#v", host)
	}
	if host.Password != "" || host.CredentialRef != "" {
		t.Fatalf("Xftp SCP import retained a credential: %#v", host)
	}
	text, err := decodeXShellText(encodeXFTP(host, host.Name))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Protocol=SCP") || strings.Contains(text, "must-not-import") {
		t.Fatalf("SCP session did not round-trip safely: %s", text)
	}
}

func TestReadFileImportsXFTPAndDirectoryImportsMixedNativeSessions(t *testing.T) {
	root := t.TempDir()
	xftpPath := filepath.Join(root, "remote.xfp")
	contents := "[SessionInfo]\nSessionName=plain-ftp\n[Connection]\nProtocol=FTP\nHost=ftp.example.test\n"
	if err := os.WriteFile(xftpPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	ftpCatalog, err := ReadFile(xftpPath)
	if err != nil {
		t.Fatalf("read Xftp .xfp file: %v", err)
	}
	ftp := ftpCatalog.HostRecords()[0]
	if ftp.Kind != model.HostKindFTP || ftp.Port != 21 || ftp.Name != "plain-ftp" || ftp.TLSMode != model.FTPTLSOff {
		t.Fatalf("imported FTP session = %#v", ftp)
	}

	xshPath := filepath.Join(root, "ssh", "edge.xsh")
	if err := os.MkdirAll(filepath.Dir(xshPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xshPath, []byte("[SessionInfo]\nSessionName=edge\n[CONNECTION]\nProtocol=SSH\nHost=ssh.example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mixed, err := ReadFile(root)
	if err != nil {
		t.Fatalf("read mixed native session directory: %v", err)
	}
	if len(mixed.Groups) != 1 || len(mixed.Hosts) != 2 {
		t.Fatalf("mixed session groups/hosts = %d/%d", len(mixed.Groups), len(mixed.Hosts))
	}
	byAddress := make(map[string]model.Host)
	for _, host := range mixed.HostRecords() {
		byAddress[host.IP] = host
	}
	if byAddress["ftp.example.test"].Kind != model.HostKindFTP || byAddress["ssh.example.test"].GroupId == "" {
		t.Fatalf("mixed native sessions = %#v", byAddress)
	}
}

func TestWriteXFTPDirectoryExportsAndRoundTripsSupportedSessions(t *testing.T) {
	groups := []model.Group{{Id: "group", Name: "Files"}}
	hosts := []model.Host{
		{Id: "sftp", Kind: model.HostKindSSH, Name: "secure-files", IP: "sftp.example.test", Port: 2202, User: "deploy", GroupId: "group"},
		{Id: "scp", Kind: model.HostKindSSH, FileProtocol: model.SSHFileProtocolSCP, Name: "legacy-files", IP: "scp.example.test", Port: 22, User: "deploy", GroupId: "group"},
		{Id: "ftp", Kind: model.HostKindFTP, Name: "plain-files", IP: "ftp.example.test", Port: 21, User: "anonymous", TLSMode: model.FTPTLSOff, GroupId: "group"},
		{Id: "ftps", Kind: model.HostKindFTP, Name: "protected-files", IP: "ftps.example.test", Port: 21, TLSMode: model.FTPTLSExplicit},
		{Id: "telnet", Kind: model.HostKindTelnet, Name: "console", IP: "console.example.test", Port: 23},
	}
	dest := filepath.Join(t.TempDir(), "xftp-sessions")
	result, err := WriteXFTPDirectory(dest, groups, hosts)
	if err != nil {
		t.Fatalf("export Xftp sessions: %v", err)
	}
	if result.Exported != 3 || len(result.Skipped) != 2 {
		t.Fatalf("Xftp export result = %#v, want 3 exported and 2 skipped", result)
	}
	for _, path := range []string{
		filepath.Join(dest, "Files", "secure-files.xfp"),
		filepath.Join(dest, "Files", "legacy-files.xfp"),
		filepath.Join(dest, "Files", "plain-files.xfp"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("exported Xftp session %q missing: %v", path, err)
		}
	}
	imported, err := ReadFile(dest)
	if err != nil {
		t.Fatalf("import exported Xftp directory: %v", err)
	}
	if len(imported.Groups) != 1 || len(imported.Hosts) != 3 {
		t.Fatalf("Xftp round-trip groups/hosts = %d/%d", len(imported.Groups), len(imported.Hosts))
	}
	byAddress := make(map[string]model.Host)
	for _, host := range imported.HostRecords() {
		byAddress[host.IP] = host
	}
	if byAddress["sftp.example.test"].Kind != model.HostKindSSH ||
		byAddress["scp.example.test"].FileProtocol != model.SSHFileProtocolSCP ||
		byAddress["ftp.example.test"].Kind != model.HostKindFTP ||
		byAddress["ftp.example.test"].TLSMode != model.FTPTLSOff {
		t.Fatalf("Xftp round trip mapped protocols incorrectly: %#v", byAddress)
	}
	if _, err := WriteXFTPDirectory(dest, groups, hosts); err == nil {
		t.Fatal("Xftp export unexpectedly overwrote an existing directory")
	}
}

func TestWriteXFTPDirectorySkipsActiveFTPUntilNativeFieldIsVerified(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "xftp-active")
	result, err := WriteXFTPDirectory(destination, nil, []model.Host{{
		Id: "active-ftp", Kind: model.HostKindFTP, Name: "active", IP: "ftp.example.test", Port: 21, FTPActiveMode: true,
	}})
	if err != nil {
		t.Fatalf("export Xftp sessions: %v", err)
	}
	if result.Exported != 0 || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0], "主动数据连接") {
		t.Fatalf("active FTP export result = %#v; want explicit lossless skip", result)
	}
}

func TestDecodeXFTPRequiresExplicitSupportedProtocol(t *testing.T) {
	for name, contents := range map[string]string{
		"missing protocol": "[Connection]\nHost=example.test\n",
		"unknown protocol": "[Connection]\nProtocol=FTPS\nHost=example.test\n",
		"missing host":     "[Connection]\nProtocol=SFTP\nPort=22\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeXFTP([]byte(contents), "invalid.xfp"); err == nil {
				t.Fatal("invalid or unsupported Xftp session was accepted")
			}
		})
	}
}
