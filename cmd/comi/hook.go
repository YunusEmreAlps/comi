package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"comi/internal/snapshot"
)

// hookTimeout bounds a snapshot taken for a hook, as the agent's command
// waits for it. A snapshot cut short leaves nothing behind.
const hookTimeout = 30 * time.Second

// cmdHook snapshots the repository an AI agent works in, right before the
// agent runs a shell command. It is meant to run as a Claude Code PreToolUse
// hook, which passes JSON with the agent's working directory on stdin.
func cmdHook(args []string) error {
	if len(args) > 0 {
		return errors.New("usage: comi hook (reads a Claude Code hook's JSON on stdin)")
	}
	var in io.Reader = os.Stdin
	if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice != 0 {
		in = strings.NewReader("") // run by hand: snapshot the current directory
	}
	runHook(in, os.Stdout)
	return nil
}

// runHook never fails: a snapshot that can't be taken, for example outside
// a git repository, must not stop the agent's command. What happened goes
// to out, which Claude Code keeps in its debug log.
func runHook(in io.Reader, out io.Writer) {
	var input struct {
		Cwd string `json:"cwd"`
	}
	// Input that isn't the hook's JSON says nothing reliable about where
	// the agent works, so nothing is snapshotted. No input at all means a
	// run by hand, in the current directory.
	if err := json.NewDecoder(in).Decode(&input); err != nil && err != io.EOF {
		fmt.Fprintln(out, "comi: reading the hook's input:", err)
		return
	}
	if input.Cwd == "" {
		input.Cwd = "."
	}

	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	msg, err := hookSnapshot(ctx, input.Cwd)
	if err != nil {
		fmt.Fprintln(out, "comi:", err)
		return
	}
	fmt.Fprintln(out, "comi:", msg)
}

func hookSnapshot(ctx context.Context, dir string) (string, error) {
	repo, p, err := target(dir)
	if err != nil {
		return "", err
	}
	opts, err := snapshotOptions(p)
	if err != nil {
		return "", err
	}
	// The agent waits for the hook, so pushing is left to the scheduler.
	opts.Push = false
	return snapshot.Take(ctx, repo, opts, time.Now())
}
