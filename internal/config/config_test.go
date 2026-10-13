package config

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestConfigAndStateRoundTrip(t *testing.T) {
	t.Setenv("COMI_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil || len(cfg.Projects) != 0 {
		t.Fatalf("empty config: got %+v, %v", cfg, err)
	}

	added := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	cfg.Projects = []Project{{Name: "blog", Path: "/code/blog", Schedule: "30m", Branch: "main", NoPush: true, Added: added}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(loaded.Projects))
	}
	p := loaded.Projects[0]
	if p.Name != "blog" || p.Schedule != "30m" || p.Branch != "main" || !p.NoPush || !p.Added.Equal(added) {
		t.Errorf("loaded project = %+v", p)
	}

	state := State{"/code/blog": added.Add(time.Hour)}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	loadedState, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !loadedState["/code/blog"].Equal(added.Add(time.Hour)) {
		t.Errorf("loaded state = %v", loadedState)
	}
}

func TestStateLastRun(t *testing.T) {
	added := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	p := Project{Path: "/code/blog", Added: added}

	if got := (State{}).LastRun(p); !got.Equal(added) {
		t.Errorf("never ran: got %s, want Added", got)
	}
	ran := added.Add(time.Hour)
	if got := (State{p.Path: ran}).LastRun(p); !got.Equal(ran) {
		t.Errorf("ran after add: got %s, want %s", got, ran)
	}
	// Re-adding a project resets Added, which must win over an older run.
	if got := (State{p.Path: added.Add(-time.Hour)}).LastRun(p); !got.Equal(added) {
		t.Errorf("re-added: got %s, want Added", got)
	}
}

func TestConfigFind(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Projects: []Project{
		{Name: "blog", Path: filepath.Join(dir, "blog")},
		{Name: "api", Path: filepath.Join(dir, "api")},
	}}

	tests := map[string]int{
		"blog":                    0,
		"api":                     1,
		filepath.Join(dir, "api"): 1,
		"missing":                 -1,
	}
	if runtime.GOOS == "windows" {
		tests[strings.ToUpper(filepath.Join(dir, "api"))] = 1
	}
	for key, want := range tests {
		if got := cfg.Find(key); got != want {
			t.Errorf("Find(%q) = %d, want %d", key, got, want)
		}
	}
}

func TestUniqueName(t *testing.T) {
	cfg := &Config{Projects: []Project{{Name: "blog"}, {Name: "blog-2"}}}
	if got := cfg.UniqueName("blog", -1); got != "blog-3" {
		t.Errorf("new project: got %q, want blog-3", got)
	}
	if got := cfg.UniqueName("blog", 0); got != "blog" {
		t.Errorf("same project keeps its name: got %q", got)
	}
	if got := cfg.UniqueName("api", -1); got != "api" {
		t.Errorf("free name: got %q", got)
	}
}
