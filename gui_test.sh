#!/bin/bash

echo "🔍 Camera Dashboard Go - GUI Diagnostic"
echo "====================================="

echo "🖥  Display Environment:"
echo "   DISPLAY: $DISPLAY"
echo "   XDG_SESSION_TYPE: $XDG_SESSION_TYPE"
echo "   WAYLAND_DISPLAY: $WAYLAND_DISPLAY"

echo ""
echo "🔍 System Info:"
echo "   OS: $(uname -a)"
echo "   Desktop: $XDG_CURRENT_DESKTOP"

echo ""
echo "📋 Installed Libraries:"
echo "   Fyne: $(go list -m fyne.io/fyne/v2 2>/dev/null || echo 'Not installed')"
echo "   OpenGL: $(glxinfo 2>/dev/null | grep 'OpenGL version' | head -1 || echo 'Not available')"
echo "   X11: $(xdpyinfo 2>/dev/null | grep 'server string' | head -1 || echo 'Not available')"

echo ""
echo "🎯 GUI Test Attempts:"

echo "   1. Testing basic Go GUI..."
go run -c 'package main; import("fyne.io/fyne/v2/app","log"); func main(){a:=app.New(); log.Println("Fyne created")}' 2>/dev/null
if [ $? -eq 0 ]; then
    echo "      ✅ Basic Fyne works"
else
    echo "      ❌ Basic Fyne failed"
fi

echo "   2. Testing window creation..."
timeout 3s go run -c 'package main; import("fyne.io/fyne/v2/app","fyne.io/fyne/v2/widget","time"); func main(){a:=app.New(); w:=a.NewWindow("Test"); w.SetContent(widget.NewLabel("Test")); w.ShowAndRun()}' 2>/dev/null &
TEST_PID=$!
sleep 4

if kill -0 $TEST_PID 2>/dev/null; then
    kill $TEST_PID 2>/dev/null
    echo "      ✅ Window creation works"
else
    echo "      ❌ Window creation failed"
fi

echo ""
echo "🚀 Recommendations:"

if [ -z "$DISPLAY" ]; then
    echo "   ❌ No DISPLAY set - GUI cannot start"
    echo "   For local X11: export DISPLAY=:0"
    echo "   For SSH: ssh -X user@host"
elif [ "$XDG_SESSION_TYPE" = "wayland" ]; then
    echo "   ⚠️  Wayland detected - Fyne may need XWayland"
else
    echo "   ✅ X11 environment detected"
fi

echo ""
echo "🔧 To Run Camera Dashboard:"
echo "   export DISPLAY=:0  # Set display if needed"
echo "   ./camera-dashboard-go"