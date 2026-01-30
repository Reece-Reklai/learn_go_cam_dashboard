#!/bin/bash

# Camera Dashboard Go - Wayland/Hyprland Compatible Launcher

echo "🎥 Camera Dashboard Go - Starting..."
echo "================================"

# Check display environment
if [ "$XDG_SESSION_TYPE" = "wayland" ]; then
    echo "🖥  Wayland detected, using X11 backend..."
    export GDK_BACKEND=x11
    export SDL_VIDEODRIVER=x11
    export QT_QPA_PLATFORM=xcb
fi

echo "🚀 Launching Camera Dashboard..."

# Start the application
./camera-dashboard-go

echo ""
echo "👋 Camera Dashboard stopped"