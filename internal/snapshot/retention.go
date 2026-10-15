package snapshot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"comi/internal/git"
)

const day = 24 * time.Hour

// Policy decides which old snapshots are kept. Going back in time, it keeps
// every snapshot younger than All, then the newest snapshot of each hour
// until Hourly, then the newest of each day until Daily. Older snapshots are
// removed. The newest snapshot is always kept. The zero Policy keeps
// everything.
type Policy struct {
	All, Hourly, Daily time.Duration
}

// DefaultPolicy keeps everything for a day, hourly snapshots for a week and
// daily snapshots for a month.
var DefaultPolicy = Policy{All: day, Hourly: 7 * day, Daily: 30 * day}

// ParsePolicy parses "24h,7d,30d" (All, Hourly, Daily) or "forever".
// An empty string means DefaultPolicy.
func ParsePolicy(s string) (Policy, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "":
		return DefaultPolicy, nil
	case "forever":
		return Policy{}, nil
	}

	invalid := fmt.Errorf(`invalid keep policy %q: use "forever" or three ages like "24h,7d,30d" (all, hourly, daily)`, s)
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return Policy{}, invalid
	}
	var d [3]time.Duration
	for i, part := range parts {
		v, err := parseAge(strings.TrimSpace(part))
		if err != nil || v < 0 {
			return Policy{}, invalid
		}
		d[i] = v
	}
	if d[0] > d[1] || d[1] > d[2] {
		return Policy{}, fmt.Errorf("invalid keep policy %q: the ages must not decrease", s)
	}
	if d[2] == 0 {
		return Policy{}, fmt.Errorf(`invalid keep policy %q: use "forever" to keep everything`, s)
	}
	return Policy{All: d[0], Hourly: d[1], Daily: d[2]}, nil
}

// parseAge is time.ParseDuration plus a "d" (day) unit.
func parseAge(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		return time.Duration(days) * day, err
	}
	return time.ParseDuration(s)
}

func (p Policy) Forever() bool { return p == Policy{} }

func (p Policy) String() string {
	if p.Forever() {
		return "forever"
	}
	return formatAge(p.All) + "," + formatAge(p.Hourly) + "," + formatAge(p.Daily)
}

func formatAge(d time.Duration) string {
	switch {
	case d == 0:
		return "0"
	case d%day == 0:
		return fmt.Sprintf("%dd", d/day)
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return d.String()
}

// keep reports, for snapshots ordered newest first, which ones the policy
// keeps.
func (p Policy) keep(snaps []Snapshot, now time.Time) []bool {
	kept := make([]bool, len(snaps))
	hours, days := map[time.Time]bool{}, map[time.Time]bool{}
	for i, s := range snaps {
		t := s.Time.Local()
		y, m, d := t.Date()
		hour := time.Date(y, m, d, t.Hour(), 0, 0, 0, time.Local)
		date := time.Date(y, m, d, 0, 0, 0, 0, time.Local)

		switch age := now.Sub(s.Time); {
		case p.Forever(), i == 0, age < p.All:
			kept[i] = true
		case age < p.Hourly:
			kept[i] = !hours[hour]
		case age < p.Daily:
			kept[i] = !days[date]
		}
		if kept[i] {
			hours[hour], days[date] = true, true
		}
	}
	return kept
}

// pruneDelay is how long a snapshot may outlive its tier before Take removes
// it. Removing snapshots rewrites the chain and changes snapshot ids, so this
// keeps it to about once a day.
const pruneDelay = day

// due reports whether a snapshot that should go has waited out pruneDelay.
func (p Policy) due(snaps []Snapshot, kept []bool, now time.Time) bool {
	for i, s := range snaps {
		if kept[i] {
			continue
		}
		age := now.Sub(s.Time)
		boundary := p.All // when it stopped being kept
		if age >= p.Daily {
			boundary = p.Daily
		} else if age >= p.Hourly {
			boundary = p.Hourly
		}
		if age-boundary >= pruneDelay {
			return true
		}
	}
	return false
}

// Prune removes the snapshots of branch that policy doesn't keep and returns
// them. With dryRun it only reports what would be removed.
//
// Snapshots form a chain, so the kept snapshots newer than the oldest removed
// one are rewritten: their files, times and parents stay the same, but they
// get new ids. Selecting snapshots by age keeps working across a prune.
// Disk space is freed once git's garbage collection drops the unreferenced
// objects.
func Prune(ctx context.Context, repo, branch string, policy Policy, now time.Time, dryRun bool) ([]Snapshot, error) {
	return prune(ctx, repo, branch, policy, now, false, dryRun)
}

func prune(ctx context.Context, repo, branch string, policy Policy, now time.Time, onlyIfDue, dryRun bool) ([]Snapshot, error) {
	if policy.Forever() {
		return nil, nil
	}
	snaps, err := List(ctx, repo, branch, 1<<30)
	if err != nil || len(snaps) == 0 {
		return nil, err
	}
	kept := policy.keep(snaps, now)
	if onlyIfDue && !policy.due(snaps, kept, now) {
		return nil, nil
	}

	var removed []Snapshot
	for i, s := range snaps {
		if !kept[i] {
			removed = append(removed, s)
		}
	}
	if len(removed) == 0 || dryRun {
		return removed, nil
	}

	// Rebuild the chain from the oldest kept snapshot up. Until the first
	// removed one, nothing changes, so those commits are reused as they are.
	var prev string
	rewriting := false
	for i := len(snaps) - 1; i >= 0; i-- {
		s := snaps[i]
		if !kept[i] {
			rewriting = true
			continue
		}
		if !rewriting {
			prev = s.ID
			continue
		}
		id, err := commitTree(ctx, repo, s.tree, []string{prev, base(snaps, i)}, branch, s.Files, s.Time)
		if err != nil {
			return nil, err
		}
		prev = id
	}

	// Fails, rather than losing it, if a snapshot was taken meanwhile.
	if _, err := git.Run(ctx, repo, "update-ref", "-m", "comi prune", Ref(branch), prev, snaps[0].ID); err != nil {
		return nil, fmt.Errorf("update-ref: %w", err)
	}
	return removed, nil
}

// base returns the commit that was checked out when snapshot i was taken:
// its second parent, or its only parent if it is the first in the chain.
func base(snaps []Snapshot, i int) string {
	parents := snaps[i].parents
	if len(parents) == 0 {
		return "" // taken before the repository had any commit
	}
	if i+1 < len(snaps) && parents[0] == snaps[i+1].ID {
		if len(parents) > 1 {
			return parents[1]
		}
		return "" // previous snapshot only: no commit yet back then
	}
	return parents[0]
}
