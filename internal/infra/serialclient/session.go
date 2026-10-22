// Package serialclient adapts a local serial device to the terminal session
// interface used by the workspace.
package serialclient

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"go.bug.st/serial"
	"tui-ssh-workbench/internal/model"
)

// Session owns an open serial device. Serial connections do not negotiate a
// terminal size, so Resize intentionally does nothing.
type Session struct {
	port     serial.Port
	close    sync.Once
	closeErr error
}

// ListPorts returns the serial device paths available on the local machine in
// stable order. Paths are trimmed and duplicate entries are removed so the
// form can present a predictable picker while still allowing manual entry.
func ListPorts() ([]string, error) {
	return ListPortsWith(serial.GetPortsList)
}

// ListPortsWith isolates platform enumeration for tests and callers that need
// to inject a device source.
func ListPortsWith(list func() ([]string, error)) ([]string, error) {
	if list == nil {
		return nil, errors.New("串口设备枚举器不可用")
	}
	ports, err := list()
	if err != nil {
		return nil, fmt.Errorf("枚举串口设备: %w", err)
	}
	seen := make(map[string]struct{}, len(ports))
	clean := make([]string, 0, len(ports))
	for _, port := range ports {
		port = strings.TrimSpace(port)
		if port == "" {
			continue
		}
		if _, exists := seen[port]; exists {
			continue
		}
		seen[port] = struct{}{}
		clean = append(clean, port)
	}
	sort.Strings(clean)
	return clean, nil
}

// Mode validates and converts a saved serial host's settings to the driver
// configuration.
func Mode(host model.Host) (*serial.Mode, error) {
	if host.Kind != model.HostKindSerial {
		return nil, errors.New("主机类型不是串口")
	}
	if strings.TrimSpace(host.IP) == "" {
		return nil, errors.New("串口设备路径不能为空")
	}
	baudRate := host.SerialBaudRate
	if baudRate == 0 {
		baudRate = 9600
	}
	if baudRate < 50 || baudRate > 4_000_000 {
		return nil, errors.New("波特率必须在 50 到 4000000 之间")
	}
	dataBits := host.SerialDataBits
	if dataBits == 0 {
		dataBits = 8
	}
	if dataBits < 5 || dataBits > 8 {
		return nil, errors.New("数据位必须是 5、6、7 或 8")
	}
	parity := serial.NoParity
	switch normalizedSerialParity(host.SerialParity) {
	case model.SerialParityNone:
	case model.SerialParityEven:
		parity = serial.EvenParity
	case model.SerialParityOdd:
		parity = serial.OddParity
	case model.SerialParityMark:
		parity = serial.MarkParity
	case model.SerialParitySpace:
		parity = serial.SpaceParity
	default:
		return nil, errors.New("校验位必须是 none、even、odd、mark 或 space")
	}
	stopBits := serial.OneStopBit
	switch normalizedSerialStopBits(host.SerialStopBits) {
	case model.SerialStopBitsOne:
	case model.SerialStopBitsOne5:
		stopBits = serial.OnePointFiveStopBits
	case model.SerialStopBitsTwo:
		stopBits = serial.TwoStopBits
	default:
		return nil, errors.New("停止位必须是 1、1.5 或 2")
	}
	flowControl := serial.NoFlowControl
	switch normalizedSerialFlowControl(host.SerialFlowControl) {
	case model.SerialFlowControlNone:
	case model.SerialFlowControlXONXOFF:
		flowControl = serial.SoftwareFlowControl
	case model.SerialFlowControlRTSCTS:
		flowControl = serial.HardwareFlowControl
	default:
		return nil, errors.New("流控制必须是 none、xon_xoff 或 rts_cts")
	}
	return &serial.Mode{
		BaudRate: baudRate, DataBits: dataBits, Parity: parity, StopBits: stopBits,
		FlowControl: flowControl,
	}, nil
}

// Open opens the device path saved in host using its serial settings.
func Open(host model.Host) (*Session, error) {
	return OpenWith(serial.Open, host)
}

// OpenWith opens a serial host through the supplied driver function. It is
// also useful for testing without a physical serial device.
func OpenWith(open func(string, *serial.Mode) (serial.Port, error), host model.Host) (*Session, error) {
	if open == nil {
		return nil, errors.New("串口驱动不可用")
	}
	mode, err := Mode(host)
	if err != nil {
		return nil, err
	}
	port, err := open(strings.TrimSpace(host.IP), mode)
	if err != nil {
		return nil, fmt.Errorf("打开串口设备 %q: %w", strings.TrimSpace(host.IP), err)
	}
	if port == nil {
		return nil, errors.New("串口驱动返回空设备")
	}
	if err := port.SetReadTimeout(serial.NoTimeout); err != nil {
		_ = port.Close()
		return nil, fmt.Errorf("设置串口读取超时: %w", err)
	}
	return &Session{port: port}, nil
}

func (s *Session) Read(buffer []byte) (int, error) {
	if s == nil || s.port == nil {
		return 0, io.ErrClosedPipe
	}
	return s.port.Read(buffer)
}

func (s *Session) Write(data []byte) error {
	if s == nil || s.port == nil {
		return io.ErrClosedPipe
	}
	for len(data) > 0 {
		written, err := s.port.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func (*Session) Resize(int, int) error { return nil }

func (s *Session) Close() error {
	if s == nil || s.port == nil {
		return nil
	}
	s.close.Do(func() { s.closeErr = s.port.Close() })
	return s.closeErr
}

func normalizedSerialParity(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return model.SerialParityNone
	}
	return value
}

func normalizedSerialStopBits(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return model.SerialStopBitsOne
	}
	return value
}

func normalizedSerialFlowControl(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return model.SerialFlowControlNone
	}
	return value
}
