# wincron - Windows crontab clone in Go.

> [!CAUTION]
> `wincron` runs as a highly privileged `NT AUTHORITY\SYSTEM` account directly from `C:\Windows\system32`. This grants it full administrative control over the machine and it lacks a standard user environment. Proceed with care.

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

## Install

Run `install.bat`. This will automatically build the executable (if it is not already present) and install the background service.

## Uninstall

To remove the service and files, run the uninstall script:
```bat
"%ProgramFiles%\wincron\uninstall.bat"
```
