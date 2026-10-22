package ui

import (
	"io"
	"net"
	"testing"
	"time"

	"tui-ssh-workbench/internal/infra/rloginclient"
	"tui-ssh-workbench/internal/model"
)

func TestRloginHostConnectUsesRloginHandshakeAndOpensPane(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	type serverResult struct{ err error }
	server := make(chan serverResult, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			server <- serverResult{err}
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		localUser, userErr := rloginclient.CurrentUsername()
		if userErr != nil {
			server <- serverResult{userErr}
			return
		}
		want := "\x00" + localUser + "\x00som\x00xterm-256color/9600\x00"
		handshake := make([]byte, len(want))
		if _, err := io.ReadFull(conn, handshake); err != nil {
			server <- serverResult{err}
			return
		}
		if string(handshake) != want {
			server <- serverResult{err: io.ErrUnexpectedEOF}
			return
		}
		if _, err := conn.Write([]byte{0}); err != nil {
			server <- serverResult{err}
			return
		}
		size := make([]byte, 12)
		if _, err := io.ReadFull(conn, size); err != nil {
			server <- serverResult{err}
			return
		}
		input := make([]byte, len("whoami\r"))
		if _, err := io.ReadFull(conn, input); err != nil {
			server <- serverResult{err}
			return
		}
		if string(input) != "whoami\r" {
			server <- serverResult{err: io.ErrUnexpectedEOF}
			return
		}
		server <- serverResult{}
	}()

	host := model.Host{
		Id: "rlogin-host", Kind: model.HostKindTelnet, TerminalProtocol: model.TerminalProtocolRlogin,
		Name: "legacy console", IP: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "som",
	}
	state := modelState{config: model.DefaultConfig(), width: 100, height: 30}
	command := state.startHostConnect(host, state.newTabTarget())
	if command == nil {
		t.Fatal("Rlogin connect did not return an asynchronous command")
	}
	message, ok := command().(terminalConnectedMsg)
	if !ok || message.err != nil || message.session == nil {
		t.Fatalf("Rlogin connect result = %#v, want connected session", message)
	}
	updated, _ := state.Update(message)
	state = unwrapModel(t, updated)
	pane := state.activeTerminalPane()
	if pane == nil || pane.host.TerminalProtocol != model.TerminalProtocolRlogin || pane.session == nil {
		t.Fatalf("active pane = %#v, want an Rlogin terminal", pane)
	}
	if err := pane.session.Write([]byte("whoami\r")); err != nil {
		t.Fatalf("send Rlogin input: %v", err)
	}
	if result := <-server; result.err != nil {
		t.Fatalf("Rlogin test server: %v", result.err)
	}
	if err := state.Close(); err != nil {
		t.Fatalf("close Rlogin session: %v", err)
	}
}
