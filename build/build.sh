#!/bin/bash

echo "========================================"
echo "Building Localaw Executable for Linux"
echo "========================================"
echo

# Get script directory and go to project root
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR/.."

# Create build directory if not exists
if [ ! -d "build" ]; then
    mkdir -p build
fi

# Install pyinstaller if not installed
if ! python3 -c "import pyinstaller" &> /dev/null; then
    echo "Installing pyinstaller..."
    pip install pyinstaller
fi

# Check if virtual environment exists and activate if needed
if [ -d "localawenv" ]; then
    echo "Activating virtual environment..."
    source localawenv/bin/activate
fi

# Build the executable for Linux
echo "Building..."
pyinstaller --onefile --name Localaw \
    --add-data "web:web" \
    --add-data "src:src" \
    --hidden-import numpy \
    --hidden-import uvicorn \
    --hidden-import fastapi \
    --hidden-import openai \
    --hidden-import httpx \
    --hidden-import tzdata \
    --collect-all numpy \
    --collect-all uvicorn \
    --collect-all fastapi \
    --collect-all openai \
    --console \
    src/main.py

if [ $? -eq 0 ]; then
    echo
    echo "========================================"
    echo "Build completed successfully!"
    echo "Executable: dist/Localaw"
    echo "========================================"
    
    # Move executable to build directory
    mv dist/Localaw build/ 2>/dev/null
    
    # Make the executable executable
    chmod +x build/Localaw
    
    echo "Executable moved to: build/Localaw"
    echo "File permissions set to executable"
else
    echo
    echo "Build failed!"
    exit 1
fi

echo
read -p "Press Enter to continue..."