@echo off
echo ========================================
echo Building Localaw Executable
echo ========================================
echo.

REM Get script directory and go to project root
set SCRIPT_DIR=%~dp0
cd /d "%SCRIPT_DIR%.."

REM Create build directory if not exists
if not exist "build" mkdir build

REM Install pyinstaller if not installed
pip show pyinstaller >nul 2>&1
if %errorlevel% neq 0 (
    echo Installing pyinstaller...
    pip install pyinstaller
)

REM Build the executable
echo Building...
pyinstaller --onefile --name Localaw ^
    --add-data "web;web" ^
    --add-data "src;src" ^
    --hidden-import numpy ^
    --hidden-import uvicorn ^
    --hidden-import fastapi ^
    --hidden-import openai ^
    --hidden-import httpx ^
    --hidden-import tzdata ^
    --collect-all numpy ^
    --collect-all uvicorn ^
    --collect-all fastapi ^
    --collect-all openai ^
    --console ^
    src/main.py

if %errorlevel% equ 0 (
    echo.
    echo ========================================
    echo Build completed successfully!
    echo Executable: dist\Localaw.exe
    echo ========================================
    move dist\Localaw.exe build\ >nul 2>&1
    echo Executable moved to: build\Localaw.exe
) else (
    echo.
    echo Build failed!
)

echo.
pause
