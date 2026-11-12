package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/YunusEmreAlps/comi/internal/config"
	"github.com/YunusEmreAlps/comi/internal/git"
	"github.com/YunusEmreAlps/comi/internal/lock"
	"github.com/YunusEmreAlps/comi/internal/schedule"
	"github.com/YunusEmreAlps/comi/internal/snapshot"
)

func cmdStatus(args []string) error {
	return showStatus(context.Background(), os.Stdout, args)
}

func showStatus(ctx context.Context, out io.Writer, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: comi status [name|path]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	projects := cfg.Projects
	if len(args) == 1 {
		i, err := registeredProject(cfg, args[0])
		if err != nil {
			return err
		}
		projects = projects[i : i+1]
	}
	state, err := config.LoadState()
	if err != nil {
		return err
	}
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	running, err := lock.Running(dir)
	if err != nil {
		return err
	}
	if running {
		fmt.Fprintln(out, "Scheduler: running")
	} else {
		fmt.Fprintln(out, "Scheduler: stopped (start with comi run)")
	}
	if len(projects) == 0 {
		fmt.Fprintln(out, "No projects yet. Add one with comi add.")
	}
	for _, p := range projects {
		fmt.Fprintf(out, "\n%s (%s)\n", p.Name, modeOrDefault(p.Mode))
		next := "invalid schedule"
		if sc, err := schedule.Parse(p.Schedule); err == nil {
			next = formatTime(sc.Next(state.LastRun(p)))
		}
		r := state[p.Path]
		fmt.Fprintf(out, "  Next scheduled run: %s\n  Last attempt: %s\n  Last successful run: %s\n", next, statusTime(r.LastAttempt), statusTime(r.LastSuccess))
		if r.Result != "" {
			fmt.Fprintln(out, "  Last result:", r.Result)
		}
		if r.Error != "" {
			fmt.Fprintf(out, "  Last error (%s): %s\n", statusTime(r.LastFailure), r.Error)
		}
		if err := showBackupStatus(ctx, out, p); err != nil {
			fmt.Fprintln(out, "  Repository status unavailable:", err)
		}
	}
	return nil
}

func statusTime(t time.Time) string {
	if t.IsZero() {
		return "none recorded"
	}
	return formatTime(t)
}

func showBackupStatus(ctx context.Context, out io.Writer, p config.Project) error {
	if _, err := git.Dir(ctx, p.Path); err != nil {
		return err
	}
	branch := p.Branch
	if branch == "" {
		if p.IsSnapshot() {
			branch = snapshot.CurrentBranch(ctx, p.Path)
		} else {
			branch, _ = git.CurrentBranch(ctx, p.Path)
		}
	}
	if p.IsSnapshot() {
		latest, pending, err := snapshot.BackupStatus(ctx, p.Path, branch)
		if err != nil {
			return err
		}
		backup := "none"
		if latest != nil {
			backup = fmt.Sprintf("%s (%s)", latest.Short(), formatTime(latest.Time))
		}
		fmt.Fprintf(out, "  Latest local snapshot (%s): %s\n", branch, backup)
		push := "up to date"
		switch {
		case p.NoPush:
			push = "disabled"
		case pending && !git.HasRemote(ctx, p.Path, "origin"):
			push = "pending (origin is missing)"
		case pending:
			push = "pending"
		case latest == nil:
			push = "no snapshots"
		}
		fmt.Fprintln(out, "  Push:", push)
		return nil
	}
	push, err := commitPushStatus(ctx, p, branch)
	if err != nil {
		return err
	}
	if branch == "" {
		branch = "detached HEAD"
	}
	fmt.Fprintf(out, "  Push (%s): %s\n", branch, push)
	return nil
}

func commitPushStatus(ctx context.Context, p config.Project, branch string) (string, error) {
	if p.NoPush {
		return "disabled", nil
	}
	if branch == "" {
		return "unavailable in detached HEAD", nil
	}
	ref := "refs/heads/" + branch
	refs, err := git.Run(ctx, p.Path, "for-each-ref", "--format=%(refname) %(objectname) %(upstream)", ref)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(refs, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != ref {
			continue
		}
		if len(fields) == 2 {
			return "no upstream", nil
		}
		upstream := git.Resolve(ctx, p.Path, fields[2])
		if upstream == "" {
			return "upstream not available locally", nil
		}
		ahead, err := git.Run(ctx, p.Path, "rev-list", "--count", upstream+".."+fields[1])
		if err != nil {
			return "", err
		}
		switch ahead {
		case "0":
			return "no outgoing commits", nil
		case "1":
			return "pending (1 outgoing commit)", nil
		default:
			return fmt.Sprintf("pending (%s outgoing commits)", ahead), nil
		}
	}
	return "no commits on this branch", nil
}
