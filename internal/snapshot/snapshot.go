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
}

type Snapshot struct {
	ID     string
	Time   time.Time
	Branch string
	Files  int // files that differ from the commit checked out at the time
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

// Take saves a snapshot of repo if anything changed since the last one, and
// pushes snapshots that haven't reached origin yet when opts.Push is set.
func Take(ctx context.Context, repo string, opts Options, now time.Time) (string, error) {
	branch := CurrentBranch(ctx, repo)
	if opts.Branch != "" && branch != opts.Branch {
		return fmt.Sprintf("skipped: on branch %q, only snapshots %q", branch, opts.Branch), nil
	}

	snap, err := save(ctx, repo, branch, now)
	if err != nil {
		return "", err
	}
	msg := "no changes"
	if snap != nil {
		msg = fmt.Sprintf("snapshot %s (%d %s changed)", snap.Short(), snap.Files, plural(snap.Files, "file", "files"))
	}
	if !opts.Push {
		return msg, nil
	}

	pushed, err := push(ctx, repo, branch)
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

	args := []string{"commit-tree", tree, "-m", fmt.Sprintf("%s: %d files changed\n\nbranch: %s", subjectPrefix, files, branch)}
	for _, parent := range []string{prev, head} {
		if parent != "" {
			args = append(args, "-p", parent)
		}
	}
	date := now.Format(time.RFC3339)
	id, err := git.RunEnv(ctx, repo, []string{
		"GIT_AUTHOR_NAME=comi", "GIT_AUTHOR_EMAIL=comi@localhost", "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=comi", "GIT_COMMITTER_EMAIL=comi@localhost", "GIT_COMMITTER_DATE=" + date,
	}, args...)
	if err != nil {
		return nil, fmt.Errorf("commit-tree: %w", err)
	}
	// Passing the old value makes the update fail instead of losing a
	// snapshot written concurrently.
	if _, err := git.Run(ctx, repo, "update-ref", "-m", subjectPrefix, ref, id, prev); err != nil {
		return nil, fmt.Errorf("update-ref: %w", err)
	}
	return &Snapshot{ID: id, Time: now, Branch: branch, Files: files}, nil
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

// push sends the branch's snapshots to origin unless they are already there.
func push(ctx context.Context, repo, branch string) (bool, error) {
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
		"--format=%H%x09%ct%x09%s", ref)
	if err != nil {
		return nil, err
	}

	var snaps []Snapshot
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || !strings.HasPrefix(parts[2], subjectPrefix) {
			break // reached the commit the first snapshot was taken on
		}
		var unix int64
		fmt.Sscan(parts[1], &unix)
		s := Snapshot{ID: parts[0], Time: time.Unix(unix, 0), Branch: branch}
		fmt.Sscanf(parts[2], subjectPrefix+": %d", &s.Files)
		snaps = append(snaps, s)
	}
	return snaps, nil
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

	if age, err := time.ParseDuration(query); err == nil {
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
