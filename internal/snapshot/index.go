package snapshot

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/YunusEmreAlps/comi/internal/git"
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
	objectEnv    []string
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

func (idx *index) env() []string {
	return append([]string{"GIT_INDEX_FILE=" + idx.path}, idx.objectEnv...)
}

func captureTree(ctx context.Context, root, gitDir string) (*index, string, []string, error) {
	idx, err := openIndex(gitDir)
	if err != nil {
		return nil, "", nil, err
	}
	tree, leftOut, err := idx.writeTree(ctx, root)
	if err != nil {
		idx.close()
		return nil, "", nil, err
	}
	return idx, tree, leftOut, nil
}

// WorkingTree writes a tree using the same file selection as a snapshot.
func WorkingTree(ctx context.Context, root string) (string, error) {
	gitDir, err := git.Dir(ctx, root)
	if err != nil {
		return "", err
	}
	idx, tree, _, err := captureTree(ctx, root, gitDir)
	if err == nil {
		idx.close()
	}
	return tree, err
}

// checkRestore rejects overwrites of files that the backup does not contain.
func (idx *index) checkRestore(ctx context.Context, root, backupTree, source string, paths []string) error {
	args := append([]string{"diff", "--no-renames", "--ignore-submodules=none", "--diff-filter=A", "--name-only", "-z", backupTree, source, "--"}, paths...)
	added, err := git.RunZ(ctx, root, idx.env(), args...)
	if err != nil || len(added) == 0 {
		return err
	}
	outside, err := git.RunZ(ctx, root, idx.env(), "ls-files", "--others", "-z")
	if err != nil || len(outside) == 0 {
		return err
	}
	ignoreCase, err := ignoreCasePaths(ctx, root)
	if err != nil {
		return err
	}
	if conflicts := restoreCollisions(added, outside, ignoreCase); len(conflicts) > 0 {
		return fmt.Errorf("restore would overwrite %q, which is missing from the backup. Move it before restoring", conflicts[0])
	}
	return nil
}

func ignoreCasePaths(ctx context.Context, root string) (bool, error) {
	value, err := git.Run(ctx, root, "config", "--type=bool", "--default=false", "--get", "core.ignoreCase")
	return value == "true", err
}

func restoreCollisions(added, existing []string, ignoreCase bool) []string {
	key := func(p string) string { return p }
	if ignoreCase {
		key = strings.ToLower
	}
	targets, parents := map[string]bool{}, map[string]bool{}
	for _, p := range added {
		targets[key(p)] = true
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			parents[key(dir)] = true
		}
	}
	var collisions []string
	for _, p := range existing {
		p = strings.TrimSuffix(p, "/") // an embedded repository may end in a slash
		if parents[key(p)] {
			collisions = append(collisions, p)
			continue
		}
		for parent := p; parent != "."; parent = path.Dir(parent) {
			if targets[key(parent)] {
				collisions = append(collisions, p)
				break
			}
		}
	}
	return collisions
}

// writeTree stores the working tree, as `git add -A` would see it, and
// returns its tree id, along with the files left out for being too large
// (see maxFileSize). git status finds the files that changed since comi's
// index last saw them, and only those are added: on Windows, status looks
// through a large tree about three times faster than git add does.
func (idx *index) writeTree(ctx context.Context, root string) (string, []string, error) {
	if err := idx.syncIgnored(ctx, root); err != nil {
		return "", nil, err
	}
	entries, err := git.RunZ(ctx, root, idx.env(), "status", "--porcelain=v2", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return "", nil, fmt.Errorf("status: %w", err)
	}
	var changed []string
	inIndex := map[string]bool{} // in comi's index, as opposed to untracked
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if e == "" {
			continue
		}
		switch e[0] {
		case '1': // 1 XY sub mH mI mW hH hI path; Y is the working tree
			if f := strings.SplitN(e, " ", 9); len(f) == 9 && f[1][1] != '.' {
				changed = append(changed, f[8])
				inIndex[f[8]] = true
			}
		case '2': // a rename, followed by the original path
			if f := strings.SplitN(e, " ", 10); len(f) == 10 && i+1 < len(entries) {
				changed = append(changed, f[9], entries[i+1])
				inIndex[f[9]], inIndex[entries[i+1]] = true, true
			}
			i++
		case 'u': // unmerged: u XY sub m1 m2 m3 mW h1 h2 h3 path
			if f := strings.SplitN(e, " ", 11); len(f) == 11 {
				changed = append(changed, f[10])
				inIndex[f[10]] = true
			}
		case '?': // untracked; a nested repository ends in a slash
			changed = append(changed, strings.TrimSuffix(e[2:], "/"))
		}
	}

	leftOut, err := tooLarge(ctx, root, changed)
	if err != nil {
		return "", nil, err
	}
	var add, drop []string
	skip := map[string]bool{}
	for _, p := range leftOut {
		skip[p] = true
		if inIndex[p] {
			drop = append(drop, p) // saved before it grew too large
		}
	}
	for _, p := range changed {
		if !skip[p] {
			add = append(add, p)
		}
	}
	for _, op := range []struct {
		paths []string
		flags []string
	}{{add, []string{"--add", "--remove"}}, {drop, []string{"--force-remove"}}} {
		if len(op.paths) == 0 {
			continue
		}
		input := strings.Join(op.paths, "\x00") + "\x00"
		args := append(append([]string{"update-index", "-z"}, op.flags...), "--stdin")
		if _, err := git.RunInput(ctx, root, idx.env(), input, args...); err != nil {
			return "", nil, fmt.Errorf("update-index: %w", err)
		}
	}

	tree, err := git.RunEnv(ctx, root, idx.env(), "write-tree")
	return tree, leftOut, err
}

// maxFileSize is the largest file a snapshot saves that the user's index
// doesn't track. Larger ones are left out: reading them on every change is
// slow, and GitHub refuses files over 100 MB, which would make every later
// push of the snapshots fail. A variable so tests can lower it.
var maxFileSize int64 = 50 << 20

// tooLarge returns the paths over maxFileSize that the user's index doesn't
// track. Tracked files are always saved: restoring a snapshot that lacks a
// tracked file deletes it.
func tooLarge(ctx context.Context, root string, paths []string) ([]string, error) {
	var large []string
	for _, p := range paths {
		if fi, err := os.Lstat(filepath.Join(root, p)); err == nil && fi.Mode().IsRegular() && fi.Size() > maxFileSize {
			large = append(large, p)
		}
	}
	if len(large) == 0 {
		return nil, nil
	}
	tracked, err := git.RunZ(ctx, root, []string{"GIT_LITERAL_PATHSPECS=1"}, append([]string{"ls-files", "-z", "--cached", "--"}, large...)...)
	if err != nil {
		return nil, err
	}
	isTracked := map[string]bool{}
	for _, p := range tracked {
		isTracked[p] = true
	}
	var out []string
	for _, p := range large {
		if !isTracked[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

// syncIgnored makes comi's index hold the same ignored files as the user's.
// `git add -A` never adds an ignored file and never drops one that is
// already in the index, so without this a file ignored after it was first
// saved would stay in every snapshot, and an ignored file the user tracks
// anyway (git add -f) would be left out.
func (idx *index) syncIgnored(ctx context.Context, root string) error {
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
		return ours.err
	}
	if theirs.err != nil {
		return theirs.err
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
			return fmt.Errorf("update-index: %w", err)
		}
	}
	return nil
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
