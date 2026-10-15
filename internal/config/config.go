// Package config stores comi's projects and their run state on disk.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Modes a project can run in.
const (
	ModeCommit   = "commit"   // commit to the current branch (and push)
	ModeSnapshot = "snapshot" // save hidden snapshots, never touch the branch
)

type Project struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Mode     string    `json:"mode,omitempty"` // "" means ModeCommit
	Schedule string    `json:"schedule"`
	Message  string    `json:"message,omitempty"`
	Branch   string    `json:"branch,omitempty"`
	Keep     string    `json:"keep,omitempty"` // snapshot retention, see snapshot.ParsePolicy
	NoPush   bool      `json:"no_push,omitempty"`
	Added    time.Time `json:"added"`
}

func (p Project) IsSnapshot() bool { return p.Mode == ModeSnapshot }

type Config struct {
	Projects []Project `json:"projects"`
}

// State holds the last run time of each project, keyed by path. It lives in a
// separate file so the scheduler never overwrites projects added while it runs.
type State map[string]time.Time

const (
	configFile = "projects.json"
	stateFile  = "state.json"
)

// Dir is $COMI_HOME, or <user config dir>/comi.
func Dir() (string, error) {
	if d := os.Getenv("COMI_HOME"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "comi"), nil
}

func Load() (*Config, error) {
	cfg := &Config{}
	return cfg, readJSON(configFile, cfg)
}

func (c *Config) Save() error { return writeJSON(configFile, c) }

func LoadState() (State, error) {
	s := State{}
	return s, readJSON(stateFile, &s)
}

func (s State) Save() error { return writeJSON(stateFile, s) }

// LastRun is the reference point for the project's next run.
func (s State) LastRun(p Project) time.Time {
	if t := s[p.Path]; t.After(p.Added) {
		return t
	}
	return p.Added
}

// Find returns the index of the project matching a name or path, or -1.
func (c *Config) Find(key string) int {
	abs, _ := filepath.Abs(key)
	for i, p := range c.Projects {
		if p.Name == key || samePath(p.Path, abs) {
			return i
		}
	}
	return -1
}

// UniqueName returns name, or name-2, name-3... if another project (not the
// one at index self) already uses it.
func (c *Config) UniqueName(name string, self int) string {
	candidate := name
	for n := 2; ; n++ {
		if i := c.Find(candidate); i < 0 || i == self {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", name, n)
	}
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func readJSON(name string, v any) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// writeJSON writes atomically so a crash never leaves a half-written file.
func writeJSON(name string, v any) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path+".tmp", data, 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
