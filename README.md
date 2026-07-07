# wincron - Windows crontab clone in Go.

> [!CAUTION]
> By default, `wincron` runs jobs as the highly privileged `NT AUTHORITY\SYSTEM` account, which grants full administrative control but lacks a standard user profile or `PATH`. Proceed with care, and consider using the `user=NAME` parameter to run jobs with reduced, per-user privileges ([see below](#running-jobs-as-a-user)).

## Usage

To print the help menu, run:
```bat
wincron.exe --help
```

## Schedule syntax

Supports numeric 5-field cron expressions (minute, hour, day-of-month, month, day-of-week), plus the `@reboot` nickname:
```
# minute hour day-of-month month day-of-week command
0 5 * * 1 tar -zcf C:\backups\home.tgz C:\Users
@reboot foo.exe
```
`@reboot` jobs run once each time the scheduler starts: at boot, on `wincron.exe start` (or a service restart), and on each foreground `wincron.exe run`. They are not re-run when the crontab is edited.

Not supported: named aliases (`JAN`, `MON`, ...) and other nicknames (`@daily`, `@hourly`, ...).

The crontab lives in `crontab.txt` next to the executable (`%ProgramFiles%\wincron\crontab.txt` for a service install). Edits are picked up automatically within a minute; if an edit contains an error, the previous jobs are kept and the error is logged. Commands are executed with `cmd.exe /C`, so anything that works at a cmd prompt (pipes, redirection, `&&`, batch files) works in a job.

## Environment variables

Lines of the form `NAME=value` set environment variables for every job below them, on top of the service's own environment:
```
BACKUP_DIR=C:\backups
0 5 * * 1 backup.exe %BACKUP_DIR%
```
Names must match `[A-Za-z_][A-Za-z0-9_]*`; whitespace around the name and value is trimmed. Assignments only affect jobs defined later in the file.

## Running jobs as a user

Jobs default to `SYSTEM`, but adding `user=NAME` to the schedule runs the job using that specific user's full profile, `PATH`, and environment.

Use the format `* * * * * user=foo command` to assign a job to a specific user. Quote names containing spaces: `user="Jan Noguer"` (single or double quotes).

Crucially, the specified user must already be logged in when the job triggers, otherwise wincron will skip the execution.

The first token after the schedule is treated as a user field only when it starts with `user=` (case-insensitive). To run a command whose first word starts with `user=`, invoke it through `cmd /c`. A standalone `USER=name` line is an environment assignment, not a user field.

Note that `crontab.txt` remains the privilege boundary: jobs still default to `SYSTEM`, so only administrators should be able to edit the file.

Run `Get-LocalUser` to see all users.

## Logging

Job starts, captured output (up to 64 KB per run), and exit statuses are written to `wincron.log` next to the executable (mirrored to stdout with `wincron.exe run`). The log rotates at 10 MB and one previous file is kept as `wincron.log.1`.

## Missed minutes and clock changes

If the scheduler wakes up late (machine asleep, heavy load), each job that was due during the missed window is started once, not once per missed minute. Minutes missed more than 60 minutes ago are skipped entirely.

Schedules follow local time. During daylight-saving changes this behaves like plain wall-clock cron: jobs inside a skipped hour do not run that day, and jobs inside a repeated hour run twice.

## Install

Run `install.bat`. This will automatically build the executable (if it is not already present) and install the background service.

## Uninstall

To remove the service and files, run the uninstall script:
```bat
"%ProgramFiles%\wincron\uninstall.bat"
```
