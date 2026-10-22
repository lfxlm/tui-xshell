package telnetclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type memoryConn struct {
	mu     sync.Mutex
	writes bytes.Buffer
}

func (c *memoryConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *memoryConn) Close() error                     { return nil }
func (c *memoryConn) LocalAddr() net.Addr              { return memoryAddr("local") }
func (c *memoryConn) RemoteAddr() net.Addr             { return memoryAddr("remote") }
func (c *memoryConn) SetDeadline(time.Time) error      { return nil }
func (c *memoryConn) SetReadDeadline(time.Time) error  { return nil }
func (c *memoryConn) SetWriteDeadline(time.Time) error { return nil }

func (c *memoryConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes.Write(data)
}

func (c *memoryConn) written() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.writes.Bytes()...)
}

type memoryAddr string

func (a memoryAddr) Network() string { return "memory" }
func (a memoryAddr) String() string  { return string(a) }

func TestInitialNegotiationAndServerOptions(t *testing.T) {
	conn := &memoryConn{}
	session := newSession(conn, 80, 24)
	if err := session.sendInitialNegotiation(); err != nil {
		t.Fatal(err)
	}
	wantInitial := []byte{
		iac, do, optionEcho, iac, do, optionSGA,
		iac, will, optionSGA, iac, will, optionNAWS, iac, will, optionTTYPE,
	}
	if got := conn.written(); !bytes.Equal(got, wantInitial) {
		t.Fatalf("initial negotiation = %v, want %v", got, wantInitial)
	}

	input := append([]byte("banner\r\n"), iac, iac)
	input = append(input,
		iac, will, optionEcho,
		iac, do, optionNAWS,
		iac, do, optionTTYPE,
		iac, sb, optionTTYPE, ttypeSend, iac, se,
		iac, will, 99,
	)
	output, err := session.decode(input)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{'b', 'a', 'n', 'n', 'e', 'r', '\r', '\n', iac}; !bytes.Equal(output, want) {
		t.Fatalf("decoded terminal output = %v, want %v", output, want)
	}
	wantNegotiations := append([]byte{
		iac, sb, optionNAWS, 0, 80, 0, 24, iac, se,
		iac, sb, optionTTYPE, ttypeIs,
	}, []byte("xterm-256color")...)
	wantNegotiations = append(wantNegotiations, iac, se, iac, dont, 99)
	if got := conn.written()[len(wantInitial):]; !bytes.Equal(got, wantNegotiations) {
		t.Fatalf("server option responses = %v, want %v", got, wantNegotiations)
	}
}

func TestResizeSendsNAWSAndEscapesUserIAC(t *testing.T) {
	conn := &memoryConn{}
	session := newSession(conn, 80, 24)
	session.localEnabled[optionNAWS] = true
	if err := session.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	wantNAWS := []byte{iac, sb, optionNAWS, 0, 120, 0, 40, iac, se}
	if got := conn.written(); !bytes.Equal(got, wantNAWS) {
		t.Fatalf("NAWS resize = %v, want %v", got, wantNAWS)
	}
	if err := session.Write([]byte{'a', iac, 'b'}); err != nil {
		t.Fatal(err)
	}
	if got, want := conn.written()[len(wantNAWS):], []byte{'a', iac, iac, 'b'}; !bytes.Equal(got, want) {
		t.Fatalf("escaped terminal input = %v, want %v", got, want)
	}
	if err := session.Resize(0, 40); err == nil {
		t.Fatal("expected invalid dimensions to fail")
	}
}

func TestDecoderHandlesNegotiationSplitAcrossReads(t *testing.T) {
	conn := &memoryConn{}
	session := newSession(conn, 100, 30)
	if err := session.sendInitialNegotiation(); err != nil {
		t.Fatal(err)
	}
	first, err := session.decode([]byte{'b', iac, will})
	if err != nil || string(first) != "b" {
		t.Fatalf("first partial decode = %q, %v; want %q, nil", first, err, "b")
	}
	second, err := session.decode([]byte{
		optionEcho, 'a', iac, do, optionTTYPE,
		iac, sb, optionTTYPE, ttypeSend, iac,
	})
	if err != nil || string(second) != "a" {
		t.Fatalf("second partial decode = %q, %v; want %q, nil", second, err, "a")
	}
	third, err := session.decode([]byte{se, 'n'})
	if err != nil || string(third) != "n" {
		t.Fatalf("third partial decode = %q, %v; want %q, nil", third, err, "n")
	}
	written := conn.written()
	wantTail := append([]byte{iac, sb, optionTTYPE, ttypeIs}, []byte("xterm-256color")...)
	wantTail = append(wantTail, iac, se)
	if got := written[len(written)-len(wantTail):]; !bytes.Equal(got, wantTail) {
		t.Fatalf("split TTYPE response = %v, want %v", got, wantTail)
	}
}

func TestAddressBracketsIPv6(t *testing.T) {
	if got, want := Address("2001:db8::10", 2323), "[2001:db8::10]:2323"; got != want {
		t.Fatalf("Address() = %q, want %q", got, want)
	}
}

func TestDialRunsTelnetNegotiationAndStreamsDecodedOutput(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer conn.Close()
		initial := make([]byte, 15)
		if _, err := io.ReadFull(conn, initial); err != nil {
			serverResult <- err
			return
		}
		negotiation := []byte{
			iac, do, optionNAWS,
			iac, do, optionTTYPE,
			iac, sb, optionTTYPE, ttypeSend, iac, se,
		}
		if _, err := conn.Write(append([]byte("connected\r\n"), negotiation...)); err != nil {
			serverResult <- err
			return
		}
		responses := make([]byte, 9+20)
		if _, err := io.ReadFull(conn, responses); err != nil {
			serverResult <- err
			return
		}
		want := append([]byte{iac, sb, optionNAWS, 0, 80, 0, 24, iac, se},
			append([]byte{iac, sb, optionTTYPE, ttypeIs}, []byte("xterm-256color")...)...)
		want = append(want, iac, se)
		if !bytes.Equal(responses, want) {
			serverResult <- errors.New("client sent unexpected terminal negotiation response")
			return
		}
		serverResult <- nil
	}()

	session, err := Dial(context.Background(), listener.Addr().String(), time.Second, 80, 24)
	if err != nil {
		t.Fatalf("dial local Telnet server: %v", err)
	}
	defer session.Close()
	output := make([]byte, len("connected\r\n"))
	if _, err := io.ReadFull(session, output); err != nil {
		t.Fatalf("read decoded terminal output: %v", err)
	}
	if got, want := string(output), "connected\r\n"; got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
	if err := <-serverResult; err != nil {
		t.Fatalf("Telnet server integration: %v", err)
	}
}
