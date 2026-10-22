//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly || solaris

package rloginclient

import (
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

func rloginUrgentDataSupported() bool { return true }

func configureRloginUrgentData(connection net.Conn) error {
	tcp, ok := connection.(*net.TCPConn)
	if !ok {
		return errors.New("Rlogin 紧急数据需要 TCP 连接")
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return err
	}
	var operationErr error
	err = raw.Control(func(fd uintptr) {
		operationErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_OOBINLINE, 1)
	})
	if err != nil {
		return err
	}
	return operationErr
}

// readRloginUrgentChunk waits for either ordinary TCP data or an urgent byte.
// Go's network poller does not report TCP urgent data, so poll the descriptor
// directly while the RawConn callback keeps it valid.
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
		pollFD := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLPRI}}
		for {
			_, operationErr = unix.Poll(pollFD, timeoutMillis)
			if !errors.Is(operationErr, unix.EINTR) {
				break
			}
		}
		if operationErr != nil || pollFD[0].Revents == 0 {
			if operationErr != nil {
				operationErr = fmt.Errorf("poll: %w", operationErr)
			}
			return
		}
		revents := pollFD[0].Revents
		if revents&(unix.POLLPRI|unix.POLLIN) != 0 {
			atMark, markErr := unix.IoctlGetInt(int(fd), unix.SIOCATMARK)
			if markErr != nil {
				operationErr = fmt.Errorf("check urgent mark: %w", markErr)
				return
			}
			if atMark != 0 {
				var urgent [1]byte
				var urgentCount int
				urgentCount, operationErr = unix.Read(int(fd), urgent[:])
				if operationErr != nil {
					operationErr = fmt.Errorf("read urgent byte: %w", operationErr)
					return
				}
				if urgentCount == 0 {
					operationErr = io.EOF
					return
				}
				control, hasControl = urgent[0], true
				return
			}
			if revents&unix.POLLPRI != 0 {
				// Drain only pre-mark bytes so the urgent byte remains identifiable.
				count, operationErr = unix.Read(int(fd), buffer[:1])
				if operationErr == nil && count == 0 {
					operationErr = io.EOF
				}
				return
			}
		}
		if revents&unix.POLLIN != 0 {
			count, operationErr = unix.Read(int(fd), buffer)
			if operationErr != nil {
				operationErr = fmt.Errorf("read: %w", operationErr)
			}
			if count == 0 && operationErr == nil {
				operationErr = io.EOF
			}
			return
		}
		if revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			operationErr = io.EOF
		}
	})
	if err != nil {
		return count, control, hasControl, fmt.Errorf("Rlogin 紧急数据读取失败：%w", err)
	}
	if operationErr != nil {
		return count, control, hasControl, fmt.Errorf("Rlogin 紧急数据系统调用失败：%w", operationErr)
	}
	return count, control, hasControl, nil
}
