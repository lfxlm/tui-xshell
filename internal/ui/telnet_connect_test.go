package ui

import (
	"io"
	"net"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestTelnetHostConnectOpensTerminalPaneAndForwardsInput(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	type serverResult struct {
		initial []byte
		input   []byte
		err     error
	}
	server := make(chan serverResult, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			server <- serverResult{err: err}
			return
		}
		defer conn.Close()
		initial := make([]byte, 15)
		if _, err := io.ReadFull(conn, initial); err != nil {
			server <- serverResult{err: err}
			return
		}
		input := make([]byte, len("whoami\r"))
		if _, err := io.ReadFull(conn, input); err != nil {
			server <- serverResult{err: err}
			return
		}
		server <- serverResult{initial: initial, input: input}
	}()

	host := model.Host{
		Id: "telnet-host", Kind: model.HostKindTelnet, Name: "legacy console",
		IP: "127.0.0.1", Port: port,
	}
	state := modelState{config: model.DefaultConfig(), width: 100, height: 30}
	target := state.newTabTarget()
	connect := state.startHostConnect(host, target)
	if connect == nil {
		t.Fatal("Telnet connect did not return an asynchronous command")
	}
	message, ok := connect().(terminalConnectedMsg)
	if !ok || message.err != nil || message.session == nil {
		t.Fatalf("Telnet connect result = %#v, want a connected session", message)
	}
	updated, _ := state.Update(message)
	state = unwrapModel(t, updated)
	pane := state.activeTerminalPane()
	if pane == nil || pane.host.Kind != model.HostKindTelnet || pane.session == nil {
		t.Fatalf("active pane = %#v, want a Telnet terminal session", pane)
	}
	if err := pane.session.Write([]byte("whoami\r")); err != nil {
		t.Fatalf("send terminal input: %v", err)
	}
	result := <-server
	if result.err != nil {
		t.Fatalf("Telnet test server: %v", result.err)
	}
	wantInitial := []byte{255, 253, 1, 255, 253, 3, 255, 251, 3, 255, 251, 31, 255, 251, 24}
	if string(result.initial) != string(wantInitial) {
		t.Fatalf("initial Telnet negotiation = %v, want %v", result.initial, wantInitial)
	}
	if got, want := string(result.input), "whoami\r"; got != want {
		t.Fatalf("Telnet terminal input = %q, want %q", got, want)
	}
	if err := state.Close(); err != nil {
		t.Fatalf("close Telnet session: %v", err)
	}
}
