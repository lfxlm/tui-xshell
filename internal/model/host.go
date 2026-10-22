package model

import (
	"time"

	"tui-ssh-workbench/internal/consts"
)

type HostKind string

const (
	HostKindSSH    HostKind = "ssh"
	HostKindLocal  HostKind = "local"
	HostKindFTP    HostKind = "ftp"
	HostKindTelnet HostKind = "telnet"
	HostKindSerial HostKind = "serial"
)

const (
	SerialParityNone  = "none"
	SerialParityEven  = "even"
	SerialParityOdd   = "odd"
	SerialParityMark  = "mark"
	SerialParitySpace = "space"

	SerialStopBitsOne  = "1"
	SerialStopBitsOne5 = "1.5"
	SerialStopBitsTwo  = "2"

	SerialFlowControlNone    = "none"
	SerialFlowControlXONXOFF = "xon_xoff"
	SerialFlowControlRTSCTS  = "rts_cts"
)

// SSHFileProtocol selects the file transfer protocol used for an SSH host.
// SSH terminal connections still use SSH regardless of this setting.
type SSHFileProtocol string

const (
	SSHFileProtocolSFTP SSHFileProtocol = "sftp"
	SSHFileProtocolSCP  SSHFileProtocol = "scp"
)

// TerminalProtocol selects the terminal protocol stored for legacy r-commands.
// It is meaningful only for HostKindTelnet entries.
type TerminalProtocol string

const (
	TerminalProtocolTelnet TerminalProtocol = "telnet"
	TerminalProtocolRlogin TerminalProtocol = "rlogin"
)

const TerminalEncodingUTF8 = "UTF-8"

var terminalEncodingOptions = [...]string{
	TerminalEncodingUTF8, "GB18030", "GBK", "Big5", "Shift_JIS", "Windows-1252",
}

func TerminalEncodingOptions() []string {
	return append([]string(nil), terminalEncodingOptions[:]...)
}

func IsTerminalEncodingSupported(name string) bool {
	for _, option := range terminalEncodingOptions {
		if name == option {
			return true
		}
	}
	return false
}

const (
	FTPTLSOff      = "off"
	FTPTLSExplicit = "explicit"
	FTPTLSImplicit = "implicit"
)

type Host struct {
	Id                    string           `json:"id"`
	Kind                  HostKind         `json:"kind"`
	IP                    string           `json:"ip"`
	Name                  string           `json:"name"`
	GroupId               string           `json:"group_id"`
	Description           string           `json:"description"`
	Port                  int              `json:"port"`
	User                  string           `json:"user"`
	TLSMode               string           `json:"tls_mode,omitempty"`
	FTPActiveMode         bool             `json:"ftp_active_mode,omitempty"`
	TerminalProtocol      TerminalProtocol `json:"terminal_protocol,omitempty"`
	TerminalEncoding      string           `json:"terminal_encoding,omitempty"`
	TerminalInputEncoding string           `json:"terminal_input_encoding,omitempty"`
	FileProtocol          SSHFileProtocol  `json:"file_protocol,omitempty"`
	X11Forwarding         bool             `json:"x11_forwarding,omitempty"`
	SerialBaudRate        int              `json:"serial_baud_rate,omitempty"`
	SerialDataBits        int              `json:"serial_data_bits,omitempty"`
	SerialParity          string           `json:"serial_parity,omitempty"`
	SerialStopBits        string           `json:"serial_stop_bits,omitempty"`
	SerialFlowControl     string           `json:"serial_flow_control,omitempty"`
	Favorite              bool             `json:"favorite,omitempty"`
	// FavoriteOrder is local UI metadata and is not included in exported sessions.
	FavoriteOrder int `json:"-"`
	// LastConnectedAt is local usage metadata and is never exported in a catalog.
	LastConnectedAt time.Time `json:"-"`
	// Password is transient; persistence and JSON output must never store it.
	Password string `json:"-"`
	// CredentialRef is an opaque operating-system keychain reference, not the credential itself.
	CredentialRef string `json:"credential_ref"`
	// AuthProfileID points to a local reusable authentication profile. It is
	// intentionally omitted from portable session exports because the profile
	// and its keychain reference are local to this installation.
	AuthProfileID string `json:"-"`
	// IdentityFile is a local SSH private-key path; private-key contents are never stored.
	IdentityFile string `json:"-"`
	// LocalStartDir is a machine-local starting folder for dual-pane file sessions.
	LocalStartDir string `json:"-"`
	// RemoteStartDir is the initial remote folder for file sessions.
	RemoteStartDir string `json:"remote_start_dir,omitempty"`
	Startup      string `json:"startup"`
	// StartupScriptPath points to a machine-local send/expect script. The file
	// path is deliberately excluded from portable session exports.
	StartupScriptPath string `json:"-"`
	// ProxyJumpList contains the ordered host IDs used to build an SSH tunnel chain.
	ProxyJumpList []string `json:"proxy_jump_list"`
	// ConnectionStatus is runtime state and is not part of saved host settings.
	ConnectionStatus consts.ConnectionStatus `json:"-"`
}

// HostBatchPatch contains only the host fields explicitly selected for a
// multi-host edit. Nil fields are left untouched; pointers also allow callers
// to intentionally clear string values and unassign a group.
type HostBatchPatch struct {
	GroupID      *string
	Favorite     *bool
	Description  *string
	User         *string
	Port         *int
	IdentityFile *string
	// Names maps every selected host ID to its replacement name. When present,
	// it must contain exactly one non-empty name for every host in the batch.
	Names map[string]string
}

// AuthProfile stores reusable SSH authentication settings. CredentialRef is
// an opaque operating-system keychain reference; secrets are never persisted
// in SQLite or exported session files.
type AuthProfile struct {
	Id            string `json:"id"`
	Name          string `json:"name"`
	User          string `json:"user,omitempty"`
	CredentialRef string `json:"-"`
	IdentityFile  string `json:"-"`
}

// IdentityFileReference identifies a saved host or authentication profile that
// points at a local SSH private-key path. CurrentPath is the exact value read
// from storage and is used for optimistic concurrency checks during rotation.
type IdentityFileReference struct {
	HostID        string
	AuthProfileID string
	CurrentPath   string
}

// WithAuthProfile overlays non-empty profile values on a host. Empty profile
// fields deliberately fall back to the host's per-connection settings.
func (host Host) WithAuthProfile(profile AuthProfile) Host {
	if profile.User != "" {
		host.User = profile.User
	}
	if profile.CredentialRef != "" {
		host.CredentialRef = profile.CredentialRef
	}
	if profile.IdentityFile != "" {
		host.IdentityFile = profile.IdentityFile
	}
	return host
}

// PortForwardKind identifies a persisted SSH port-forward rule.
type PortForwardKind string

const (
	PortForwardLocalTCP  PortForwardKind = "local_tcp"
	PortForwardRemoteTCP PortForwardKind = "remote_tcp"
	PortForwardSOCKS5    PortForwardKind = "socks5"
)

// PortForwardRule stores an SSH forwarding rule that is restored whenever its
// owning host is connected. ListenAddress is local for local/SOCKS rules and
// remote for remote TCP rules; RemoteAddress is empty for SOCKS5.
type PortForwardRule struct {
	Id            string          `json:"id"`
	HostId        string          `json:"host_id"`
	Kind          PortForwardKind `json:"kind"`
	ListenAddress string          `json:"listen_address"`
	RemoteAddress string          `json:"remote_address"`
}
