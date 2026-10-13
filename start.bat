@echo off
rem Switch the console to UTF-8 so output renders correctly.
chcp 65001 >nul 2>&1
rem ARTEX supervisor launch script (Windows)
rem
rem Usage:
rem   start.bat                  run in foreground (Ctrl-C stops)
rem   start.bat -addr :9000      extra args are passed through to artex unchanged
rem
rem It does one thing: run artex.exe, then decide whether to relaunch it based on the exit code.
rem
rem   0      clean user stop  -> exit the loop
rem   75     program asked restart -> rerun immediately (the page's "one-click update" or "rollback" was used)
rem   other  crash            -> rerun with backoff (1->2->4... up to 60s)
rem
rem No download, SHA256 verify, or swap logic here — artex itself does all of that at startup
rem (selfupdate package). This script stays dumb; see the notes at the top of start.sh.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex.exe"
if not exist "%BIN%" (
	echo [artex] executable not found: %BIN% 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex] exited normally
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem Update/rollback staged: on rerun artex completes the swap at startup.
	echo [artex] restart requested (applying new version)...
	set /a delay=1
	goto loop
)

echo [artex] abnormal exit ^(code=!code!^), restarting in !delay!s 1>&2
rem timeout fails in a redirected console; ping is the fallback (a delay of N seconds needs N+1 pings).
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
