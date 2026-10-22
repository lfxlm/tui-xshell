//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly && !solaris && !windows

package rloginclient

import (
	"errors"
	"net"
	"time"
)

func rloginUrgentDataSupported() bool { return false }

func configureRloginUrgentData(net.Conn) error { return nil }

func readRloginUrgentChunk(net.Conn, []byte, time.Duration) (int, byte, bool, error) {
	return 0, 0, false, errors.New("当前平台不支持 Rlogin 紧急数据读取")
}
