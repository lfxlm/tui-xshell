package sessionfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestCatalogRoundTripExcludesMachineCredentials(t *testing.T) {
	catalog := NewCatalog(
		[]model.Group{{Id: "group-1", Name: "Production", LocalStartDir: "/Users/operator/Downloads", RemoteStartDir: "/srv/releases"}},
		[]model.Host{{
			Id: "host-1", Kind: model.HostKindSSH, Name: "edge", IP: "192.0.2.10",
			FileProtocol:          model.SSHFileProtocolSCP,
			TerminalEncoding:      "GB18030",
			TerminalInputEncoding: model.TerminalEncodingUTF8,
			X11Forwarding:         true,
			GroupId:               "group-1", Port: 2222, User: "operator", Password: "password-secret",
			IdentityFile:  "/Users/operator/.ssh/private-key-secret-path",
			CredentialRef: "keychain-secret", Startup: "login-script-secret", StartupScriptPath: "/private/scripts/login-secret-path.json", Favorite: true, FavoriteOrder: 7,
			LocalStartDir: "/Users/operator/Downloads", RemoteStartDir: "/srv/releases",
			ProxyJumpList: []string{"host-gateway"},
		}, {
			Id: "ftp-1", Kind: model.HostKindFTP, Name: "archive", IP: "files.example.test",
			Port: 990, TLSMode: model.FTPTLSImplicit, FTPActiveMode: true, Password: "ftp-password-secret",
		}, {
			Id: "telnet-1", Kind: model.HostKindTelnet, Name: "console", IP: "192.0.2.44",
			Port: 513, User: "operator", TerminalProtocol: model.TerminalProtocolRlogin,
		}, {
			Id: "serial-1", Kind: model.HostKindSerial, Name: "router-console", IP: "/dev/ttyUSB0",
			SerialBaudRate: 115200, SerialDataBits: 7, SerialParity: model.SerialParityEven, SerialStopBits: model.SerialStopBitsTwo, SerialFlowControl: model.SerialFlowControlRTSCTS,
		}},
		[]model.PortForwardRule{{
			Id: "rule-1", HostId: "host-1", Kind: model.PortForwardSOCKS5,
			ListenAddress: "127.0.0.1:1080",
		}},
	)

	data, err := catalog.Encode()
	if err != nil {
		t.Fatalf("encode catalog: %v", err)
	}
	for _, secret := range []string{"password-secret", "ftp-password-secret", "keychain-secret", "credential_ref", "private-key-secret-path", "identity_file", "login-script-secret", "startup", "login-secret-path"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("catalog contains machine credential data %q", secret)
		}
	}

	decoded, err := Decode(data)
	if err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if decoded.Format != Format || decoded.Version != Version || len(decoded.Groups) != 1 || len(decoded.Hosts) != 4 || len(decoded.PortForwardRules) != 1 {
		t.Fatalf("decoded catalog = %#v", decoded)
	}
	host := decoded.HostRecords()[0]
	if host.Password != "" || host.CredentialRef != "" || host.IdentityFile != "" || host.FavoriteOrder != 0 || host.ProxyJumpList[0] != "host-gateway" || !host.Favorite || host.FileProtocol != model.SSHFileProtocolSCP || !host.X11Forwarding || host.TerminalEncoding != "GB18030" || host.TerminalInputEncoding != model.TerminalEncodingUTF8 {
		t.Fatalf("portable host fields = %#v", host)
	}
	if strings.Contains(string(data), "favorite_order") {
		t.Fatal("portable catalog includes machine-local favorite shortcut order")
	}
	if strings.Contains(string(data), "last_connected_at") {
		t.Fatal("portable catalog includes machine-local recent connection metadata")
	}
	if strings.Contains(string(data), "/Users/operator/Downloads") {
		t.Fatal("portable catalog includes a machine-local starting directory")
	}
	if decoded.Groups[0].RemoteStartDir != "/srv/releases" || decoded.Groups[0].LocalStartDir != "" {
		t.Fatalf("portable group start directories = local %q, remote %q", decoded.Groups[0].LocalStartDir, decoded.Groups[0].RemoteStartDir)
	}
	if host.RemoteStartDir != "/srv/releases" {
		t.Fatalf("portable remote starting directory = %q, want /srv/releases", host.RemoteStartDir)
	}
	ftpHost := decoded.HostRecords()[1]
	if ftpHost.TLSMode != model.FTPTLSImplicit || !ftpHost.FTPActiveMode || ftpHost.Password != "" {
		t.Fatalf("portable FTP host fields = %#v", ftpHost)
	}
	telnetHost := decoded.HostRecords()[2]
	if telnetHost.Kind != model.HostKindTelnet || telnetHost.TerminalProtocol != model.TerminalProtocolRlogin || telnetHost.User != "operator" || telnetHost.Password != "" {
		t.Fatalf("portable Rlogin host fields = %#v", telnetHost)
	}
	serialHost := decoded.HostRecords()[3]
	if serialHost.Kind != model.HostKindSerial || serialHost.IP != "/dev/ttyUSB0" || serialHost.SerialBaudRate != 115200 ||
		serialHost.SerialDataBits != 7 || serialHost.SerialParity != model.SerialParityEven || serialHost.SerialStopBits != model.SerialStopBitsTwo || serialHost.SerialFlowControl != model.SerialFlowControlRTSCTS {
		t.Fatalf("portable serial host fields = %#v", serialHost)
	}
}

func TestCatalogDecodeRejectsUnsupportedAndTrailingData(t *testing.T) {
	valid, err := NewCatalog(nil, nil, nil).Encode()
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"wrong format":    []byte(`{"format":"other","version":1}`),
		"wrong version":   []byte(`{"format":"` + Format + `","version":99}`),
		"unknown field":   []byte(`{"format":"` + Format + `","version":1,"hosts":[],"groups":[],"port_forward_rules":[],"password":"x"}`),
		"multiple values": append(append([]byte(nil), valid...), []byte(`{}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(data); err == nil {
				t.Fatal("Decode unexpectedly accepted invalid catalog data")
			}
		})
	}
}

func TestCatalogVersionEightRemainsReadableWithoutInputEncoding(t *testing.T) {
	data := []byte(`{"format":"` + Format + `","version":8,"groups":[],"hosts":[{"id":"host-1","kind":"ssh","name":"edge","ip":"192.0.2.10","port":22,"terminal_encoding":"GB18030","proxy_jump_list":[]}],"port_forward_rules":[]}`)
	catalog, err := Decode(data)
	if err != nil {
		t.Fatalf("decode version 8 catalog: %v", err)
	}
	host := catalog.HostRecords()[0]
	if host.TerminalEncoding != "GB18030" || host.TerminalInputEncoding != "" {
		t.Fatalf("version 8 terminal encodings = %q/%q", host.TerminalEncoding, host.TerminalInputEncoding)
	}
}

func TestCatalogVersionNineRemainsReadableWithoutFTPDataMode(t *testing.T) {
	data := []byte(`{"format":"` + Format + `","version":9,"groups":[],"hosts":[{"id":"ftp-1","kind":"ftp","name":"archive","ip":"files.example.test","port":21,"tls_mode":"off","proxy_jump_list":[]}],"port_forward_rules":[]}`)
	catalog, err := Decode(data)
	if err != nil {
		t.Fatalf("decode version 9 catalog: %v", err)
	}
	hosts := catalog.HostRecords()
	if len(hosts) != 1 || hosts[0].FTPActiveMode {
		t.Fatalf("version 9 FTP host = %#v; want passive default", hosts)
	}
}

func TestCatalogDecodeSupportsVersionOneWithoutFavorites(t *testing.T) {
	data := []byte(`{"format":"` + Format + `","version":1,"groups":[],"hosts":[{"id":"host-1","kind":"ssh","name":"edge","ip":"192.0.2.10","group_id":"","description":"","port":22,"user":"operator","proxy_jump_list":[]}],"port_forward_rules":[]}`)
	catalog, err := Decode(data)
	if err != nil {
		t.Fatalf("decode version 1 catalog: %v", err)
	}
	if host := catalog.HostRecords()[0]; host.Favorite || host.Name != "edge" {
		t.Fatalf("version 1 host = %#v, want non-favorite legacy host", host)
	}
}

func TestCatalogReadAndWriteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	want := NewCatalog(nil, []model.Host{{Id: "local", Kind: model.HostKindLocal, Name: "local"}}, nil)
	if err := WriteFile(path, want); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat catalog: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("catalog permissions = %o, want no group/other access", info.Mode().Perm())
	}
	if err := WriteFile(path, want); err == nil {
		t.Fatal("WriteFile overwrote an existing catalog")
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	if got.HostRecords()[0].Name != "local" {
		t.Fatalf("round trip host = %#v", got.HostRecords()[0])
	}
}
