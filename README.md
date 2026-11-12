<p align="center">
  <img src="assets/comi.svg" alt="The comi mascot serving Git commits on a tray" width="140">
</p>

<h1 align="center">comi</h1>

<p align="center"><strong>Scheduled Git snapshots and automatic commits.</strong></p>

<p align="center">
  Back up uncommitted work, preview a restore, and schedule backups across repositories.
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="#restore-files">Restore</a> ·
  <a href="#project-namespaces">Namespaces</a> ·
  <a href="#command-reference">Commands</a>
</p>

<p align="center">
  <img alt="Windows, macOS and Linux" src="https://img.shields.io/badge/platform-Windows%20%7C%20macOS%20%7C%20Linux-475569">
  <a href="LICENSE"><img alt="MIT license" src="https://img.shields.io/badge/license-MIT-blue"></a>
</p>

## Quick start

After [installing comi](#install), run these commands in a Git repository with uncommitted changes.

```sh
comi snapshot -push=false
comi history
```

Edit one of the saved files again and preview the restore.

```text
$ comi restore latest -n
Restore preview for snapshot d62ea2a2
  write  "notes.txt"
```

| Mode | What it saves | When to use it |
| --- | --- | --- |
| **Snapshot** | Uncommitted files in a separate Git ref. The current branch and staging area stay intact. | Code in progress, experiments, and work before running an agent |
| **Commit** | A regular commit on the checked-out branch. | Notes, docs, and other repositories where you want automatic commits |

## Install

Comi runs on **Windows, macOS, and Linux** with `git` on your `PATH`. Release builds target amd64 and arm64.

Build from source with Go 1.23 or newer.

```sh
git clone https://github.com/YunusEmreAlps/comi.git
cd comi
go install ./cmd/comi
```

For a pinned install, replace `vX.Y.Z` with a release tag that uses the `github.com/YunusEmreAlps/comi` module path.

```sh
go install github.com/YunusEmreAlps/comi/cmd/comi@vX.Y.Z
```

Go puts the binary in `GOBIN` or in the `bin` directory under `GOPATH`. Add that directory to your `PATH` and run `comi version`.

When a binary release is available, you can download the matching archive, extract it, and put `comi` or `comi.exe` on your `PATH`. [GitHub Releases](https://github.com/YunusEmreAlps/comi/releases)

## Schedule projects

Register the current repository for local snapshots every five minutes. `comi now .` runs that project immediately.

```sh
comi add . -mode snapshot -s 5m -no-push
comi now .
comi run
```

One scheduler handles all registered projects and checks for due work every 30 seconds. In another terminal, `comi status` shows the scheduler, last successful run, most recent error, and pending pushes based on local Git refs. Use `comi status .` to check the current project. To run the scheduler at login, see [background setup](#run-in-the-background).

Use `comi add` with no arguments for guided setup. To change one setting, run `comi add . -s 15m`. Other settings are retained.

| Schedule | Example | When it runs |
| --- | --- | --- |
| Interval | `30m` or `1h30m` | Every interval, with a one-minute minimum |
| Daily | `09:00,18:00` | At each listed time in your local time zone |

A missed schedule runs once when the scheduler resumes. Manual runs via `comi now` leave the scheduled run time intact.

### Automatic commits

In a repository where you want commits on the current branch, run `comi add . -mode commit -s 30m -no-push`. Commit mode stages changes with `git add -A`. It skips detached HEAD and repositories in a merge, rebase, cherry-pick, or revert. Use `-b main` to limit runs to one branch.

The default commit message is `auto: {date} {time}`. Set `-m "wip: {files} files at {time}"` to use a different template.

## Project namespaces

Save a root directory once and use targets like `work/api` from any working directory.

```powershell
comi projects add work "D:\GitHub\Work"
comi projects list
comi add work/api -mode snapshot -s 5m -no-push
comi status work/api
comi history -p work/api
```

`comi projects add` maps the root. `comi add work/api` schedules the repository. Commands such as `history` and `restore` can use the shorthand without scheduling it. Use `/` in namespace targets on every platform.

Project names and absolute paths work alongside namespace targets. An exact registered name takes precedence in commands that accept project names. Use `./work/api` or `.\work\api` to select a path relative to the current directory. Updating a namespace root changes the shorthand and keeps the saved paths of scheduled projects.

## Restore files

Select a snapshot by `latest`, an ID prefix, or an age such as `2h`. An age selects the newest snapshot at least that old. History follows the current branch by default. Pass `-b <branch>` to read another branch's snapshots.

```sh
comi history
comi diff latest -stat
comi restore latest -n
```

After reviewing the preview, run `comi restore latest` for the whole snapshot or `comi restore latest src/main.go` for a single file. Use `-p work/api` to target another repository. With `-p`, relative file paths start at that repository's root. Otherwise they start in your current directory.

Restore saves a backup when the current files differ from both `HEAD` and the latest snapshot. It prints the new backup ID and stops if the restore would overwrite a file that the backup excludes. You can take snapshots in detached HEAD or during a conflicted merge or rebase. Both snapshot and restore preserve `HEAD` and the staging area.

### Snapshot retention

Snapshots include tracked files and unignored untracked files up to 50 MiB. The default policy `24h,7d,30d` keeps every snapshot for 24 hours, the newest per hour through day 7, and the newest per day through day 30. The latest snapshot is always kept.

Use `-keep 2h,3d,14d` for custom cutoffs or `-keep forever` to retain every snapshot. Run `comi prune -n` to preview cleanup and `comi prune` to apply it. Pruning can change snapshot IDs, so age selectors are useful for older snapshots.

## Remote backups

Scheduled projects push by default. The examples above use `-no-push` for local backups. Snapshot mode sends `refs/comi/<branch>` to `origin`. Commit mode pushes to the branch's upstream or tries `origin` on its first push.

> [!IMPORTANT]
> Both modes can save untracked files. Check your `.gitignore` for secrets and build output before enabling push, and review anything you have already staged or tracked. Set up an SSH key or credential helper so Git can authenticate without prompting.

To enable push for a registered snapshot project, run `comi add . -no-push=false` from its repository. Failed snapshot pushes stay local and are retried on subsequent runs. If another clone pushes a divergent snapshot history to the same ref, comi rejects the update and keeps the local backup.

### Recover snapshots in a fresh clone

Use `git ls-remote --refs origin 'refs/comi/*'` to find snapshot refs. In the fresh clone, fetch the branch you backed up. Replace `main` if needed.

```sh
git fetch --no-tags origin refs/comi/main:refs/comi/main
comi history -b main
comi restore latest -b main -n
```

Run `comi restore latest -b main` when you are ready to apply the restore.

## Claude Code hook

`comi hook` takes a local snapshot before Claude Code runs a Bash or PowerShell tool call. Add this hook to `.claude/settings.json` in a project or `~/.claude/settings.json` for all projects. Merge it into the existing settings if the file already exists. [Claude Code hook reference](https://code.claude.com/docs/en/hooks)

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash|PowerShell",
        "hooks": [{ "type": "command", "command": "comi hook" }]
      }
    ]
  }
}
```

The hook uses the agent's current Git repository and lets the tool call continue if a snapshot fails. For a scheduled snapshot project with push enabled, the scheduler can send those snapshots to `origin`.

## Command reference

| Command | Use |
| --- | --- |
| `comi add [path] [flags]` | Register or update a scheduled project. |
| `comi list` or `comi ls` | List scheduled projects and their settings. |
| `comi status [project]` | Show the scheduler, recent runs, and push status. |
| `comi now [project ...]` | Run the selected projects immediately, or all projects without arguments. |
| `comi remove <project> ...` or `comi rm` | Remove scheduling for a project. |
| `comi run` | Start the scheduler in the foreground. |
| `comi projects add <namespace> <directory>` | Set a namespace root. |
| `comi projects list` | List namespace roots. |
| `comi snapshot [-p project] [-push]` | Save a snapshot now. |
| `comi history [-p project] [-b branch] [-n 20]` | List snapshots. |
| `comi diff <snapshot> [-p project] [-b branch] [-stat]` | Compare the snapshot with the working files. |
| `comi restore <snapshot> [-p project] [-b branch] [-n] [file ...]` | Restore files or preview with `-n`. |
| `comi prune [-p project] [-keep policy] [-n]` | Apply or preview snapshot retention for all branches. |
| `comi hook` | Save a local snapshot for a Claude Code tool call. |
| `comi help` · `comi version` | Show usage or the installed version. |

<details>
<summary><strong>Project flags</strong></summary>

| `comi add` flag | Default | Use |
| --- | --- | --- |
| `-mode commit\|snapshot` | `commit` | Select automatic commits or snapshots. |
| `-s <schedule>` | `1h` for commits, `5m` for snapshots | Set the interval or daily times. |
| `-m <message>` | `auto: {date} {time}` | Set a template with `{date}`, `{time}`, or `{files}`. |
| `-b <branch>` | Any branch | Restrict runs to a branch. `-b ""` clears the restriction. |
| `-n <name>` | Folder name | Set a short project name. |
| `-keep <policy>` | `24h,7d,30d` | Set snapshot retention. |
| `-no-push` | Push enabled | Keep scheduled runs local. |

</details>

## Run in the background

Keep `comi run` active for scheduled runs. Choose your platform to start it at login.

<details>
<summary><strong>Windows</strong> · Startup shortcut</summary>

Run this in PowerShell to start comi minimized at login.

```powershell
$s = (New-Object -ComObject WScript.Shell).CreateShortcut("$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Startup\comi.lnk")
$s.TargetPath = (Get-Command comi).Source
$s.Arguments = "run"
$s.WindowStyle = 7
$s.Save()
```

To stop it, close the comi window. To remove it, delete `comi.lnk` from the Startup folder (`Win+R` → `shell:startup`).

</details>

<details>
<summary><strong>macOS</strong> · launchd</summary>

Save the configuration as `~/Library/LaunchAgents/comi.plist` and replace `/Users/YOU/go/bin/comi` with the output of `command -v comi`.

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
<summary><strong>Linux</strong> · systemd user service</summary>

Save the configuration as `~/.config/systemd/user/comi.service` and adjust the path to match `command -v comi`.

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

## Configuration

Set `COMI_HOME` to override the data directory.

| Platform | Default |
| --- | --- |
| Windows | `%AppData%\comi` |
| macOS | `~/Library/Application Support/comi` |
| Linux | `$XDG_CONFIG_HOME/comi` or `~/.config/comi` |

`projects.json` stores scheduled projects, `namespaces.json` stores namespace roots, and `state.json` records run history. The scheduler holds an OS lock on `comi.lock` while running.

<details>
<summary><strong>How snapshots are stored</strong></summary>

Snapshots use a separate Git index in the repository's Git directory. Comi records each branch under `refs/comi/<branch>` and uses `refs/comi/detached` in detached HEAD. Walk a branch's snapshot history with `git log --first-parent refs/comi/main`.

Comi skips a new snapshot if its tree matches `HEAD` or the last snapshot. Automatic pruning checks at most once per hour and can rewrite the chain. Git garbage collection eventually removes unreachable objects.

</details>

## Development

```sh
go vet ./...
go test ./...
```

The [CI workflow](.github/workflows/ci.yml) runs on Windows, macOS, and Linux. Linux also checks formatting and runs the race detector. The [release workflow](.github/workflows/release.yml) publishes builds for version tags using [GoReleaser](.goreleaser.yaml).

## License

[MIT](LICENSE). The name comes from Turkish *komi*, a restaurant assistant. The mascot is based on the Go gopher by [Renée French](https://reneefrench.blogspot.com/), licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
