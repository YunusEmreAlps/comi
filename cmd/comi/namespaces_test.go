package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YunusEmreAlps/comi/internal/config"
	"github.com/YunusEmreAlps/comi/internal/gittest"
)

func TestNamespaceTargets(t *testing.T) {
	t.Setenv("COMI_HOME", t.TempDir())
	work, personal := t.TempDir(), t.TempDir()
	workRepo := gittest.NewLocalRepo(t, filepath.Join(work, "api"))
	personalRepo := gittest.NewLocalRepo(t, filepath.Join(personal, "api"))
	for name, root := range map[string]string{"work": work, "personal": personal} {
		if err := cmdProjects([]string{"add", name, root}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cmdProjects([]string{"list"}); err != nil {
		t.Fatal(err)
	}
	gittest.WriteFile(t, personalRepo, "personal.txt", "personal work\n")
	if err := cmdSnapshot([]string{"-p", "personal/api"}); err != nil {
		t.Fatal(err)
	}
	if _, p, err := target("personal/api"); err != nil || p != nil {
		t.Fatalf("snapshot should work without registering the project: %+v, %v", p, err)
	}

	if err := cmdAdd([]string{workRepo, "-n", "api-work", "-mode", "snapshot", "-s", "5m", "-no-push"}); err != nil {
		t.Fatal(err)
	}
	gittest.WriteFile(t, workRepo, "src/app.txt", "original\n")
	if err := cmdNow([]string{"work/api"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdAdd([]string{"work/api", "-s", "10m"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"api-work", "work/api", workRepo} {
		root, p, err := target(key)
		if err != nil || p == nil || p.Name != "api-work" || p.Schedule != "10m" || root != p.Path {
			t.Fatalf("target %q returned %q, %+v, %v", key, root, p, err)
		}
	}
	var out bytes.Buffer
	if err := showStatus(context.Background(), &out, []string{"work/api"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "api-work (snapshot)") || strings.Contains(out.String(), "Last attempt: none recorded") {
		t.Errorf("status did not retain the existing project's run: %s", &out)
	}
	gittest.WriteFile(t, workRepo, "src/app.txt", "changed\n")
	if err := cmdRestore([]string{"latest", "-p", "work/api", "src/app.txt"}); err != nil {
		t.Fatal(err)
	}
	if got := gittest.ReadFile(t, workRepo, "src/app.txt"); got != "original\n" {
		t.Errorf("namespace restore produced %q", got)
	}
	if err := cmdRemove([]string{"work/api"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil || len(cfg.Projects) != 0 {
		t.Fatalf("namespace removal left a registration: %+v, %v", cfg, err)
	}
}

func TestNamespacePathBoundaries(t *testing.T) {
	t.Setenv("COMI_HOME", t.TempDir())
	parentRepo := gittest.NewLocalRepo(t, t.TempDir())
	root := filepath.Join(parentRepo, "projects")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cmdProjects([]string{"add", "work", root}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"work/../other", "work//other", "work/."} {
		if _, _, err := target(key); err == nil {
			t.Errorf("target %q escaped its namespace", key)
		}
	}
	if path, base, err := projectPath("./work/api"); err != nil || base != "" || !filepath.IsAbs(path) {
		t.Errorf("explicit relative path was expanded as a namespace: %q, %q, %v", path, base, err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(parentRepo, link); err == nil {
		if _, _, err := target("work/escape"); err == nil {
			t.Error("symlink target escaped its namespace")
		}
	}
}
