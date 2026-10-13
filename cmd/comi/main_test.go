package main

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"comi/internal/config"
	"comi/internal/git"
	"comi/internal/gittest"
	"comi/internal/snapshot"
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
	if err != nil || time.Since(state[commitRepo]) > time.Minute || time.Since(state[snapRepo]) > time.Minute {
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
