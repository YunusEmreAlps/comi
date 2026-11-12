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

func TestConcurrentAcquire(t *testing.T) {
	dir := t.TempDir()
	const contenders = 16
	type result struct {
		release func()
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, contenders)
	for range contenders {
		go func() {
			<-start
			release, err := Acquire(dir)
			results <- result{release, err}
		}()
	}
	close(start)
	acquired := 0
	for range contenders {
		r := <-results
		if r.err == nil {
			acquired++
			defer r.release() // hold every acquired lock until all contenders finish
		} else if !strings.Contains(r.err.Error(), "already running") {
			t.Errorf("unexpected lock error: %v", r.err)
		}
	}
	if acquired != 1 {
		t.Errorf("%d contenders acquired the lock, want 1", acquired)
	}
}
