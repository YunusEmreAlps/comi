// Package lock makes sure only one comi scheduler runs per data directory.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const fileName = "comi.lock"

var ErrBusy = errors.New("lock is held")

// Acquire takes the lock in dir, so two schedulers never commit the same
// project at once. The OS releases the lock when the handle closes or the
// process exits. The file remains so all contenders lock the same file.
func Acquire(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fileName)
	release, err = AcquireFile(path)
	if errors.Is(err, ErrBusy) {
		return nil, fmt.Errorf("comi is already running (lock: %s): %w", path, err)
	}
	return release, err
}

// AcquireFile takes an exclusive lock on path without waiting.
func AcquireFile(path string) (func(), error) {
	return acquireFile(path, os.O_CREATE|os.O_RDWR)
}

// Running reports whether a scheduler holds the lock in dir.
func Running(dir string) (bool, error) {
	release, err := acquireFile(filepath.Join(dir, fileName), os.O_RDWR)
	switch {
	case errors.Is(err, ErrBusy):
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	default:
		release()
		return false, nil
	}
}

func acquireFile(path string, flags int) (func(), error) {
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, err
	}
	if err := tryLock(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return func() { f.Close() }, nil
}
