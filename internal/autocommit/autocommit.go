// Package autocommit commits a repository's changes to its current branch and
// pushes them.
package autocommit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"comi/internal/git"
)

const DefaultMessage = "auto: {date} {time}"

type Options struct {
	Message string // template, see RenderMessage; "" means DefaultMessage
	Branch  string // only commit while this branch is checked out; "" means any
	NoPush  bool
}

// RenderMessage fills {date}, {time} and {files} in a message template.
func RenderMessage(tmpl string, now time.Time, files int) string {
	if tmpl == "" {
		tmpl = DefaultMessage
	}
	return strings.NewReplacer(
		"{date}", now.Format("2006-01-02"),
		"{time}", now.Format("15:04"),
		"{files}", strconv.Itoa(files),
	).Replace(tmpl)
}

// SkipReason explains why committing now would be unsafe, or returns "".
func SkipReason(ctx context.Context, repo, branchLock string) (string, error) {
	gitDir, err := git.Dir(ctx, repo)
	if err != nil {
		return "", err
	}
	for _, op := range []struct{ file, name string }{
		{"rebase-merge", "rebase"},
		{"rebase-apply", "rebase"},
		{"MERGE_HEAD", "merge"},
		{"CHERRY_PICK_HEAD", "cherry-pick"},
		{"REVERT_HEAD", "revert"},
	} {
		if _, err := os.Stat(filepath.Join(gitDir, op.file)); err == nil {
			return op.name + " in progress", nil
		}
	}

	branch, err := git.CurrentBranch(ctx, repo)
	if err != nil {
		return "detached HEAD", nil
	}
	if branchLock != "" && branch != branchLock {
		return fmt.Sprintf("on branch %q, only commits on %q", branch, branchLock), nil
	}
	return "", nil
}

// Sync commits all changes and pushes. Commits left unpushed by an earlier
// failure (e.g. no network) are pushed on the next run.
func Sync(ctx context.Context, repo string, opts Options, now time.Time) (string, error) {
	if reason, err := SkipReason(ctx, repo, opts.Branch); err != nil || reason != "" {
		return "skipped: " + reason, err
	}

	status, err := git.Run(ctx, repo, "status", "--porcelain")
	if err != nil {
		return "", err
	}

	committed := false
	if status != "" {
		files := len(strings.Split(status, "\n"))
		if _, err := git.Run(ctx, repo, "add", "-A"); err != nil {
			return "", fmt.Errorf("add: %w", err)
		}
		if _, err := git.Run(ctx, repo, "commit", "-q", "-m", RenderMessage(opts.Message, now, files)); err != nil {
			return "", fmt.Errorf("commit: %w", err)
		}
		committed = true
	}

	if opts.NoPush {
		if committed {
			return "committed", nil
		}
		return "no changes", nil
	}

	hasUpstream := git.Resolve(ctx, repo, "@{u}") != ""
	if !committed {
		if !hasUpstream {
			return "no changes", nil
		}
		ahead, err := git.Run(ctx, repo, "rev-list", "--count", "@{u}..HEAD")
		if err != nil || ahead == "0" {
			return "no changes", nil
		}
	}

	args := []string{"push", "-q"}
	if !hasUpstream {
		args = append(args, "-u", "origin", "HEAD")
	}
	if _, err := git.Run(ctx, repo, args...); err != nil {
		if committed {
			return "", fmt.Errorf("committed, but push failed (will retry next run): %w", err)
		}
		return "", fmt.Errorf("push: %w", err)
	}
	if committed {
		return "committed and pushed", nil
	}
	return "pushed pending commits", nil
}
