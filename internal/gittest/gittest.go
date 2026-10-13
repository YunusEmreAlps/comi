// Package gittest creates throwaway git repositories for tests.
package gittest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"comi/internal/git"
)

// NewRepo creates a repository on branch main with one commit, pushed to a
// bare remote that is set as origin.
func NewRepo(t *testing.T) (repo, remote string) {
	t.Helper()
	base := t.TempDir()
	repo = filepath.Join(base, "repo")
	remote = filepath.Join(base, "remote.git")

	Git(t, base, "init", "-q", "--bare", "-b", "main", remote)
	repo = NewLocalRepo(t, repo)
	Git(t, repo, "remote", "add", "origin", remote)
	WriteFile(t, repo, "README.md", "hello\n")
	Git(t, repo, "add", "-A")
	Git(t, repo, "commit", "-q", "-m", "init")
	Git(t, repo, "push", "-q", "-u", "origin", "main")
	return repo, remote
}

// NewLocalRepo creates an empty repository (no commits, no remote) at dir.
func NewLocalRepo(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	Git(t, filepath.Dir(dir), "init", "-q", "-b", "main", dir)
	for _, kv := range [][2]string{
		{"user.name", "comi test"},
		{"user.email", "test@example.com"},
		{"commit.gpgsign", "false"},
		{"core.autocrlf", "false"},
	} {
		Git(t, dir, "config", kv[0], kv[1])
	}
	return dir
}

// Git runs a git command and fails the test if it fails.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git.Run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// WriteFile writes a file inside dir, creating parent directories.
func WriteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ReadFile returns the content of a file inside dir.
func ReadFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// StartMerge leaves repo (on main, from NewRepo) in a conflicted merge.
func StartMerge(t *testing.T, repo string) {
	t.Helper()
	Git(t, repo, "checkout", "-q", "-b", "other")
	WriteFile(t, repo, "README.md", "other\n")
	Git(t, repo, "commit", "-q", "-am", "other")
	Git(t, repo, "checkout", "-q", "main")
	WriteFile(t, repo, "README.md", "main\n")
	Git(t, repo, "commit", "-q", "-am", "main")
	if _, err := git.Run(context.Background(), repo, "merge", "other"); err == nil {
		t.Fatal("expected a merge conflict")
	}
}
