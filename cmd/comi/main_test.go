package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/comi/internal/config"
	"github.com/YunusEmreAlps/comi/internal/git"
	"github.com/YunusEmreAlps/comi/internal/gittest"
	"github.com/YunusEmreAlps/comi/internal/lock"
	"github.com/YunusEmreAlps/comi/internal/snapshot"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		args     []string
		pos      []string
		schedule string
		noPush   bool
	}{
		{[]string{"path"}, []string{"path"}, "", false},
		{[]string{"path", "-s", "2h", "-no-push"}, []string{"path"}, "2h", true},
		{[]string{"-s", "2h", "path"}, []string{"path"}, "2h", false},
		{[]string{"-s", "2h", "a", "-no-push", "b"}, []string{"a", "b"}, "2h", true},
		{nil, nil, "", false},
	}
	for _, tt := range tests {
		fs := newFlagSet("test")
		fs.SetOutput(io.Discard)
		schedule := fs.String("s", "", "")
		noPush := fs.Bool("no-push", false, "")

		pos, err := parseArgs(fs, tt.args)
		if err != nil {
			t.Errorf("%v: %v", tt.args, err)
			continue
		}
		if !reflect.DeepEqual(pos, tt.pos) || *schedule != tt.schedule || *noPush != tt.noPush {
			t.Errorf("%v: got pos=%v s=%q no-push=%v", tt.args, pos, *schedule, *noPush)
		}
	}

	fs := newFlagSet("test")
	fs.SetOutput(io.Discard)
	if _, err := parseArgs(fs, []string{"path", "-x"}); err == nil {
		t.Error("expected an error for an unknown flag")
	}
}

func TestRunDue(t *testing.T) {
	t.Setenv("COMI_HOME", t.TempDir())
	commitRepo, commitRemote := gittest.NewRepo(t)
	snapRepo, _ := gittest.NewRepo(t)

	cfg := &config.Config{Projects: []config.Project{
		{Name: "commit", Path: commitRepo, Schedule: "1h", Added: time.Now().Add(-2 * time.Hour)},
		{Name: "snap", Path: snapRepo, Mode: config.ModeSnapshot, Schedule: "1h", NoPush: true, Added: time.Now().Add(-2 * time.Hour)},
	}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	commits := func() string { return gittest.Git(t, commitRemote, "rev-list", "--count", "main") }

	gittest.WriteFile(t, commitRepo, "a.txt", "a")
	gittest.WriteFile(t, snapRepo, "a.txt", "a")
	if err := runDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := commits(); got != "2" {
		t.Errorf("commit mode: remote has %s commits, want 2", got)
	}
	if git.Resolve(context.Background(), snapRepo, snapshot.Ref("main")) == "" {
		t.Error("snapshot mode: no snapshot was taken")
	}
	if status := gittest.Git(t, snapRepo, "status", "--porcelain"); !strings.Contains(status, "a.txt") {
		t.Error("snapshot mode must leave the change uncommitted")
	}
	state, err := config.LoadState()
	if err != nil || time.Since(state[commitRepo].LastScheduled) > time.Minute || time.Since(state[snapRepo].LastScheduled) > time.Minute {
		t.Fatalf("last runs not recorded: %v, %v", state, err)
	}

	// Both ran just now, so the next run is an hour away.
	gittest.WriteFile(t, commitRepo, "b.txt", "b")
	if err := runDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := commits(); got != "2" {
		t.Errorf("project that is not due was committed: remote has %s commits", got)
	}
}

func TestDiffShowsNewUntrackedFiles(t *testing.T) {
	t.Setenv("COMI_HOME", t.TempDir())
	repo := gittest.NewLocalRepo(t, t.TempDir())
	gittest.WriteFile(t, repo, "saved.txt", "snapshot\n")
	if _, err := snapshot.Take(context.Background(), repo, snapshot.Options{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	gittest.WriteFile(t, repo, "untracked.txt", "new work\n")

	output, err := os.Create(filepath.Join(t.TempDir(), "diff.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	stdout := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = stdout }()
	if err := cmdDiff([]string{"latest", "-p", repo, "-stat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "untracked.txt") || strings.Contains(string(data), "saved.txt") {
		t.Errorf("unexpected diff output: %q", data)
	}
	if got := gittest.Git(t, repo, "ls-files"); got != "" {
		t.Errorf("diff added files to the user's index: %q", got)
	}
}

func TestStatusShowsManualRunAndPendingSnapshot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMI_HOME", dir)
	repo := gittest.NewLocalRepo(t, t.TempDir())
	p := config.Project{Name: "app", Path: repo, Mode: config.ModeSnapshot, Schedule: "5m", Added: time.Now()}
	cfg := config.Config{Projects: []config.Project{p}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	gittest.WriteFile(t, repo, "new.txt", "work\n")
	if err := cmdNow([]string{"app"}); err != nil {
		t.Fatal(err)
	}
	cfg.Projects[0].Keep = "invalid"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := cmdNow([]string{"app"}); err == nil {
		t.Fatal("expected the second run to fail")
	}
	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	r := state[p.Path]
	if r.LastSuccess.IsZero() || r.LastFailure.IsZero() || !r.LastScheduled.IsZero() {
		t.Fatalf("unexpected manual run history: %+v", r)
	}
	release, err := lock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var out bytes.Buffer
	if err := showStatus(context.Background(), &out, []string{"app"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Scheduler: running", "Latest local snapshot (main):", "Push: pending (origin is missing)", "Last error ("} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q from status:\n%s", want, &out)
		}
	}
}

func TestStatusShowsCommitPushState(t *testing.T) {
	repo := gittest.NewLocalRepo(t, t.TempDir())
	gittest.Git(t, repo, "commit", "--allow-empty", "-qm", "base")
	p := config.Project{Path: repo}
	ctx := context.Background()
	if got, err := commitPushStatus(ctx, p, "main"); err != nil || got != "no upstream" {
		t.Fatalf("commit push status without an upstream: %q, %v", got, err)
	}
	gittest.Git(t, repo, "remote", "add", "origin", filepath.Join(t.TempDir(), "origin.git"))
	gittest.Git(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	gittest.Git(t, repo, "branch", "--set-upstream-to=origin/main", "main")
	gittest.Git(t, repo, "commit", "--allow-empty", "-qm", "outgoing")
	if got, err := commitPushStatus(ctx, p, "main"); err != nil || got != "pending (1 outgoing commit)" {
		t.Errorf("commit push status with an upstream: %q, %v", got, err)
	}
}

func TestRestorePreviewAndProjectRelativePaths(t *testing.T) {
	t.Setenv("COMI_HOME", t.TempDir())
	repo := gittest.NewLocalRepo(t, filepath.Join(t.TempDir(), "project with spaces"))
	cfg := config.Config{Projects: []config.Project{{Name: "app", Path: repo}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	gittest.WriteFile(t, repo, "src/saved.txt", "snapshot\n")
	if _, err := snapshot.Take(ctx, repo, snapshot.Options{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	snap, err := snapshot.Find(ctx, repo, "main", "latest", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	gittest.WriteFile(t, repo, "src/saved.txt", "current\n")
	gittest.WriteFile(t, repo, "staged.txt", "staged\n")
	gittest.Git(t, repo, "add", "staged.txt")
	gittest.WriteFile(t, repo, "untracked.txt", "keep\n")
	beforeObjects := gittest.Git(t, repo, "count-objects", "-v")
	beforeIndex := gittest.Git(t, repo, "ls-files", "--stage")
	plan, err := snapshot.PreviewRestore(ctx, repo, snap, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Write, []string{"src/saved.txt"}) || !reflect.DeepEqual(plan.Delete, []string{"staged.txt"}) {
		t.Errorf("unexpected restore plan: %+v", plan)
	}
	if err := cmdRestore([]string{"latest", "-p", "app", "-n", "src/saved.txt"}); err != nil {
		t.Fatal(err)
	}
	if got := gittest.ReadFile(t, repo, "src/saved.txt"); got != "current\n" {
		t.Errorf("preview modified the worktree: %q", got)
	}
	if got := gittest.Git(t, repo, "rev-parse", snapshot.Ref("main")); got != snap.ID {
		t.Errorf("preview modified the snapshot ref: %q", got)
	}
	if got := gittest.Git(t, repo, "count-objects", "-v"); got != beforeObjects {
		t.Errorf("preview left new objects: %s", got)
	}
	if got := gittest.Git(t, repo, "ls-files", "--stage"); got != beforeIndex {
		t.Errorf("preview modified the index: %s", got)
	}
	if err := cmdRestore([]string{"latest", "-p", "app", "src/saved.txt"}); err != nil {
		t.Fatal(err)
	}
	if got := gittest.ReadFile(t, repo, "src/saved.txt"); got != "snapshot\n" {
		t.Errorf("project-relative restore produced %q", got)
	}
}

func TestAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "just now",
		5 * time.Minute:  "5m ago",
		3 * time.Hour:    "3h ago",
		72 * time.Hour:   "3d ago",
	} {
		if got := age(d); got != want {
			t.Errorf("age(%s) = %q, want %q", d, got, want)
		}
	}
}
