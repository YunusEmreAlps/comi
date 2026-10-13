package lock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireLock(t *testing.T) {
	dir := t.TempDir()

	release, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second lock: got %v, want an 'already running' error", err)
	}

	release()
	release, err = Acquire(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release()
}

func TestAcquireLockTakesOverStaleLock(t *testing.T) {
	dir := t.TempDir()

	// A PID no running process has, and a corrupt file: both are stale.
	for _, content := range []string{"999999999", "garbage", ""} {
		if err := os.WriteFile(filepath.Join(dir, fileName), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		release, err := Acquire(dir)
		if err != nil {
			t.Fatalf("stale lock %q: %v", content, err)
		}
		release()
	}
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("current process should be alive")
	}
	if processAlive(999999999) {
		t.Error("PID 999999999 should not be alive")
	}
}
