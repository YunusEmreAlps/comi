package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"comi/internal/autocommit"
	"comi/internal/config"
	"comi/internal/git"
	"comi/internal/lock"
	"comi/internal/schedule"
	"comi/internal/snapshot"
)

const (
	tickInterval = 30 * time.Second
	// dueSlack absorbs timer jitter so a run due a few ms after a tick is not
	// pushed back a whole tick.
	dueSlack = time.Second
)

func cmdAdd(args []string) error {
	fs := newFlagSet("add")
	mode := fs.String("mode", "", "commit or snapshot")
	sched := fs.String("s", "", "schedule: interval (30m, 2h) or daily times (20:00 or 09:00,18:00)")
	message := fs.String("m", "", "commit message template")
	name := fs.String("n", "", "project name (default: folder name)")
	branch := fs.String("b", "", "only run while this branch is checked out")
	noPush := fs.Bool("no-push", false, "keep everything local")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return errors.New("add takes a single path")
	}

	in := bufio.NewReader(os.Stdin)
	interactive := len(pos) == 0
	var path string
	if interactive {
		path = prompt(in, "Project path", ".")
	} else {
		path = pos[0]
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	root, err := git.Root(abs)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Re-adding a known project updates it; settings not given are kept.
	existing := cfg.Find(root)
	p := config.Project{Name: filepath.Base(root), Mode: config.ModeCommit}
	if existing >= 0 {
		p = cfg.Projects[existing]
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if set["mode"] {
		p.Mode = *mode
	} else if interactive {
		p.Mode = prompt(in, "Mode: commit (real commits) or snapshot (hidden backups)", modeOrDefault(p.Mode))
	}
	p.Mode = strings.ToLower(strings.TrimSpace(modeOrDefault(p.Mode)))
	if p.Mode != config.ModeCommit && p.Mode != config.ModeSnapshot {
		return fmt.Errorf("unknown mode %q: use commit or snapshot", p.Mode)
	}

	if p.Schedule == "" {
		p.Schedule = "1h"
		if p.IsSnapshot() {
			p.Schedule = "5m"
		}
	}
	if set["s"] {
		p.Schedule = *sched
	} else if interactive || existing < 0 {
		p.Schedule = prompt(in, "Schedule (5m, 2h, 20:00, 09:00,18:00)", p.Schedule)
	}
	if set["m"] {
		p.Message = *message
	} else if interactive && !p.IsSnapshot() {
		p.Message = prompt(in, "Commit message", messageOrDefault(p.Message))
	}
	if set["b"] {
		p.Branch = *branch
	} else if interactive {
		p.Branch = prompt(in, `Only run on branch (blank = any branch, "-" = clear)`, p.Branch)
		if p.Branch == "-" {
			p.Branch = ""
		}
	}
	if set["no-push"] {
		p.NoPush = *noPush
	} else if interactive {
		def := "y"
		if p.NoPush {
			def = "n"
		}
		p.NoPush = strings.HasPrefix(strings.ToLower(prompt(in, "Push to origin? (y/n)", def)), "n")
	}

	p.Schedule = strings.TrimSpace(p.Schedule)
	sc, err := schedule.Parse(p.Schedule)
	if err != nil {
		return err
	}
	if p.Message == autocommit.DefaultMessage {
		p.Message = ""
	}
	if p.Branch = strings.TrimSpace(p.Branch); p.Branch != "" {
		if err := git.CheckBranchName(root, p.Branch); err != nil {
			return err
		}
	}

	if set["n"] {
		if i := cfg.Find(*name); i >= 0 && i != existing {
			return fmt.Errorf("name %q is already used by %s", *name, cfg.Projects[i].Path)
		}
		p.Name = *name
	} else {
		p.Name = cfg.UniqueName(p.Name, existing)
	}
	p.Path = root
	p.Added = time.Now()

	if existing >= 0 {
		cfg.Projects[existing] = p
	} else {
		cfg.Projects = append(cfg.Projects, p)
	}
	if err := cfg.Save(); err != nil {
		return err
	}

	verb := "added"
	if existing >= 0 {
		verb = "updated"
	}
	fmt.Printf("%s %s (%s, %s mode)\nnext run: %s\n", verb, p.Name, p.Path, p.Mode, formatTime(sc.Next(p.Added)))
	if p.Branch != "" {
		if cur, _ := git.CurrentBranch(context.Background(), root); cur != p.Branch {
			fmt.Printf("note: %q is not checked out right now, runs are skipped until it is\n", p.Branch)
		}
	}
	fmt.Println(`make sure "comi run" is running for scheduled runs`)
	return nil
}

func cmdList([]string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.Projects) == 0 {
		fmt.Println(`no projects yet, add one with "comi add"`)
		return nil
	}
	state, err := config.LoadState()
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tMODE\tSCHEDULE\tNEXT RUN\tBRANCH\tPUSH\tMESSAGE\tPATH")
	for _, p := range cfg.Projects {
		next := "invalid schedule"
		if sc, err := schedule.Parse(p.Schedule); err == nil {
			next = formatTime(sc.Next(state.LastRun(p)))
		}
		msg := messageOrDefault(p.Message)
		if p.IsSnapshot() {
			msg = "-"
		}
		branch := p.Branch
		if branch == "" {
			branch = "(any)"
		}
		push := "yes"
		if p.NoPush {
			push = "no"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			p.Name, modeOrDefault(p.Mode), p.Schedule, next, branch, push, msg, p.Path)
	}
	return w.Flush()
}

func cmdRemove(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: comi remove <name|path> ...")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, key := range args {
		i := cfg.Find(key)
		if i < 0 {
			return fmt.Errorf("project %q not found", key)
		}
		fmt.Println("removed", cfg.Projects[i].Name)
		cfg.Projects = append(cfg.Projects[:i], cfg.Projects[i+1:]...)
	}
	return cfg.Save()
}

func cmdNow(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	projects := cfg.Projects
	if len(args) > 0 {
		projects = nil
		for _, key := range args {
			i := cfg.Find(key)
			if i < 0 {
				return fmt.Errorf("project %q not found", key)
			}
			projects = append(projects, cfg.Projects[i])
		}
	}
	if len(projects) == 0 {
		fmt.Println(`no projects yet, add one with "comi add"`)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	failed := false
	for i, r := range runAll(ctx, projects, time.Now()) {
		if r.err != nil {
			failed = true
			fmt.Printf("%s: error: %v\n", projects[i].Name, r.err)
		} else {
			fmt.Printf("%s: %s\n", projects[i].Name, r.msg)
		}
	}
	if failed {
		return errors.New("some projects failed")
	}
	return nil
}

func cmdRun([]string) error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	release, err := lock.Acquire(dir)
	if err != nil {
		return err
	}
	defer release()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.SetFlags(log.Ldate | log.Ltime)
	log.Printf("comi %s started (data: %s), press Ctrl+C to stop", appVersion(), dir)

	tick := time.NewTicker(tickInterval)
	defer tick.Stop()
	for {
		// Config is re-read every tick so "comi add/remove" apply without a restart.
		if err := runDue(ctx); err != nil {
			log.Println("error:", err)
		}
		select {
		case <-ctx.Done():
			log.Println("stopped")
			return nil
		case <-tick.C:
		}
	}
}

func runDue(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	state, err := config.LoadState()
	if err != nil {
		return err
	}

	now := time.Now()
	var due []config.Project
	for _, p := range cfg.Projects {
		sc, err := schedule.Parse(p.Schedule)
		if err != nil {
			log.Printf("%s: %v", p.Name, err)
			continue
		}
		if !now.Add(dueSlack).Before(sc.Next(state.LastRun(p))) {
			due = append(due, p)
		}
	}
	if len(due) == 0 {
		return nil
	}

	for i, r := range runAll(ctx, due, now) {
		if ctx.Err() != nil {
			return nil // interrupted: leave state so these run again next start
		}
		if r.err != nil {
			log.Printf("%s: error: %v", due[i].Name, r.err)
		} else {
			log.Printf("%s: %s", due[i].Name, r.msg)
		}
		state[due[i].Path] = now
	}
	return state.Save()
}

type result struct {
	msg string
	err error
}

// runAll runs the projects in parallel.
func runAll(ctx context.Context, projects []config.Project, now time.Time) []result {
	res := make([]result, len(projects))
	var wg sync.WaitGroup
	for i, p := range projects {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i].msg, res[i].err = runProject(ctx, p, now)
		}()
	}
	wg.Wait()
	return res
}

func runProject(ctx context.Context, p config.Project, now time.Time) (string, error) {
	if p.IsSnapshot() {
		return snapshot.Take(ctx, p.Path, snapshot.Options{Branch: p.Branch, Push: !p.NoPush}, now)
	}
	return autocommit.Sync(ctx, p.Path, autocommit.Options{Message: p.Message, Branch: p.Branch, NoPush: p.NoPush}, now)
}

func modeOrDefault(mode string) string {
	if mode == "" {
		return config.ModeCommit
	}
	return mode
}

func messageOrDefault(msg string) string {
	if msg == "" {
		return autocommit.DefaultMessage
	}
	return msg
}
