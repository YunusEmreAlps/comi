// Package snapshot saves the working tree, including uncommitted and
// untracked files, as hidden commits under refs/comi/<branch>.
//
// A snapshot never touches HEAD, the current branch, the index or the working
// tree, and is safe to take in any state: detached HEAD, a conflicted merge or
// the middle of a rebase. Each snapshot's first parent is the previous
// snapshot and its second parent is HEAD at the time, so the snapshots of a
// branch form a chain that `git log --first-parent refs/comi/<branch>` walks.
//
// Snapshots can be taken every few minutes, so taking one starts as few git
// processes as it can, each reading no more than it needs: on Windows,
// starting a process costs more than most of the work.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"comi/internal/git"
)

const (
	// RefPrefix is where snapshots live, one ref per branch. Refs outside
	// refs/heads don't show up as branches, locally or on the remote.
	RefPrefix = "refs/comi/"
	// pushedPrefix remembers the last snapshot pushed, so a failed push is
	// retried without asking the remote.
	pushedPrefix = "refs/comi-pushed/"

	subjectPrefix = "comi snapshot"
	identity      = "comi <comi@localhost>"
	detached      = "detached"
)

type Options struct {
	Branch string // only snapshot while this branch is checked out; "" means any
	Push   bool   // push snapshots to origin
	Keep   Policy // which old snapshots to keep; the zero Policy keeps all
}

type Snapshot struct {
	ID     string
	Time   time.Time
	Branch string
	Files  int // files that differ from the commit checked out at the time

	tree    string
	parents []string
}

func (s Snapshot) Short() string { return s.ID[:min(len(s.ID), 8)] }

// Ref returns the snapshot ref of a branch.
func Ref(branch string) string { return RefPrefix + branch }

// CurrentBranch is the branch whose snapshots are used by default.
func CurrentBranch(ctx context.Context, repo string) string {
	if b, err := git.CurrentBranch(ctx, repo); err == nil {
		return b
	}
	return detached
}

// state is what a new snapshot is compared with.
type state struct {
	root, gitDir, branch string
	head, headTree       string // the commit checked out
	tip, tipTree         string // the branch's newest snapshot
	pushed               string // the newest snapshot origin has
}

// readState reads the state with two git processes.
func readState(ctx context.Context, repo string) (state, error) {
	var s state
	out, err := git.Run(ctx, repo, "rev-parse", "--show-toplevel", "--absolute-git-dir", "--symbolic-full-name", "HEAD")
	if err != nil {
		// On a branch without commits, HEAD doesn't resolve yet.
		if out, err = git.Run(ctx, repo, "rev-parse", "--show-toplevel", "--absolute-git-dir"); err != nil {
			return s, err
		}
		ref, _ := git.Run(ctx, repo, "symbolic-ref", "-q", "HEAD")
		out += "\n" + ref
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		return s, fmt.Errorf("rev-parse: unexpected output %q", out)
	}
	s.root, s.gitDir, s.branch = filepath.Clean(lines[0]), lines[1], detached
	if ref := lines[2]; ref != "HEAD" && ref != "" {
		s.branch = git.BranchName(ref)
	}

	ref := Ref(s.branch)
	ids, err := git.ResolveAll(ctx, repo, "HEAD", "HEAD^{tree}", ref, ref+"^{tree}", pushedPrefix+s.branch)
	if err != nil {
		return s, err
	}
	s.head, s.headTree, s.tip, s.tipTree, s.pushed = ids[0], ids[1], ids[2], ids[3], ids[4]
	return s, nil
}

// Take saves a snapshot of repo if anything changed since the last one,
// removes old snapshots that opts.Keep no longer keeps (at most about once a
// day), and pushes snapshots that haven't reached origin yet when opts.Push
// is set.
func Take(ctx context.Context, repo string, opts Options, now time.Time) (string, error) {
	st, err := readState(ctx, repo)
	if err != nil {
		return "", err
	}
	if opts.Branch != "" && st.branch != opts.Branch {
		return fmt.Sprintf("skipped: on branch %q, only snapshots %q", st.branch, opts.Branch), nil
	}

	snap, leftOut, err := save(ctx, repo, &st, now)
	if err != nil {
		return "", err
	}
	var removed []Snapshot
	if !opts.Keep.Forever() && pruneCheckDue(st.gitDir, now) {
		if removed, st.tip, err = prune(ctx, repo, st.branch, st.tip, opts.Keep, now, true, false); err != nil {
			return "", fmt.Errorf("removing old snapshots: %w", err)
		}
		markPruneCheck(st.gitDir, now)
	}

	msg := "no changes"
	if snap != nil {
		// Pruning rewrites the chain, so the new snapshot may have a new id.
		snap.ID = st.tip
		msg = fmt.Sprintf("snapshot %s (%d %s changed)", snap.Short(), snap.Files, plural(snap.Files, "file", "files"))
	}
	if len(removed) > 0 {
		msg += fmt.Sprintf(", removed %d old %s", len(removed), plural(len(removed), "snapshot", "snapshots"))
	}
	if len(leftOut) > 0 {
		msg += ", " + leftOutNote(leftOut)
	}
	if !opts.Push {
		return msg, nil
	}

	pushed, err := push(ctx, repo, st.branch, st.tip, st.pushed)
	switch {
	case err != nil && snap != nil:
		return "", fmt.Errorf("%s saved, but push failed (will retry next run): %w", msg, err)
	case err != nil:
		return "", fmt.Errorf("push: %w", err)
	case pushed && snap != nil:
		return msg + ", pushed", nil
	case pushed:
		return "pushed pending snapshot", nil
	}
	return msg, nil
}

// save writes a snapshot commit and moves the branch's snapshot ref to it.
// It returns nil when the working tree matches HEAD or the last snapshot,
// and in any case the files it left out for being too large.
func save(ctx context.Context, repo string, st *state, now time.Time) (*Snapshot, []string, error) {
	idx, err := openIndex(st.gitDir)
	if err != nil {
		return nil, nil, err
	}
	defer idx.close()

	tree, leftOut, err := idx.writeTree(ctx, st.root)
	if err != nil {
		return nil, nil, err
	}
	unchanged := func(tree string) bool { return tree == st.headTree || tree == st.tipTree }
	if !unchanged(tree) {
		// Checked only when a snapshot is about to be written: most runs
		// find nothing changed and never get here.
		synced, err := idx.syncIgnored(ctx, st.root)
		if err != nil {
			return nil, nil, err
		}
		if synced {
			if tree, leftOut, err = idx.writeTree(ctx, st.root); err != nil {
				return nil, nil, err
			}
		}
	}
	idx.keep()
	if unchanged(tree) {
		return nil, leftOut, nil // nothing uncommitted, or the same as the last snapshot
	}

	var changed string
	if st.head != "" {
		changed, err = git.Run(ctx, repo, "diff-tree", "-r", "--name-only", st.head, tree)
	} else {
		changed, err = git.Run(ctx, repo, "ls-tree", "-r", "--name-only", tree)
	}
	if err != nil {
		return nil, nil, err
	}
	if changed == "" {
		return nil, leftOut, nil // a repository without commits or files
	}
	files := len(strings.Split(changed, "\n"))

	id, err := commitTree(ctx, repo, tree, []string{st.tip, st.head}, st.branch, files, now)
	if err != nil {
		return nil, nil, err
	}
	// Passing the old value makes the update fail instead of losing a
	// snapshot written concurrently.
	if _, err := git.Run(ctx, repo, "update-ref", "-m", subjectPrefix, Ref(st.branch), id, st.tip); err != nil {
		return nil, nil, fmt.Errorf("update-ref: %w", err)
	}
	st.tip, st.tipTree = id, tree
	return &Snapshot{ID: id, Time: now, Branch: st.branch, Files: files}, leftOut, nil
}

// leftOutNote tells which files a snapshot left out for being too large.
func leftOutNote(paths []string) string {
	limit := fmt.Sprintf("over %d MB", maxFileSize>>20)
	if maxFileSize < 1<<20 {
		limit = fmt.Sprintf("over %d bytes", maxFileSize)
	}
	if len(paths) == 1 {
		return fmt.Sprintf("left out %s (%s)", paths[0], limit)
	}
	names := strings.Join(paths[:min(len(paths), 3)], ", ")
	if len(paths) > 3 {
		names += ", ..."
	}
	return fmt.Sprintf("left out %d files %s (%s)", len(paths), limit, names)
}

// pruneCheckFile records, in the git directory, when Take last looked for
// old snapshots to remove. Removing one waits a day (pruneDelay) anyway, so
// looking hourly is enough and spares most snapshots a git process.
const pruneCheckFile = "comi-pruned"

func pruneCheckDue(gitDir string, now time.Time) bool {
	data, err := os.ReadFile(filepath.Join(gitDir, pruneCheckFile))
	if err != nil {
		return true
	}
	unix, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return true
	}
	since := now.Sub(time.Unix(unix, 0))
	return since < 0 || since >= time.Hour
}

func markPruneCheck(gitDir string, now time.Time) {
	os.WriteFile(filepath.Join(gitDir, pruneCheckFile), []byte(strconv.FormatInt(now.Unix(), 10)), 0o644)
}

// commitTree writes a snapshot commit. Empty parents are skipped. The same
// inputs always produce the same commit id.
func commitTree(ctx context.Context, repo, tree string, parents []string, branch string, files int, at time.Time) (string, error) {
	args := []string{"commit-tree", tree, "-m", message(files, branch)}
	for _, p := range nonEmpty(parents...) {
		args = append(args, "-p", p)
	}
	date := fmt.Sprintf("%d +0000", at.Unix())
	id, err := git.RunEnv(ctx, repo, []string{
		"GIT_AUTHOR_NAME=comi", "GIT_AUTHOR_EMAIL=comi@localhost", "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=comi", "GIT_COMMITTER_EMAIL=comi@localhost", "GIT_COMMITTER_DATE=" + date,
	}, args...)
	if err != nil {
		return "", fmt.Errorf("commit-tree: %w", err)
	}
	return id, nil
}

// message is a snapshot commit's message; its subject is what List parses.
func message(files int, branch string) string {
	return fmt.Sprintf("%s: %d files changed\n\nbranch: %s", subjectPrefix, files, branch)
}

func nonEmpty(ids ...string) []string {
	var out []string
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

// Push sends the branch's snapshots to origin unless they are already there.
// It reports whether anything was pushed; without an origin remote it does
// nothing.
func Push(ctx context.Context, repo, branch string) (bool, error) {
	ids, err := git.ResolveAll(ctx, repo, Ref(branch), pushedPrefix+branch)
	if err != nil {
		return false, err
	}
	return push(ctx, repo, branch, ids[0], ids[1])
}

// push sends tip, the branch's newest snapshot, to origin unless pushed says
// origin has it already.
func push(ctx context.Context, repo, branch, tip, pushed string) (bool, error) {
	if tip == "" || tip == pushed {
		return false, nil
	}
	if !git.HasRemote(ctx, repo, "origin") {
		return false, nil
	}
	if _, err := git.Run(ctx, repo, "push", "-q", "origin", "+"+tip+":"+Ref(branch)); err != nil {
		return false, err
	}
	if _, err := git.Run(ctx, repo, "update-ref", pushedPrefix+branch, tip); err != nil {
		return false, err
	}
	return true, nil
}

// List returns the snapshots of a branch, newest first, at most limit.
func List(ctx context.Context, repo, branch string, limit int) ([]Snapshot, error) {
	return list(ctx, repo, branch, git.Resolve(ctx, repo, Ref(branch)), limit)
}

// list returns the snapshots in the chain that ends at tip. The chain
// continues into the branch's own history, so git is stopped at the first
// commit that isn't a snapshot instead of walking that history to its start.
func list(ctx context.Context, repo, branch, tip string, limit int) ([]Snapshot, error) {
	if tip == "" || limit <= 0 {
		return nil, nil
	}
	var snaps []Snapshot
	err := git.Lines(ctx, repo, func(line string) bool {
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) != 5 || !strings.HasPrefix(parts[4], subjectPrefix) {
			return false // reached the commit the first snapshot was taken on
		}
		var unix int64
		fmt.Sscan(parts[3], &unix)
		s := Snapshot{ID: parts[0], Time: time.Unix(unix, 0), Branch: branch, tree: parts[1], parents: strings.Fields(parts[2])}
		fmt.Sscanf(parts[4], subjectPrefix+": %d", &s.Files)
		snaps = append(snaps, s)
		return len(snaps) < limit
	}, "log", "--first-parent", fmt.Sprintf("-n%d", limit), "--format=%H%x09%T%x09%P%x09%ct%x09%s", tip)
	return snaps, err
}

// Branches returns the branches that have snapshots.
func Branches(ctx context.Context, repo string) ([]string, error) {
	out, err := git.Run(ctx, repo, "for-each-ref", "--format=%(refname)", RefPrefix)
	if err != nil || out == "" {
		return nil, err
	}
	var branches []string
	for _, ref := range strings.Split(out, "\n") {
		branches = append(branches, strings.TrimPrefix(ref, RefPrefix))
	}
	return branches, nil
}

// Find resolves a snapshot by id prefix ("3f2a91c0"), by age ("2h": the
// newest snapshot at least two hours old) or as "latest".
func Find(ctx context.Context, repo, branch, query string, now time.Time) (Snapshot, error) {
	snaps, err := List(ctx, repo, branch, 10000)
	if err != nil {
		return Snapshot{}, err
	}
	if len(snaps) == 0 {
		return Snapshot{}, fmt.Errorf("no snapshots for branch %q", branch)
	}
	if query == "latest" {
		return snaps[0], nil
	}

	if age, err := parseAge(query); err == nil {
		cutoff := now.Add(-age)
		for _, s := range snaps {
			if !s.Time.After(cutoff) {
				return s, nil
			}
		}
		return Snapshot{}, fmt.Errorf("no snapshot of %q is older than %s", branch, query)
	}

	var found []Snapshot
	for _, s := range snaps {
		if strings.HasPrefix(s.ID, strings.ToLower(query)) {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return Snapshot{}, fmt.Errorf("no snapshot %q on branch %q", query, branch)
	default:
		return Snapshot{}, fmt.Errorf("snapshot id %q is ambiguous, use more characters", query)
	}
}

// Restore puts files from a snapshot back into the working tree; with no
// paths, the whole tree. The index and HEAD are not changed. The current
// state is snapshotted first, so a restore can itself be undone.
func Restore(ctx context.Context, repo string, snap Snapshot, paths []string, now time.Time) (*Snapshot, error) {
	st, err := readState(ctx, repo)
	var backup *Snapshot
	if err == nil {
		backup, _, err = save(ctx, repo, &st, now)
	}
	if err != nil {
		return nil, fmt.Errorf("could not save the current state before restoring: %w", err)
	}
	if len(paths) == 0 {
		paths = []string{":/"}
	}
	args := append([]string{"restore", "--source=" + snap.ID, "--worktree", "--"}, paths...)
	if _, err := git.Run(ctx, repo, args...); err != nil {
		return backup, errors.New(strings.TrimPrefix(err.Error(), "error: "))
	}
	return backup, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
