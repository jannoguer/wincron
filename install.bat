@echo off
:: asks for admin escalation
net session >nul 2>&1 || (
  powershell -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
  exit /b
)
cd /d "%~dp0"

:: build the binary (optimized) if not already present
if not exist wincron.exe (
  where go >nul 2>nul || (
    echo wincron.exe not found and Go is not installed.
    echo Download the release zip from GitHub, or install Go to build from source.
    goto :fail
  )
  go build -ldflags "-s -w" -trimpath -o wincron.exe . || goto :fail
)

echo wincron.exe found, proceeding with installation...

set "TARGET=%ProgramFiles%\wincron"

:: an existing install must be stopped and uninstalled before we overwrite it.
:: ask for consent first; crontab.txt is preserved, unrelated files are left alone.
if not exist "%TARGET%\wincron.exe" goto :install
echo.
echo An existing wincron installation was found at "%TARGET%".
set "REPLY="
set /p REPLY="Stop and uninstall it before updating? Your crontab.txt will be kept. [y/N] "
if /i not "%REPLY%"=="y" (
  echo aborted, nothing was changed.
  goto :fail
)
"%TARGET%\wincron.exe" stop >nul 2>&1
"%TARGET%\wincron.exe" uninstall >nul 2>&1

:install
:: install the binary
mkdir "%TARGET%" 2>nul
copy /y wincron.exe "%TARGET%" >nul || goto :fail
cd /d "%TARGET%"

wincron.exe install || goto :fail
if not exist crontab.txt (
  >crontab.txt  echo # e.g.: run a backup of all your user accounts at 5 a.m every week with:
  >>crontab.txt echo # 0 5 * * 1 tar -zcf C:\backups\home.tgz C:\Users
  >>crontab.txt echo.
)
wincron.exe start

:: generate self-deleting, self-elevating uninstaller. it removes only wincron's
:: own files, then removes the folder if it ends up empty (left alone otherwise).
(
  echo @echo off
  echo net session ^>nul 2^>^&1 ^|^| ^(
  echo   powershell -Command "Start-Process -FilePath '%%~f0' -Verb RunAs"
  echo   exit /b
  echo ^)
  echo "%TARGET%\wincron.exe" stop ^>nul
  echo "%TARGET%\wincron.exe" uninstall ^>nul
  echo cd /d "%SystemRoot%" ^>nul
  echo ^(goto^) 2^>nul ^& del /q "%TARGET%\wincron.exe" "%TARGET%\crontab.txt" "%TARGET%\wincron.log" "%TARGET%\wincron.log.1" "%%~f0" 2^>nul ^& rd "%TARGET%" ^>nul 2^>^&1
) > uninstall.bat

echo done.
pause
exit /b 0

:fail
echo install failed.
pause
exit /b 1
