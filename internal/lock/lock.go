// Package lock makes sure only one comi scheduler runs per data directory.
package lock

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const fileName = "comi.lock"

// Acquire takes the lock in dir, so two schedulers never commit the same
// project at once. The lock file holds the owner's PID; a lock left behind
// by a crashed process is taken over.
func Acquire(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fileName)

	for range 2 {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, err = fmt.Fprint(f, os.Getpid())
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				os.Remove(path)
				return nil, err
			}
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}

		data, _ := os.ReadFile(path)
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 && processAlive(pid) {
			return nil, fmt.Errorf("comi is already running (pid %d)", pid)
		}
		os.Remove(path)
	}
	return nil, fmt.Errorf("could not acquire lock %s", path)
}
