// Package sessionfile reads and writes portable session catalogs and imports
// or exports NetSarang native session files and directory trees.
package sessionfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tui-ssh-workbench/internal/model"
)

const (
	Format       = "tui-ssh-workbench/session-catalog"
	Version      = 12
	MaxFileBytes = 16 << 20
)

// Catalog is a versioned export of saved hosts, groups and auto-start
// forwarding rules. Passwords, runtime state and machine-local credential
// references are intentionally excluded.
type Catalog struct {
	Format           string                  `json:"format"`
	Version          int                     `json:"version"`
	ExportedAt       time.Time               `json:"exported_at"`
	Groups           []model.Group           `json:"groups"`
	Hosts            []hostRecord            `json:"hosts"`
	PortForwardRules []model.PortForwardRule `json:"port_forward_rules"`
}

type hostRecord struct {
	Id                    string                 `json:"id"`
	Kind                  model.HostKind         `json:"kind"`
	IP                    string                 `json:"ip"`
	Name                  string                 `json:"name"`
	GroupId               string                 `json:"group_id"`
	Description           string                 `json:"description"`
	Port                  int                    `json:"port"`
	User                  string                 `json:"user"`
	TLSMode               string                 `json:"tls_mode,omitempty"`
	FTPActiveMode         bool                   `json:"ftp_active_mode,omitempty"`
	RemoteStartDir        string                 `json:"remote_start_dir,omitempty"`
	TerminalProtocol      model.TerminalProtocol `json:"terminal_protocol,omitempty"`
	TerminalEncoding      string                 `json:"terminal_encoding,omitempty"`
	TerminalInputEncoding string                 `json:"terminal_input_encoding,omitempty"`
	FileProtocol          model.SSHFileProtocol  `json:"file_protocol,omitempty"`
	X11Forwarding         bool                   `json:"x11_forwarding,omitempty"`
	SerialBaudRate        int                    `json:"serial_baud_rate,omitempty"`
	SerialDataBits        int                    `json:"serial_data_bits,omitempty"`
	SerialParity          string                 `json:"serial_parity,omitempty"`
	SerialStopBits        string                 `json:"serial_stop_bits,omitempty"`
	SerialFlowControl     string                 `json:"serial_flow_control,omitempty"`
	Favorite              bool                   `json:"favorite,omitempty"`
	ProxyJumpList         []string               `json:"proxy_jump_list"`
}

// NewCatalog creates a portable snapshot and removes machine-local data.
func NewCatalog(groups []model.Group, hosts []model.Host, rules []model.PortForwardRule) Catalog {
	catalog := Catalog{
		Format: Format, Version: Version, ExportedAt: time.Now().UTC(),
		Groups:           append([]model.Group{}, groups...),
		Hosts:            make([]hostRecord, 0, len(hosts)),
		PortForwardRules: append([]model.PortForwardRule{}, rules...),
	}
	for _, host := range hosts {
		fileProtocol := host.FileProtocol
		if host.Kind == model.HostKindSSH && fileProtocol == "" {
			fileProtocol = model.SSHFileProtocolSFTP
		}
		catalog.Hosts = append(catalog.Hosts, hostRecord{
			Id: host.Id, Kind: host.Kind, IP: host.IP, Name: host.Name,
			GroupId: host.GroupId, Description: host.Description, Port: host.Port,
			User: host.User, TLSMode: host.TLSMode, FTPActiveMode: host.Kind == model.HostKindFTP && host.FTPActiveMode, RemoteStartDir: host.RemoteStartDir, TerminalProtocol: normalizedTerminalProtocol(host), TerminalEncoding: normalizedTerminalEncoding(host), TerminalInputEncoding: host.TerminalInputEncoding, FileProtocol: fileProtocol,
			X11Forwarding:  host.Kind == model.HostKindSSH && host.X11Forwarding,
			SerialBaudRate: host.SerialBaudRate, SerialDataBits: host.SerialDataBits,
			SerialParity: host.SerialParity, SerialStopBits: host.SerialStopBits,
			SerialFlowControl: normalizedSerialFlowControl(host),
			Favorite:          host.Favorite,
			ProxyJumpList:     append([]string{}, host.ProxyJumpList...),
		})
	}
	return catalog
}

// HostRecords converts the portable host records to runtime models. Secret and
// machine-local fields remain empty by construction.
func (c Catalog) HostRecords() []model.Host {
	hosts := make([]model.Host, 0, len(c.Hosts))
	for _, record := range c.Hosts {
		fileProtocol := record.FileProtocol
		if record.Kind == model.HostKindSSH && fileProtocol == "" {
			// Catalog versions before 3 treated every SSH file connection as SFTP.
			fileProtocol = model.SSHFileProtocolSFTP
		}
		terminalProtocol := record.TerminalProtocol
		if record.Kind == model.HostKindTelnet && terminalProtocol == "" {
			terminalProtocol = model.TerminalProtocolTelnet
		}
		hosts = append(hosts, model.Host{
			Id: record.Id, Kind: record.Kind, IP: record.IP, Name: record.Name,
			GroupId: record.GroupId, Description: record.Description, Port: record.Port,
			User: record.User, TLSMode: record.TLSMode, FTPActiveMode: record.Kind == model.HostKindFTP && record.FTPActiveMode, RemoteStartDir: record.RemoteStartDir, TerminalProtocol: terminalProtocol, TerminalEncoding: record.TerminalEncoding, TerminalInputEncoding: record.TerminalInputEncoding, FileProtocol: fileProtocol,
			X11Forwarding:  record.Kind == model.HostKindSSH && record.X11Forwarding,
			SerialBaudRate: record.SerialBaudRate, SerialDataBits: record.SerialDataBits,
			SerialParity: record.SerialParity, SerialStopBits: record.SerialStopBits,
			SerialFlowControl: record.SerialFlowControl,
			Favorite:          record.Favorite,
			ProxyJumpList:     append([]string(nil), record.ProxyJumpList...),
		})
	}
	return hosts
}

func normalizedSerialFlowControl(host model.Host) string {
	if host.Kind == model.HostKindSerial && host.SerialFlowControl == "" {
		return model.SerialFlowControlNone
	}
	return host.SerialFlowControl
}

func normalizedTerminalProtocol(host model.Host) model.TerminalProtocol {
	if host.Kind == model.HostKindTelnet && host.TerminalProtocol == "" {
		return model.TerminalProtocolTelnet
	}
	return host.TerminalProtocol
}

func normalizedTerminalEncoding(host model.Host) string {
	if host.Kind == model.HostKindFTP {
		return ""
	}
	if host.TerminalEncoding == "" {
		return model.TerminalEncodingUTF8
	}
	return host.TerminalEncoding
}

// Encode serializes a catalog as indented UTF-8 JSON.
func (c Catalog) Encode() ([]byte, error) {
	if c.Format != Format || c.Version < 1 || c.Version > Version {
		return nil, errors.New("session catalog format or version is invalid")
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode session catalog: %w", err)
	}
	return append(data, '\n'), nil
}

// Decode validates the file format and rejects unknown fields so a future
// catalog cannot be silently imported with lost settings.
func Decode(data []byte) (Catalog, error) {
	if len(data) > MaxFileBytes {
		return Catalog{}, fmt.Errorf("session catalog exceeds %d bytes", MaxFileBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode session catalog: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Catalog{}, errors.New("session catalog contains multiple JSON values")
		}
		return Catalog{}, fmt.Errorf("decode trailing session catalog data: %w", err)
	}
	if catalog.Format != Format {
		return Catalog{}, fmt.Errorf("unsupported session catalog format %q", catalog.Format)
	}
	if catalog.Version < 1 || catalog.Version > Version {
		return Catalog{}, fmt.Errorf("unsupported session catalog version %d", catalog.Version)
	}
	if catalog.Groups == nil {
		catalog.Groups = []model.Group{}
	}
	if catalog.Hosts == nil {
		catalog.Hosts = []hostRecord{}
	}
	if catalog.PortForwardRules == nil {
		catalog.PortForwardRules = []model.PortForwardRule{}
	}
	return catalog, nil
}

// ReadFile reads a session catalog, a supported native session file, or an
// Xshell session directory with strict size limits.
func ReadFile(path string) (Catalog, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Catalog{}, fmt.Errorf("stat session catalog: %w", err)
	}
	if info.IsDir() {
		return ReadNativeSessionDirectory(path)
	}
	if !info.Mode().IsRegular() {
		return Catalog{}, errors.New("session catalog must be a regular file or native session directory")
	}
	file, err := os.Open(path)
	if err != nil {
		return Catalog{}, fmt.Errorf("open session catalog: %w", err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return Catalog{}, fmt.Errorf("stat session catalog: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Catalog{}, errors.New("session catalog must be a regular file")
	}
	if info.Size() > MaxFileBytes {
		return Catalog{}, fmt.Errorf("session catalog exceeds %d bytes", MaxFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return Catalog{}, fmt.Errorf("read session catalog: %w", err)
	}
	if len(data) > MaxFileBytes {
		return Catalog{}, fmt.Errorf("session catalog exceeds %d bytes", MaxFileBytes)
	}
	if strings.EqualFold(filepath.Ext(path), ".xsh") {
		return DecodeXShell(data, filepath.Base(path))
	}
	if strings.EqualFold(filepath.Ext(path), ".xfp") {
		return DecodeXFTP(data, filepath.Base(path))
	}
	return Decode(data)
}

// WriteFile creates a new owner-readable catalog file and refuses to replace
// an existing path. The caller can review the destination before exporting.
func WriteFile(path string, catalog Catalog) error {
	data, err := catalog.Encode()
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create session catalog (existing files are not overwritten): %w", err)
	}
	writeErr := error(nil)
	if _, writeErr = file.Write(data); writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write session catalog: %w", writeErr)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close session catalog: %w", closeErr)
	}
	return nil
}
