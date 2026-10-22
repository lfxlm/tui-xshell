package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/model"
)

type terminalTrafficTestSession struct {
	reader   *bytes.Reader
	writes   [][]byte
	writeErr error
}

func (s *terminalTrafficTestSession) Read(data []byte) (int, error) { return s.reader.Read(data) }
func (s *terminalTrafficTestSession) Write(data []byte) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.writes = append(s.writes, append([]byte(nil), data...))
	return nil
}
func (*terminalTrafficTestSession) Resize(int, int) error { return nil }
func (*terminalTrafficTestSession) Close() error          { return nil }

func TestTrackedTerminalSessionCountsSuccessfulPayloadBytes(t *testing.T) {
	underlying := &terminalTrafficTestSession{reader: bytes.NewReader([]byte("remote"))}
	session := trackTerminalSession(underlying)
	buffer := make([]byte, 3)
	for index := 0; index < 2; index++ {
		if _, err := session.Read(buffer); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.Write([]byte("user")); err != nil {
		t.Fatal(err)
	}
	if got := terminalTrafficSummary(session.traffic); got != "↓6B ↑4B" {
		t.Fatalf("terminal traffic summary = %q, want %q", got, "↓6B ↑4B")
	}

	underlying.writeErr = errors.New("write failed")
	if err := session.Write([]byte("not sent")); err == nil {
		t.Fatal("write failure was hidden")
	}
	if got := session.traffic.sent.Load(); got != 4 {
		t.Fatalf("failed write counted %d bytes, want 4", got)
	}
}

func TestTerminalPaneHeaderDisplaysPerPaneTraffic(t *testing.T) {
	traffic := &terminalTraffic{}
	traffic.received.Add(2048)
	traffic.sent.Add(4)
	pane := &terminalPane{
		host:             model.Host{Name: "server", Kind: model.HostKindSSH},
		terminalEncoding: "UTF-8",
		terminal:         vt.NewSafeEmulator(80, 4),
		traffic:          traffic,
	}
	view := (modelState{}).renderTerminalPane(pane, paneRect{width: 100, height: 2})
	if !strings.Contains(view, "↓2.0K ↑4B") {
		t.Fatalf("pane header omitted traffic counters:\n%s", view)
	}
}

func TestConnectedPaneWrapsSessionAndReportsItsTraffic(t *testing.T) {
	host := model.Host{Id: "host-1", Name: "server", Kind: model.HostKindSSH, TerminalEncoding: model.TerminalEncodingUTF8}
	target := connectionTarget{kind: newTabTarget, tabID: "tab-1", paneID: "pane-1", width: 80, height: 20}
	underlying := &terminalTrafficTestSession{reader: bytes.NewReader([]byte("remote"))}
	state := modelState{
		config: model.DefaultConfig(), width: 80, height: 24,
		connectingHost: &host, connectionTarget: &target,
	}
	updated, _ := state.Update(terminalConnectedMsg{host: host, target: target, session: underlying})
	state = unwrapModel(t, updated)
	pane := state.paneByID(target.paneID)
	if pane == nil || pane.traffic == nil {
		t.Fatal("connected pane did not attach its traffic counter")
	}
	if err := pane.session.Write([]byte("user")); err != nil {
		t.Fatal(err)
	}
	if _, err := pane.session.Read(make([]byte, 6)); err != nil {
		t.Fatal(err)
	}
	if got := terminalTrafficSummary(pane.traffic); got != "↓6B ↑4B" {
		t.Fatalf("connected pane traffic = %q, want %q", got, "↓6B ↑4B")
	}
}
