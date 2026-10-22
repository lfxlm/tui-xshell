//go:build windows

package rloginclient

import (
	"errors"
	"io"
	"net"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wsaPollReadNormal = 0x0100
	wsaPollReadBand   = 0x0200
	wsaPollError      = 0x0001
	wsaPollHangup     = 0x0002
	wsaPollInvalid    = 0x0004
)

type wsaPollFD struct {
	fd      windows.Handle
	events  int16
	revents int16
}

var (
	winsockDLL          = windows.NewLazySystemDLL("ws2_32.dll")
	wsaPollProc         = winsockDLL.NewProc("WSAPoll")
	wsaGetLastErrorProc = winsockDLL.NewProc("WSAGetLastError")
)

func rloginUrgentDataSupported() bool { return true }

func configureRloginUrgentData(net.Conn) error { return nil }

// WSAPoll reports normal data with POLLRDNORM and urgent TCP data with
// POLLRDBAND. Keep the socket in Winsock's default non-inline OOB mode so the
// urgent control byte can be consumed separately from terminal output.
func readRloginUrgentChunk(connection net.Conn, buffer []byte, wait time.Duration) (int, byte, bool, error) {
	tcp, ok := connection.(*net.TCPConn)
	if !ok {
		return 0, 0, false, errors.New("Rlogin 紧急数据需要 TCP 连接")
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return 0, 0, false, err
	}
	timeoutMillis := int(wait / time.Millisecond)
	if wait > 0 && timeoutMillis == 0 {
		timeoutMillis = 1
	}
	var count int
	var control byte
	var hasControl bool
	var operationErr error
	err = raw.Control(func(fd uintptr) {
		pollFD := wsaPollFD{fd: windows.Handle(fd), events: wsaPollReadNormal | wsaPollReadBand}
		ready, _, _ := wsaPollProc.Call(uintptr(unsafe.Pointer(&pollFD)), 1, uintptr(timeoutMillis))
		if int32(ready) == -1 {
			code, _, _ := wsaGetLastErrorProc.Call()
			operationErr = syscall.Errno(code)
			return
		}
		if ready == 0 {
			return
		}
		if pollFD.revents&wsaPollReadBand != 0 {
			var urgent [1]byte
			n, _, recvErr := windows.Recvfrom(windows.Handle(fd), urgent[:], windows.MSG_OOB)
			if recvErr == nil && n > 0 {
				control, hasControl = urgent[0], true
			} else if recvErr != nil && !errors.Is(recvErr, windows.WSAEWOULDBLOCK) {
				operationErr = recvErr
				return
			}
		}
		if pollFD.revents&wsaPollReadNormal != 0 {
			count, _, operationErr = windows.Recvfrom(windows.Handle(fd), buffer, 0)
			if count == 0 && operationErr == nil {
				operationErr = io.EOF
			}
			return
		}
		if pollFD.revents&(wsaPollError|wsaPollHangup|wsaPollInvalid) != 0 {
			operationErr = io.EOF
		}
	})
	if err != nil {
		return count, control, hasControl, err
	}
	return count, control, hasControl, operationErr
}
