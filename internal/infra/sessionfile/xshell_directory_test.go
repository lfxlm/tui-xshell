package sessionfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestReadXShellDirectoryImportsNestedGroupsAndStableSessions(t *testing.T) {
	root := t.TempDir()
	contents := "[SessionInfo]\nVersion=7.0\nSessionName=shared\n[CONNECTION]\nProtocol=SSH\nHost=example.test\nPort=22\n"
	for _, path := range []string{"prod/shared.xsh", "dev/shared.xsh"} {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := ReadXShellDirectory(root)
	if err != nil {
		t.Fatalf("read Xshell directory: %v", err)
	}
	second, err := ReadXShellDirectory(root)
	if err != nil {
		t.Fatalf("read Xshell directory again: %v", err)
	}
	if len(first.Groups) != 2 || len(first.Hosts) != 2 {
		t.Fatalf("imported groups/hosts = %d/%d, want 2/2", len(first.Groups), len(first.Hosts))
	}
	if first.Groups[0].Id != second.Groups[0].Id || first.Hosts[0].Id != second.Hosts[0].Id || first.Hosts[1].Id != second.Hosts[1].Id {
		t.Fatalf("directory import IDs are unstable: %#v / %#v", first, second)
	}
	if first.Hosts[0].Id == first.Hosts[1].Id {
		t.Fatal("same-named sessions in different groups received the same ID")
	}
	groupIDs := make(map[string]bool)
	for _, group := range first.Groups {
		groupIDs[group.Id] = true
	}
	for _, host := range first.HostRecords() {
		if !groupIDs[host.GroupId] {
			t.Errorf("session %q assigned to missing group %q", host.Name, host.GroupId)
		}
	}
}

func TestReadXShellDirectorySkipsSymlinksAndRejectsEmpty(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "external.xsh")
	if err := os.WriteFile(outside, []byte("[SessionInfo]\n[CONNECTION]\nProtocol=SSH\nHost=example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.xsh")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ReadXShellDirectory(root); err == nil || !strings.Contains(err.Error(), "no .xsh") {
		t.Fatalf("symlink-only directory error = %v, want no sessions", err)
	}
	if _, err := ReadXShellDirectory(outside); err == nil {
		t.Fatal("single file was accepted as an Xshell directory")
	}
}

func TestWriteXShellDirectoryRoundTripAndSafeSkips(t *testing.T) {
	groups := []model.Group{
		{Id: "group-parent", Name: "Production/West"},
		{Id: "group-child", ParentId: "group-parent", Name: "API"},
		{Id: "group-file-collision", Name: "edge.xsh"},
	}
	hosts := []model.Host{
		{Id: "ssh", Kind: model.HostKindSSH, Name: "edge", IP: "2001:db8::10", Port: 2200, User: "operator", GroupId: "group-child", Description: "primary", TerminalEncoding: "GB18030"},
		{Id: "telnet", Kind: model.HostKindTelnet, Name: "console", IP: "console.example.test", Port: 23, GroupId: "group-parent"},
		{Id: "rlogin", Kind: model.HostKindTelnet, TerminalProtocol: model.TerminalProtocolRlogin, Name: "legacy", IP: "legacy.example.test", Port: 513, GroupId: "group-parent"},
		{Id: "root-host", Kind: model.HostKindSSH, Name: "edge", IP: "root.example.test", Port: 22},
		{Id: "ftp", Kind: model.HostKindFTP, Name: "ftp", IP: "files.example.test", Port: 21},
		{Id: "jump", Kind: model.HostKindSSH, Name: "jump", IP: "jump.example.test", Port: 22, ProxyJumpList: []string{"gateway"}},
		{Id: "script", Kind: model.HostKindSSH, Name: "script", IP: "script.example.test", Port: 22, Startup: "echo do-not-export"},
		{Id: "script-path", Kind: model.HostKindSSH, Name: "script-path", IP: "script-path.example.test", Port: 22, StartupScriptPath: "~/private/login.json"},
		{Id: "forward", Kind: model.HostKindSSH, Name: "forward", IP: "forward.example.test", Port: 22},
	}
	rules := []model.PortForwardRule{{Id: "rule", HostId: "forward", Kind: model.PortForwardLocalTCP}}
	dest := filepath.Join(t.TempDir(), "native-sessions")
	result, err := WriteXShellDirectory(dest, groups, hosts, rules)
	if err != nil {
		t.Fatalf("export Xshell directory: %v", err)
	}
	if result.Exported != 4 || len(result.Skipped) != 5 {
		t.Fatalf("export result = %#v, want 4 exported and 5 skipped", result)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("export directory mode = %o, want 700", info.Mode().Perm())
	}
	sshPath := filepath.Join(dest, "Production_West", "API", "edge.xsh")
	fileInfo, err := os.Stat(sshPath)
	if err != nil {
		t.Fatalf("stat exported SSH session: %v", err)
	}
	if runtime.GOOS != "windows" && fileInfo.Mode().Perm() != 0o600 {
		t.Errorf("exported session mode = %o, want 600", fileInfo.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dest, "edge (2).xsh")); err != nil {
		t.Fatalf("host session did not avoid a colliding group directory: %v", err)
	}
	rloginPath := filepath.Join(dest, "Production_West", "legacy.xsh")
	rloginData, err := os.ReadFile(rloginPath)
	rloginText, decodeErr := decodeXShellText(rloginData)
	if err != nil || decodeErr != nil || !strings.Contains(rloginText, "Protocol=Rlogin") {
		t.Fatalf("exported Rlogin session = %q, err=%v, decodeErr=%v", rloginData, err, decodeErr)
	}
	data, err := os.ReadFile(sshPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "\xff\xfe") {
		t.Fatal("exported Xshell file is not UTF-16 LE with a BOM")
	}
	text, err := decodeXShellText(data)
	if err != nil {
		t.Fatalf("decode exported Xshell text: %v", err)
	}
	if strings.Contains(text, "StartupScript") || strings.Contains(text, "Password=") {
		t.Fatalf("export contains a script or credential field: %q", text)
	}
	if !strings.Contains(text, "CodePage=54936") {
		t.Fatalf("export omitted Xshell terminal code page: %q", text)
	}

	imported, err := ReadFile(dest)
	if err != nil {
		t.Fatalf("import exported Xshell directory: %v", err)
	}
	if len(imported.Groups) != 3 || len(imported.Hosts) != 4 {
		t.Fatalf("round-trip groups/hosts = %d/%d, want 3/4", len(imported.Groups), len(imported.Hosts))
	}
	gotHosts := imported.HostRecords()
	foundTelnet, foundRlogin, foundSSH := false, false, false
	for _, host := range gotHosts {
		foundTelnet = foundTelnet || host.Kind == model.HostKindTelnet && host.IP == "console.example.test"
		foundRlogin = foundRlogin || host.Kind == model.HostKindTelnet && host.TerminalProtocol == model.TerminalProtocolRlogin && host.IP == "legacy.example.test"
		if host.Kind == model.HostKindSSH && host.IP == "2001:db8::10" {
			foundSSH = host.TerminalEncoding == "GB18030" && host.TerminalInputEncoding == model.TerminalEncodingUTF8
		}
	}
	if !foundTelnet || !foundRlogin || !foundSSH {
		t.Fatalf("round trip lost supported sessions: %#v", gotHosts)
	}
	if _, err := WriteXShellDirectory(dest, groups, hosts, rules); err == nil {
		t.Fatal("export unexpectedly overwrote an existing directory")
	}
}

func TestWriteXShellDirectoryRoundTripsGBKCodePage(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "gbk-sessions")
	host := model.Host{
		Id: "gbk-host", Kind: model.HostKindSSH, Name: "gbk-host",
		IP: "example.test", Port: 22, TerminalEncoding: "GBK",
	}
	result, err := WriteXShellDirectory(destination, nil, []model.Host{host}, nil)
	if err != nil || result.Exported != 1 {
		t.Fatalf("export GBK host = %#v, %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "gbk-host.xsh"))
	if err != nil {
		t.Fatal(err)
	}
	text, err := decodeXShellText(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "CodePage=936") {
		t.Fatalf("Xshell export did not preserve CP936: %q", text)
	}
	imported, err := DecodeXShell(data, "gbk-host.xsh")
	if err != nil {
		t.Fatal(err)
	}
	got := imported.HostRecords()[0]
	if got.TerminalEncoding != "GBK" || got.TerminalInputEncoding != model.TerminalEncodingUTF8 {
		t.Fatalf("Xshell CP936 round trip output/input = %q/%q, want GBK/UTF-8", got.TerminalEncoding, got.TerminalInputEncoding)
	}
}

func TestWriteXShellDirectoryExportsUngroupedHost(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "sessions")
	result, err := WriteXShellDirectory(dest, nil, []model.Host{{
		Id: "bad", Kind: model.HostKindSSH, Name: "bad", IP: "example.test", Port: 22,
	}}, nil)
	if err != nil {
		t.Fatalf("write minimal supported session: %v", err)
	}
	if result.Exported != 1 || len(result.Skipped) != 0 {
		t.Fatalf("ungrouped export result = %#v", result)
	}
	if entries, err := os.ReadDir(dest); err != nil || len(entries) != 1 || entries[0].Name() != "bad.xsh" {
		t.Fatalf("export contents = %v, error = %v", entries, err)
	}
}

func TestWriteXShellDirectoryRoundTripsSerialSession(t *testing.T) {
	host := model.Host{
		Id: "serial", Kind: model.HostKindSerial, Name: "router-console", IP: "COM2",
		SerialBaudRate: 115200, SerialDataBits: 7, SerialParity: model.SerialParityEven,
		SerialStopBits: model.SerialStopBitsOne5, SerialFlowControl: model.SerialFlowControlRTSCTS,
	}
	dest := filepath.Join(t.TempDir(), "serial-sessions")
	result, err := WriteXShellDirectory(dest, nil, []model.Host{host}, nil)
	if err != nil {
		t.Fatalf("export serial session: %v", err)
	}
	if result.Exported != 1 || len(result.Skipped) != 0 {
		t.Fatalf("serial export result = %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(dest, "router-console.xsh"))
	if err != nil {
		t.Fatal(err)
	}
	text, err := decodeXShellText(data)
	if err != nil {
		t.Fatalf("decode exported session: %v", err)
	}
	for _, expected := range []string{"Protocol=SERIAL", "BaudRate=13", "StopBits=1", "FlowCtrl=2", "Parity=2", "DataBits=2", "ComPort=1"} {
		if !strings.Contains(text, expected) {
			t.Errorf("exported serial session missing %q:\n%s", expected, text)
		}
	}
	imported, err := ReadXShellDirectory(dest)
	if err != nil {
		t.Fatalf("import exported serial session: %v", err)
	}
	got := imported.HostRecords()[0]
	if got.Kind != model.HostKindSerial || got.IP != "COM2" || got.SerialBaudRate != host.SerialBaudRate ||
		got.SerialDataBits != host.SerialDataBits || got.SerialParity != host.SerialParity || got.SerialStopBits != host.SerialStopBits || got.SerialFlowControl != host.SerialFlowControl {
		t.Fatalf("serial round trip = %#v, want %#v", got, host)
	}
}

func TestWriteXShellDirectoryMapsAllSerialFlowControlOptions(t *testing.T) {
	for flow, encoded := range map[string]string{
		model.SerialFlowControlNone:    "0",
		model.SerialFlowControlXONXOFF: "1",
		model.SerialFlowControlRTSCTS:  "2",
	} {
		t.Run(flow, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "sessions")
			host := model.Host{
				Id: "serial", Kind: model.HostKindSerial, Name: "console", IP: "COM1",
				SerialFlowControl: flow,
			}
			result, err := WriteXShellDirectory(destination, nil, []model.Host{host}, nil)
			if err != nil || result.Exported != 1 {
				t.Fatalf("export flow control %q = %#v, %v", flow, result, err)
			}
			data, err := os.ReadFile(filepath.Join(destination, "console.xsh"))
			if err != nil {
				t.Fatal(err)
			}
			text, err := decodeXShellText(data)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text, "FlowCtrl="+encoded) {
				t.Fatalf("exported FlowCtrl did not match %q:\n%s", encoded, text)
			}
			catalog, err := DecodeXShell(data, "console.xsh")
			if err != nil {
				t.Fatal(err)
			}
			if got := catalog.HostRecords()[0].SerialFlowControl; got != flow {
				t.Fatalf("round-trip flow control = %q, want %q", got, flow)
			}
		})
	}
}

func TestWriteXShellDirectorySkipsUnrepresentableSerialSession(t *testing.T) {
	hosts := []model.Host{
		{Id: "custom-device", Kind: model.HostKindSerial, Name: "custom-device", IP: "/dev/ttyUSB0", SerialBaudRate: 9600, SerialDataBits: 8},
		{Id: "custom-baud", Kind: model.HostKindSerial, Name: "custom-baud", IP: "COM1", SerialBaudRate: 12345, SerialDataBits: 8},
	}
	result, err := WriteXShellDirectory(filepath.Join(t.TempDir(), "sessions"), nil, hosts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Exported != 0 || len(result.Skipped) != len(hosts) {
		t.Fatalf("serial export result = %#v, want both sessions skipped", result)
	}
}
