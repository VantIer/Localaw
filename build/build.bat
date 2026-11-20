@echo off
REM Build Localaw for every supported target. Fully static, CGO disabled,
REM zero external runtime dependencies. Requires Go 1.25+.
setlocal
cd /d "%~dp0.."

set CGO_ENABLED=0
set TAGS=netgo,osusergo
set LDFLAGS=-s -w
if not exist dist mkdir dist

call :build windows amd64 .exe
call :build windows 386   .exe
call :build linux   amd64
call :build linux   386
call :build linux   arm64
call :build darwin  amd64
call :build darwin  arm64
goto :eof

:build
set GOOS=%~1
set GOARCH=%~2
set EXT=%~3
echo ^>^> localaw %GOOS%/%GOARCH%
go build -trimpath -tags "%TAGS%" -ldflags "%LDFLAGS%" -o "dist\localaw_%GOOS%_%GOARCH%%EXT%" ./cmd/localaw || exit /b 1
goto :eof
