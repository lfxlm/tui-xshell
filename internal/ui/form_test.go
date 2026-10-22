package ui

import (
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestNewHostEditFormCopiesSavedFields(t *testing.T) {
	host := model.Host{
		Id: "host-1", Kind: model.HostKindSSH, Name: "dev", IP: "10.0.0.8",
		Port: 2202, User: "som", GroupId: "group-1", Description: "development",
		IdentityFile: "~/.ssh/workbench_ed25519", ProxyJumpList: []string{"jump-1"},
		X11Forwarding: true, Startup: "cd /srv/app", StartupScriptPath: "~/login.json",
		LocalStartDir: "~/Downloads", RemoteStartDir: "/srv/releases",
	}
	form := newHostEditForm(host)
	if !form.editing || form.id != host.Id || form.hostKind != host.Kind {
		t.Fatalf("edit state = %#v, want editing host %q", form, host.Id)
	}
	if form.name != host.Name || form.address != host.IP || form.port != "2202" ||
		form.username != host.User || form.identityFile != host.IdentityFile || form.groupID != host.GroupId || form.description != host.Description {
		t.Fatalf("form fields = %#v, want saved host fields", form)
	}
	form.field = 5
	if form.currentText() != host.IdentityFile || form.isChoiceField() {
		t.Fatalf("identity file form field = %q choice=%v; want editable saved path", form.currentText(), form.isChoiceField())
	}
	form.setCurrentText("/tmp/new-key")
	if form.identityFile != "/tmp/new-key" {
		t.Fatalf("edited identity file = %q, want new path", form.identityFile)
	}
	form.field = 6
	if form.proxyJumpID != "jump-1" || !form.proxyJumpChoiceField() || form.groupFieldIndex() != 7 || form.fieldCount() != 15 || !form.x11Forwarding {
		t.Fatalf("jump host form state = %#v, want jump host and shifted SSH fields", form)
	}
	if !form.isChoiceFieldAt(9) || form.valueAt(9) != "开启" {
		t.Fatalf("X11 choice field = %q choice=%v, want enabled choice", form.valueAt(9), form.isChoiceFieldAt(9))
	}
	form.authProfileID = "profile-1"
	form.authProfileName = "开发环境"
	form.field = 10
	if !form.authProfileChoiceField() || form.valueAt(10) != "开发环境" {
		t.Fatalf("auth profile choice field = %q choice=%v", form.valueAt(10), form.authProfileChoiceField())
	}
	form.field = 11
	if form.currentText() != host.Startup || form.isChoiceField() {
		t.Fatalf("startup command form field = %q choice=%v, want editable command", form.currentText(), form.isChoiceField())
	}
	form.setCurrentText("cd /opt/app")
	if form.startup != "cd /opt/app" {
		t.Fatalf("edited startup command = %q, want updated command", form.startup)
	}
	form.field = 12
	if form.currentText() != host.StartupScriptPath || form.fieldLabels()[12] != "提示符登录脚本" {
		t.Fatalf("startup script field = %q label=%q, want saved local path", form.currentText(), form.fieldLabels()[12])
	}
	form.setCurrentText("/tmp/login.json")
	if form.startupScriptPath != "/tmp/login.json" {
		t.Fatalf("edited startup script path = %q, want updated path", form.startupScriptPath)
	}
	form.field = 13
	if form.currentText() != host.LocalStartDir || form.fieldLabels()[13] != "本地起始目录" {
		t.Fatalf("local starting directory field = %q label=%q", form.currentText(), form.fieldLabels()[13])
	}
	form.setCurrentText("~/Documents")
	if form.localStartDir != "~/Documents" {
		t.Fatalf("edited local starting directory = %q, want updated path", form.localStartDir)
	}
	form.field = 14
	if form.currentText() != host.RemoteStartDir || form.fieldLabels()[14] != "远程起始目录" {
		t.Fatalf("remote starting directory field = %q label=%q", form.currentText(), form.fieldLabels()[14])
	}
	form.setCurrentText("/srv/app")
	if form.remoteStartDir != "/srv/app" {
		t.Fatalf("edited remote starting directory = %q, want updated path", form.remoteStartDir)
	}
}

func TestSwitchingEditedLocalHostCyclesFTPAndTerminalProtocolPorts(t *testing.T) {
	form := newHostEditForm(model.Host{Id: "local-1", Kind: model.HostKindLocal, Name: "local"})
	form.cycleHostKind(1)
	if form.hostKind != model.HostKindFTP || form.port != "21" {
		t.Fatalf("switching local host to FTP produced kind=%q port=%q, want FTP port 21", form.hostKind, form.port)
	}
	form.cycleHostKind(1)
	if form.hostKind != model.HostKindTelnet || form.port != "23" {
		t.Fatalf("switching FTP host to Telnet produced kind=%q port=%q, want Telnet port 23", form.hostKind, form.port)
	}
	form.cycleHostKind(1)
	if form.hostKind != model.HostKindTelnet || form.terminalProtocol != model.TerminalProtocolRlogin || form.port != "513" {
		t.Fatalf("switching Telnet host to Rlogin produced kind=%q protocol=%q port=%q", form.hostKind, form.terminalProtocol, form.port)
	}
	form.cycleHostKind(1)
	if form.hostKind != model.HostKindSerial || form.fieldCount() != 10 || form.groupFieldIndex() != 8 {
		t.Fatalf("switching Rlogin host to serial produced kind=%q fields=%d groupField=%d", form.hostKind, form.fieldCount(), form.groupFieldIndex())
	}
	form.cycleHostKind(1)
	if form.hostKind != model.HostKindSSH || form.port != "22" {
		t.Fatalf("switching serial host to SSH produced kind=%q port=%q, want SSH port 22", form.hostKind, form.port)
	}
}

func TestRloginHostFormUsesRemoteUsername(t *testing.T) {
	form := newHostEditForm(model.Host{
		Id: "rlogin-1", Kind: model.HostKindTelnet, TerminalProtocol: model.TerminalProtocolRlogin,
		Name: "legacy host", IP: "192.0.2.1", Port: 513, User: "operator",
	})
	if got, want := form.fieldLabels()[4], "远程用户名"; got != want {
		t.Fatalf("Rlogin username label = %q, want %q", got, want)
	}
	if got, want := form.valueAt(1), "Rlogin（明文）"; got != want {
		t.Fatalf("Rlogin type label = %q, want %q", got, want)
	}
}

func TestSerialHostFormExposesDeviceAndFramingChoices(t *testing.T) {
	form := newHostEditForm(model.Host{
		Id: "serial-1", Kind: model.HostKindSerial, Name: "console", IP: "/dev/ttyUSB0",
		SerialBaudRate: 115200, SerialDataBits: 7, SerialParity: model.SerialParityEven,
		SerialStopBits: model.SerialStopBitsTwo, SerialFlowControl: model.SerialFlowControlXONXOFF,
	})
	if got := form.fieldLabels(); len(got) != 10 || got[7] != "流控制" || got[8] != "所属分组" {
		t.Fatalf("serial field labels = %#v", got)
	}
	if form.valueAt(3) != "115200" || form.valueAt(4) != "7" || form.valueAt(5) != "偶校验（Even）" || form.valueAt(6) != "2" {
		t.Fatalf("serial form settings = %q, %q, %q, %q", form.valueAt(3), form.valueAt(4), form.valueAt(5), form.valueAt(6))
	}
	if form.valueAt(7) != "XON/XOFF" || form.valueAt(8) != "" || !form.isChoiceFieldAt(7) {
		t.Fatalf("serial flow-control form value = %q choice=%v", form.valueAt(7), form.isChoiceFieldAt(7))
	}
	form.field = 5
	if !form.isChoiceField() {
		t.Fatal("serial parity is not presented as a choice")
	}
	form.cycleSerialSetting(1)
	if form.serialParity != model.SerialParityOdd {
		t.Fatalf("cycled serial parity = %q, want odd", form.serialParity)
	}
	form.field = 6
	form.cycleSerialSetting(-1)
	if form.serialStopBits != model.SerialStopBitsOne5 {
		t.Fatalf("cycled serial stop bits = %q, want 1.5", form.serialStopBits)
	}
	form.field = 7
	form.cycleSerialSetting(1)
	if form.serialFlowControl != model.SerialFlowControlRTSCTS {
		t.Fatalf("cycled serial flow control = %q, want RTS/CTS", form.serialFlowControl)
	}
}

func TestTelnetHostFormUsesLoginHintAndGroupFields(t *testing.T) {
	form := newHostEditForm(model.Host{
		Id: "telnet-1", Kind: model.HostKindTelnet, Name: "console",
		IP: "192.0.2.44", Port: 23, User: "operator", GroupId: "group-1",
		Description: "legacy device",
	})
	if got, want := form.fieldCount(), 7; got != want {
		t.Fatalf("Telnet form field count = %d, want %d", got, want)
	}
	if got, want := form.groupFieldIndex(), 5; got != want {
		t.Fatalf("Telnet group field = %d, want %d", got, want)
	}
	if got, want := form.fieldLabels()[4], "登录名提示"; got != want {
		t.Fatalf("Telnet username label = %q, want %q", got, want)
	}
	if got, want := form.valueAt(6), "legacy device"; got != want {
		t.Fatalf("Telnet description = %q, want %q", got, want)
	}
}

func TestFTPTLSModeSelectsImplicitPort(t *testing.T) {
	form := newHostEditForm(model.Host{Kind: model.HostKindFTP, Port: 21, TLSMode: model.FTPTLSOff})
	form.field = 5
	form.cycleFTPTLSMode(1)
	if form.tlsMode != model.FTPTLSExplicit || form.port != "21" {
		t.Fatalf("explicit FTPS mode = %q port=%q, want explicit FTPS on port 21", form.tlsMode, form.port)
	}
	form.cycleFTPTLSMode(1)
	if form.tlsMode != model.FTPTLSImplicit || form.port != "990" {
		t.Fatalf("implicit FTPS mode = %q port=%q, want implicit FTPS on port 990", form.tlsMode, form.port)
	}
}

func TestFTPHostFormPersistsDataConnectionModeAndGroupFields(t *testing.T) {
	form := newHostEditForm(model.Host{
		Kind: model.HostKindFTP, Name: "archive", IP: "ftp.example.test", Port: 21,
		FTPActiveMode: true, GroupId: "group-1", Description: "old server",
		LocalStartDir: "~/Downloads", RemoteStartDir: "incoming",
	})
	if form.fieldCount() != len(form.fieldLabels()) || form.groupFieldIndex() != 7 {
		t.Fatalf("FTP form fields count=%d labels=%d group=%d", form.fieldCount(), len(form.fieldLabels()), form.groupFieldIndex())
	}
	form.field = 6
	if !form.isChoiceField() || form.valueAt(6) != "主动（服务器连接本机）" {
		t.Fatalf("FTP data mode field = %q choice=%t", form.valueAt(6), form.isChoiceField())
	}
	form.cycleFTPDataMode(-1)
	if form.ftpActiveMode || form.valueAt(6) != "被动（本机连接服务器，默认）" {
		t.Fatalf("FTP data mode after cycling = %q active=%t", form.valueAt(6), form.ftpActiveMode)
	}
	if form.valueAt(7) != "" || form.valueAt(8) != "old server" {
		t.Fatalf("FTP group/description fields = %q / %q", form.valueAt(7), form.valueAt(8))
	}
	if form.valueAt(9) != "~/Downloads" || form.valueAt(10) != "incoming" {
		t.Fatalf("FTP starting-directory fields = %q / %q", form.valueAt(9), form.valueAt(10))
	}
}

func TestGroupEditParentChoicesExcludeSelfAndDescendants(t *testing.T) {
	groups := []model.Group{
		{Id: "root", Name: "Root"},
		{Id: "child", Name: "Child", ParentId: "root"},
		{Id: "grandchild", Name: "Grandchild", ParentId: "child"},
		{Id: "other", Name: "Other"},
	}
	form := newGroupEditForm(groups[0])
	state := modelState{groups: groups, form: form}

	parentID := form.groupID
	for range 8 {
		parentID = state.cycleGroupID(parentID, 1)
		if parentID == "root" || parentID == "child" || parentID == "grandchild" {
			t.Fatalf("invalid parent choice %q would create a group hierarchy cycle", parentID)
		}
	}
}
