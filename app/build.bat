@echo off
REM Build the compack Go binary for the current platform, then run a native command.
REM Usage: build.bat [dev|build|test|check|...]

setlocal enabledelayedexpansion

set SCRIPT_DIR=%~dp0
set ROOT_DIR=%SCRIPT_DIR%..
set ASSETS_DIR=%SCRIPT_DIR%assets

REM Detect architecture
if "%PROCESSOR_ARCHITECTURE%"=="AMD64" (
    set GOARCH=amd64
) else if "%PROCESSOR_ARCHITECTURE%"=="ARM64" (
    set GOARCH=arm64
) else (
    echo Unknown architecture: %PROCESSOR_ARCHITECTURE%
    exit /b 1
)

set GOOS=windows
set BINARY_NAME=compack.exe

if not exist "%ASSETS_DIR%" mkdir "%ASSETS_DIR%"

echo Building compack for %GOOS%/%GOARCH%...
set GOOS=%GOOS%
set GOARCH=%GOARCH%
go build -o "%ASSETS_DIR%\%BINARY_NAME%" "%ROOT_DIR%"
if errorlevel 1 (
    echo Build failed
    exit /b 1
)
echo Binary built: %ASSETS_DIR%\%BINARY_NAME%

REM Run the native command with all arguments passed to this script
if "%~1"=="" (
    echo Usage: %~nx0 [dev^|build^|test^|check^|...]
    echo Example: %~nx0 dev
    exit /b 0
)

native %*
