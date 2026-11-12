package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/YunusEmreAlps/comi/internal/lock"
)

// State keeps run history by project path, separately from project settings.
type State map[string]RunState

type RunState struct {
	LastScheduled time.Time `json:"last_scheduled"`
	LastAttempt   time.Time `json:"last_attempt"`
	LastSuccess   time.Time `json:"last_success"`
	LastFailure   time.Time `json:"last_failure"`
	Result        string    `json:"result,omitempty"`
	Error         string    `json:"error,omitempty"`
}

func (r *RunState) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		// Older versions recorded only the time of a scheduled attempt.
		var at time.Time
		if err := json.Unmarshal(data, &at); err != nil {
			return err
		}
		*r = RunState{LastScheduled: at, LastAttempt: at}
		return nil
	}
	type runState RunState
	return json.Unmarshal(data, (*runState)(r))
}

func LoadState() (State, error) {
	s := State{}
	err := readJSON(stateFile, &s)
	if s == nil {
		s = State{}
	}
	return s, err
}

func (s State) Save() error { return writeJSON(stateFile, s) }

// LastRun is the reference point for the project's next scheduled run.
func (s State) LastRun(p Project) time.Time {
	if t := s[p.Path].LastScheduled; t.After(p.Added) {
		return t
	}
	return p.Added
}

type RunUpdate struct {
	Path       string
	At         time.Time
	Scheduled  bool
	Successful bool
	Result     string
	Error      string
}

// RecordRuns merges results from manual and scheduled runs without letting an
// older attempt overwrite newer status. Manual runs leave the schedule intact.
func RecordRuns(ctx context.Context, updates []RunUpdate) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		release, err := lock.AcquireFile(filepath.Join(dir, "comi-state.lock"))
		if err == nil {
			defer release()
			break
		}
		if !errors.Is(err, lock.ErrBusy) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting to record run status: %w", ctx.Err())
		case <-tick.C:
		}
	}

	state, err := LoadState()
	if err != nil {
		return err
	}
	for _, u := range updates {
		r := state[u.Path]
		if u.Scheduled && u.At.After(r.LastScheduled) {
			r.LastScheduled = u.At
		}
		if u.Successful && u.At.After(r.LastSuccess) {
			r.LastSuccess = u.At
		}
		if u.Error != "" && !u.At.Before(r.LastFailure) {
			r.LastFailure, r.Error = u.At, u.Error
		}
		if !u.At.Before(r.LastAttempt) {
			r.LastAttempt, r.Result = u.At, u.Result
		}
		state[u.Path] = r
	}
	return state.Save()
}
