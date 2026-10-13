// Package git runs git commands for comi.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Timeout bounds every git command, so a hung network call never blocks
// the scheduler.
const Timeout = 2 * time.Minute

// Run runs git in dir and returns its trimmed stdout. On failure the error
// carries git's own message.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	return RunEnv(ctx, dir, nil, args...)
}

// RunEnv is Run with extra environment variables.
func RunEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Never block on an interactive credential prompt.
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = out
		}
		if msg != "" {
			err = errors.New(msg)
		}
	}
	return out, err
}

// Root returns the top-level directory of the repository containing dir.
func Root(dir string) (string, error) {
	out, err := Run(context.Background(), dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not a git repository", dir)
	}
	return filepath.Clean(out), nil
}

// Dir returns the absolute path of the repository's .git directory.
func Dir(ctx context.Context, repo string) (string, error) {
	return Run(ctx, repo, "rev-parse", "--absolute-git-dir")
}

// CurrentBranch returns the checked-out branch, or an error in detached HEAD.
func CurrentBranch(ctx context.Context, repo string) (string, error) {
	return Run(ctx, repo, "symbolic-ref", "--short", "-q", "HEAD")
}

// Resolve returns the commit a revision points to, or "" if it doesn't exist.
func Resolve(ctx context.Context, repo, rev string) string {
	out, err := Run(ctx, repo, "rev-parse", "--verify", "-q", rev)
	if err != nil {
		return ""
	}
	return out
}

// HasRemote reports whether the repository has a remote with this name.
func HasRemote(ctx context.Context, repo, name string) bool {
	_, err := Run(ctx, repo, "remote", "get-url", name)
	return err == nil
}

// CheckBranchName returns an error if name is not a valid branch name.
func CheckBranchName(repo, name string) error {
	if _, err := Run(context.Background(), repo, "check-ref-format", "--branch", name); err != nil {
		return fmt.Errorf("invalid branch name %q", name)
	}
	return nil
}
