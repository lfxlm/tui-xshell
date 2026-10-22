package ui

import (
	"net"
	"strings"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestProbeSSHBannerRecognizesSSHProtocol(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			defer connection.Close()
			_, _ = connection.Write([]byte("SSH-2.0-test-server\r\n"))
		}
	}()

	address := listener.Addr().(*net.TCPAddr)
	if !probeSSHBanner(model.Host{IP: "127.0.0.1", Port: address.Port}) {
		t.Fatal("probeSSHBanner() did not recognize an SSH protocol banner")
	}
}

func TestProbeSSHBannerRejectsNonSSHService(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			defer connection.Close()
			_, _ = connection.Write([]byte("HTTP/1.1 200 OK\r\n"))
		}
	}()

	address := listener.Addr().(*net.TCPAddr)
	if probeSSHBanner(model.Host{IP: "127.0.0.1", Port: address.Port}) {
		t.Fatal("probeSSHBanner() treated a non-SSH service as SSH")
	}
}

func TestStartSSHServerProbesMarksSSHHostsAsChecking(t *testing.T) {
	state := modelState{hosts: []model.Host{
		{Id: "ssh", Kind: model.HostKindSSH, IP: "127.0.0.1", Port: 22},
		{Id: "local", Kind: model.HostKindLocal},
	}}

	if command := state.startSSHServerProbes(); command == nil {
		t.Fatal("startSSHServerProbes() returned no asynchronous command")
	}
	if state.sshProbeStates["ssh"] != sshProbeChecking {
		t.Fatalf("SSH host state = %v, want checking", state.sshProbeStates["ssh"])
	}
	if _, exists := state.sshProbeStates["local"]; exists {
		t.Fatal("local terminal should not receive an SSH probe state")
	}
}

func TestHostCardAndLegendShowSSHProbeState(t *testing.T) {
	first, _ := hostCardLinesWithFocus(model.Host{Kind: model.HostKindSSH, Name: "example"}, false, 0, false, 60, false, false, true, sshProbeOffline)
	if !strings.Contains(first, "●") {
		t.Fatalf("SSH host row does not render its probe indicator: %q", first)
	}

	state := modelState{hosts: []model.Host{{Kind: model.HostKindSSH}}}
	legend := state.hostFilterLine(100)
	for _, label := range []string{"在线", "探测中", "无法连接"} {
		if !strings.Contains(legend, label) {
			t.Fatalf("SSH probe legend is missing %q: %q", label, legend)
		}
	}
}
