//go:build windows

package terminalog

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
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
	lock := &activeLogLock{file: file, path: path}
	if err := lock.lock(); err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, false, true, nil
		}
		return nil, false, true, err
	}
	return lock, true, true, nil
}

func (l *activeLogLock) lock() error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(l.file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped,
	)
}

func (l *activeLogLock) close(removePath bool) error {
	if l == nil || l.file == nil {
		return nil
	}
	var firstErr error
	var overlapped windows.Overlapped
	if err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &overlapped); firstErr == nil && err != nil {
		firstErr = err
	}
	if err := l.file.Close(); firstErr == nil && err != nil {
		firstErr = err
	}
	l.file = nil
	if removePath {
		if err := os.Remove(l.path); firstErr == nil && err != nil && !errors.Is(err, os.ErrNotExist) {
			firstErr = err
		}
	}
	return firstErr
}
