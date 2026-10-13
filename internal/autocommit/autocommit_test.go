package autocommit

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"comi/internal/gittest"
)

var now = time.Date(2026, 1, 15, 20, 0, 0, 0, time.Local)

func sync(t *testing.T, repo string, opts Options) (string, error) {
	t.Helper()
	return Sync(context.Background(), repo, opts, now)
}

func TestRenderMessage(t *testing.T) {
	now := time.Date(2026, 1, 15, 9, 5, 0, 0, time.UTC)
	tests := map[string]string{
		"":                             "auto: 2026-01-15 09:05",
		"wip: {files} files at {time}": "wip: 3 files at 09:05",
		"daily: {date}":                "daily: 2026-01-15",
		"no placeholders":              "no placeholders",
		"{date} {date} {files}{files}": "2026-01-15 2026-01-15 33",
	}
	for tmpl, want := range tests {
		if got := RenderMessage(tmpl, now, 3); got != want {
			t.Errorf("RenderMessage(%q) = %q, want %q", tmpl, got, want)
		}
	}
}

func TestSyncNoChanges(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	msg, err := sync(t, repo, Options{})
	if err != nil || msg != "no changes" {
		t.Fatalf("got %q, %v; want no changes", msg, err)
	}
}

func TestSyncCommitsAndPushes(t *testing.T) {
	repo, remote := gittest.NewRepo(t)
	gittest.WriteFile(t, repo, "a.txt", "a")
	gittest.WriteFile(t, repo, "README.md", "changed\n")

	msg, err := sync(t, repo, Options{Message: "save {files} at {time}"})
	if err != nil || msg != "committed and pushed" {
		t.Fatalf("got %q, %v", msg, err)
	}
	if got := gittest.Git(t, remote, "log", "-1", "--format=%s", "main"); got != "save 2 at 20:00" {
		t.Errorf("remote commit message = %q", got)
	}
	if status := gittest.Git(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("working tree not clean: %q", status)
	}
}

func TestSyncNoPush(t *testing.T) {
	repo, remote := gittest.NewRepo(t)
	before := gittest.Git(t, remote, "rev-parse", "main")
	gittest.WriteFile(t, repo, "a.txt", "a")

	msg, err := sync(t, repo, Options{NoPush: true})
	if err != nil || msg != "committed" {
		t.Fatalf("got %q, %v", msg, err)
	}
	if after := gittest.Git(t, remote, "rev-parse", "main"); after != before {
		t.Error("remote changed although NoPush is set")
	}
	if got := gittest.Git(t, repo, "log", "-1", "--format=%s"); got != "auto: 2026-01-15 20:00" {
		t.Errorf("default message = %q", got)
	}
}

func TestSyncRetriesFailedPush(t *testing.T) {
	repo, remote := gittest.NewRepo(t)
	gittest.Git(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	gittest.WriteFile(t, repo, "a.txt", "a")

	if _, err := sync(t, repo, Options{}); err == nil || !strings.Contains(err.Error(), "push failed") {
		t.Fatalf("got %v, want a push failure", err)
	}

	gittest.Git(t, repo, "remote", "set-url", "origin", remote)
	msg, err := sync(t, repo, Options{})
	if err != nil || msg != "pushed pending commits" {
		t.Fatalf("got %q, %v; want pushed pending commits", msg, err)
	}
	if ahead := gittest.Git(t, repo, "rev-list", "--count", "@{u}..HEAD"); ahead != "0" {
		t.Errorf("%s commits still unpushed", ahead)
	}
}

func TestSyncSetsUpstreamOnFirstPush(t *testing.T) {
	repo, remote := gittest.NewRepo(t)
	gittest.Git(t, repo, "checkout", "-q", "-b", "feature")
	gittest.WriteFile(t, repo, "a.txt", "a")

	msg, err := sync(t, repo, Options{})
	if err != nil || msg != "committed and pushed" {
		t.Fatalf("got %q, %v", msg, err)
	}
	if up := gittest.Git(t, repo, "rev-parse", "--abbrev-ref", "@{u}"); up != "origin/feature" {
		t.Errorf("upstream = %q, want origin/feature", up)
	}
	gittest.Git(t, remote, "rev-parse", "--verify", "feature")
}

func TestSyncSkips(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, repo string)
		branch string
		reason string
	}{
		{
			name:   "detached HEAD",
			setup:  func(t *testing.T, repo string) { gittest.Git(t, repo, "checkout", "-q", "--detach") },
			reason: "detached HEAD",
		},
		{
			name:   "branch lock",
			setup:  func(t *testing.T, repo string) { gittest.Git(t, repo, "checkout", "-q", "-b", "feature") },
			branch: "main",
			reason: `on branch "feature", only commits on "main"`,
		},
		{
			name:   "merge in progress",
			setup:  gittest.StartMerge,
			reason: "merge in progress",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, _ := gittest.NewRepo(t)
			tt.setup(t, repo)
			gittest.WriteFile(t, repo, "a.txt", "a")
			head := gittest.Git(t, repo, "rev-parse", "HEAD")

			msg, err := sync(t, repo, Options{Branch: tt.branch})
			if err != nil || msg != "skipped: "+tt.reason {
				t.Fatalf("got %q, %v; want skipped: %s", msg, err, tt.reason)
			}
			if after := gittest.Git(t, repo, "rev-parse", "HEAD"); after != head {
				t.Error("a commit was made although the run was skipped")
			}
		})
	}
}
