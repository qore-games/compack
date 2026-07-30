@echo off
REM Development wrapper: builds the Go binary and runs native dev
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

REM Run native dev with all arguments
native dev %*
