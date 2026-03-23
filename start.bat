@echo off
echo ========================================
echo Localaw - Local AI Assistant
echo ========================================
echo.
echo Select mode:
echo 1. CLI Mode
echo 2. Web Server Mode
echo 3. Exit
echo.

set /p choice="Enter choice (1/2/3): "

if "%choice%"=="1" goto cli
if "%choice%"=="2" goto web
if "%choice%"=="3" goto end

:cli
echo Starting CLI mode...
python -m src.main
goto end

:web
echo Starting Web Server...
echo Open http://127.0.0.1:8880 in your browser
python -m src.web_server
goto end

:end
pause
