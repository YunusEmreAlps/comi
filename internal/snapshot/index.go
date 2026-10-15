package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"comi/internal/git"
)

// indexName is comi's own index, kept in the git directory between
// snapshots. Like any git index it records each file's size and modification
// time, so `git add -A` only reads and hashes the files that changed since
// the last snapshot. Starting from the user's index every time would hash
// every untracked file on every run.
const indexName = "comi-index"

// index is one run's private copy of comi's index, so runs that overlap
// (the scheduler and a manual snapshot) never share one.
type index struct {
	gitDir, path string
	kept         bool
}

// openIndex copies comi's index, or the user's the first time.
func openIndex(gitDir string) (*index, error) {
	f, err := os.CreateTemp(gitDir, indexName+"-")
	if err != nil {
		return nil, err
	}
	f.Close()
	idx := &index{gitDir: gitDir, path: f.Name()}

	for _, name := range []string{indexName, "index"} {
		src := filepath.Join(gitDir, name)
		fi, err := os.Stat(src)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(src)
		if err == nil {
			err = os.WriteFile(idx.path, data, 0o644)
		}
		// git rechecks files changed in the same second the index was
		// written, so the copy keeps the original's time: a newer one would
		// hide such changes.
		if err == nil {
			err = os.Chtimes(idx.path, fi.ModTime(), fi.ModTime())
		}
		if err != nil {
			idx.close()
			return nil, err
		}
		return idx, nil
	}
	// A new repository has no index yet, and git needs the file to be absent.
	if err := os.Remove(idx.path); err != nil {
		return nil, err
	}
	return idx, nil
}

func (idx *index) env() []string { return []string{"GIT_INDEX_FILE=" + idx.path} }

// writeTree stores the working tree, as `git add -A` would see it, and
// returns its tree id. git status finds the files that changed since comi's
// index last saw them, and only those are added: on Windows, status looks
// through a large tree about three times faster than git add does.
func (idx *index) writeTree(ctx context.Context, root string) (string, error) {
	entries, err := git.RunZ(ctx, root, idx.env(), "status", "--porcelain=v2", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return "", fmt.Errorf("status: %w", err)
	}
	var changed []string
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if e == "" {
			continue
		}
		switch e[0] {
		case '1': // 1 XY sub mH mI mW hH hI path; Y is the working tree
			if f := strings.SplitN(e, " ", 9); len(f) == 9 && f[1][1] != '.' {
				changed = append(changed, f[8])
			}
		case '2': // a rename, followed by the original path
			if f := strings.SplitN(e, " ", 10); len(f) == 10 && i+1 < len(entries) {
				changed = append(changed, f[9], entries[i+1])
			}
			i++
		case 'u': // unmerged: u XY sub m1 m2 m3 mW h1 h2 h3 path
			if f := strings.SplitN(e, " ", 11); len(f) == 11 {
				changed = append(changed, f[10])
			}
		case '?': // untracked; a nested repository ends in a slash
			changed = append(changed, strings.TrimSuffix(e[2:], "/"))
		}
	}
	if len(changed) > 0 {
		input := strings.Join(changed, "\x00") + "\x00"
		if _, err := git.RunInput(ctx, root, idx.env(), input, "update-index", "-z", "--add", "--remove", "--stdin"); err != nil {
			return "", fmt.Errorf("update-index: %w", err)
		}
	}
	return git.RunEnv(ctx, root, idx.env(), "write-tree")
}

// syncIgnored makes comi's index hold the same ignored files as the user's.
// `git add -A` never adds an ignored file and never drops one that is
// already in the index, so without this a file ignored after it was first
// saved would stay in every snapshot, and an ignored file the user tracks
// anyway (git add -f) would be left out. It reports whether it changed
// anything.
func (idx *index) syncIgnored(ctx context.Context, root string) (bool, error) {
	type result struct {
		paths map[string]bool
		err   error
	}
	ignored := func(env []string) chan result {
		ch := make(chan result, 1)
		go func() {
			paths, err := git.RunZ(ctx, root, env, "ls-files", "-z", "--cached", "--ignored", "--exclude-standard")
			set := map[string]bool{}
			for _, p := range paths {
				set[p] = true
			}
			ch <- result{set, err}
		}()
		return ch
	}
	oursCh, theirsCh := ignored(idx.env()), ignored(nil)
	ours, theirs := <-oursCh, <-theirsCh
	if ours.err != nil {
		return false, ours.err
	}
	if theirs.err != nil {
		return false, theirs.err
	}

	var drop, add []string
	for p := range ours.paths {
		if !theirs.paths[p] {
			drop = append(drop, p)
		}
	}
	for p := range theirs.paths {
		if ours.paths[p] {
			continue
		}
		// A file deleted since is already gone from comi's index.
		if fi, err := os.Lstat(filepath.Join(root, p)); err == nil && !fi.IsDir() {
			add = append(add, p)
		}
	}
	for _, op := range []struct {
		flag  string
		paths []string
	}{{"--force-remove", drop}, {"--add", add}} {
		if len(op.paths) == 0 {
			continue
		}
		input := strings.Join(op.paths, "\x00") + "\x00"
		if _, err := git.RunInput(ctx, root, idx.env(), input, "update-index", "-z", op.flag, "--stdin"); err != nil {
			return false, fmt.Errorf("update-index: %w", err)
		}
	}
	return len(drop)+len(add) > 0, nil
}

// keep makes this copy comi's index for the next run. If another run has
// the current one open, keeping fails and the next run starts from that.
func (idx *index) keep() {
	if os.Rename(idx.path, filepath.Join(idx.gitDir, indexName)) == nil {
		idx.kept = true
	}
}

func (idx *index) close() {
	if !idx.kept {
		os.Remove(idx.path)
	}
}
