package snapshot

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"comi/internal/git"
	"comi/internal/gittest"
)

func TestParsePolicy(t *testing.T) {
	valid := map[string]Policy{
		"":             DefaultPolicy,
		"24h,7d,30d":   DefaultPolicy,
		" 1d, 7d,30d ": DefaultPolicy,
		"forever":      {},
		"0,0,90d":      {Daily: 90 * day},
		"2h,48h,14d":   {All: 2 * time.Hour, Hourly: 2 * day, Daily: 14 * day},
	}
	for in, want := range valid {
		got, err := ParsePolicy(in)
		if err != nil || got != want {
			t.Errorf("ParsePolicy(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"7d", "1d,7d", "1d,7d,30d,90d", "7d,1d,30d", "x,7d,30d", "-1h,7d,30d", "0,0,0", "1w,2w,4w"} {
		if _, err := ParsePolicy(in); err == nil {
			t.Errorf("ParsePolicy(%q): expected an error", in)
		}
	}

	for in, want := range map[string]string{"": "1d,7d,30d", "forever": "forever", "2h,36h,14d": "2h,36h,14d"} {
		p, _ := ParsePolicy(in)
		if got := p.String(); got != want {
			t.Errorf("%q.String() = %q, want %q", in, got, want)
		}
	}
}

func TestPolicyKeep(t *testing.T) {
	now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.Local)
	at := func(day, hour, min int) time.Time { return time.Date(2026, 1, day, hour, min, 0, 0, time.Local) }

	tests := []struct {
		name string
		time time.Time
		keep bool
	}{
		{"1h old: all tier", now.Add(-time.Hour), true},
		{"23h old: all tier", now.Add(-23 * time.Hour), true},
		{"hourly tier, newest of 10:xx", at(29, 10, 50), true},
		{"hourly tier, older in 10:xx", at(29, 10, 10), false},
		{"hourly tier, newest of 09:xx", at(29, 9, 30), true},
		{"daily tier, newest of Jan 20", at(20, 18, 0), true},
		{"daily tier, older on Jan 20", at(20, 8, 0), false},
		{"daily tier, newest of Jan 19", at(19, 8, 0), true},
		{"expired: 40 days old", now.Add(-40 * day), false},
	}
	snaps := make([]Snapshot, len(tests))
	for i, tt := range tests {
		snaps[i] = Snapshot{Time: tt.time}
	}
	kept := DefaultPolicy.keep(snaps, now)
	for i, tt := range tests {
		if kept[i] != tt.keep {
			t.Errorf("%s: kept = %v, want %v", tt.name, kept[i], tt.keep)
		}
	}

	if kept := (Policy{}).keep(snaps, now); strings.Contains(fmt.Sprint(kept), "false") {
		t.Errorf("forever must keep everything: %v", kept)
	}
	// The newest snapshot is kept however old it is.
	if kept := DefaultPolicy.keep([]Snapshot{{Time: now.Add(-100 * day)}}, now); !kept[0] {
		t.Error("the only snapshot was not kept")
	}
}

func TestPolicyDue(t *testing.T) {
	now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.Local)
	snaps := func(ages ...time.Duration) []Snapshot {
		s := []Snapshot{{Time: now}}
		for _, a := range ages {
			s = append(s, Snapshot{Time: now.Add(-a)})
		}
		return s
	}
	tests := []struct {
		name string
		snap []Snapshot
		due  bool
	}{
		{"nothing to remove", snaps(time.Hour, 2*time.Hour), false},
		// now is 12:00, so these pairs fall in the same hour (10:xx).
		{"extra in the same hour, just past 24h", snaps(25*time.Hour+30*time.Minute, 25*time.Hour+40*time.Minute), false},
		{"extra in the same hour, past 48h", snaps(49*time.Hour+30*time.Minute, 49*time.Hour+40*time.Minute), true},
		{"expired, just past 30d", snaps(30*day + time.Hour), false},
		{"expired, past 31d", snaps(31*day + time.Hour), true},
	}
	for _, tt := range tests {
		kept := DefaultPolicy.keep(tt.snap, now)
		if got := DefaultPolicy.due(tt.snap, kept, now); got != tt.due {
			t.Errorf("%s: due = %v, want %v (kept %v)", tt.name, got, tt.due, kept)
		}
	}
}

// takeAt writes content to a.txt and snapshots it at the given time.
func takeAt(t *testing.T, repo, content string, at time.Time) {
	t.Helper()
	gittest.WriteFile(t, repo, "a.txt", content)
	if msg := take(t, repo, Options{}, at); !strings.HasPrefix(msg, "snapshot ") {
		t.Fatalf("snapshot at %s: %q", at, msg)
	}
}

func TestPrune(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.Local)
	// Oldest to newest: two in one hour three days ago (one goes), one that
	// expired (goes), and two recent ones.
	takeAt(t, repo, "expired", now.Add(-40*day))
	takeAt(t, repo, "hour-old", now.Add(-3*day-50*time.Minute))
	takeAt(t, repo, "hour-new", now.Add(-3*day-10*time.Minute))
	takeAt(t, repo, "recent-1", now.Add(-2*time.Hour))
	takeAt(t, repo, "recent-2", now.Add(-time.Hour))
	before, _ := List(ctx, repo, "main", 100)
	head := gittest.Git(t, repo, "rev-parse", "HEAD")

	dry, err := Prune(ctx, repo, "main", DefaultPolicy, now, true)
	if err != nil || len(dry) != 2 {
		t.Fatalf("dry run: removed %d, %v; want 2", len(dry), err)
	}
	if got := git.Resolve(ctx, repo, Ref("main")); got != before[0].ID {
		t.Fatal("dry run changed the snapshots")
	}

	removed, err := Prune(ctx, repo, "main", DefaultPolicy, now, false)
	if err != nil || len(removed) != 2 {
		t.Fatalf("removed %d, %v; want 2", len(removed), err)
	}
	after, _ := List(ctx, repo, "main", 100)
	var contents []string
	for _, s := range after {
		contents = append(contents, gittest.Git(t, repo, "show", s.ID+":a.txt"))
	}
	if got := strings.Join(contents, ","); got != "recent-2,recent-1,hour-new" {
		t.Errorf("kept snapshots = %s, want recent-2,recent-1,hour-new", got)
	}
	for i, s := range after {
		if !s.Time.Equal(before[i].Time) {
			t.Errorf("snapshot %d time changed: %s -> %s", i, before[i].Time, s.Time)
		}
	}
	// The oldest kept snapshot now starts the chain, on the commit it was taken on.
	if got := gittest.Git(t, repo, "rev-parse", after[2].ID+"^1"); got != head {
		t.Errorf("oldest kept snapshot's parent = %s, want HEAD %s", got, head)
	}
	if found, err := Find(ctx, repo, "main", "90m", now); err != nil || !found.Time.Equal(before[1].Time) {
		t.Errorf("Find by age after prune = %+v, %v", found, err)
	}
	// The rewritten snapshots are exactly what commit-tree would write.
	for _, s := range after[:2] {
		if id, err := commitTree(ctx, repo, s.tree, s.parents, "main", s.Files, s.Time); err != nil || id != s.ID {
			t.Errorf("snapshot %s: commit-tree gives %s, %v", s.Short(), id, err)
		}
	}
	if leftover := gittest.Git(t, repo, "for-each-ref", rewritePrefix); leftover != "" {
		t.Errorf("rewrite left refs behind: %s", leftover)
	}

	if again, err := Prune(ctx, repo, "main", DefaultPolicy, now, false); err != nil || len(again) != 0 {
		t.Errorf("second prune removed %d, %v; want 0", len(again), err)
	}
	if gittest.Git(t, repo, "rev-parse", "HEAD") != head {
		t.Error("prune changed HEAD")
	}
}

func TestTakePrunesWhenDue(t *testing.T) {
	repo, remote := gittest.NewRepo(t)
	now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.Local)
	takeAt(t, repo, "old-1", now.Add(-3*day-50*time.Minute))
	takeAt(t, repo, "old-2", now.Add(-3*day-10*time.Minute))

	gittest.WriteFile(t, repo, "a.txt", "new")
	msg, err := Take(ctx, repo, Options{Keep: DefaultPolicy, Push: true}, now)
	if err != nil || !strings.Contains(msg, "removed 1 old snapshot") || !strings.HasSuffix(msg, ", pushed") {
		t.Fatalf("got %q, %v", msg, err)
	}
	snaps, _ := List(ctx, repo, "main", 100)
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(snaps))
	}
	// The message names the snapshot as it is after the prune.
	if !strings.Contains(msg, snaps[0].Short()) {
		t.Errorf("message %q does not name the newest snapshot %s", msg, snaps[0].Short())
	}
	if got := git.Resolve(ctx, remote, Ref("main")); got != snaps[0].ID {
		t.Errorf("remote has %s, want the pruned chain %s", got, snaps[0].ID)
	}

	// Without a keep policy nothing is removed.
	repo2, _ := gittest.NewRepo(t)
	takeAt(t, repo2, "old-1", now.Add(-3*day-50*time.Minute))
	takeAt(t, repo2, "old-2", now.Add(-3*day-10*time.Minute))
	gittest.WriteFile(t, repo2, "a.txt", "new")
	take(t, repo2, Options{}, now)
	if snaps, _ := List(ctx, repo2, "main", 100); len(snaps) != 3 {
		t.Errorf("forever policy: got %d snapshots, want 3", len(snaps))
	}
}

func TestBranches(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	if b, err := Branches(ctx, repo); err != nil || len(b) != 0 {
		t.Fatalf("no snapshots: %v, %v", b, err)
	}
	takeAt(t, repo, "a", t0)
	gittest.Git(t, repo, "checkout", "-q", "-b", "feature/x")
	takeAt(t, repo, "b", t0)
	b, err := Branches(ctx, repo)
	if err != nil || strings.Join(b, ",") != "feature/x,main" {
		t.Errorf("Branches = %v, %v", b, err)
	}
}
