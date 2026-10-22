package serialclient

import (
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"go.bug.st/serial"
	"tui-ssh-workbench/internal/model"
)

type fakePort struct {
	mode       *serial.Mode
	readData   []byte
	written    []byte
	maxWrite   int
	timeout    time.Duration
	closed     bool
	timeoutErr error
}

func (p *fakePort) SetMode(mode *serial.Mode) error { p.mode = mode; return nil }
func (p *fakePort) Read(buffer []byte) (int, error) {
	if len(p.readData) == 0 {
		return 0, io.EOF
	}
	n := copy(buffer, p.readData)
	p.readData = p.readData[n:]
	return n, nil
}
func (p *fakePort) Write(data []byte) (int, error) {
	n := len(data)
	if p.maxWrite > 0 && n > p.maxWrite {
		n = p.maxWrite
	}
	p.written = append(p.written, data[:n]...)
	return n, nil
}
func (*fakePort) Drain() error                                         { return nil }
func (*fakePort) ResetInputBuffer() error                              { return nil }
func (*fakePort) ResetOutputBuffer() error                             { return nil }
func (*fakePort) SetDTR(bool) error                                    { return nil }
func (*fakePort) SetRTS(bool) error                                    { return nil }
func (*fakePort) GetModemStatusBits() (*serial.ModemStatusBits, error) { return nil, nil }
func (p *fakePort) SetReadTimeout(timeout time.Duration) error {
	p.timeout = timeout
	return p.timeoutErr
}
func (p *fakePort) Close() error            { p.closed = true; return nil }
func (*fakePort) Break(time.Duration) error { return nil }

func TestModeMapsSavedSerialSettings(t *testing.T) {
	mode, err := Mode(model.Host{
		Kind: model.HostKindSerial, IP: "/dev/ttyUSB0", SerialBaudRate: 115200,
		SerialDataBits: 7, SerialParity: model.SerialParityEven, SerialStopBits: model.SerialStopBitsTwo,
		SerialFlowControl: model.SerialFlowControlRTSCTS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mode.BaudRate != 115200 || mode.DataBits != 7 || mode.Parity != serial.EvenParity || mode.StopBits != serial.TwoStopBits || mode.FlowControl != serial.HardwareFlowControl {
		t.Fatalf("serial mode = %#v", mode)
	}
	for value, want := range map[string]serial.FlowControl{
		model.SerialFlowControlNone:    serial.NoFlowControl,
		model.SerialFlowControlXONXOFF: serial.SoftwareFlowControl,
		model.SerialFlowControlRTSCTS:  serial.HardwareFlowControl,
	} {
		mode, err := Mode(model.Host{Kind: model.HostKindSerial, IP: "/dev/ttyUSB0", SerialFlowControl: value})
		if err != nil || mode.FlowControl != want {
			t.Errorf("Mode(flow=%q) = %#v, %v; want %v", value, mode, err, want)
		}
	}
}

func TestListPortsNormalizesAndSortsDeviceNames(t *testing.T) {
	ports, err := ListPortsWith(func() ([]string, error) {
		return []string{" /dev/ttyUSB1 ", "", "/dev/ttyUSB0", "/dev/ttyUSB1", "COM3"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/dev/ttyUSB0", "/dev/ttyUSB1", "COM3"}
	if !reflect.DeepEqual(ports, want) {
		t.Fatalf("ports = %#v, want %#v", ports, want)
	}
}

func TestListPortsPropagatesEnumerationErrors(t *testing.T) {
	wantErr := errors.New("device access denied")
	if _, err := ListPortsWith(func() ([]string, error) { return nil, wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("enumeration error = %v, want wrapped %v", err, wantErr)
	}
	if _, err := ListPortsWith(nil); err == nil {
		t.Fatal("nil enumerator was accepted")
	}
}

func TestModeDefaultsAndRejectsInvalidSettings(t *testing.T) {
	mode, err := Mode(model.Host{Kind: model.HostKindSerial, IP: "/dev/ttyUSB0"})
	if err != nil {
		t.Fatal(err)
	}
	if mode.BaudRate != 9600 || mode.DataBits != 8 || mode.Parity != serial.NoParity || mode.StopBits != serial.OneStopBit || mode.FlowControl != serial.NoFlowControl {
		t.Fatalf("default serial mode = %#v", mode)
	}
	for name, host := range map[string]model.Host{
		"missing device":   {Kind: model.HostKindSerial},
		"wrong kind":       {Kind: model.HostKindSSH, IP: "/dev/ttyUSB0"},
		"bad baud":         {Kind: model.HostKindSerial, IP: "/dev/ttyUSB0", SerialBaudRate: 1},
		"bad data bits":    {Kind: model.HostKindSerial, IP: "/dev/ttyUSB0", SerialDataBits: 9},
		"bad parity":       {Kind: model.HostKindSerial, IP: "/dev/ttyUSB0", SerialParity: "invalid"},
		"bad stop bits":    {Kind: model.HostKindSerial, IP: "/dev/ttyUSB0", SerialStopBits: "3"},
		"bad flow control": {Kind: model.HostKindSerial, IP: "/dev/ttyUSB0", SerialFlowControl: "invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Mode(host); err == nil {
				t.Fatal("Mode unexpectedly accepted invalid settings")
			}
		})
	}
}

func TestSessionUsesDeviceModeAndClosesPortOnFailure(t *testing.T) {
	port := &fakePort{readData: []byte("ok"), maxWrite: 2}
	var path string
	session, err := OpenWith(func(gotPath string, mode *serial.Mode) (serial.Port, error) {
		path = gotPath
		port.mode = mode
		return port, nil
	}, model.Host{
		Kind: model.HostKindSerial, IP: " /dev/ttyUSB0 ", SerialBaudRate: 19200,
		SerialFlowControl: model.SerialFlowControlXONXOFF,
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/dev/ttyUSB0" || port.mode.BaudRate != 19200 || port.mode.FlowControl != serial.SoftwareFlowControl || port.timeout != serial.NoTimeout {
		t.Fatalf("opened path=%q mode=%#v timeout=%s", path, port.mode, port.timeout)
	}
	buffer := make([]byte, 8)
	if n, err := session.Read(buffer); err != nil || string(buffer[:n]) != "ok" {
		t.Fatalf("Read = %q, %v", buffer[:n], err)
	}
	if err := session.Write([]byte("hello")); err != nil || string(port.written) != "hello" {
		t.Fatalf("Write bytes=%q err=%v", port.written, err)
	}
	if err := session.Resize(120, 40); err != nil {
		t.Fatalf("Resize returned %v", err)
	}
	if err := session.Close(); err != nil || !port.closed {
		t.Fatalf("Close err=%v closed=%v", err, port.closed)
	}

	closeFailurePort := &fakePort{timeoutErr: errors.New("timeout setup failed")}
	if _, err := OpenWith(func(string, *serial.Mode) (serial.Port, error) { return closeFailurePort, nil },
		model.Host{Kind: model.HostKindSerial, IP: "/dev/ttyUSB0"}); err == nil {
		t.Fatal("OpenWith unexpectedly accepted timeout setup failure")
	}
	if !closeFailurePort.closed {
		t.Fatal("port was not closed after timeout setup failure")
	}
}
