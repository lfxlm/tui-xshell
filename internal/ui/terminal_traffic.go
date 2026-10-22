package ui

import (
	"fmt"
	"sync/atomic"
)

// terminalTraffic counts payload bytes on one terminal session channel. It
// intentionally excludes transport framing and separate SFTP/forward channels.
type terminalTraffic struct {
	received atomic.Uint64
	sent     atomic.Uint64
}

type trackedTerminalSession struct {
	terminalSession
	traffic *terminalTraffic
}

func trackTerminalSession(session terminalSession) *trackedTerminalSession {
	return &trackedTerminalSession{terminalSession: session, traffic: &terminalTraffic{}}
}

func (s *trackedTerminalSession) Read(buffer []byte) (int, error) {
	n, err := s.terminalSession.Read(buffer)
	if n > 0 {
		s.traffic.received.Add(uint64(n))
	}
	return n, err
}

func (s *trackedTerminalSession) Write(data []byte) error {
	if err := s.terminalSession.Write(data); err != nil {
		return err
	}
	s.traffic.sent.Add(uint64(len(data)))
	return nil
}

func terminalTrafficSummary(traffic *terminalTraffic) string {
	if traffic == nil {
		return ""
	}
	return fmt.Sprintf("↓%s ↑%s", formatTerminalTrafficBytes(traffic.received.Load()), formatTerminalTrafficBytes(traffic.sent.Load()))
}

func formatTerminalTrafficBytes(value uint64) string {
	switch {
	case value < 1024:
		return fmt.Sprintf("%dB", value)
	case value < 1<<20:
		return fmt.Sprintf("%.1fK", float64(value)/1024)
	case value < 1<<30:
		return fmt.Sprintf("%.1fM", float64(value)/(1<<20))
	default:
		return fmt.Sprintf("%.1fG", float64(value)/(1<<30))
	}
}
