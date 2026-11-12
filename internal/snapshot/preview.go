package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/YunusEmreAlps/comi/internal/git"
)

type RestorePlan struct {
	Write  []string
	Delete []string
}

// PreviewRestore reports changes to working files without creating a backup.
// Its index and newly created Git objects are temporary.
func PreviewRestore(ctx context.Context, repo string, snap Snapshot, paths []string) (RestorePlan, error) {
	var plan RestorePlan
	st, err := readState(ctx, repo)
	if err != nil {
		return plan, err
	}
	if len(paths) > 0 {
		args := append([]string{"ls-files", "-z", "--with-tree=" + snap.ID, "--error-unmatch", "--"}, paths...)
		if _, err := git.RunZ(ctx, st.root, nil, args...); err != nil {
			return plan, err
		}
	}
	objects, err := git.Run(ctx, st.root, "rev-parse", "--git-path", "objects")
	if err != nil {
		return plan, err
	}
	if !filepath.IsAbs(objects) {
		objects = filepath.Join(st.root, objects)
	}
	tmp, err := os.MkdirTemp("", "comi-preview-")
	if err != nil {
		return plan, err
	}
	defer os.RemoveAll(tmp)
	idx, err := openIndex(st.gitDir)
	if err != nil {
		return plan, err
	}
	defer idx.close()
	alternates := strconv.Quote(objects)
	if existing := os.Getenv("GIT_ALTERNATE_OBJECT_DIRECTORIES"); existing != "" {
		alternates += string(os.PathListSeparator) + existing
	}
	idx.objectEnv = []string{"GIT_OBJECT_DIRECTORY=" + tmp, "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + alternates}
	tree, _, err := idx.writeTree(ctx, st.root)
	if err != nil {
		return plan, err
	}
	if err := idx.checkRestore(ctx, st.root, tree, snap.ID, paths); err != nil {
		return plan, err
	}

	args := append([]string{"diff", "--no-renames", "--ignore-submodules=none", "--name-status", "-z", tree, snap.ID, "--"}, paths...)
	fields, err := git.RunZ(ctx, st.root, idx.env(), args...)
	if err != nil {
		return plan, err
	}
	if len(fields)%2 != 0 {
		return plan, fmt.Errorf("unexpected output while comparing the restore target")
	}
	var added, removed []string
	for i := 0; i < len(fields); i += 2 {
		p := fields[i+1]
		if fields[i] == "D" {
			removed = append(removed, p)
			continue
		}
		plan.Write = append(plan.Write, p)
		if fields[i] == "A" {
			added = append(added, p)
		}
	}
	deletes := map[string]bool{}
	if len(removed) > 0 {
		tracked, err := git.RunZ(ctx, st.root, nil, "ls-files", "--cached", "-z")
		if err != nil {
			return RestorePlan{}, err
		}
		inIndex := map[string]bool{}
		for _, p := range tracked {
			inIndex[p] = true
		}
		for _, p := range removed {
			if inIndex[p] {
				deletes[p] = true
			}
		}
	}
	if len(added) > 0 {
		current, err := git.RunZ(ctx, st.root, idx.env(), "ls-tree", "-r", "--name-only", "-z", tree)
		if err != nil {
			return RestorePlan{}, err
		}
		ignoreCase, err := ignoreCasePaths(ctx, st.root)
		if err != nil {
			return RestorePlan{}, err
		}
		for _, p := range restoreCollisions(added, current, ignoreCase) {
			deletes[p] = true
		}
	}
	for p := range deletes {
		plan.Delete = append(plan.Delete, p)
	}
	sort.Strings(plan.Delete)
	return plan, nil
}
