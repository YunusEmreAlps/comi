package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/tabwriter"
	"time"

	"comi/internal/config"
	"comi/internal/git"
	"comi/internal/snapshot"
)

func cmdSnapshot(args []string) error {
	fs := newFlagSet("snapshot")
	project := fs.String("p", ".", "project name or path")
	push := fs.Bool("push", false, "push to origin (default: as configured for the project)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return errors.New("usage: comi snapshot [-p project] [-push]")
	}

	repo, p, err := target(*project)
	if err != nil {
		return err
	}
	var opts snapshot.Options
	if p != nil && p.IsSnapshot() {
		opts.Push = !p.NoPush
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "push" {
			opts.Push = *push
		}
	})

	msg, err := snapshot.Take(context.Background(), repo, opts, time.Now())
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

func cmdHistory(args []string) error {
	fs := newFlagSet("history")
	project := fs.String("p", ".", "project name or path")
	branch := fs.String("b", "", "branch (default: the current branch)")
	limit := fs.Int("n", 20, "number of snapshots to show")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}

	ctx := context.Background()
	repo, _, err := target(*project)
	if err != nil {
		return err
	}
	if *branch == "" {
		*branch = snapshot.CurrentBranch(ctx, repo)
	}
	snaps, err := snapshot.List(ctx, repo, *branch, *limit)
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		fmt.Printf("no snapshots of branch %q yet, take one with \"comi snapshot\"\n", *branch)
		return nil
	}

	fmt.Printf("snapshots of branch %q in %s\n\n", *branch, repo)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SNAPSHOT\tTIME\tAGE\tFILES CHANGED")
	now := time.Now()
	for _, s := range snaps {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", s.Short(), formatTime(s.Time), age(now.Sub(s.Time)), s.Files)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Println("\ncompare: comi diff <snapshot>    restore: comi restore <snapshot> [file ...]")
	return nil
}

func cmdDiff(args []string) error {
	fs := newFlagSet("diff")
	project := fs.String("p", ".", "project name or path")
	branch := fs.String("b", "", "branch (default: the current branch)")
	stat := fs.Bool("stat", false, "only list changed files")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: comi diff <snapshot> [-p project] [-stat]")
	}

	repo, snap, err := findSnapshot(*project, *branch, pos[0])
	if err != nil {
		return err
	}
	gitArgs := []string{"diff"}
	if *stat {
		gitArgs = append(gitArgs, "--stat")
	}
	// Show what changed from the snapshot to the files on disk now.
	cmd := exec.Command("git", append(gitArgs, snap.ID)...)
	cmd.Dir = repo
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func cmdRestore(args []string) error {
	fs := newFlagSet("restore")
	project := fs.String("p", ".", "project name or path")
	branch := fs.String("b", "", "branch whose snapshots to use (default: the current branch)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return errors.New("usage: comi restore <snapshot> [-p project] [file ...]")
	}

	repo, snap, err := findSnapshot(*project, *branch, pos[0])
	if err != nil {
		return err
	}
	// Paths are relative to where the user is, not to the repository root.
	var paths []string
	for _, p := range pos[1:] {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		paths = append(paths, abs)
	}

	backup, err := snapshot.Restore(context.Background(), repo, snap, paths, time.Now())
	if backup != nil {
		fmt.Printf("saved your current files as snapshot %s (undo with: comi restore %s)\n", backup.Short(), backup.Short())
	}
	if err != nil {
		return err
	}
	what := "all files"
	if len(paths) == 1 {
		what = pos[1]
	} else if len(paths) > 1 {
		what = fmt.Sprintf("%d paths", len(paths))
	}
	fmt.Printf("restored %s from snapshot %s (%s)\n", what, snap.Short(), formatTime(snap.Time))
	return nil
}

// target resolves a project name or a path to a repository, and returns the
// project if the repository is one comi manages.
func target(key string) (string, *config.Project, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", nil, err
	}
	if i := cfg.Find(key); i >= 0 {
		return cfg.Projects[i].Path, &cfg.Projects[i], nil
	}
	abs, err := filepath.Abs(key)
	if err != nil {
		return "", nil, err
	}
	root, err := git.Root(abs)
	if err != nil {
		return "", nil, fmt.Errorf("%q is neither a project name nor inside a git repository", key)
	}
	if i := cfg.Find(root); i >= 0 {
		return root, &cfg.Projects[i], nil
	}
	return root, nil, nil
}

func findSnapshot(project, branch, query string) (string, snapshot.Snapshot, error) {
	ctx := context.Background()
	repo, _, err := target(project)
	if err != nil {
		return "", snapshot.Snapshot{}, err
	}
	if branch == "" {
		branch = snapshot.CurrentBranch(ctx, repo)
	}
	snap, err := snapshot.Find(ctx, repo, branch, query, time.Now())
	return repo, snap, err
}

func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
