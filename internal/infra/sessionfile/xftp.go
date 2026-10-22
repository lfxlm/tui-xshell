package sessionfile

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"tui-ssh-workbench/internal/model"
)

// DecodeXFTP imports the common connection fields from an Xftp .xfp session.
// It deliberately requires an explicit SFTP or FTP protocol so an unknown
// native value can never silently become a weaker connection type.
func DecodeXFTP(data []byte, filename string) (Catalog, error) {
	return decodeXFTP(data, filename, "")
}

func decodeXFTP(data []byte, filename, identity string) (Catalog, error) {
	if len(data) > MaxFileBytes {
		return Catalog{}, fmt.Errorf("Xftp session exceeds %d bytes", MaxFileBytes)
	}
	text, err := decodeXShellText(data)
	if err != nil {
		return Catalog{}, fmt.Errorf("decode Xftp session: %w", err)
	}
	sections, err := parseNativeSessionINI(text)
	if err != nil {
		return Catalog{}, err
	}
	connection := sections["connection"]
	if connection == nil {
		return Catalog{}, errors.New("Xftp 会话缺少 Connection 分节")
	}
	protocol := strings.ToLower(strings.TrimSpace(connection["protocol"]))
	var kind model.HostKind
	var fileProtocol model.SSHFileProtocol
	var defaultPort int
	switch protocol {
	case "sftp":
		kind, fileProtocol, defaultPort = model.HostKindSSH, model.SSHFileProtocolSFTP, 22
	case "scp":
		kind, fileProtocol, defaultPort = model.HostKindSSH, model.SSHFileProtocolSCP, 22
	case "ftp":
		kind, defaultPort = model.HostKindFTP, 21
	default:
		return Catalog{}, fmt.Errorf("Xftp 会话协议 %q 暂不支持导入", connection["protocol"])
	}
	hostname := strings.TrimSpace(connection["host"])
	if hostname == "" {
		hostname = strings.TrimSpace(connection["hostname"])
	}
	if hostname == "" {
		return Catalog{}, errors.New("Xftp 会话缺少连接主机")
	}
	port := defaultPort
	if value := strings.TrimSpace(connection["port"]); value != "" {
		port, err = strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return Catalog{}, errors.New("Xftp 会话端口必须是 1 到 65535 之间的整数")
		}
	}
	username := strings.TrimSpace(sections["connection:authentication"]["username"])
	if username == "" {
		username = strings.TrimSpace(connection["username"])
	}
	if username == "" {
		username = strings.TrimSpace(sections["connection:ftp"]["username"])
	}
	if !utf8.ValidString(hostname) || !utf8.ValidString(username) {
		return Catalog{}, errors.New("Xftp 会话的主机名或用户名不是有效 UTF-8")
	}
	info := sections["sessioninfo"]
	name := strings.TrimSpace(info["sessionname"])
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	}
	if name == "" || name == "." {
		name = hostname
	}
	description := strings.TrimSpace(info["description"])
	host := model.Host{
		Id:   xftpHostID(kind, fileProtocol, name, hostname, port, username, identity),
		Kind: kind, Name: name, IP: hostname, Port: port, User: username, FileProtocol: fileProtocol,
		Description: description,
	}
	if kind == model.HostKindFTP {
		host.TLSMode = model.FTPTLSOff
	}
	return NewCatalog(nil, []model.Host{host}, nil), nil
}

func xftpHostID(kind model.HostKind, fileProtocol model.SSHFileProtocol, name, host string, port int, username, identity string) string {
	parts := []string{string(kind), name, host, strconv.Itoa(port), username, identity}
	if fileProtocol == model.SSHFileProtocolSCP {
		// Keep the established SFTP/FTP IDs stable across upgrades while
		// preventing a same-named SCP session from colliding with SFTP.
		parts = append([]string{"scp"}, parts...)
	}
	key := strings.Join(parts, "\x00")
	digest := sha256.Sum256([]byte(key))
	return "xfp-" + hex.EncodeToString(digest[:12])
}

func encodeXFTP(host model.Host, name string) []byte {
	protocol := "SFTP"
	if host.Kind == model.HostKindFTP {
		protocol = "FTP"
	} else if host.FileProtocol == model.SSHFileProtocolSCP {
		protocol = "SCP"
	}
	text := "[SessionInfo]\r\nVersion=8.0\r\nSessionName=" + name + "\r\nDescription=" + host.Description + "\r\n\r\n" +
		"[Connection]\r\nProtocol=" + protocol + "\r\nHost=" + host.IP + "\r\nPort=" + strconv.Itoa(host.Port) + "\r\n\r\n" +
		"[Connection:Authentication]\r\nUserName=" + host.User + "\r\n"
	units := utf16.Encode([]rune(text))
	data := []byte{0xff, 0xfe}
	for _, unit := range units {
		var encoded [2]byte
		binary.LittleEndian.PutUint16(encoded[:], unit)
		data = append(data, encoded[:]...)
	}
	return data
}
