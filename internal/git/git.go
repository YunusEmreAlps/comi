// Package git runs git commands for comi.
package git

import (
	"bufio"
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
	return RunInput(ctx, dir, nil, "", args...)
}

// RunEnv is Run with extra environment variables.
func RunEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	return RunInput(ctx, dir, env, "", args...)
}

// RunInput is RunEnv with input on stdin.
func RunInput(ctx context.Context, dir string, env []string, input string, args ...string) (string, error) {
	out, err := run(ctx, dir, env, input, args)
	return strings.TrimSpace(out), err
}

// RunZ runs a git command whose output is NUL-separated (-z) and returns
// the fields, so paths come back exactly as they are.
func RunZ(ctx context.Context, dir string, env []string, args ...string) ([]string, error) {
	out, err := run(ctx, dir, env, "", args)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(out, "\x00"), "\x00"), nil
}

func run(ctx context.Context, dir string, env []string, input string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	cmd := command(ctx, dir, env, args)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), gitError(err, stderr.String(), stdout.String())
}

// Lines runs git in dir and calls fn with each line of its output until fn
// returns false, then stops git, so a long output that is only needed in
// part is never produced in full. After an early stop the error is nil.
func Lines(ctx context.Context, dir string, fn func(line string) bool, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	cmd := command(ctx, dir, nil, args)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(nil, 1<<20)
	stopped := false
	for !stopped && sc.Scan() {
		stopped = !fn(sc.Text())
	}
	if stopped || sc.Err() != nil {
		cancel() // kills git, which may still be writing
	}
	err = cmd.Wait()
	if stopped {
		return nil
	}
	if sc.Err() != nil {
		return sc.Err()
	}
	return gitError(err, stderr.String(), "")
}

func command(ctx context.Context, dir string, env []string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Never block on an interactive credential prompt.
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	return cmd
}

// gitError replaces err with git's own message, if it printed one.
func gitError(err error, stderr, stdout string) error {
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = strings.TrimSpace(stdout)
	}
	if msg != "" {
		return errors.New(msg)
	}
	return err
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
	ref, err := Run(ctx, repo, "symbolic-ref", "-q", "HEAD")
	return BranchName(ref), err
}

// BranchName turns the full name HEAD points to into a branch name:
// refs/heads/main is main.
func BranchName(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

// Resolve returns the commit a revision points to, or "" if it doesn't exist.
func Resolve(ctx context.Context, repo, rev string) string {
	out, err := Run(ctx, repo, "rev-parse", "--verify", "-q", rev)
	if err != nil {
		return ""
	}
	return out
}

// ResolveAll is Resolve for several revisions at once, with one git process.
func ResolveAll(ctx context.Context, repo string, revs ...string) ([]string, error) {
	out, err := RunInput(ctx, repo, nil, strings.Join(revs, "\n")+"\n", "cat-file", "--batch-check=%(objectname)")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(out, "\n")
	if len(lines) != len(revs) {
		return nil, fmt.Errorf("cat-file: %d results for %d revisions", len(lines), len(revs))
	}
	ids := make([]string, len(revs))
	for i, line := range lines {
		// Revisions that don't resolve come back as "<rev> missing".
		if !strings.Contains(line, " ") {
			ids[i] = line
		}
	}
	return ids, nil
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
