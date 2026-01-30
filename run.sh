#!/bin/bash

# Test script for Camera Dashboard Go

echo "🎥 Camera Dashboard Go - Test Runner"
echo "=================================="

# Check display
if [ -z "$DISPLAY" ]; then
    echo "❌ No display found. Set DISPLAY environment variable."
    echo "   For local display: export DISPLAY=:0"
    echo "   For SSH/X11: ssh -X user@host"
    exit 1
fi

echo "✅ Display found: $DISPLAY"

# Check if application is built
if [ ! -f "./camera-dashboard-go" ]; then
    echo "🔨 Building application..."
    go build -o camera-dashboard-go .
    if [ $? -ne 0 ]; then
        echo "❌ Build failed!"
        exit 1
    fi
fi

echo "✅ Application built successfully"

# Test console version first
echo ""
echo "📊 Testing camera discovery..."
go run main_console.go

echo ""
echo "🖥  Starting GUI application..."
echo "   The application window should appear shortly"
echo "   Press Ctrl+C to stop"
echo ""

# Start GUI application
./camera-dashboard-go

echo ""
echo "👋 Camera Dashboard stopped"