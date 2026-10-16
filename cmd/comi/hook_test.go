package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"comi/internal/config"
	"comi/internal/git"
	"comi/internal/gittest"
	"comi/internal/snapshot"
)

func TestHook(t *testing.T) {
	t.Setenv("COMI_HOME", t.TempDir())
	// The hook falls back to the current directory, which must not be the
	// repository these tests run in.
	wd, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
	ctx := context.Background()
	repo, remote := gittest.NewRepo(t)
	cfg := &config.Config{Projects: []config.Project{
		{Name: "snap", Path: repo, Mode: config.ModeSnapshot, Schedule: "1h", Added: time.Now()},
	}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	gittest.WriteFile(t, repo, "src/a.go", "package a\n")

	// Claude Code passes the agent's directory, which can be a subdirectory.
	input := fmt.Sprintf(`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":%q,"tool_input":{"command":"rm -rf src"}}`, filepath.Join(repo, "src"))
	var out strings.Builder
	runHook(strings.NewReader(input), &out)
	if !strings.HasPrefix(out.String(), "comi: snapshot ") {
		t.Errorf("output = %q", out.String())
	}
	if git.Resolve(ctx, repo, snapshot.Ref("main")) == "" {
		t.Error("no snapshot was taken")
	}
	// The project pushes, but not from the hook: the agent would wait for it.
	if git.Resolve(ctx, remote, snapshot.Ref("main")) != "" {
		t.Error("the hook pushed")
	}

	// Outside a repository, or with input it can't read, it only reports.
	for _, input := range []string{fmt.Sprintf(`{"cwd":%q}`, t.TempDir()), `{"cwd":"C:\Users"}`, `not json`} {
		out.Reset()
		runHook(strings.NewReader(input), &out)
		if !strings.HasPrefix(out.String(), "comi: ") || strings.Contains(out.String(), "snapshot ") {
			t.Errorf("%s: %q", input, out.String())
		}
	}
}
