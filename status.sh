#!/bin/bash

echo "🎥 Camera Dashboard Go - Status Check"
echo "================================="

echo "📋 Build Status:"
go build -o camera-dashboard-go . 2>&1
if [ $? -eq 0 ]; then
    echo "✅ Build successful"
    echo "📁 Binary size: $(ls -lh camera-dashboard-go | awk '{print $5}')"
else
    echo "❌ Build failed"
    exit 1
fi

echo ""
echo "🔍 System Check:"
echo "   Display: $DISPLAY"
echo "   Go version: $(go version)"
echo "   OS: $(uname -a)"

echo ""
echo "📷 Camera Detection:"
go run -c 'package main; import("camera-dashboard-go/internal/camera","fmt"); func main(){cameras,_:=camera.DiscoverCameras(); fmt.Printf("Found %d cameras\n",len(cameras))}' 2>/dev/null

echo ""
echo "🖥  GUI Test:"
echo "   Starting GUI for 3 seconds..."
timeout 3s ./camera-dashboard-go 2>/dev/null &
GUI_PID=$!

sleep 4

if kill -0 $GUI_PID 2>/dev/null; then
    kill $GUI_PID
    echo "✅ GUI started successfully"
else
    echo "⚠️  GUI may have display issues"
fi

echo ""
echo "🚀 To run the application:"
echo "   ./camera-dashboard-go"
echo "   OR"
echo "   ./run.sh"