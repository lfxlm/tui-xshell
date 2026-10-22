package terminalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxRetentionAge = 3650 * 24 * time.Hour

// PruneResult reports the effect of applying the global terminal-log policy.
// Active recordings are protected and can leave the directory temporarily
// above MaxTotalBytes until those recordings stop.
type PruneResult struct {
	RemovedFiles int
	RemovedBytes int64
	Remaining    int64
	ActiveBytes  int64
}

// Prune removes expired logs first, then the oldest inactive log segments
// until the directory is within MaxTotalBytes. A zero limit disables that
// policy. Session locks keep recordings opened by other processes safe.
func Prune(directory string, options Options) (PruneResult, error) {
	var result PruneResult
	if options.MaxTotalBytes < 0 || options.MaxTotalBytes > 100<<30 {
		return result, errors.New("terminal log total size must be between 0 and 100 GiB")
	}
	if options.MaxAge < 0 || options.MaxAge > maxRetentionAge {
		return result, errors.New("terminal log retention age must be between 0 and 3650 days")
	}
	entries, err := List(directory)
	if err != nil {
		return result, err
	}
	if len(entries) == 0 {
		return result, nil
	}

	locks := make(map[string]*activeLogLock)
	active := make(map[string]bool)
	defer func() {
		for _, lock := range locks {
			_ = lock.close(true)
		}
	}()
	for _, entry := range entries {
		base := logSessionBase(entry.Name)
		if _, checked := active[base]; checked {
			continue
		}
		lock, acquired, markerExists, err := acquireActiveLogLock(filepath.Join(directory, base+".active"))
		if err != nil {
			return result, fmt.Errorf("inspect active terminal log %q: %w", base, err)
		}
		active[base] = markerExists && !acquired
		if acquired && lock != nil {
			locks[base] = lock
		}
	}

	total := int64(0)
	for _, entry := range entries {
		total += entry.Size
		if active[logSessionBase(entry.Name)] {
			result.ActiveBytes += entry.Size
		}
	}
	remove := make(map[string]bool)
	if options.MaxAge > 0 {
		cutoff := time.Now().Add(-options.MaxAge)
		for _, entry := range entries {
			if !active[logSessionBase(entry.Name)] && entry.Modified.Before(cutoff) {
				remove[entry.Name] = true
				total -= entry.Size
			}
		}
	}
	if options.MaxTotalBytes > 0 && total > options.MaxTotalBytes {
		for index := len(entries) - 1; index >= 0 && total > options.MaxTotalBytes; index-- {
			entry := entries[index]
			if active[logSessionBase(entry.Name)] || remove[entry.Name] {
				continue
			}
			remove[entry.Name] = true
			total -= entry.Size
		}
	}

	for _, entry := range entries {
		if !remove[entry.Name] {
			continue
		}
		if err := removeListedLog(directory, entry.Name); err != nil {
			return result, err
		}
		result.RemovedFiles++
		result.RemovedBytes += entry.Size
	}
	result.Remaining = total
	return result, nil
}

func logSessionBase(name string) string {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if index := logSegmentIndex(name); index > 0 {
		separator := strings.LastIndexByte(stem, '.')
		if separator >= 0 {
			stem = stem[:separator]
		}
	}
	return stem
}

func removeListedLog(directory, name string) error {
	if name == "" || filepath.Base(name) != name || filepath.Ext(name) != ".log" {
		return errors.New("terminal log entry has an invalid filename")
	}
	path := filepath.Join(directory, name)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect terminal log %q before cleanup: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to clean non-regular terminal log %q", name)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove terminal log %q: %w", name, err)
	}
	return nil
}
