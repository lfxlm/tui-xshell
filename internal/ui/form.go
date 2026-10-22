package ui

import (
	"strconv"

	"tui-ssh-workbench/internal/model"
)

type formKind uint8

const (
	groupForm formKind = iota + 1
	hostForm
	shortcutBarForm
)

type formState struct {
	kind              formKind
	id                string
	editing           bool
	field             int
	hostKind          model.HostKind
	terminalProtocol  model.TerminalProtocol
	name              string
	address           string
	port              string
	serialBaudRate    string
	serialDataBits    string
	serialParity      string
	serialStopBits    string
	serialFlowControl string
	username          string
	identityFile      string
	authProfileID     string
	authProfileName   string
	description       string
	startup           string
	startupScriptPath string
	localStartDir     string
	remoteStartDir    string
	groupID           string
	proxyJumpID       string
	tlsMode           string
	ftpActiveMode     bool
	x11Forwarding     bool
	errorText         string
	saving            bool
}

func newGroupForm(parentID string) *formState {
	return &formState{kind: groupForm, groupID: parentID}
}

func newHostForm(groupID string) *formState {
	return &formState{
		kind: hostForm, hostKind: model.HostKindSSH, groupID: groupID, port: "22", tlsMode: model.FTPTLSOff,
		serialBaudRate: "9600", serialDataBits: "8", serialParity: model.SerialParityNone,
		serialStopBits: model.SerialStopBitsOne, serialFlowControl: model.SerialFlowControlNone,
	}
}

func newGroupEditForm(group model.Group) *formState {
	return &formState{
		kind:           groupForm,
		id:             group.Id,
		editing:        true,
		name:           group.Name,
		groupID:        group.ParentId,
		authProfileID:  group.AuthProfileID,
		localStartDir:  group.LocalStartDir,
		remoteStartDir: group.RemoteStartDir,
	}
}

func newShortcutBarForm() *formState {
	return &formState{kind: shortcutBarForm}
}

func newShortcutBarEditForm(bar model.ShortcutBar) *formState {
	return &formState{kind: shortcutBarForm, id: bar.Id, editing: true, name: bar.Name}
}

func newHostEditForm(host model.Host) *formState {
	return &formState{
		kind:              hostForm,
		id:                host.Id,
		editing:           true,
		hostKind:          host.Kind,
		terminalProtocol:  host.TerminalProtocol,
		name:              host.Name,
		address:           host.IP,
		port:              strconv.Itoa(host.Port),
		serialBaudRate:    serialFormValue(host.SerialBaudRate, 9600),
		serialDataBits:    serialFormValue(host.SerialDataBits, 8),
		serialParity:      serialFormText(host.SerialParity, model.SerialParityNone),
		serialStopBits:    serialFormText(host.SerialStopBits, model.SerialStopBitsOne),
		serialFlowControl: serialFormText(host.SerialFlowControl, model.SerialFlowControlNone),
		username:          host.User,
		identityFile:      host.IdentityFile,
		authProfileID:     host.AuthProfileID,
		startup:           host.Startup,
		startupScriptPath: host.StartupScriptPath,
		localStartDir:     host.LocalStartDir,
		remoteStartDir:    host.RemoteStartDir,
		description:       host.Description,
		groupID:           host.GroupId,
		proxyJumpID:       firstProxyJump(host.ProxyJumpList),
		tlsMode:           host.TLSMode,
		ftpActiveMode:     host.FTPActiveMode,
		x11Forwarding:     host.X11Forwarding,
	}
}

func (f *formState) fieldCount() int {
	if f.kind == shortcutBarForm {
		return 1
	}
	if f.kind == groupForm {
		return 5
	}
	if f.hostKind == model.HostKindLocal {
		return 4
	}
	if f.hostKind == model.HostKindSerial {
		return 10
	}
	if f.hostKind == model.HostKindTelnet {
		return 7
	}
	if f.hostKind == model.HostKindSSH {
		return 15
	}
	return 11
}

func (f *formState) fieldLabels() []string {
	if f.kind == shortcutBarForm {
		return []string{"连接栏名称"}
	}
	if f.kind == groupForm {
		return []string{"名称", "父级分组", "SSH 认证配置（继承）", "本地起始目录", "远程起始目录"}
	}
	if f.hostKind == model.HostKindLocal {
		return []string{"名称", "类型", "所属分组", "备注"}
	}
	if f.hostKind == model.HostKindFTP {
		return []string{"名称", "类型", "地址/IP", "端口", "用户名", "安全模式", "数据连接模式", "所属分组", "备注", "本地起始目录", "远程起始目录"}
	}
	if f.hostKind == model.HostKindTelnet {
		usernameLabel := "登录名提示"
		if f.terminalProtocol == model.TerminalProtocolRlogin {
			usernameLabel = "远程用户名"
		}
		return []string{"名称", "类型", "地址/IP", "端口", usernameLabel, "所属分组", "备注"}
	}
	if f.hostKind == model.HostKindSerial {
		return []string{"名称", "类型", "设备路径", "波特率", "数据位", "校验位", "停止位", "流控制", "所属分组", "备注"}
	}
	return []string{"名称", "类型", "地址/IP", "端口", "用户名", "私钥文件", "跳板机", "所属分组", "备注", "X11 转发", "认证配置", "SSH 连接后命令", "提示符登录脚本", "本地起始目录", "远程起始目录"}
}

func (f *formState) typeChoiceField() bool {
	return f.kind == hostForm && f.field == 1
}

func (f *formState) groupChoiceField() bool {
	if f.kind == shortcutBarForm {
		return false
	}
	if f.kind == groupForm {
		return f.field == 1
	}
	if f.hostKind == model.HostKindLocal {
		return f.field == 2
	}
	return f.field == f.groupFieldIndex()
}

func (f *formState) isChoiceField() bool {
	return f.typeChoiceField() || f.proxyJumpChoiceField() || f.tlsModeChoiceField() || f.ftpActiveModeChoiceField() || f.x11ChoiceField() || f.authProfileChoiceField() || f.serialChoiceField() || f.groupChoiceField()
}

func (f *formState) serialChoiceField() bool {
	return f.kind == hostForm && f.hostKind == model.HostKindSerial && (f.field == 5 || f.field == 6 || f.field == 7)
}

func (f *formState) x11ChoiceField() bool {
	return f.kind == hostForm && f.hostKind == model.HostKindSSH && f.field == 9
}

func (f *formState) authProfileChoiceField() bool {
	return (f.kind == hostForm && f.hostKind == model.HostKindSSH && f.field == 10) || (f.kind == groupForm && f.field == 2)
}

func (f *formState) proxyJumpChoiceField() bool {
	return f.kind == hostForm && f.hostKind == model.HostKindSSH && f.field == 6
}

func (f *formState) tlsModeChoiceField() bool {
	return f.kind == hostForm && f.hostKind == model.HostKindFTP && f.field == 5
}

func (f *formState) ftpActiveModeChoiceField() bool {
	return f.kind == hostForm && f.hostKind == model.HostKindFTP && f.field == 6
}

func (f *formState) isChoiceFieldAt(field int) bool {
	if f.kind == hostForm && field == 1 {
		return true
	}
	if f.kind == hostForm && f.hostKind == model.HostKindSSH && field == 6 {
		return true
	}
	if f.kind == hostForm && f.hostKind == model.HostKindFTP && field == 5 {
		return true
	}
	if f.kind == hostForm && f.hostKind == model.HostKindFTP && field == 6 {
		return true
	}
	if f.kind == hostForm && f.hostKind == model.HostKindSSH && field == 9 {
		return true
	}
	if f.kind == hostForm && f.hostKind == model.HostKindSSH && field == 10 {
		return true
	}
	if f.kind == groupForm && field == 2 {
		return true
	}
	if f.kind == hostForm && f.hostKind == model.HostKindSerial && (field == 5 || field == 6 || field == 7) {
		return true
	}
	return field == f.groupFieldIndex()
}

func (f *formState) isPortField() bool {
	return f.kind == hostForm && f.hostKind != model.HostKindLocal && f.hostKind != model.HostKindSerial && f.field == 3
}

func (f *formState) isNumericField() bool {
	if f.kind != hostForm {
		return false
	}
	return f.isPortField() || (f.hostKind == model.HostKindSerial && (f.field == 3 || f.field == 4))
}

func (f *formState) groupFieldIndex() int {
	if f.kind == shortcutBarForm {
		return -1
	}
	if f.kind == groupForm {
		return 1
	}
	if f.hostKind == model.HostKindLocal {
		return 2
	}
	if f.hostKind == model.HostKindTelnet {
		return 5
	}
	if f.hostKind == model.HostKindSerial {
		return 8
	}
	if f.hostKind == model.HostKindFTP {
		return 7
	}
	return 7
}

func (f *formState) valueAt(field int) string {
	if f.kind == shortcutBarForm {
		if field == 0 {
			return f.name
		}
		return ""
	}
	if f.kind == groupForm {
		switch field {
		case 0:
			return f.name
		case 3:
			return f.localStartDir
		case 4:
			return f.remoteStartDir
		}
		return ""
	}
	if field == 1 {
		if f.hostKind == model.HostKindLocal {
			return "本地终端"
		}
		if f.hostKind == model.HostKindFTP {
			return "FTP / FTPS"
		}
		if f.hostKind == model.HostKindTelnet {
			if f.terminalProtocol == model.TerminalProtocolRlogin {
				return "Rlogin（明文）"
			}
			return "Telnet（明文）"
		}
		if f.hostKind == model.HostKindSerial {
			return "串口设备"
		}
		return "SSH"
	}
	if f.hostKind == model.HostKindLocal {
		switch field {
		case 0:
			return f.name
		case 3:
			return f.description
		}
		return ""
	}
	if f.hostKind == model.HostKindSerial {
		switch field {
		case 0:
			return f.name
		case 2:
			return f.address
		case 3:
			return f.serialBaudRate
		case 4:
			return f.serialDataBits
		case 5:
			return serialParityLabel(f.serialParity)
		case 6:
			return f.serialStopBits
		case 7:
			return serialFlowControlLabel(f.serialFlowControl)
		case 9:
			return f.description
		}
		return ""
	}
	switch field {
	case 0:
		return f.name
	case 2:
		return f.address
	case 3:
		return f.port
	case 4:
		return f.username
	case 5:
		if f.hostKind == model.HostKindFTP {
			return ftpTLSModeLabel(f.tlsMode)
		}
		if f.hostKind == model.HostKindSSH {
			return f.identityFile
		}
		return ""
	case 6:
		if f.hostKind == model.HostKindFTP {
			return ftpDataModeLabel(f.ftpActiveMode)
		}
		if f.hostKind == model.HostKindTelnet {
			return f.description
		}
		return ""
	case 7:
		if f.hostKind == model.HostKindSSH || f.hostKind == model.HostKindFTP {
			return ""
		}
		return f.description
	case 8:
		return f.description
	case 9:
		if f.hostKind == model.HostKindFTP {
			return f.localStartDir
		}
		if f.x11Forwarding {
			return "开启"
		}
		return "关闭"
	case 10:
		if f.hostKind == model.HostKindFTP {
			return f.remoteStartDir
		}
		if f.authProfileName == "" {
			return "不使用"
		}
		return f.authProfileName
	case 11:
		return f.startup
	case 12:
		return f.startupScriptPath
	case 13:
		return f.localStartDir
	case 14:
		return f.remoteStartDir
	default:
		return ""
	}
}

func (f *formState) currentText() string {
	return f.valueAt(f.field)
}

func (f *formState) setCurrentText(value string) {
	if f.kind == shortcutBarForm {
		if f.field == 0 {
			f.name = value
		}
		return
	}
	if f.kind == groupForm {
		switch f.field {
		case 0:
			f.name = value
		case 3:
			f.localStartDir = value
		case 4:
			f.remoteStartDir = value
		}
		return
	}
	if f.hostKind == model.HostKindLocal {
		switch f.field {
		case 0:
			f.name = value
		case 3:
			f.description = value
		}
		return
	}
	if f.hostKind == model.HostKindSerial {
		switch f.field {
		case 0:
			f.name = value
		case 2:
			f.address = value
		case 3:
			f.serialBaudRate = value
		case 4:
			f.serialDataBits = value
		case 9:
			f.description = value
		}
		return
	}
	switch f.field {
	case 0:
		f.name = value
	case 2:
		f.address = value
	case 3:
		f.port = value
	case 4:
		f.username = value
	case 5:
		if f.hostKind == model.HostKindSSH {
			f.identityFile = value
		}
	case 11:
		if f.hostKind == model.HostKindSSH {
			f.startup = value
		}
	case 12:
		if f.hostKind == model.HostKindSSH {
			f.startupScriptPath = value
		}
	case 9:
		if f.hostKind == model.HostKindFTP {
			f.localStartDir = value
		}
	case 10:
		if f.hostKind == model.HostKindFTP {
			f.remoteStartDir = value
		}
	case 13:
		if f.hostKind == model.HostKindSSH {
			f.localStartDir = value
		}
	case 14:
		if f.hostKind == model.HostKindSSH {
			f.remoteStartDir = value
		}
	case 6:
		if f.hostKind == model.HostKindTelnet {
			f.description = value
		}
	case 7:
		if f.hostKind != model.HostKindSSH && f.hostKind != model.HostKindFTP {
			f.description = value
		}
	case 8:
		f.description = value
	}
}

func (f *formState) cycleHostKind(delta int) {
	type hostChoice struct {
		kind     model.HostKind
		protocol model.TerminalProtocol
		port     string
	}
	choices := []hostChoice{
		{kind: model.HostKindSSH, port: "22"},
		{kind: model.HostKindLocal},
		{kind: model.HostKindFTP, port: "21"},
		{kind: model.HostKindTelnet, protocol: model.TerminalProtocolTelnet, port: "23"},
		{kind: model.HostKindTelnet, protocol: model.TerminalProtocolRlogin, port: "513"},
		{kind: model.HostKindSerial},
	}
	index := 0
	for candidate, choice := range choices {
		if choice.kind == f.hostKind && (choice.kind != model.HostKindTelnet || choice.protocol == f.terminalProtocol || f.terminalProtocol == "" && choice.protocol == model.TerminalProtocolTelnet) {
			index = candidate
			break
		}
	}
	oldDefault := map[string]bool{"": true, "0": true, "21": true, "22": true, "23": true, "513": true, "990": true}
	index = (index + delta + len(choices)) % len(choices)
	choice := choices[index]
	if oldDefault[f.port] {
		f.port = choice.port
	}
	f.hostKind = choice.kind
	f.terminalProtocol = choice.protocol
	if choice.kind == model.HostKindSSH {
		f.proxyJumpID = ""
		f.tlsMode = ""
	}
	if choice.kind == model.HostKindFTP && f.tlsMode == "" {
		f.tlsMode = model.FTPTLSOff
	}
	if choice.kind == model.HostKindSerial {
		if f.serialBaudRate == "" {
			f.serialBaudRate = "9600"
		}
		if f.serialDataBits == "" {
			f.serialDataBits = "8"
		}
		if f.serialParity == "" {
			f.serialParity = model.SerialParityNone
		}
		if f.serialStopBits == "" {
			f.serialStopBits = model.SerialStopBitsOne
		}
		if f.serialFlowControl == "" {
			f.serialFlowControl = model.SerialFlowControlNone
		}
	}
	if f.field >= f.fieldCount() {
		f.field = f.fieldCount() - 1
	}
}

func (f *formState) cycleSerialSetting(delta int) {
	if f.field == 5 {
		choices := []string{model.SerialParityNone, model.SerialParityEven, model.SerialParityOdd, model.SerialParityMark, model.SerialParitySpace}
		f.serialParity = cycleString(choices, f.serialParity, delta)
	} else if f.field == 6 {
		choices := []string{model.SerialStopBitsOne, model.SerialStopBitsOne5, model.SerialStopBitsTwo}
		f.serialStopBits = cycleString(choices, f.serialStopBits, delta)
	} else if f.field == 7 {
		choices := []string{model.SerialFlowControlNone, model.SerialFlowControlXONXOFF, model.SerialFlowControlRTSCTS}
		f.serialFlowControl = cycleString(choices, f.serialFlowControl, delta)
	}
}

func cycleString(choices []string, current string, delta int) string {
	index := 0
	for candidate, choice := range choices {
		if choice == current {
			index = candidate
			break
		}
	}
	return choices[(index+delta+len(choices))%len(choices)]
}

func serialFormValue(value, fallback int) string {
	if value == 0 {
		value = fallback
	}
	return strconv.Itoa(value)
}

func serialFormText(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func serialParityLabel(parity string) string {
	switch parity {
	case model.SerialParityEven:
		return "偶校验（Even）"
	case model.SerialParityOdd:
		return "奇校验（Odd）"
	case model.SerialParityMark:
		return "Mark"
	case model.SerialParitySpace:
		return "Space"
	default:
		return "无校验（None）"
	}
}

func serialFlowControlLabel(flowControl string) string {
	switch flowControl {
	case model.SerialFlowControlXONXOFF:
		return "XON/XOFF"
	case model.SerialFlowControlRTSCTS:
		return "RTS/CTS"
	default:
		return "无"
	}
}

func (f *formState) cycleFTPTLSMode(delta int) {
	modes := []string{model.FTPTLSOff, model.FTPTLSExplicit, model.FTPTLSImplicit}
	index := 0
	for candidate, mode := range modes {
		if mode == f.tlsMode {
			index = candidate
			break
		}
	}
	index = (index + delta + len(modes)) % len(modes)
	previous := f.tlsMode
	f.tlsMode = modes[index]
	if previous == model.FTPTLSImplicit && f.tlsMode != model.FTPTLSImplicit && f.port == "990" {
		f.port = "21"
	} else if previous != model.FTPTLSImplicit && f.tlsMode == model.FTPTLSImplicit && (f.port == "" || f.port == "21") {
		f.port = "990"
	}
}

func (f *formState) cycleFTPDataMode(delta int) {
	if delta != 0 {
		f.ftpActiveMode = !f.ftpActiveMode
	}
}

func (f *formState) cycleX11Forwarding(delta int) {
	if delta != 0 {
		f.x11Forwarding = !f.x11Forwarding
	}
}

func ftpTLSModeLabel(mode string) string {
	switch mode {
	case model.FTPTLSExplicit:
		return "显式 FTPS"
	case model.FTPTLSImplicit:
		return "隐式 FTPS"
	default:
		return "明文 FTP"
	}
}

func ftpDataModeLabel(active bool) string {
	if active {
		return "主动（服务器连接本机）"
	}
	return "被动（本机连接服务器，默认）"
}

func (f *formState) moveField(delta int) {
	f.field = (f.field + delta + f.fieldCount()) % f.fieldCount()
	f.errorText = ""
}

func firstProxyJump(proxyJumps []string) string {
	if len(proxyJumps) == 0 {
		return ""
	}
	return proxyJumps[0]
}
