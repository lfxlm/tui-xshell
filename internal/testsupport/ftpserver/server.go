// Package ftpserver contains a small FTP/FTPS integration server used by tests.
package ftpserver

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"path"
	"strconv"
	"strings"
	"time"
)

type Upload struct {
	Path    string
	Content []byte
}

type Server struct {
	Address     string
	RestOffsets chan uint64
	Uploads     chan Upload

	listener net.Listener
	finished chan struct{}
}

// Start starts a one-session FTP server. mode accepts off, explicit, or
// implicit. The server supports the commands used by the workbench client.
func Start(mode string, certificate tls.Certificate, initialFiles ...map[string][]byte) (*Server, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{"/hello.txt": []byte("hello")}
	if len(initialFiles) > 0 {
		files = make(map[string][]byte, len(initialFiles[0]))
		for name, content := range initialFiles[0] {
			files[name] = append([]byte(nil), content...)
		}
	}
	directories := map[string]bool{"/": true}
	for remotePath := range files {
		for directory := path.Dir(remotePath); directory != "/" && directory != "."; directory = path.Dir(directory) {
			directories[directory] = true
		}
	}
	server := &Server{
		Address: listener.Addr().String(), RestOffsets: make(chan uint64, 16),
		Uploads: make(chan Upload, 16), listener: listener, finished: make(chan struct{}),
	}
	go server.serve(mode, certificate, files, directories)
	return server, nil
}

func (server *Server) Close() error {
	if server == nil {
		return nil
	}
	_ = server.listener.Close()
	select {
	case <-server.finished:
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("test FTP server did not stop")
	}
}

func (server *Server) serve(mode string, certificate tls.Certificate, files map[string][]byte, directories map[string]bool) {
	defer close(server.finished)
	conn, err := server.listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	secure := mode == "implicit"
	if secure {
		conn = tls.Server(conn, testTLSConfig(certificate))
		if err := conn.(*tls.Conn).Handshake(); err != nil {
			return
		}
	}
	control := textproto.NewConn(conn)
	if _, err := fmt.Fprint(conn, "220 test FTP ready\r\n"); err != nil {
		return
	}
	var dataListener net.Listener
	var renameSource string
	var restOffset uint64
	defer func() {
		if dataListener != nil {
			_ = dataListener.Close()
		}
	}()
	for {
		line, err := control.ReadLine()
		if err != nil {
			return
		}
		command, argument, _ := strings.Cut(line, " ")
		switch strings.ToUpper(command) {
		case "AUTH":
			if mode != "explicit" || !strings.EqualFold(argument, "TLS") {
				_, _ = fmt.Fprint(conn, "502 unsupported\r\n")
				continue
			}
			_, _ = fmt.Fprint(conn, "234 begin TLS\r\n")
			conn = tls.Server(conn, testTLSConfig(certificate))
			if err := conn.(*tls.Conn).Handshake(); err != nil {
				return
			}
			control = textproto.NewConn(conn)
			secure = true
		case "USER":
			_, _ = fmt.Fprint(conn, "331 password required\r\n")
		case "PASS":
			_, _ = fmt.Fprint(conn, "230 logged in\r\n")
		case "FEAT":
			_, _ = fmt.Fprint(conn, "211-Features:\r\n MDTM\r\n211 End\r\n")
		case "TYPE", "PBSZ", "PROT", "OPTS":
			_, _ = fmt.Fprint(conn, "200 ok\r\n")
		case "MDTM":
			_, _ = fmt.Fprint(conn, "213 20261020120000\r\n")
		case "REST":
			restOffset, err = strconv.ParseUint(argument, 10, 64)
			if err != nil {
				_, _ = fmt.Fprint(conn, "501 invalid offset\r\n")
				continue
			}
			select {
			case server.RestOffsets <- restOffset:
			default:
			}
			_, _ = fmt.Fprint(conn, "350 restart position accepted\r\n")
		case "PWD":
			_, _ = fmt.Fprint(conn, "257 \"/\" is current directory\r\n")
		case "SIZE":
			content, found := files[argument]
			if found {
				_, _ = fmt.Fprintf(conn, "213 %d\r\n", len(content))
			} else {
				_, _ = fmt.Fprint(conn, "550 file not found\r\n")
			}
		case "RNFR":
			renameSource = argument
			_, _ = fmt.Fprint(conn, "350 ready for destination\r\n")
		case "RNTO":
			content, found := files[renameSource]
			if !found {
				_, _ = fmt.Fprint(conn, "550 source not found\r\n")
				continue
			}
			files[argument] = content
			delete(files, renameSource)
			renameSource = ""
			select {
			case server.Uploads <- Upload{Path: argument, Content: append([]byte(nil), content...)}:
			default:
			}
			_, _ = fmt.Fprint(conn, "250 renamed\r\n")
		case "DELE":
			delete(files, argument)
			_, _ = fmt.Fprint(conn, "250 deleted\r\n")
		case "MKD":
			directories[path.Clean(argument)] = true
			_, _ = fmt.Fprintf(conn, "257 %q created\r\n", path.Clean(argument))
		case "EPSV":
			if dataListener != nil {
				_ = dataListener.Close()
			}
			dataListener, err = net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				_, _ = fmt.Fprint(conn, "425 no data listener\r\n")
				continue
			}
			port := dataListener.Addr().(*net.TCPAddr).Port
			_, _ = fmt.Fprintf(conn, "229 Entering Extended Passive Mode (|||%d|)\r\n", port)
		case "LIST":
			if dataListener == nil {
				_, _ = fmt.Fprint(conn, "425 no passive listener\r\n")
				continue
			}
			_, _ = fmt.Fprint(conn, "150 opening data\r\n")
			data, acceptErr := dataListener.Accept()
			_ = dataListener.Close()
			dataListener = nil
			if acceptErr != nil {
				return
			}
			if secure {
				data = tls.Server(data, testTLSConfig(certificate))
				if err := data.(*tls.Conn).Handshake(); err != nil {
					_ = data.Close()
					return
				}
			}
			listArgument := strings.TrimSpace(strings.TrimPrefix(argument, "-a"))
			listDirectory := path.Clean(listArgument)
			if listDirectory == "." || listDirectory == "" {
				listDirectory = "/"
			}
			type listedEntry struct {
				name string
				size int
				dir  bool
			}
			entries := make([]listedEntry, 0)
			for directory := range directories {
				if directory != listDirectory && path.Dir(directory) == listDirectory {
					entries = append(entries, listedEntry{name: path.Base(directory), dir: true})
				}
			}
			for remotePath, content := range files {
				if path.Dir(remotePath) == listDirectory {
					entries = append(entries, listedEntry{name: path.Base(remotePath), size: len(content)})
				}
			}
			for _, entry := range entries {
				permissions := "-rw-r--r--"
				if entry.dir {
					permissions = "drwxr-xr-x"
				}
				if _, err := fmt.Fprintf(data, "%s 1 tester users %d Jan 02 15:04 %s\r\n", permissions, entry.size, entry.name); err != nil {
					_ = data.Close()
					return
				}
			}
			_ = data.Close()
			_, _ = fmt.Fprint(conn, "226 transfer complete\r\n")
		case "RETR", "STOR":
			if dataListener == nil {
				_, _ = fmt.Fprint(conn, "425 no passive listener\r\n")
				continue
			}
			_, _ = fmt.Fprint(conn, "150 opening data\r\n")
			data, acceptErr := dataListener.Accept()
			_ = dataListener.Close()
			dataListener = nil
			if acceptErr != nil {
				return
			}
			if secure {
				data = tls.Server(data, testTLSConfig(certificate))
				if err := data.(*tls.Conn).Handshake(); err != nil {
					_ = data.Close()
					return
				}
			}
			if strings.EqualFold(command, "RETR") {
				content := files[argument]
				if restOffset > uint64(len(content)) {
					_ = data.Close()
					_, _ = fmt.Fprint(conn, "551 restart position outside file\r\n")
					continue
				}
				_, _ = data.Write(content[restOffset:])
			} else {
				content, readErr := io.ReadAll(data)
				if readErr != nil {
					_ = data.Close()
					return
				}
				prefix := files[argument]
				if restOffset > uint64(len(prefix)) {
					_ = data.Close()
					_, _ = fmt.Fprint(conn, "551 restart position outside file\r\n")
					continue
				}
				prefix = prefix[:restOffset]
				files[argument] = append(prefix, content...)
			}
			restOffset = 0
			_ = data.Close()
			_, _ = fmt.Fprint(conn, "226 transfer complete\r\n")
		case "QUIT":
			_, _ = fmt.Fprint(conn, "221 goodbye\r\n")
			return
		default:
			_, _ = fmt.Fprint(conn, "502 unsupported\r\n")
		}
	}
}

func testTLSConfig(certificate tls.Certificate) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
}
