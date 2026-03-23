#!/bin/bash

echo "========================================"
echo "Localaw - Local AI Assistant"
echo "========================================"
echo ""
echo "Select mode:"
echo "1. CLI Mode"
echo "2. Web Server Mode"
echo "3. Exit"
echo ""

read -p "Enter choice (1/2/3): " choice

case $choice in
    1)
        echo "Starting CLI mode..."
        python -m src.main --mode cli
        ;;
    2)
        echo "Starting Web Server..."
        echo "Open http://127.0.0.1:8880 in your browser"
        python -m src.main --mode web
        ;;
    3)
        exit 0
        ;;
esac
