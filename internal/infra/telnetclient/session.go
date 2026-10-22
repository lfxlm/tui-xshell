// Package telnetclient implements a small RFC 854 terminal client.
package telnetclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	iac  = 255
	dont = 254
	do   = 253
	wont = 252
	will = 251
	sb   = 250
	se   = 240

	optionEcho  = 1
	optionSGA   = 3
	optionTTYPE = 24
	optionNAWS  = 31
	ttypeIs     = 0
	ttypeSend   = 1

	maxSubnegotiationBytes = 4096
	defaultTimeout         = 15 * time.Second
)

// Session owns a Telnet TCP stream and negotiates common interactive-terminal
// options. Telnet does not provide SSH-style authentication or encryption.
type Session struct {
	conn net.Conn
	read *io.PipeReader
	pipe *io.PipeWriter

	writeMu sync.Mutex
	stateMu sync.Mutex
	close   sync.Once

	width, height int
	localEnabled  map[byte]bool
	remoteEnabled map[byte]bool
	localOffered  map[byte]bool
	remoteAsked   map[byte]bool
	localRefused  map[byte]bool
	remoteRefused map[byte]bool

	state        uint8
	optionVerb   byte
	subOption    byte
	subHasOption bool
	subData      []byte
}

// Dial opens a Telnet TCP connection and negotiates terminal size and type.
func Dial(ctx context.Context, address string, timeout time.Duration, width, height int) (*Session, error) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial Telnet server: %w", err)
	}
	session := newSession(conn, width, height)
	if err := session.sendInitialNegotiation(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("start Telnet negotiation: %w", err)
	}
	go session.readLoop()
	return session, nil
}

func newSession(conn net.Conn, width, height int) *Session {
	reader, writer := io.Pipe()
	return &Session{
		conn: conn, read: reader, pipe: writer,
		width: width, height: height,
		localEnabled: make(map[byte]bool), remoteEnabled: make(map[byte]bool),
		localOffered: make(map[byte]bool), remoteAsked: make(map[byte]bool),
		localRefused: make(map[byte]bool), remoteRefused: make(map[byte]bool),
		subData: make([]byte, 0, 64),
	}
}

func (s *Session) sendInitialNegotiation() error {
	// The server owns echo and suppress-go-ahead. The client offers suppress-go-
	// ahead, NAWS (window size), and xterm-compatible terminal type.
	initial := []byte{
		iac, do, optionEcho,
		iac, do, optionSGA,
		iac, will, optionSGA,
		iac, will, optionNAWS,
		iac, will, optionTTYPE,
	}
	s.remoteAsked[optionEcho] = true
	s.remoteAsked[optionSGA] = true
	s.localOffered[optionSGA] = true
	s.localOffered[optionNAWS] = true
	s.localOffered[optionTTYPE] = true
	return s.writeRaw(initial)
}

func (s *Session) Read(buffer []byte) (int, error) { return s.read.Read(buffer) }

// Write forwards terminal input and escapes Telnet's IAC byte as required by
// RFC 854. It never interprets user input as Telnet commands.
func (s *Session) Write(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	encoded := make([]byte, 0, len(data))
	for _, value := range data {
		encoded = append(encoded, value)
		if value == iac {
			encoded = append(encoded, iac)
		}
	}
	return s.writeRaw(encoded)
}

// Resize updates the negotiated NAWS terminal dimensions when the server has
// accepted the client's window-size option.
func (s *Session) Resize(width, height int) error {
	if width < 1 || height < 1 {
		return errors.New("Telnet terminal dimensions must be positive")
	}
	s.stateMu.Lock()
	s.width, s.height = width, height
	negotiated := s.localEnabled[optionNAWS]
	s.stateMu.Unlock()
	if !negotiated {
		return nil
	}
	return s.writeNaws()
}

func (s *Session) Close() error {
	var err error
	s.close.Do(func() {
		err = s.conn.Close()
		_ = s.pipe.CloseWithError(io.ErrClosedPipe)
		_ = s.read.Close()
	})
	return err
}

func (s *Session) readLoop() {
	defer func() { _ = s.pipe.Close() }()
	buffer := make([]byte, 4096)
	for {
		count, err := s.conn.Read(buffer)
		if count > 0 {
			decoded, parseErr := s.decode(buffer[:count])
			if parseErr != nil {
				_ = s.pipe.CloseWithError(parseErr)
				return
			}
			if len(decoded) > 0 {
				if _, writeErr := s.pipe.Write(decoded); writeErr != nil {
					return
				}
			}
		}
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				_ = s.pipe.Close()
			} else {
				_ = s.pipe.CloseWithError(err)
			}
			return
		}
	}
}

const (
	stateData uint8 = iota
	stateIAC
	stateOption
	stateSubnegotiation
	stateSubnegotiationIAC
)

func (s *Session) decode(data []byte) ([]byte, error) {
	output := make([]byte, 0, len(data))
	for _, value := range data {
		switch s.state {
		case stateData:
			if value == iac {
				s.state = stateIAC
			} else {
				output = append(output, value)
			}
		case stateIAC:
			switch value {
			case iac:
				output = append(output, iac)
				s.state = stateData
			case will, wont, do, dont:
				s.optionVerb = value
				s.state = stateOption
			case sb:
				s.subOption = 0
				s.subHasOption = false
				s.subData = s.subData[:0]
				s.state = stateSubnegotiation
			default: // NOP, AYT, BREAK and other single-byte commands.
				s.state = stateData
			}
		case stateOption:
			if err := s.negotiate(s.optionVerb, value); err != nil {
				return nil, err
			}
			s.state = stateData
		case stateSubnegotiation:
			if !s.subHasOption {
				s.subOption = value
				s.subHasOption = true
			} else if value == iac {
				s.state = stateSubnegotiationIAC
			} else if len(s.subData) < maxSubnegotiationBytes {
				s.subData = append(s.subData, value)
			}
		case stateSubnegotiationIAC:
			switch value {
			case se:
				if err := s.handleSubnegotiation(s.subOption, s.subData); err != nil {
					return nil, err
				}
				s.state = stateData
			case iac:
				if len(s.subData) < maxSubnegotiationBytes {
					s.subData = append(s.subData, iac)
				}
				s.state = stateSubnegotiation
			default:
				s.state = stateSubnegotiation
			}
		}
	}
	return output, nil
}

func (s *Session) negotiate(verb, option byte) error {
	switch verb {
	case will:
		if supportsRemote(option) {
			if !s.remoteAsked[option] {
				if err := s.writeRaw([]byte{iac, do, option}); err != nil {
					return err
				}
				s.remoteAsked[option] = true
			}
			s.remoteEnabled[option] = true
			s.remoteRefused[option] = false
		} else if !s.remoteRefused[option] {
			if err := s.writeRaw([]byte{iac, dont, option}); err != nil {
				return err
			}
			s.remoteRefused[option] = true
		}
	case wont:
		s.remoteEnabled[option] = false
		s.remoteAsked[option] = false
	case do:
		if supportsLocal(option) {
			if !s.localOffered[option] {
				if err := s.writeRaw([]byte{iac, will, option}); err != nil {
					return err
				}
				s.localOffered[option] = true
			}
			s.stateMu.Lock()
			s.localEnabled[option] = true
			s.stateMu.Unlock()
			s.localRefused[option] = false
			if option == optionNAWS {
				return s.writeNaws()
			}
		} else if !s.localRefused[option] {
			if err := s.writeRaw([]byte{iac, wont, option}); err != nil {
				return err
			}
			s.localRefused[option] = true
		}
	case dont:
		s.stateMu.Lock()
		enabled := s.localEnabled[option]
		s.localEnabled[option] = false
		s.stateMu.Unlock()
		if enabled {
			if err := s.writeRaw([]byte{iac, wont, option}); err != nil {
				return err
			}
		}
		s.localOffered[option] = false
	}
	return nil
}

func supportsRemote(option byte) bool { return option == optionEcho || option == optionSGA }
func supportsLocal(option byte) bool {
	return option == optionSGA || option == optionNAWS || option == optionTTYPE
}

func (s *Session) handleSubnegotiation(option byte, data []byte) error {
	s.stateMu.Lock()
	ttypeEnabled := s.localEnabled[optionTTYPE]
	s.stateMu.Unlock()
	if option == optionTTYPE && ttypeEnabled && len(data) > 0 && data[0] == ttypeSend {
		payload := append([]byte{ttypeIs}, []byte("xterm-256color")...)
		return s.writeSubnegotiation(optionTTYPE, payload)
	}
	return nil
}

func (s *Session) writeNaws() error {
	s.stateMu.Lock()
	negotiated := s.localEnabled[optionNAWS]
	s.stateMu.Unlock()
	if !negotiated {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeNawsLocked()
}

func (s *Session) writeNawsLocked() error {
	s.stateMu.Lock()
	width, height := clampUint16(s.width), clampUint16(s.height)
	s.stateMu.Unlock()
	payload := []byte{byte(width >> 8), byte(width), byte(height >> 8), byte(height)}
	return s.writeSubnegotiationLocked(optionNAWS, payload)
}

func clampUint16(value int) uint16 {
	if value < 1 {
		return 1
	}
	if value > 65535 {
		return 65535
	}
	return uint16(value)
}

func (s *Session) writeSubnegotiation(option byte, payload []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeSubnegotiationLocked(option, payload)
}

func (s *Session) writeSubnegotiationLocked(option byte, payload []byte) error {
	data := []byte{iac, sb, option}
	for _, value := range payload {
		data = append(data, value)
		if value == iac {
			data = append(data, iac)
		}
	}
	data = append(data, iac, se)
	return s.writeRawLocked(data)
}

func (s *Session) writeRaw(data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeRawLocked(data)
}

func (s *Session) writeRawLocked(data []byte) error {
	for len(data) > 0 {
		count, err := s.conn.Write(data)
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		data = data[count:]
	}
	return nil
}

// Address builds a host:port pair while correctly bracketing IPv6 literals.
func Address(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) }
