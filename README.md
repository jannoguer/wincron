# wincron - Windows crontab clone in Go.

> [!CAUTION]
> `wincron` runs as a highly privileged `NT AUTHORITY\SYSTEM` account directly from `C:\Windows\system32`. This grants it full administrative control over the machine and it lacks a standard user environment. Proceed with care.

## Usage

Works just like standard crontab. To print the help menu, run:

```bat
wincron.exe --help
```

## Install

Run `install.bat`. This will automatically build the executable (if it is not already present) and install the background service.

## Uninstall

To remove the service and files, run the uninstall script from the installation directory:

```bat
"%ProgramFiles%\wincron\uninstall.bat"
```
