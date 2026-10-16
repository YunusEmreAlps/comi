// Command comi commits or snapshots git repositories on a schedule.
package main

import (
	"fmt"
	"os"

	"comi/internal/autocommit"
	"comi/internal/config"
)

const usage = `comi - scheduled git commits and snapshots

Projects:
  comi add [path] [flags]        add a project, or update one
      -mode commit|snapshot      commit: real commits on your branch (default)
                                 snapshot: hidden backups, your branch is never touched
      -s <schedule>              when to run (default: 1h, or 5m for snapshots)
      -m <message>               commit message template (commit mode)
      -b <branch>                only run while this branch is checked out; -b "" clears
      -keep <policy>             snapshot retention (default: 24h,7d,30d, see below)
      -n <name>                  project name (default: folder name)
      -no-push                   keep everything local
  comi list                      projects and their next run
  comi remove <name|path> ...
  comi now [name|path ...]       run right away (all projects if none given)
  comi run                       start the scheduler (keep it running)

Snapshots (work in any git repository; -p defaults to the current directory):
  comi snapshot [-p project]                    take a snapshot now
  comi history  [-p project] [-b branch] [-n N] list snapshots
  comi diff     <snapshot> [-p project] [-stat] compare a snapshot with your files
  comi restore  <snapshot> [-p project] [file ...]
                                                put files back from a snapshot;
                                                the current state is snapshotted first
  comi prune    [-p project] [-keep policy] [-n]
                                                remove old snapshots now (-n: dry run)
  comi hook                                     snapshot before an AI agent's shell
                                                command (a Claude Code PreToolUse hook)

  <snapshot> is "latest", an id prefix (3f2a91c0) or an age (30m, 2h, 3d: the
  newest snapshot at least that old).

  Retention: "24h,7d,30d" keeps every snapshot for 24h, the newest of each
  hour up to 7d and of each day up to 30d; older ones are removed. "forever"
  keeps all. Old snapshots are removed about once a day; this gives the kept
  ones new ids, so prefer ages and "latest" over ids for older snapshots.

Schedules: 30m, 2h, 1h30m (interval) or 20:00, 09:00,18:00 (daily times)
Message placeholders: {date} {time} {files}   default: "` + autocommit.DefaultMessage + `"

  comi version
Data directory: %s (override with COMI_HOME)
`

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	commands := map[string]func([]string) error{
		"add":      cmdAdd,
		"list":     cmdList,
		"ls":       cmdList,
		"remove":   cmdRemove,
		"rm":       cmdRemove,
		"now":      cmdNow,
		"run":      cmdRun,
		"snapshot": cmdSnapshot,
		"history":  cmdHistory,
		"diff":     cmdDiff,
		"restore":  cmdRestore,
		"prune":    cmdPrune,
		"hook":     cmdHook,
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "version", "-v", "--version":
		fmt.Println("comi", appVersion())
		return
	case "help", "-h", "--help":
		printUsage()
		return
	}

	run, ok := commands[cmd]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		printUsage()
		os.Exit(2)
	}
	if err := run(args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func printUsage() {
	dir, _ := config.Dir()
	fmt.Fprintf(os.Stderr, usage, dir)
}
