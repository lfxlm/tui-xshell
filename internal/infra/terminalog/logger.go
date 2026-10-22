// Package terminalog stores terminal output in private, rotating local files.
package terminalog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// MaxLogBytes is the largest supported individual log segment and preview.
	MaxLogBytes      int64 = 100 << 20
	queueCapacity          = 128
	maxRetainedFiles       = 100
)

var (
	ErrQueueFull = errors.New("terminal log write queue is full")
	ErrClosed    = errors.New("terminal log is closed")
)

type Options struct {
	MaxBytes       int64
	RetainedFiles  int
	MaxTotalBytes  int64
	MaxAge         time.Duration
	RedactPatterns []string
}

// DefaultOptions returns the built-in segment size and retention settings.
func DefaultOptions() Options {
	return Options{MaxBytes: MaxLogBytes, RetainedFiles: 5, MaxTotalBytes: 1 << 30, MaxAge: 90 * 24 * time.Hour}
}

// Logger asynchronously stores VT output, applying any configured redaction.
// It never records keyboard input.
type Logger struct {
	file       *os.File
	path       string
	directory  string
	activeLock *activeLogLock
	options    Options
	redactor   *Redactor
	queue      chan []byte
	stop       chan struct{}
	done       chan struct{}

	mu               sync.Mutex
	stopped          bool
	errMu            sync.RWMutex
	err              error
	maintenanceErrMu sync.RWMutex
	maintenanceErr   error
}

// Entry describes a regular terminal output log stored in the private log
// directory. Path is derived from the directory and the entry's own name.
type Entry struct {
	Path     string
	Name     string
	Size     int64
	Modified time.Time
}

// New creates a private log file in directory. label is reduced to safe ASCII
// filename characters; the file itself is created exclusively with mode 0600.
func New(directory, label string) (*Logger, error) {
	return NewWithOptions(directory, label, DefaultOptions())
}

// NewWithOptions creates a private rotating log and applies the configured
// redaction patterns only to persisted output, leaving terminal display intact.
func NewWithOptions(directory, label string, options Options) (*Logger, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("terminal log directory is empty")
	}
	if options.MaxBytes <= 0 || options.MaxBytes > MaxLogBytes {
		return nil, fmt.Errorf("terminal log segment size must be between 1 byte and %d bytes", MaxLogBytes)
	}
	if options.RetainedFiles < 1 || options.RetainedFiles > maxRetainedFiles {
		return nil, fmt.Errorf("terminal log retained file count must be between 1 and %d", maxRetainedFiles)
	}
	if options.MaxTotalBytes < 0 || options.MaxTotalBytes > 100<<30 {
		return nil, errors.New("terminal log total size must be between 0 and 100 GiB")
	}
	if options.MaxAge < 0 || options.MaxAge > maxRetentionAge {
		return nil, errors.New("terminal log retention age must be between 0 and 3650 days")
	}
	redactor, err := NewRedactor(options.RedactPatterns)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create terminal log directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect terminal log directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("terminal log path must be a real directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("secure terminal log directory: %w", err)
	}
	filePrefix := time.Now().UTC().Format("20060102T150405Z") + "-" + safeLabel(label) + "-"
	marker, err := os.CreateTemp(directory, filePrefix+"*.active")
	if err != nil {
		return nil, fmt.Errorf("create active terminal log marker: %w", err)
	}
	markerPath := marker.Name()
	if err := marker.Chmod(0o600); err != nil {
		_ = marker.Close()
		_ = os.Remove(markerPath)
		return nil, fmt.Errorf("secure active terminal log marker: %w", err)
	}
	if err := marker.Close(); err != nil {
		_ = os.Remove(markerPath)
		return nil, fmt.Errorf("close active terminal log marker: %w", err)
	}
	activeLock, acquired, exists, err := acquireActiveLogLock(markerPath)
	if err != nil {
		_ = os.Remove(markerPath)
		return nil, fmt.Errorf("lock active terminal log marker: %w", err)
	}
	if !acquired {
		if exists {
			return nil, errors.New("active terminal log marker was unexpectedly locked")
		}
		return nil, errors.New("active terminal log marker disappeared before it could be locked")
	}
	if _, err := Prune(directory, options); err != nil {
		_ = activeLock.close(true)
		return nil, fmt.Errorf("apply terminal log retention policy: %w", err)
	}
	logPath := strings.TrimSuffix(markerPath, filepath.Ext(markerPath)) + ".log"
	file, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = activeLock.close(true)
		return nil, fmt.Errorf("create terminal log file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		_ = activeLock.close(true)
		return nil, fmt.Errorf("secure terminal log file: %w", err)
	}
	logger := newConfiguredLoggerWithRetention(file, options, redactor, directory, activeLock)
	return logger, nil
}

// List returns regular .log files newest first. It rejects a symlink or
// non-directory log root and ignores symlink entries.
func List(directory string) ([]Entry, error) {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect terminal log directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("terminal log path must be a real directory")
	}
	children, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read terminal log directory: %w", err)
	}
	entries := make([]Entry, 0, len(children))
	for _, child := range children {
		if child.IsDir() || filepath.Ext(child.Name()) != ".log" {
			continue
		}
		path := filepath.Join(directory, child.Name())
		childInfo, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("inspect terminal log file %q: %w", child.Name(), err)
		}
		if !childInfo.Mode().IsRegular() || childInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		entries = append(entries, Entry{
			Path: path, Name: child.Name(), Size: childInfo.Size(), Modified: childInfo.ModTime(),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Modified.Equal(entries[j].Modified) {
			leftSegment, rightSegment := logSegmentIndex(entries[i].Name), logSegmentIndex(entries[j].Name)
			if leftSegment != rightSegment {
				return leftSegment < rightSegment
			}
			return entries[i].Name > entries[j].Name
		}
		return entries[i].Modified.After(entries[j].Modified)
	})
	return entries, nil
}

// Remove deletes one regular log file directly inside directory. It accepts a
// filename rather than an arbitrary path so UI callers cannot escape the log
// root through a crafted entry.
func Remove(directory, name string) error {
	if strings.TrimSpace(directory) == "" {
		return errors.New("terminal log directory is empty")
	}
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) || filepath.Ext(name) != ".log" {
		return errors.New("terminal log name must be a .log file in the log directory")
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect terminal log directory: %w", err)
	}
	if !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("terminal log path must be a real directory")
	}
	path := filepath.Join(directory, name)
	fileInfo, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect terminal log file %q: %w", name, err)
	}
	if !fileInfo.Mode().IsRegular() || fileInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("terminal log must be a regular file")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove terminal log file %q: %w", name, err)
	}
	return nil
}

func logSegmentIndex(name string) int {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	separator := strings.LastIndexByte(base, '.')
	if separator < 0 {
		return 0
	}
	index, err := strconv.Atoi(base[separator+1:])
	if err != nil || index < 1 {
		return 0
	}
	return index
}

func newLogger(file *os.File, limit int64) *Logger {
	redactor, _ := NewRedactor(nil)
	return newConfiguredLogger(file, Options{MaxBytes: limit, RetainedFiles: 5}, redactor)
}

func newConfiguredLogger(file *os.File, options Options, redactor *Redactor) *Logger {
	return newConfiguredLoggerWithRetention(file, options, redactor, "", nil)
}

func newConfiguredLoggerWithRetention(file *os.File, options Options, redactor *Redactor, directory string, activeLock *activeLogLock) *Logger {
	logger := &Logger{
		file: file, path: file.Name(), directory: directory, activeLock: activeLock, options: options, redactor: redactor,
		queue: make(chan []byte, queueCapacity), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go logger.run()
	return logger
}

// Path returns the created log file path.
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Err returns the asynchronous writer error, if any.
func (l *Logger) Err() error {
	if l == nil {
		return nil
	}
	l.errMu.RLock()
	defer l.errMu.RUnlock()
	return l.err
}

// MaintenanceErr returns the most recent retention cleanup error without
// stopping an otherwise healthy output writer.
func (l *Logger) MaintenanceErr() error {
	if l == nil {
		return nil
	}
	l.maintenanceErrMu.RLock()
	defer l.maintenanceErrMu.RUnlock()
	return l.maintenanceErr
}

func (l *Logger) setMaintenanceErr(err error) {
	l.maintenanceErrMu.Lock()
	l.maintenanceErr = err
	l.maintenanceErrMu.Unlock()
}

// Write queues a copy of data without waiting for disk I/O. Queue saturation
// is reported so the caller can stop recording instead of silently dropping
// terminal output.
func (l *Logger) Write(data []byte) error {
	if l == nil {
		return ErrClosed
	}
	if len(data) == 0 {
		return nil
	}
	copyOfData := append([]byte(nil), data...)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return ErrClosed
	}
	if err := l.Err(); err != nil {
		return err
	}
	select {
	case l.queue <- copyOfData:
		return nil
	default:
		return ErrQueueFull
	}
}

// Stop stops accepting output and asks the writer to flush its queued bytes.
// It does not wait for disk I/O.
func (l *Logger) Stop() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if !l.stopped {
		l.stopped = true
		close(l.stop)
	}
	l.mu.Unlock()
}

// Close stops recording and waits for the queued output to be flushed.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.Stop()
	<-l.done
	if err := l.Err(); err != nil {
		return err
	}
	return l.MaintenanceErr()
}

func (l *Logger) run() {
	defer close(l.done)
	defer func() {
		if l.file != nil {
			if err := l.file.Close(); err != nil {
				l.setErr(err)
			}
		}
		if l.activeLock != nil {
			if err := l.activeLock.close(true); err != nil {
				l.setMaintenanceErr(fmt.Errorf("release active terminal log marker: %w", err))
			} else {
				l.setMaintenanceErr(nil)
			}
			l.activeLock = nil
		}
		if l.directory != "" && (l.options.MaxTotalBytes > 0 || l.options.MaxAge > 0) {
			if _, err := Prune(l.directory, l.options); err != nil {
				l.setMaintenanceErr(fmt.Errorf("cleanup terminal log retention: %w", err))
			} else {
				l.setMaintenanceErr(nil)
			}
		}
	}()
	var retentionTicker *time.Ticker
	var retentionTicks <-chan time.Time
	if l.directory != "" && (l.options.MaxTotalBytes > 0 || l.options.MaxAge > 0) {
		retentionTicker = time.NewTicker(time.Minute)
		retentionTicks = retentionTicker.C
		defer retentionTicker.Stop()
	}
	var written int64
	write := func(data []byte) bool {
		for len(data) > 0 {
			if written >= l.options.MaxBytes {
				if err := l.rotate(); err != nil {
					l.setErr(err)
					return false
				}
				written = 0
			}
			chunkSize := min(int64(len(data)), l.options.MaxBytes-written)
			chunk := data[:chunkSize]
			n, err := l.file.Write(chunk)
			written += int64(n)
			if err != nil {
				l.setErr(err)
				return false
			}
			if n != len(chunk) {
				l.setErr(io.ErrShortWrite)
				return false
			}
			data = data[n:]
		}
		return true
	}
	process := func(data []byte) bool {
		if l.redactor != nil {
			data = l.redactor.Write(data)
		}
		return write(data)
	}
	for {
		select {
		case data := <-l.queue:
			if !process(data) {
				return
			}
		case <-retentionTicks:
			if _, err := Prune(l.directory, l.options); err != nil {
				l.setMaintenanceErr(fmt.Errorf("cleanup terminal log retention: %w", err))
			} else {
				l.setMaintenanceErr(nil)
			}
		case <-l.stop:
			for {
				select {
				case data := <-l.queue:
					if !process(data) {
						return
					}
				default:
					if l.redactor != nil && !write(l.redactor.Flush()) {
						return
					}
					return
				}
			}
		}
	}
}

func (l *Logger) rotate() error {
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			l.file = nil
			return fmt.Errorf("close terminal log segment: %w", err)
		}
		l.file = nil
	}
	if l.options.RetainedFiles <= 1 {
		if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove previous terminal log segment: %w", err)
		}
	} else {
		oldest := l.archivePath(l.options.RetainedFiles - 1)
		if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove expired terminal log segment: %w", err)
		}
		for index := l.options.RetainedFiles - 2; index >= 1; index-- {
			oldPath, newPath := l.archivePath(index), l.archivePath(index+1)
			if err := os.Rename(oldPath, newPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("shift terminal log segment %q: %w", filepath.Base(oldPath), err)
			}
		}
		if err := os.Rename(l.path, l.archivePath(1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("archive terminal log segment: %w", err)
		}
	}
	file, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create next terminal log segment: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(l.path)
		return fmt.Errorf("secure next terminal log segment: %w", err)
	}
	l.file = file
	return nil
}

func (l *Logger) archivePath(index int) string {
	extension := filepath.Ext(l.path)
	base := strings.TrimSuffix(l.path, extension)
	return fmt.Sprintf("%s.%03d%s", base, index, extension)
}

func (l *Logger) setErr(err error) {
	if err == nil {
		return
	}
	l.errMu.Lock()
	if l.err == nil {
		l.err = err
	}
	l.errMu.Unlock()
}

func safeLabel(label string) string {
	var result strings.Builder
	for _, char := range label {
		if len(result.String()) >= 40 {
			break
		}
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			result.WriteRune(char)
		} else if char == ' ' || char == '.' {
			result.WriteByte('-')
		}
	}
	value := strings.Trim(result.String(), "-_")
	if value == "" {
		return "terminal"
	}
	return filepath.Base(value)
}
