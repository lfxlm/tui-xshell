package ftpclient

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCopyRemoteFileToUsesFXPBetweenPlainFTPServers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sourceServer := startFXPTestServer(t, map[string][]byte{"/source.txt": []byte("server-to-server payload")}, nil)
	destinationServer := startFXPTestServer(t, nil, []string{"/target"})

	source := dialFXPTestClient(t, sourceServer.address)
	destination := dialFXPTestClient(t, destinationServer.address)
	var progress []int64
	err := source.CopyRemoteFileTo(ctx, destination, "/source.txt", "/target/copied.txt", false, func(current, _ int64) {
		progress = append(progress, current)
	})
	if err != nil {
		t.Fatalf("copy remote file with FXP: %v", err)
	}

	content, exists := destinationServer.file("/target/copied.txt")
	if got := string(content); !exists || got != "server-to-server payload" {
		t.Fatalf("destination file = %q", got)
	}
	sourceServer.mu.Lock()
	activeRetr, passiveRetr := sourceServer.activeRetr, sourceServer.passiveRetr
	sourceServer.mu.Unlock()
	if activeRetr != 1 || passiveRetr != 0 {
		t.Fatalf("source RETR mode = active %d / passive %d; want one server-initiated FXP transfer", activeRetr, passiveRetr)
	}
	if len(progress) == 0 || progress[len(progress)-1] != int64(len("server-to-server payload")) {
		t.Fatalf("FXP progress = %v", progress)
	}
}

func TestCopyRemoteFileToFallsBackWhenSourceRejectsPORT(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sourceServer := startFXPTestServer(t, map[string][]byte{"/source.txt": []byte("fallback payload")}, nil)
	sourceServer.rejectPORT.Store(true)
	destinationServer := startFXPTestServer(t, nil, []string{"/target"})

	source := dialFXPTestClient(t, sourceServer.address)
	destination := dialFXPTestClient(t, destinationServer.address)
	if err := source.CopyRemoteFileTo(ctx, destination, "/source.txt", "/target/copied.txt", false, nil); err != nil {
		t.Fatalf("copy remote file after FXP refusal: %v", err)
	}
	content, exists := destinationServer.file("/target/copied.txt")
	if got := string(content); !exists || got != "fallback payload" {
		t.Fatalf("fallback destination file = %q", got)
	}
	sourceServer.mu.Lock()
	activeRetr, passiveRetr := sourceServer.activeRetr, sourceServer.passiveRetr
	sourceServer.mu.Unlock()
	if activeRetr != 0 || passiveRetr != 1 {
		t.Fatalf("fallback RETR mode = active %d / passive %d", activeRetr, passiveRetr)
	}
}

func TestCopyRemoteDirectoryToUsesFXPPerFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sourceServer := startFXPTestServer(t, map[string][]byte{
		"/backup/root.txt":          []byte("root"),
		"/backup/nested/config.ini": []byte("nested-config"),
	}, []string{"/backup", "/backup/nested"})
	destinationServer := startFXPTestServer(t, nil, []string{"/target"})
	source := dialFXPTestClient(t, sourceServer.address)
	destination := dialFXPTestClient(t, destinationServer.address)

	var lastProgress int64
	if err := source.CopyRemoteDirectoryTo(ctx, destination, "/backup", "/target/copy", false, func(current, _ int64) {
		lastProgress = current
	}); err != nil {
		t.Fatalf("copy remote directory with FXP: %v", err)
	}
	for filePath, want := range map[string]string{
		"/target/copy/root.txt":          "root",
		"/target/copy/nested/config.ini": "nested-config",
	} {
		content, exists := destinationServer.file(filePath)
		if !exists || string(content) != want {
			t.Errorf("destination file %s = %q, exists=%v; want %q", filePath, content, exists, want)
		}
	}
	sourceServer.mu.Lock()
	activeRetr, passiveRetr := sourceServer.activeRetr, sourceServer.passiveRetr
	sourceServer.mu.Unlock()
	if activeRetr != 2 || passiveRetr != 0 {
		t.Fatalf("directory RETR mode = active %d / passive %d; want two FXP files", activeRetr, passiveRetr)
	}
	if want := int64(len("root") + len("nested-config")); lastProgress != want {
		t.Fatalf("final directory progress = %d; want %d", lastProgress, want)
	}
}

func TestCopyRemoteFileToReplacesTargetOnlyAfterFXPCompletes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sourceServer := startFXPTestServer(t, map[string][]byte{"/source.txt": []byte("new content")}, nil)
	destinationServer := startFXPTestServer(t, map[string][]byte{"/target/copied.txt": []byte("old content")}, []string{"/target"})
	source := dialFXPTestClient(t, sourceServer.address)
	destination := dialFXPTestClient(t, destinationServer.address)

	if err := source.CopyRemoteFileTo(ctx, destination, "/source.txt", "/target/copied.txt", true, nil); err != nil {
		t.Fatalf("overwrite remote target with FXP: %v", err)
	}
	content, exists := destinationServer.file("/target/copied.txt")
	if !exists || string(content) != "new content" {
		t.Fatalf("overwritten destination = %q, exists=%v", content, exists)
	}
	destinationServer.mu.Lock()
	fileCount := len(destinationServer.files)
	destinationServer.mu.Unlock()
	if fileCount != 1 {
		t.Fatalf("destination has %d files after atomic replacement, want one", fileCount)
	}
}

func dialFXPTestClient(t *testing.T, address string) *Client {
	t.Helper()
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	client, err := Dial(context.Background(), Config{
		Address: host, Port: port, Username: "tester", Password: "test-secret", TLSMode: TLSOff,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("dial FTP test client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

type fxpTestServer struct {
	address     string
	listener    net.Listener
	acceptDone  chan struct{}
	workers     sync.WaitGroup
	mu          sync.Mutex
	files       map[string][]byte
	directories map[string]bool
	activeRetr  int
	passiveRetr int
	rejectPORT  atomic.Bool
}

func startFXPTestServer(t *testing.T, files map[string][]byte, directories []string) *fxpTestServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fxpTestServer{
		address: listener.Addr().String(), listener: listener, acceptDone: make(chan struct{}),
		files: make(map[string][]byte, len(files)), directories: map[string]bool{"/": true},
	}
	for filePath, content := range files {
		server.files[path.Clean(filePath)] = append([]byte(nil), content...)
		for directory := path.Dir(path.Clean(filePath)); directory != "/" && directory != "."; directory = path.Dir(directory) {
			server.directories[directory] = true
		}
	}
	for _, directory := range directories {
		server.directories[path.Clean(directory)] = true
	}
	go server.acceptLoop()
	t.Cleanup(func() {
		_ = listener.Close()
		<-server.acceptDone
		server.workers.Wait()
	})
	return server
}

func (server *fxpTestServer) acceptLoop() {
	defer close(server.acceptDone)
	for {
		conn, err := server.listener.Accept()
		if err != nil {
			return
		}
		server.workers.Add(1)
		go func() {
			defer server.workers.Done()
			server.serve(conn)
		}()
	}
}

func (server *fxpTestServer) serve(conn net.Conn) {
	defer conn.Close()
	control := textproto.NewConn(conn)
	_, _ = fmt.Fprint(conn, "220 FXP test server ready\r\n")
	var passive net.Listener
	var activeAddress string
	var renameSource string
	defer func() {
		if passive != nil {
			_ = passive.Close()
		}
	}()
	for {
		line, err := control.ReadLine()
		if err != nil {
			return
		}
		command, argument, _ := strings.Cut(line, " ")
		switch strings.ToUpper(command) {
		case "USER":
			_, _ = fmt.Fprint(conn, "331 password required\r\n")
		case "PASS":
			_, _ = fmt.Fprint(conn, "230 logged in\r\n")
		case "TYPE", "OPTS":
			_, _ = fmt.Fprint(conn, "200 ok\r\n")
		case "MKD":
			server.mu.Lock()
			server.directories[path.Clean(argument)] = true
			server.mu.Unlock()
			_, _ = fmt.Fprint(conn, "257 created\r\n")
		case "FEAT":
			_, _ = fmt.Fprint(conn, "502 unsupported\r\n")
		case "EPSV":
			if passive != nil {
				_ = passive.Close()
			}
			passive, err = net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				_, _ = fmt.Fprint(conn, "425 no passive port\r\n")
				continue
			}
			port := passive.Addr().(*net.TCPAddr).Port
			_, _ = fmt.Fprintf(conn, "229 Entering Extended Passive Mode (|||%d|)\r\n", port)
		case "PORT":
			if server.rejectPORT.Load() {
				_, _ = fmt.Fprint(conn, "501 PORT disabled\r\n")
				continue
			}
			parts := strings.Split(argument, ",")
			if len(parts) != 6 {
				_, _ = fmt.Fprint(conn, "501 invalid PORT\r\n")
				continue
			}
			values := make([]int, 6)
			valid := true
			for index, part := range parts {
				values[index], err = strconv.Atoi(part)
				if err != nil {
					valid = false
					break
				}
			}
			if !valid {
				_, _ = fmt.Fprint(conn, "501 invalid PORT\r\n")
				continue
			}
			activeAddress = fmt.Sprintf("%d.%d.%d.%d:%d", values[0], values[1], values[2], values[3], values[4]*256+values[5])
			_, _ = fmt.Fprint(conn, "200 PORT accepted\r\n")
		case "LIST":
			data, ok := server.acceptPassive(&passive)
			if !ok {
				_, _ = fmt.Fprint(conn, "425 no passive connection\r\n")
				continue
			}
			_, _ = fmt.Fprint(conn, "150 opening list\r\n")
			_, _ = io.WriteString(data, server.listing(argument))
			_ = data.Close()
			_, _ = fmt.Fprint(conn, "226 list complete\r\n")
		case "SIZE":
			content, exists := server.file(argument)
			if !exists {
				_, _ = fmt.Fprint(conn, "550 not found\r\n")
				continue
			}
			_, _ = fmt.Fprintf(conn, "213 %d\r\n", len(content))
		case "RETR":
			server.mu.Lock()
			content, exists := server.files[path.Clean(argument)]
			content = append([]byte(nil), content...)
			if activeAddress == "" {
				server.passiveRetr++
			} else {
				server.activeRetr++
			}
			server.mu.Unlock()
			if !exists {
				_, _ = fmt.Fprint(conn, "550 not found\r\n")
				continue
			}
			_, _ = fmt.Fprint(conn, "150 opening source\r\n")
			var data net.Conn
			if activeAddress != "" {
				data, err = net.DialTimeout("tcp", activeAddress, time.Second)
				activeAddress = ""
			} else {
				data, _ = server.acceptPassive(&passive)
			}
			if err != nil || data == nil {
				_, _ = fmt.Fprint(conn, "425 data connection failed\r\n")
				continue
			}
			_, _ = data.Write(content)
			_ = data.Close()
			_, _ = fmt.Fprint(conn, "226 source complete\r\n")
		case "STOR":
			_, _ = fmt.Fprint(conn, "150 opening target\r\n")
			data, ok := server.acceptPassive(&passive)
			if !ok {
				_, _ = fmt.Fprint(conn, "425 data connection failed\r\n")
				continue
			}
			content, readErr := io.ReadAll(data)
			_ = data.Close()
			if readErr != nil {
				_, _ = fmt.Fprint(conn, "426 data read failed\r\n")
				continue
			}
			server.mu.Lock()
			server.files[path.Clean(argument)] = content
			server.mu.Unlock()
			_, _ = fmt.Fprint(conn, "226 target complete\r\n")
		case "RNFR":
			renameSource = path.Clean(argument)
			_, _ = fmt.Fprint(conn, "350 ready to rename\r\n")
		case "RNTO":
			server.mu.Lock()
			content, exists := server.files[renameSource]
			if exists {
				server.files[path.Clean(argument)] = content
				delete(server.files, renameSource)
			}
			server.mu.Unlock()
			if !exists {
				_, _ = fmt.Fprint(conn, "550 rename source missing\r\n")
			} else {
				_, _ = fmt.Fprint(conn, "250 renamed\r\n")
			}
			renameSource = ""
		case "DELE":
			server.mu.Lock()
			delete(server.files, path.Clean(argument))
			server.mu.Unlock()
			_, _ = fmt.Fprint(conn, "250 deleted\r\n")
		case "QUIT":
			_, _ = fmt.Fprint(conn, "221 goodbye\r\n")
			return
		default:
			_, _ = fmt.Fprint(conn, "502 unsupported\r\n")
		}
	}
}

func (server *fxpTestServer) acceptPassive(listener *net.Listener) (net.Conn, bool) {
	if *listener == nil {
		return nil, false
	}
	current := *listener
	*listener = nil
	_ = current.(*net.TCPListener).SetDeadline(time.Now().Add(2 * time.Second))
	conn, err := current.Accept()
	_ = current.Close()
	if err != nil {
		return nil, false
	}
	return conn, true
}

func (server *fxpTestServer) listing(argument string) string {
	remoteDirectory := strings.TrimSpace(strings.TrimPrefix(argument, "-a"))
	if remoteDirectory == "" {
		remoteDirectory = "/"
	}
	remoteDirectory = path.Clean(remoteDirectory)
	server.mu.Lock()
	defer server.mu.Unlock()
	var listing strings.Builder
	for directory := range server.directories {
		if directory != remoteDirectory && path.Dir(directory) == remoteDirectory {
			fmt.Fprintf(&listing, "drwxr-xr-x 1 tester users 0 Jan 02 15:04 %s\r\n", path.Base(directory))
		}
	}
	for filePath, content := range server.files {
		if path.Dir(filePath) == remoteDirectory {
			fmt.Fprintf(&listing, "-rw-r--r-- 1 tester users %d Jan 02 15:04 %s\r\n", len(content), path.Base(filePath))
		}
	}
	return listing.String()
}

func (server *fxpTestServer) file(filePath string) ([]byte, bool) {
	server.mu.Lock()
	defer server.mu.Unlock()
	content, exists := server.files[path.Clean(filePath)]
	return append([]byte(nil), content...), exists
}
