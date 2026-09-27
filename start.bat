@echo off
title MEOWFUCK
rem MEOWFUCK daemon launcher (Windows).
rem
rem KEEP THIS FILE ASCII-ONLY. cmd.exe parses UTF-8 batch comments unreliably
rem (chcp 65001 block-boundary desync executes comment fragments as commands),
rem so all Chinese documentation lives in start.sh and README instead.
rem
rem Usage:
rem   start.bat                  foreground (close the app window to stop)
rem   start.bat -addr :9000      extra args are passed through to the app
rem
rem Program lookup (renamed product first, legacy service name as fallback):
rem   1. next to this script (build-local.sh deploy layout; data/ follows exe dir)
rem   2. out\artex-local\ (source repo layout; CWD stays at the repo root so
rem      config.json and the repo data dir resolve there, via -data .\data)
rem
rem It only supervises: run the app, then decide by exit code whether to rerun.
rem   0      normal stop        - exit the loop
rem   75     restart requested  - rerun immediately (one-click update / rollback)
rem   other  crash              - relaunch with backoff (1, 2, 4... capped at 60s)
rem
rem Download / SHA256 / binary swap live in the app itself (selfupdate package);
rem this script stays dumb on purpose. See start.sh for the Unix twin.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "APP=MEOWFUCK.exe"
set "ALT=artex.exe"
set "BINDIR="

if exist "%APP%" (
    set "BINDIR=."
) else if exist "%ALT%" (
    set "APP=%ALT%"
    set "BINDIR=."
) else if exist "out\artex-local\%APP%" (
    set "BINDIR=out\artex-local"
) else if exist "out\artex-local\%ALT%" (
    set "APP=%ALT%"
    set "BINDIR=out\artex-local"
)

if not defined BINDIR (
    echo [MEOWFUCK] executable not found, expected MEOWFUCK.exe or artex.exe. Run build-local.sh first. 1>&2
    exit /b 1
)

set "BIN=%BINDIR%\%APP%"
rem Interrupted-swap self-heal: a power cut between the two renames leaves no
rem current binary, only a name.old.exe leftover. The backup name derives from
rem the running exe name, so honor both MEOWFUCK.old.exe and artex.old.exe.
set "OLDBIN=%BINDIR%\%APP:.exe=.old.exe%"
if not exist "%BIN%" if exist "%OLDBIN%" (
    echo [MEOWFUCK] interrupted upgrade detected, restoring backup... 1>&2
    move /y "%OLDBIN%" "%BIN%" >nul
)
if not exist "%BIN%" (
    echo [MEOWFUCK] executable not found: %BIN% 1>&2
    exit /b 1
)

rem Repo layout: anchor data to the repo-root data dir, same root as config.json,
rem matching how the instances are started manually.
set "DATAFLAG="
if not "%BINDIR%"=="." set "DATAFLAG=-data .\data"

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %DATAFLAG% %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
    echo [MEOWFUCK] exited normally
    exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
    rem Update or rollback is staged: rerun and the app completes the swap on boot.
    echo [MEOWFUCK] restart requested, applying new version...
    set /a delay=1
    goto loop
)

echo [MEOWFUCK] abnormal exit, code=!code!, restarting in !delay!s 1>&2
rem timeout fails in redirected consoles; ping is the fallback (N seconds needs N+1 pings).
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
