@echo off
:: auto-escalate to admin
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

:: install the binary
mkdir "%ProgramFiles%\wincron" 2>nul
move /y wincron.exe "%ProgramFiles%\wincron" >nul || goto :fail
cd /d "%ProgramFiles%\wincron"

wincron.exe install || goto :fail
if not exist crontab.txt (
  >crontab.txt  echo # e.g.: run a backup of all your user accounts at 5 a.m every week with:
  >>crontab.txt echo # 0 5 * * 1 tar -zcf /var/backups/home.tgz /home/
  >>crontab.txt echo.
)
wincron.exe start

:: generate self-deleting, self-elevating uninstaller
(
  echo @echo off
  echo net session ^>nul 2^>^&1 ^|^| ^(
  echo   powershell -Command "Start-Process -FilePath '%%~f0' -Verb RunAs"
  echo   exit /b
  echo ^)
  echo "%ProgramFiles%\wincron\wincron.exe" stop ^>nul
  echo "%ProgramFiles%\wincron\wincron.exe" uninstall ^>nul
  echo cd /d "%SystemRoot%" ^>nul
  echo ^(goto^) 2^>nul ^& rd /s /q "%ProgramFiles%\wincron" ^>nul
) > uninstall.bat

echo done.
pause
exit /b 0

:fail
echo install failed.
pause
exit /b 1
