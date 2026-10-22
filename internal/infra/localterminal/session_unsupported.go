//go:build !darwin && !linux && !freebsd && !dragonfly && !openbsd && !solaris && !windows

// Package localterminal exposes a clear unsupported-platform result where PTYs
// are not provided by the selected backend.
package localterminal

import (
	"errors"
	"io"
)

var ErrUnsupported = errors.New("local PTY terminals are not supported on this platform")

type Session struct{}

func Start(width, height int) (*Session, error) { return nil, ErrUnsupported }
func (*Session) Read([]byte) (int, error)       { return 0, io.ErrClosedPipe }
func (*Session) Write([]byte) error             { return io.ErrClosedPipe }
func (*Session) Resize(int, int) error          { return ErrUnsupported }
func (*Session) Close() error                   { return nil }
