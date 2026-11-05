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

:: an existing install is stopped and replaced in place; ask for consent first.
:: crontab.txt is preserved, unrelated files are left alone.
if not exist "%TARGET%\wincron.exe" goto :install
echo.
echo An existing wincron installation was found at "%TARGET%".
set "REPLY="
set /p REPLY="Stop and upgrade it? Your crontab.txt will be kept. [y/N] "
if /i not "%REPLY%"=="y" (
  echo aborted, nothing was changed.
  goto :fail
)

:install
wincron.exe install || goto :fail

echo done. Successfully installed at: "%TARGET%"
pause
exit /b 0

:fail
echo install failed.
pause
exit /b 1
