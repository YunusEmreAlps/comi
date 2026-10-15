// Package snapshot saves the working tree, including uncommitted and
// untracked files, as hidden commits under refs/comi/<branch>.
//
// A snapshot never touches HEAD, the current branch, the index or the working
// tree, and is safe to take in any state: detached HEAD, a conflicted merge or
// the middle of a rebase. Each snapshot's first parent is the previous
// snapshot and its second parent is HEAD at the time, so the snapshots of a
// branch form a chain that `git log --first-parent refs/comi/<branch>` walks.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// Take saves a snapshot of repo if anything changed since the last one,
// removes old snapshots that opts.Keep no longer keeps (at most about once a
// day), and pushes snapshots that haven't reached origin yet when opts.Push
// is set.
func Take(ctx context.Context, repo string, opts Options, now time.Time) (string, error) {
	branch := CurrentBranch(ctx, repo)
	if opts.Branch != "" && branch != opts.Branch {
		return fmt.Sprintf("skipped: on branch %q, only snapshots %q", branch, opts.Branch), nil
	}

	snap, err := save(ctx, repo, branch, now)
	if err != nil {
		return "", err
	}
	removed, err := prune(ctx, repo, branch, opts.Keep, now, true, false)
	if err != nil {
		return "", fmt.Errorf("removing old snapshots: %w", err)
	}

	msg := "no changes"
	if snap != nil {
		// Pruning rewrites the chain, so the new snapshot may have a new id.
		snap.ID = git.Resolve(ctx, repo, Ref(branch))
		msg = fmt.Sprintf("snapshot %s (%d %s changed)", snap.Short(), snap.Files, plural(snap.Files, "file", "files"))
	}
	if len(removed) > 0 {
		msg += fmt.Sprintf(", removed %d old %s", len(removed), plural(len(removed), "snapshot", "snapshots"))
	}
	if !opts.Push {
		return msg, nil
	}

	pushed, err := Push(ctx, repo, branch)
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
// It returns nil when the working tree matches HEAD or the last snapshot.
func save(ctx context.Context, repo, branch string, now time.Time) (*Snapshot, error) {
	tree, err := writeTree(ctx, repo)
	if err != nil {
		return nil, err
	}

	ref := Ref(branch)
	head := git.Resolve(ctx, repo, "HEAD")
	prev := git.Resolve(ctx, repo, ref)
	if head != "" && git.Resolve(ctx, repo, head+"^{tree}") == tree {
		return nil, nil // nothing uncommitted
	}
	if prev != "" && git.Resolve(ctx, repo, prev+"^{tree}") == tree {
		return nil, nil // same as the last snapshot
	}

	var changed string
	if head != "" {
		changed, err = git.Run(ctx, repo, "diff-tree", "-r", "--name-only", head, tree)
	} else {
		changed, err = git.Run(ctx, repo, "ls-tree", "-r", "--name-only", tree)
	}
	if err != nil {
		return nil, err
	}
	files := 0
	if changed != "" {
		files = len(strings.Split(changed, "\n"))
	}

	id, err := commitTree(ctx, repo, tree, []string{prev, head}, branch, files, now)
	if err != nil {
		return nil, err
	}
	// Passing the old value makes the update fail instead of losing a
	// snapshot written concurrently.
	if _, err := git.Run(ctx, repo, "update-ref", "-m", subjectPrefix, ref, id, prev); err != nil {
		return nil, fmt.Errorf("update-ref: %w", err)
	}
	return &Snapshot{ID: id, Time: now, Branch: branch, Files: files}, nil
}

// commitTree writes a snapshot commit. Empty parents are skipped. The same
// inputs always produce the same commit id.
func commitTree(ctx context.Context, repo, tree string, parents []string, branch string, files int, at time.Time) (string, error) {
	args := []string{"commit-tree", tree, "-m", fmt.Sprintf("%s: %d files changed\n\nbranch: %s", subjectPrefix, files, branch)}
	for _, p := range parents {
		if p != "" {
			args = append(args, "-p", p)
		}
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

// writeTree stores the working tree, as `git add -A` would see it, and
// returns its tree id. It works on a copy of the index, so what the user
// has staged is left alone.
func writeTree(ctx context.Context, repo string) (string, error) {
	gitDir, err := git.Dir(ctx, repo)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(gitDir, "comi-index-")
	if err != nil {
		return "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	// Starting from the real index lets git skip re-hashing unchanged files.
	// Without one (a new repository), git needs the file to be absent.
	if data, err := os.ReadFile(filepath.Join(gitDir, "index")); err == nil {
		if err := os.WriteFile(tmp.Name(), data, 0o644); err != nil {
			return "", err
		}
	} else if err := os.Remove(tmp.Name()); err != nil {
		return "", err
	}

	env := []string{"GIT_INDEX_FILE=" + tmp.Name()}
	if _, err := git.RunEnv(ctx, repo, env, "add", "-A"); err != nil {
		return "", fmt.Errorf("add: %w", err)
	}
	return git.RunEnv(ctx, repo, env, "write-tree")
}

// Push sends the branch's snapshots to origin unless they are already there.
// It reports whether anything was pushed; without an origin remote it does
// nothing.
func Push(ctx context.Context, repo, branch string) (bool, error) {
	ref, mark := Ref(branch), pushedPrefix+branch
	cur := git.Resolve(ctx, repo, ref)
	if cur == "" || cur == git.Resolve(ctx, repo, mark) {
		return false, nil
	}
	if !git.HasRemote(ctx, repo, "origin") {
		return false, nil
	}
	if _, err := git.Run(ctx, repo, "push", "-q", "origin", "+"+ref+":"+ref); err != nil {
		return false, err
	}
	if _, err := git.Run(ctx, repo, "update-ref", mark, cur); err != nil {
		return false, err
	}
	return true, nil
}

// List returns the snapshots of a branch, newest first, at most limit.
func List(ctx context.Context, repo, branch string, limit int) ([]Snapshot, error) {
	ref := Ref(branch)
	if git.Resolve(ctx, repo, ref) == "" {
		return nil, nil
	}
	out, err := git.Run(ctx, repo, "log", "--first-parent", fmt.Sprintf("-n%d", limit),
		"--format=%H%x09%T%x09%P%x09%ct%x09%s", ref)
	if err != nil {
		return nil, err
	}

	var snaps []Snapshot
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) != 5 || !strings.HasPrefix(parts[4], subjectPrefix) {
			break // reached the commit the first snapshot was taken on
		}
		var unix int64
		fmt.Sscan(parts[3], &unix)
		s := Snapshot{ID: parts[0], Time: time.Unix(unix, 0), Branch: branch, tree: parts[1], parents: strings.Fields(parts[2])}
		fmt.Sscanf(parts[4], subjectPrefix+": %d", &s.Files)
		snaps = append(snaps, s)
	}
	return snaps, nil
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
	backup, err := save(ctx, repo, CurrentBranch(ctx, repo), now)
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
