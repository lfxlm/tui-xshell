//go:build unix

package terminalog

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type activeLogLock struct {
	file *os.File
	path string
}

func acquireActiveLogLock(path string) (*activeLogLock, bool, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, true, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, true, errors.New("active terminal log marker must be a regular file")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, false, true, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, false, true, nil
		}
		return nil, false, true, err
	}
	return &activeLogLock{file: file, path: path}, true, true, nil
}

func (l *activeLogLock) close(removePath bool) error {
	if l == nil || l.file == nil {
		return nil
	}
	var firstErr error
	if removePath {
		if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			firstErr = err
		}
	}
	if err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN); firstErr == nil && err != nil {
		firstErr = err
	}
	if err := l.file.Close(); firstErr == nil && err != nil {
		firstErr = err
	}
	l.file = nil
	return firstErr
}
