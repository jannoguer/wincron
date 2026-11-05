# wincron - cron for Windows

## Quickstart

From an elevated shell, in the folder with `wincron.exe`:

```
wincron install       # copy to Program Files, add to PATH, start the service
wincron edit          # add a job, e.g.:  */5 * * * * echo hello >> C:\temp\hello.txt
wincron status        # see the jobs with their next and last runs
wincron log -f        # follow the log
```

`wincron test N` runs the job on line `N` right away, to check it before waiting for its time.

> [!CAUTION]
> By default, `wincron` runs jobs as the highly privileged `NT AUTHORITY\SYSTEM` account, which grants full administrative control but lacks a standard user profile or `PATH`. Proceed with care, and consider using the `user=NAME` parameter to run jobs with reduced, per-user privileges ([see below](#running-jobs-as-a-user)).

## Usage

```
usage: wincron <command> [-h]

commands:
  list       print jobs from the crontab file
  edit       edit a copy of the crontab, install it once it validates
  load       validate a file and install it as the crontab
  log        show the end of the log, like tail (-f follows)
  test       run a crontab line now through the service (elevated shell)
  status     show the service, the jobs, and their next and last runs
  validate   check the crontab file for errors
  daemon     run the scheduler in the foreground (like cron -f)
  install    install to Program Files, add to PATH, start the service
  uninstall  remove the service, PATH entry and files (keeps crontab.txt)
  start      start the service
  stop       stop the service
  version    print the wincron version
  help       show help, or help for one command

Run 'wincron <command> -h' for the details of one command.
```

The service commands need an elevated shell. `daemon` runs the scheduler in the current console, like `cron -f` (Ctrl+C to stop), and mirrors the log to stdout.

## Install and uninstall

From an elevated shell, run `wincron.exe install` (on Windows 11, `sudo wincron.exe install` works too). It copies itself to `%ProgramFiles%\wincron`, creates a template `crontab.txt` there if none exists, registers the service against that copy, starts it, and adds the directory to the machine `PATH` (open a new terminal to see it). The service starts at boot and is restarted by Windows on failure (after 5, 30, then 60 seconds). Because the service always runs a copy under Program Files, only administrators can replace the program a SYSTEM service executes.

Running `install` again upgrades in place: it stops the service, replaces the executable, and starts it again; `crontab.txt` is kept. `install.bat` is a thin wrapper that elevates, builds `wincron.exe` if it is not present (requires Go; otherwise use a release zip), asks before upgrading an existing install, and calls `wincron.exe install`.

`wincron uninstall` (elevated) stops and removes the service, the event source and the `PATH` entry, and deletes the executable, the log files and the state file. `crontab.txt` is kept, so the directory stays until you delete it; a later install picks the jobs up again.

## Schedule syntax

Numeric 5-field cron expressions, named aliases in the month and day-of-week fields, and nicknames:

```
# minute hour day-of-month month day-of-week command
0 5 * * 1 tar -zcf C:\backups\home.tgz C:\Users
0 9 * jan-mar mon-fri report.exe
@daily backup.exe
@reboot foo.exe
@logon notify.exe
```

A step on a bare value runs to the end of the field (`5/3` = 5, 8, ..., 59).

When both day-of-month and day-of-week are restricted, a day matching either one runs the job (`0 0 13 * 5` = every 13th and every Friday). As in standard cron, a field starting with `*`, such as `*/2`, counts as unrestricted, so `0 0 */2 * 1` runs only on Mondays that fall on odd days.

`@reboot` runs once each time the scheduler starts (boot, service restart, or foreground `daemon`), not when the crontab is edited.

`@logon` runs each time a user logs on to the machine (a console, fast-user-switch or remote session), like Task Scheduler's "At log on" trigger. Without `user=` it runs as SYSTEM on every logon. With `user=NAME` it runs only when that user logs on, in their session. Logon events are delivered by the service, so `@logon` jobs do not run under the foreground `daemon` (which says so in its log). Logons that happen while the service is stopped are not replayed.

## The crontab file

`crontab.txt` lives next to the executable (`%ProgramFiles%\wincron\crontab.txt` for a service install). Changes are picked up within a minute, keyed on the file's modification time (an edit that preserves the mtime is not noticed). A file with an error keeps the previous jobs and logs the problem; a missing file means no jobs until it appears.

The file may be UTF-8 (with or without a BOM) or UTF-16 with a BOM, as Notepad and PowerShell write it.

`wincron edit` (like `crontab -e`) opens a copy of the file in `%VISUAL%`, then `%EDITOR%`, then Notepad. The editor runs through `cmd.exe`, so it may take arguments (`EDITOR=code --wait`) or be a `.cmd` shim. When you save, the copy is validated: if it is unchanged nothing happens, if it has an error you are asked whether to retry the same edit, and if it is valid it atomically replaces the live file, so the running service never sees a half-edited crontab. If you cannot write the file (a service install under Program Files), `edit` asks for elevation and installs only the copy, with the editor still running as you. `wincron load FILE` validates a file and installs it the same way (like `crontab FILE`).

Commands run with `cmd.exe /C`: pipes, redirection, `&&`, and batch files all work. Unlike classic cron, `%` is not special, so `%VAR%` expands as usual. Jobs start in the profile directory of the account that runs them (`C:\Windows\System32\config\systemprofile` for SYSTEM, the user's profile for `user=`), like cron's `HOME`; use absolute paths anyway.

A job and everything it spawns are terminated as a group when it finishes, so `start /b foo.exe` leaves nothing behind; design jobs to run to completion, not to detach long-lived processes. On service stop, running jobs get 20 seconds to finish before being terminated.

## Status

`wincron status` works without administrator rights. It shows the service state, the paths of the crontab, log and state files, and for every job its line, schedule, user, options, next run, and last run with its result (`list` still prints the raw file, like `crontab -l`). "Next" follows the same wall-clock rules as the scheduler; a schedule that can never fire (30 February) shows `never`.

The last-run history comes from `wincron-state.json` next to the executable, which the scheduler updates after every run. A job is tracked by its line's text, so moving it keeps its history, and entries for jobs removed from the crontab are dropped at the next reload.

## Testing a job

`wincron test N` (from an elevated shell) runs the job on crontab line `N` right now, through the running service, so it gets exactly what a scheduled run gets: the SYSTEM account or the `user=` user, the same environment and working directory, the same `timeout=`. The job's output is streamed to your console and the result is printed; the exit code is non-zero if the job failed. Use the line numbers `wincron status` shows. If the crontab changed since the service last looked, it is reloaded first.

The run is logged like any other, but it does not change the run history shown by `status`, and `overlap=no` does not block it. The service listens for these requests on the named pipe `\\.\pipe\wincron`, which only SYSTEM and Administrators can open.

## Environment variables

```
BACKUP_DIR=C:\backups
0 5 * * 1 backup.exe %BACKUP_DIR%
```

Names must match `[A-Za-z_][A-Za-z0-9_]*`; whitespace around the name and value is trimmed. Values are literal: quotes are kept (`NAME="x"` gives the job `"x"`, quotes included), and `%OTHER%` inside a value is not expanded; cmd.exe only expands `%...%` in command lines. `SHELL`, `HOME`, `MAILTO`, and `TZ` have no special meaning here: jobs always run under cmd.exe in local time, and output goes to the log, never to mail.

## Running jobs as a user

`* * * * * user=foo command` runs the job with that user's full profile, `PATH`, and environment, in their profile directory. Names match case-insensitively, as `foo` or `DOMAIN\foo`; quote names with spaces: `user="Jan Noguer"` (single or double quotes). Crontab `NAME=value` lines still apply on top of the user's environment.

If the user is logged in, the job runs in their session with their token; a disconnected session (fast user switching, a dropped remote session) counts as logged in, and an active session is preferred. If the user is not logged in, the job still runs: wincron makes a background (S4U) logon for them, the same mechanism as Task Scheduler's "run whether the user is logged on or not" without storing a password. That also makes `@reboot user=NAME` work at boot.

> [!NOTE]
> A background logon has no network credentials: the job cannot reach network shares or other machines as the user, and mapped drives do not exist. It runs with the user's profile and environment but without a visible desktop. The account needs the "Log on as a batch job" or "Access this computer from the network" right (ordinary users have the latter by default). If the logon fails, the run is skipped and logged with the reason.

A `user=` job runs on the user's own desktop, so a GUI program it starts (`notepad`, a tray tool) is visible to that user. A job without `user=` runs as SYSTEM in the non-interactive session 0, where windows are never shown; use `user=` for anything that needs a window.

`user=` jobs require the scheduler itself to run as `SYSTEM`, the normal service install. A foreground `wincron.exe daemon` from a regular console skips them.

The tokens after the schedule are job options only while they start with `user=`, `timeout=`, `overlap=`, `missed=`, `power=`, `wake=`, or `retry=` (case-insensitive); run a command whose first word starts with one of those through `cmd /c`. A standalone `USER=name` line is an environment assignment, not a user field. `crontab.txt` remains the privilege boundary: jobs default to `SYSTEM`, so only administrators should be able to edit the file.

> [!TIP]
> `Get-LocalUser` lists the accounts on the machine.

## Timeouts and overlapping runs

```
0 * * * * timeout=5m overlap=no slow.exe
```

`timeout=` terminates the job and everything it spawned once the duration passes; the value needs a unit (`30s`, `5m`, `1h30m`). `overlap=no` skips a start while the previous run of the same job is still going; a job is identified by its line's text, so edits that only move it keep the tracking. Both sit with `user=` between the schedule and the command, in any order. Without them a job runs unbounded and concurrent copies are allowed.

`retry=N` (or `retry=N/DELAY`, such as `retry=3/30s`; the default delay is one minute) runs a failed job again, up to `N` more times, waiting `DELAY` between attempts. A job fails when it exits non-zero or times out. The attempts count as one run: `overlap=no` keeps the job claimed through the waits, `status` shows the final result, and the event log warning is written only after the last attempt. Pending retries are dropped when the service stops.

`power=ac` skips a start while the machine runs on battery and logs a `SKIP` line; a job whose time passes on battery is not made up later, unless it also has `missed=run`, in which case it runs once when mains power returns. An unknown power source counts as mains power, so desktops are unaffected. `power=any` is the default. `wincron test` ignores it.

`wake=yes` wakes a sleeping machine for the job: the service keeps one wake timer set to the earliest next run among the `wake=yes` jobs, and resets it after every minute tick and crontab reload. It does not apply to `@reboot` or `@logon`. Windows must allow wake timers: in the power plan's advanced settings, Sleep > Allow wake timers must be Enable. Machines with Modern Standby (S0 low-power idle) often ignore wake timers or only run them in short maintenance windows, and a machine that is shut down or hibernating without a wake-capable BIOS will not wake. `powercfg /waketimers` (elevated) lists the pending timer. While a `wake=yes` job runs (retries included), wincron holds a system-required power request, so the machine does not go back to sleep in the middle of it; `powercfg /requests` shows it.

## Logging

Each run is logged in the style of cronie, naming the account and the command:

```
2026/07/16 05:00:00 CMD (SYSTEM) backup.exe
2026/07/16 05:00:02 OUT (SYSTEM) backup.exe:
done
2026/07/16 05:00:02 END (SYSTEM) backup.exe: exit 0 (2s)
```

`SKIP` lines report runs that did not start (an earlier run still going, or a `user=` job whose background logon failed), and `WARN` lines report trouble setting up a run. Job starts, captured output (up to 64 KB per run), and exit statuses are written to `wincron.log` next to the executable (mirrored to stdout with `daemon`), or to the absolute path in the `WINCRON_LOG` environment variable (set machine-wide for the service). The log rotates at 10 MB, keeping one previous file as `wincron.log.1`. Job output that is not valid UTF-8 is converted from the console's OEM code page, so accented text stays readable. `wincron log` prints the end of that file (`-n N` lines, default 10) and `wincron log -f` follows it across rotation, like `tail -f`; it reads the same path as the service, honouring `WINCRON_LOG`.

When the service runs a job that exits non-zero, times out, or is skipped because its `user=` could not be logged on, it also writes a Warning to the Windows event log under the `wincron` source, with the command, the result and the tail of its output (view it in Event Viewer under Windows Logs > Application). A skip caused by `overlap=no` is not reported, since it is the option working as intended. If the service fails before the log file is usable, the error is reported to the same event log source.

## Troubleshooting

Start with `wincron test N` and `wincron log`: the test runs the line exactly as the service would, and the log shows the command, its output on failure and the exit code.

- `PATH` under SYSTEM: SYSTEM has the machine `PATH` only, not yours. A tool installed per user (`node`, `python` from the Store, anything under `%LOCALAPPDATA%`) is not found. Use its absolute path, or run the job with `user=NAME`.
- Mapped drives: drive letters you mapped exist only in your own session. Use the UNC path (`\\server\share`), and note that SYSTEM reaches the network as the computer account and a background `user=` logon has no network credentials at all.
- Working directory: jobs start in the account's profile directory, not where the executable or script lives. Use absolute paths, or `cd /d C:\dir && command`.
- GUI programs: a job without `user=` runs in session 0 and cannot show a window. Use `user=NAME` with that user logged on.
- Percent signs: `%` is not special as in classic cron, so `%DATE%` expands.
- A job that never runs: `wincron validate` shows a crontab error, and the log shows `SKIP` lines for `overlap=no`, `power=ac` and `user=` problems.

## Compared with cron and Task Scheduler

| | wincron | Vixie cron / cronie | Task Scheduler |
|---|---|---|---|
| Schedule format | 5 fields, nicknames | 5 fields, nicknames | Trigger dialogs, XML |
| Plain text, diffable | yes | yes | no (XML export) |
| Run a missed job | `missed=run` | anacron | "Run task as soon as possible after a missed start" |
| Run at boot or logon | `@reboot`, `@logon` | `@reboot` | yes |
| Run as a user, logged off | `user=` (S4U, no password) | yes | yes (password or S4U) |
| Timeout | `timeout=` | no | yes |
| No overlapping runs | `overlap=no` | no | yes |
| Retry on failure | `retry=` | no | yes |
| Skip on battery | `power=ac` | no | yes |
| Wake the machine | `wake=yes` | no | yes |
| Failure reports | event log | mail (`MAILTO`) | event log, history |

## Missed minutes and clock changes

```
0 5 * * * missed=run backup.exe
```

`missed=run` makes a job anacron-style: if its latest scheduled time passed while the scheduler was not running (the machine was off or asleep for longer than the catch-up window below), the job runs once, at service start or on the first tick after resume, however many runs were missed and however long ago. It is judged against the run history in `wincron-state.json`, and a job the scheduler sees for the first time never runs on the spot. The default, `missed=skip`, drops missed runs. It does not apply to `@reboot`.

If the scheduler wakes up late (machine asleep, heavy load), each job due during the missed window is started once (not once per missed minute), and minutes missed more than 60 minutes ago are skipped entirely.

Schedules follow local wall-clock time, with the same rules as Vixie cron:

- When the clock jumps forward (the spring DST change), jobs inside the skipped hour run once, at the jump.
- When the clock moves back by less than 3 hours (the autumn DST change, or setting the clock back), only jobs with a `*` in the minute or hour field (`* * * * *`, `0 * * * *`, `*/5 ...`) run again until the clock passes the time it came from. A job at a fixed time, such as `30 1 * * *`, does not run twice.
- When the clock moves back by 3 hours or more, scheduling resumes from the new time and nothing is replayed.

## Release binaries are unsigned

The release zips are not code signed, so SmartScreen may warn. Each release lists the SHA-256 of its zips in `SHA256SUMS`; check the download against it before running `install`: `(Get-FileHash wincron-X.Y.Z-windows-amd64.zip).Hash` must match the line for that file.

`packaging/scoop/wincron.json` and `packaging/winget/` hold a Scoop manifest and winget manifests for the release zips (versions are updated when they are submitted). Both only place `wincron.exe` on the `PATH`; run `wincron install` afterwards.
