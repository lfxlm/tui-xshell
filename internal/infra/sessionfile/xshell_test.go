package sessionfile

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"tui-ssh-workbench/internal/model"
)

func TestDecodeXShellSessionReadsUTF16LEAndDropsSecrets(t *testing.T) {
	contents := `[SessionInfo]
Version=7.0
SessionName=生产边缘主机
Description=来自 Xshell

[CONNECTION]
Protocol=SSH
Host=2001:db8::10
Port=2200

[CONNECTION:AUTHENTICATION]
UserName=operator
Password=never-import-this
StartupScript=do-not-run-this

[TERMINAL]
CodePage=936
`
	catalog, err := DecodeXShell(encodeUTF16LE(contents), "edge.xsh")
	if err != nil {
		t.Fatalf("decode Xshell session: %v", err)
	}
	if catalog.Version != Version || len(catalog.Hosts) != 1 {
		t.Fatalf("imported Xshell catalog = %#v", catalog)
	}
	host := catalog.HostRecords()[0]
	if host.Kind != model.HostKindSSH || host.Name != "生产边缘主机" || host.IP != "2001:db8::10" ||
		host.Port != 2200 || host.User != "operator" || host.Description != "来自 Xshell" {
		t.Fatalf("imported Xshell host = %#v", host)
	}
	if host.TerminalEncoding != "GBK" || host.TerminalInputEncoding != model.TerminalEncodingUTF8 {
		t.Fatalf("Xshell output/input encodings = %q/%q, want GBK/UTF-8", host.TerminalEncoding, host.TerminalInputEncoding)
	}
	if host.Password != "" || host.CredentialRef != "" || host.Startup != "" {
		t.Fatalf("native import retained sensitive or executable fields: %#v", host)
	}
	encoded, err := catalog.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "never-import-this") || strings.Contains(string(encoded), "do-not-run-this") {
		t.Fatalf("portable catalog contains ignored Xshell data: %s", encoded)
	}
}

func TestXShellTerminalCodePageMappingsAndUnsupportedValues(t *testing.T) {
	for codePage, want := range map[string]string{
		"65001": model.TerminalEncodingUTF8,
		"936":   "GBK",
		"54936": "GB18030",
		"950":   "Big5",
		"932":   "Shift_JIS",
		"1252":  "Windows-1252",
	} {
		t.Run(codePage, func(t *testing.T) {
			contents := "[SessionInfo]\nVersion=7.0\n[CONNECTION]\nProtocol=SSH\nHost=example.test\n[TERMINAL]\nCodePage=" + codePage + "\n"
			catalog, err := DecodeXShell([]byte(contents), "encoding.xsh")
			if err != nil {
				t.Fatalf("decode code page %s: %v", codePage, err)
			}
			if got := catalog.HostRecords()[0].TerminalEncoding; got != want {
				t.Fatalf("code page %s decoded as %q, want %q", codePage, got, want)
			}
		})
	}
	for _, codePage := range []string{"0", "not-a-number", "437"} {
		contents := "[SessionInfo]\nVersion=7.0\n[CONNECTION]\nProtocol=SSH\nHost=example.test\n[TERMINAL]\nCodePage=" + codePage + "\n"
		if _, err := DecodeXShell([]byte(contents), "unsupported.xsh"); err == nil {
			t.Errorf("unsupported Xshell code page %q was silently accepted", codePage)
		}
	}
}

func TestReadFileImportsXShellExtensionAndStableIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "staging-edge.XSH")
	contents := `[SessionInfo]
version=7.0
[CONNECTION]
Protocol=SSH2
Host=staging.example.test
Port=2222
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := ReadFile(path)
	if err != nil {
		t.Fatalf("read Xshell file: %v", err)
	}
	second, err := ReadFile(path)
	if err != nil {
		t.Fatalf("read Xshell file again: %v", err)
	}
	if len(first.Hosts) != 1 || len(second.Hosts) != 1 || first.Hosts[0].Id != second.Hosts[0].Id {
		t.Fatalf("repeated import IDs = %v / %v", first.Hosts, second.Hosts)
	}
	host := first.HostRecords()[0]
	if host.Name != "staging-edge" || host.Kind != model.HostKindSSH || host.Port != 2222 {
		t.Fatalf("extension-based Xshell host = %#v", host)
	}
}

func TestDecodeXShellSupportsTelnetAndDefaultPort(t *testing.T) {
	catalog, err := DecodeXShell([]byte("[SessionInfo]\nVersion=7.0\n[CONNECTION]\nProtocol=Telnet\nHost=console.example.test\n"), "console.xsh")
	if err != nil {
		t.Fatal(err)
	}
	host := catalog.HostRecords()[0]
	if host.Kind != model.HostKindTelnet || host.Port != 23 || host.Name != "console" {
		t.Fatalf("imported Telnet host = %#v", host)
	}
}

func TestDecodeXShellSupportsRloginAndDefaultPort(t *testing.T) {
	catalog, err := DecodeXShell([]byte("[SessionInfo]\nVersion=7.0\n[CONNECTION]\nProtocol=Rlogin\nHost=legacy.example.test\n"), "legacy.xsh")
	if err != nil {
		t.Fatal(err)
	}
	host := catalog.HostRecords()[0]
	if host.Kind != model.HostKindTelnet || host.TerminalProtocol != model.TerminalProtocolRlogin || host.Port != 513 {
		t.Fatalf("imported Rlogin host = %#v", host)
	}
}

func TestDecodeXShellSupportsSerialSession(t *testing.T) {
	contents := `[SessionInfo]
Version=6.0
SessionName=router-console
Description=serial console
[CONNECTION]
Protocol=SERIAL
[CONNECTION:SERIAL]
BaudRate=6
StopBits=0
FlowCtrl=0
Parity=0
DataBits=3
ComPort=0
`
	catalog, err := DecodeXShell([]byte(contents), "router-console.xsh")
	if err != nil {
		t.Fatal(err)
	}
	host := catalog.HostRecords()[0]
	if host.Kind != model.HostKindSerial || host.IP != "COM1" || host.Port != 0 || host.Name != "router-console" ||
		host.SerialBaudRate != 9600 || host.SerialDataBits != 8 || host.SerialParity != model.SerialParityNone || host.SerialStopBits != model.SerialStopBitsOne || host.SerialFlowControl != model.SerialFlowControlNone {
		t.Fatalf("imported Xshell serial host = %#v", host)
	}
}

func TestDecodeXShellMapsSerialFlowControl(t *testing.T) {
	base := `[SessionInfo]
Version=7.0
[CONNECTION]
Protocol=SERIAL
[CONNECTION:SERIAL]
BaudRate=6
StopBits=0
FlowCtrl=%s
Parity=0
DataBits=3
ComPort=0
`
	for value, want := range map[string]string{
		"0": model.SerialFlowControlNone,
		"1": model.SerialFlowControlXONXOFF,
		"2": model.SerialFlowControlRTSCTS,
	} {
		catalog, err := DecodeXShell([]byte(fmt.Sprintf(base, value)), "serial.xsh")
		if err != nil {
			t.Fatalf("decode flow control %s: %v", value, err)
		}
		if got := catalog.HostRecords()[0].SerialFlowControl; got != want {
			t.Errorf("FlowCtrl=%s decoded as %q, want %q", value, got, want)
		}
	}
}

func TestDecodeXShellRejectsUnsupportedSerialValuesAndFlowControl(t *testing.T) {
	base := `[SessionInfo]
Version=6.0
[CONNECTION]
Protocol=SERIAL
[CONNECTION:SERIAL]
BaudRate=6
StopBits=0
FlowCtrl=0
Parity=0
DataBits=3
ComPort=0
`
	for name, contents := range map[string]string{
		"unknown baud encoding":    strings.Replace(base, "BaudRate=6", "BaudRate=999", 1),
		"unsupported flow control": strings.Replace(base, "FlowCtrl=0", "FlowCtrl=9", 1),
		"missing port":             strings.Replace(base, "ComPort=0", "ComPort=", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeXShell([]byte(contents), "serial.xsh"); err == nil {
				t.Fatal("unsupported or incomplete serial session was accepted")
			}
		})
	}
}

func TestDecodeXShellReadsUTF16BEAndUTF8BOM(t *testing.T) {
	contents := "[SessionInfo]\nVersion=7.0\n[CONNECTION]\nProtocol=SSH\nHost=example.test\nPort=22\n"
	utf16Data := []byte{0xfe, 0xff}
	for _, unit := range utf16.Encode([]rune(contents)) {
		var encoded [2]byte
		binary.BigEndian.PutUint16(encoded[:], unit)
		utf16Data = append(utf16Data, encoded[:]...)
	}
	for name, data := range map[string][]byte{
		"UTF-16 BE": utf16Data,
		"UTF-8 BOM": append([]byte{0xef, 0xbb, 0xbf}, []byte(contents)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeXShell(data, "edge.xsh"); err != nil {
				t.Fatalf("decode session text: %v", err)
			}
		})
	}
}

func TestDecodeXShellRejectsUnsupportedOrMalformedSession(t *testing.T) {
	for name, contents := range map[string]string{
		"unsupported protocol": "[CONNECTION]\nProtocol=RLogin\nHost=example.test\n",
		"missing host":         "[CONNECTION]\nProtocol=SSH\nPort=22\n",
		"missing session info": "[CONNECTION]\nProtocol=SSH\nHost=example.test\n",
		"invalid port":         "[CONNECTION]\nProtocol=SSH\nHost=example.test\nPort=65536\n",
		"invalid section":      "[CONNECTION\nProtocol=SSH\nHost=example.test\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeXShell([]byte(contents), "invalid.xsh"); err == nil {
				t.Fatal("invalid Xshell session was accepted")
			}
		})
	}
	if _, err := DecodeXShell([]byte{0xff, 0xfe, 0x41}, "odd.xsh"); err == nil {
		t.Fatal("incomplete UTF-16 code unit was accepted")
	}
}

func encodeUTF16LE(value string) []byte {
	units := utf16.Encode([]rune(value))
	data := []byte{0xff, 0xfe}
	for _, unit := range units {
		var encoded [2]byte
		binary.LittleEndian.PutUint16(encoded[:], unit)
		data = append(data, encoded[:]...)
	}
	return data
}
