<p align="center">
  <img src="assets/comi.svg" alt="comi, a Go gopher dressed as a komi, serving commits on a tray" width="260">
</p>

<h1 align="center">comi</h1>

<p align="center">
  <b>Your repositories komi: it picks up your changes and serves them as commits, on your schedule.</b><br>
  Automatic commits, or hidden snapshots of your uncommitted work, for any number of projects, each with its own timing.
</p>

<p align="center">
  <img alt="Go 1.23+" src="https://img.shields.io/badge/go-1.23%2B-00ADD8?logo=go&logoColor=white">
  <img alt="Windows, macOS, Linux" src="https://img.shields.io/badge/platform-windows%20%7C%20macos%20%7C%20linux-lightgrey">
  <img alt="No dependencies" src="https://img.shields.io/badge/dependencies-none%20but%20git-brightgreen">
  <a href="LICENSE"><img alt="MIT license" src="https://img.shields.io/badge/license-MIT-blue"></a>
</p>

---

```text
$ comi list
NAME   MODE      SCHEDULE     NEXT RUN              BRANCH  PUSH  KEEP       MESSAGE              PATH
app    snapshot  5m           Tue 2026-10-13 21:25  (any)   yes   1d,7d,30d  -                    /home/you/code/app
blog   commit    30m          Tue 2026-10-13 21:44  (any)   yes   -          auto: {date} {time}  /home/you/code/blog
notes  commit    09:00,18:00  Wed 2026-10-14 09:00  main    no    -          daily: {date}        /home/you/code/notes
```

## Features

- **Two modes per project.**
  - **commit**: real commits on your branch, pushed for you. Good for notes, docs and dotfiles.
  - **snapshot**: a safety net for code. Your uncommitted work is saved to hidden refs every few minutes; your branch, history and staging area are never touched.
- **Get lost work back.** `git reset --hard`, `git clean`, a bad merge, an AI agent "cleaning up": `comi restore 10m` brings the files back.
- **A schedule per project**: every `30m`, every `2h`, or at `20:00` / `09:00,18:00` each day.
- **One background process** for all your repositories. Add or remove projects while it runs; no restart needed.
- **Off-machine backup.** Snapshots are pushed to `origin` as hidden refs, not branches, so a dead laptop doesn't take your work with it.
- **Snapshots don't pile up.** Old ones are thinned out automatically: everything from the last day, hourly for a week, daily for a month.
- **Push is never lost.** If a push fails (offline, auth), the commit or snapshot stays local and is pushed on the next run.
- **Safe by default.** Commits are skipped during a rebase, merge, cherry-pick or revert, or in detached HEAD. comi never switches branches.
- **Branch lock (optional).** `-b main` runs only while `main` is checked out.
- **Message templates** with `{date}`, `{time}` and `{files}`.
- **Single binary.** Windows, macOS and Linux, tested on all three; the only requirement is `git`.

## Quick start

**1. Install**: download the archive for your system from [Releases](../../releases/latest), extract it and put `comi` (or `comi.exe`) somewhere on your `PATH`. Or build it with Go 1.23+:

```sh
git clone <repo-url> comi && cd comi
go install ./cmd/comi               # installs to ~/go/bin; make sure it's on your PATH
```

**2. Add a project** (or run `comi add` alone and answer the questions), then **3. start the scheduler** and leave it running:

```sh
comi add ~/code/app -mode snapshot      # snapshot every 5 minutes
comi add ~/code/blog -s 30m             # commit every 30 minutes
comi run
```

```text
2026/10/13 21:20:00 comi 0.2.0 started (data: /home/you/.config/comi), press Ctrl+C to stop
2026/10/13 21:25:00 app: snapshot 2bb2416c (2 files changed), pushed
2026/10/13 21:30:00 app: no changes
2026/10/13 21:44:00 blog: committed and pushed
```

To start comi automatically when you log in, see [Run in the background](#run-in-the-background).

## Snapshot mode

Snapshot mode protects work you haven't committed yet. Every few minutes comi saves your files, including new untracked ones, as a hidden commit. Nothing about your repository changes: not your branch, not your history, not what you have staged.

```text
$ git reset --hard && git clean -fd          # oops: an hour of work is gone

$ comi history
SNAPSHOT  TIME                  AGE      FILES CHANGED
2bb2416c  Tue 2026-10-13 21:22  3m ago   2

$ comi diff latest -stat                     # what would come back?
 notes.txt   | 1 -
 src/main.go | 2 +-

$ comi restore latest                        # everything back
saved your current files as snapshot 994ffbda (undo with: comi restore 994ffbda)
restored all files from snapshot 2bb2416c (Tue 2026-10-13 21:22)
```

- **Pick a snapshot** by id (`2bb2416c`), by age (`30m`, `2h`, `3d`: the newest snapshot at least that old) or with `latest`.
- **Restore single files** with `comi restore 2h src/main.go`, or everything with `comi restore 2h`.
- **Restoring is undoable.** Your current files are snapshotted before anything is overwritten. Restore only writes files; the staging area and HEAD stay as they are.
- **Works in any git repository.** `comi snapshot`, `history`, `diff` and `restore` don't need the project to be added; scheduled snapshots do.
- **Works in any state**: detached HEAD, mid-rebase, in a conflicted merge. That's often exactly when you need it.

### Keeping old snapshots

Every few minutes adds up, so old snapshots are thinned out. The default policy, `24h,7d,30d`, keeps:

| Age | Kept |
| --- | --- |
| up to 24 hours | every snapshot |
| up to 7 days | the newest of each hour |
| up to 30 days | the newest of each day |
| older | nothing (the newest snapshot is always kept) |

Set another policy per project with `-keep`, for example `comi add . -mode snapshot -keep 2h,3d,14d`, or keep everything with `-keep forever`. `comi prune` cleans up right away; `comi prune -n` only shows what would go.

Old snapshots are removed during scheduled runs about once a day. Snapshots are linked to each other, so removing some gives the newer ones new ids (their files and times stay the same). For older snapshots, pick them by age (`2h`, `3d`) or `latest` rather than by id.

<details>
<summary>How it works</summary>

- Snapshots of branch `main` live under `refs/comi/main`. Refs outside `refs/heads` are not branches, so they don't show up in `git branch` or as branches on GitHub.
- A snapshot is built on a copy of the index (`git add -A`, `write-tree`, `commit-tree`), so your real index is never modified. Files in `.gitignore` are left out.
- Each snapshot's first parent is the previous snapshot and its second parent is the commit you were on, so `git log --first-parent refs/comi/main` shows them all.
- A new snapshot is only made when something changed since the last one and the files differ from your last commit.
- Pruning rebuilds the chain from the oldest kept snapshot with the same trees, times and parents, then moves the ref only if no snapshot was taken meanwhile. Disk space is freed when git's garbage collection removes the unreferenced objects.
- With push enabled, the ref is pushed to `origin` as `refs/comi/<branch>`. `refs/comi-pushed/<branch>` remembers what was sent, so failed pushes are retried without contacting the remote.

</details>

## Commands

| Command | What it does |
| --- | --- |
| `comi add [path] [flags]` | Add a project, or update it if it's already added. Without a path, it asks step by step. |
| `comi list` (`ls`) | Show projects, their schedule and next run. |
| `comi now [name ...]` | Run right now: the given projects, or all of them. |
| `comi remove <name\|path> ...` (`rm`) | Stop managing a project. Your repository is not touched. |
| `comi run` | Start the scheduler. It checks every 30 seconds which projects are due. Only one can run at a time. |
| `comi snapshot [-p project] [-push]` | Take a snapshot now. |
| `comi history [-p project] [-b branch] [-n 20]` | List snapshots of the current (or given) branch. |
| `comi diff <snapshot> [-p project] [-stat]` | Show what changed between a snapshot and your files now. |
| `comi restore <snapshot> [-p project] [file ...]` | Put files back from a snapshot (all files if none given). |
| `comi prune [-p project] [-keep policy] [-n]` | Remove old snapshots now, on every branch. `-n` only lists them. |
| `comi version` | Show the installed version. |
| `comi help` | Show usage. |

### `comi add` flags

| Flag | Default | Description |
| --- | --- | --- |
| `-mode <mode>` | `commit` | `commit` or `snapshot`, see [Snapshot mode](#snapshot-mode). |
| `-s <schedule>` | `1h` (`5m` for snapshots) | When to run, see [Schedules](#schedules). |
| `-m <message>` | `auto: {date} {time}` | Commit message template (commit mode). |
| `-b <branch>` | *(any branch)* | Only run while this branch is checked out. `-b ""` removes the lock. |
| `-n <name>` | folder name | Name used in `list`, `now`, `remove` and `-p`. |
| `-keep <policy>` | `24h,7d,30d` | Which old snapshots to keep (snapshot mode), see [Keeping old snapshots](#keeping-old-snapshots). |
| `-no-push` | off | Keep commits or snapshots local. |

Flags can go before or after the path. Running `comi add` again on the same project updates it and keeps every setting you don't pass:

```sh
comi add ~/code/api -s 20:00 -m "daily: {date}" -b main
comi add ~/code/api -s 18:00        # only the schedule changes
```

## Schedules

| Schedule | Runs |
| --- | --- |
| `30m`, `2h`, `1h30m` | every interval (minimum `1m`) |
| `20:00` | every day at 20:00 |
| `09:00,13:00,18:00` | every day at each of these times |

Times use your computer's local time zone. If comi wasn't running when a run was due (computer off, asleep), that run happens **once** as soon as comi starts again; missed runs are not repeated one by one.

## Commit messages

| Placeholder | Becomes | Example |
| --- | --- | --- |
| `{date}` | date of the run | `2026-10-13` |
| `{time}` | time of the run | `20:00` |
| `{files}` | number of changed files | `3` |

`-m "wip: {files} files at {time}"` → `wip: 3 files at 20:00`

## Commit mode: what happens on each run

1. **Safety check.** The run is skipped if:
   - the repository is in detached HEAD,
   - a rebase, merge, cherry-pick or revert is in progress, or
   - a branch lock is set (`-b`) and a different branch is checked out.

   A skipped run counts as done; the next one follows the schedule.
2. **Commit.** If anything changed, `git add -A` and `git commit` with your message.
3. **Push.** Unless `-no-push` is set:
   - the branch is pushed to its upstream, or with `-u origin HEAD` the first time;
   - commits left over from a failed push are pushed too, even when nothing new changed.

Every git command has a 2-minute timeout and never waits for a password prompt.

## Before you use it

- **Check your `.gitignore`.** Both modes save *everything* that isn't ignored, untracked files included, and push it to `origin` unless you pass `-no-push`. Make sure `.env` files, keys and build output are ignored.
- **Set up authentication** (an SSH key or a credential helper) so `git push` works without typing a password; comi can't answer prompts.
- **Think twice on shared branches.** Pushing automatically to a branch others use, or one that triggers CI/CD, can surprise your team. A personal branch with `-b`, or `-no-push`, keeps it to yourself.

## Run in the background

`comi run` needs to keep running for scheduled commits. To start it when you log in:

<details>
<summary><b>Windows</b>: Startup folder shortcut</summary>

Run in PowerShell. It starts comi minimized at every login; no admin rights needed.

```powershell
$s = (New-Object -ComObject WScript.Shell).CreateShortcut("$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Startup\comi.lnk")
$s.TargetPath = "$env:USERPROFILE\go\bin\comi.exe"
$s.Arguments = "run"
$s.WindowStyle = 7
$s.Save()
```

To stop it, close the comi window. To remove it, delete `comi.lnk` from the Startup folder (`Win+R` → `shell:startup`).

</details>

<details>
<summary><b>macOS</b>: launchd</summary>

Save as `~/Library/LaunchAgents/comi.plist`, replacing `YOU` with your user name:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>comi</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/YOU/go/bin/comi</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/comi.log</string>
  <key>StandardErrorPath</key><string>/tmp/comi.log</string>
</dict>
</plist>
```

```sh
launchctl load ~/Library/LaunchAgents/comi.plist     # start (and on every login)
tail -f /tmp/comi.log                                # watch the log
launchctl unload ~/Library/LaunchAgents/comi.plist   # stop
```

</details>

<details>
<summary><b>Linux</b>: systemd user service</summary>

Save as `~/.config/systemd/user/comi.service`:

```ini
[Unit]
Description=comi - scheduled git commits

[Service]
ExecStart=%h/go/bin/comi run
Restart=on-failure

[Install]
WantedBy=default.target
```

```sh
systemctl --user enable --now comi   # start (and on every login)
journalctl --user -u comi -f         # watch the log
loginctl enable-linger "$USER"       # optional: keep running after you log out
```

</details>

## Where settings are stored

| OS | Directory |
| --- | --- |
| Windows | `%AppData%\comi` |
| macOS | `~/Library/Application Support/comi` |
| Linux | `$XDG_CONFIG_HOME/comi` or `~/.config/comi` |

`projects.json` holds your projects; `state.json` remembers when each one last ran; `comi.lock` exists while `comi run` is running. Set `COMI_HOME` to use a different directory, for example to keep separate setups.

## Development

```text
cmd/comi/             the CLI: commands, flags, scheduler loop
internal/autocommit/  commit mode: commit, push, retry, safety checks
internal/snapshot/    snapshot mode: take, list, find, restore, push
internal/schedule/    parsing "30m" / "20:00" and computing the next run
internal/config/      projects.json and state.json
internal/lock/        one scheduler per data directory
internal/git/         running git commands
internal/gittest/     throwaway repositories for tests
```

```sh
go vet ./...
go test ./...          # unit tests plus integration tests against real temporary git repositories
```

CI runs formatting, vet and the tests on Windows, macOS and Linux for every push and pull request ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)).

To release, push a version tag. [GoReleaser](https://goreleaser.com) builds the binaries for every platform and publishes them on the Releases page:

```sh
git tag v0.2.0
git push origin v0.2.0
```

## Why "comi"?

In Turkish, a **komi** is the helper in restaurants, hotels and cafés who works in the service or kitchen area and assists the cooks and waiters:

> **komi**: Lokanta, otel ve kafe gibi yiyecek-içecek hizmeti veren yerlerde aşçılara veya garsonlara yardımcı olan, işletmenin servis veya mutfak bölümlerinde çalışan görevli.

comi is your repositories' komi. It works quietly in the background, collects your changes and serves them as commits at the time you set, and, like any good komi, picks up what falls on the floor before anyone notices. It's also only one letter away from *commit*.

## Credits

The comi mascot is based on the Go gopher, designed by [Renée French](https://reneefrench.blogspot.com/) and licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).

## License

[MIT](LICENSE)
