#!/bin/bash

# Installation script for Camera Dashboard Go
# Installs dependencies and sets up the application

set -e

echo "🎥 Camera Dashboard Go - Installation Script"
echo "============================================"

# Function to check if running on Linux
check_linux() {
    if [[ "$OSTYPE" != "linux-gnu"* ]]; then
        echo "❌ This application is designed for Linux systems"
        exit 1
    fi
}

# Function to install Go dependencies
install_go() {
    echo "📦 Installing Go dependencies..."
    
    if ! command -v go &> /dev/null; then
        echo "❌ Go is not installed. Please install Go 1.21 or later"
        echo "   On Ubuntu/Debian: sudo apt install golang-go"
        echo "   On Raspberry Pi: sudo apt install golang-go"
        exit 1
    fi
    
    echo "✅ Go version: $(go version)"
}

# Function to install system dependencies
install_system_deps() {
    echo "📦 Installing system dependencies..."
    
    if command -v apt-get &> /dev/null; then
        # Ubuntu/Debian/Raspberry Pi
        echo "   Using apt-get package manager..."
        
        # Update package list
        sudo apt-get update
        
        # Install basic dependencies
        sudo apt-get install -y \
            pkg-config \
            libgl1-mesa-dev \
            xorg-dev \
            libasound2-dev \
            pulseaudio \
            pulseaudio-utils
            
        # Install video4linux utilities for camera testing
        sudo apt-get install -y v4l-utils
        
        # Install FFmpeg for camera capture
        sudo apt-get install -y ffmpeg
        
        # Note: OpenCV support can be added with:
        # sudo apt-get install -y libopencv-dev pkg-config
        
        echo "✅ System dependencies installed"
        
    elif command -v yum &> /dev/null; then
        echo "   Using yum package manager..."
        sudo yum install -y pkgconfig mesa-libGL-devel alsa-lib-devel
        echo "✅ System dependencies installed"
        
    else
        echo "⚠️  Could not detect package manager. Please install manually:"
        echo "   - pkg-config"
        echo "   - OpenGL development libraries"
        echo "   - ALSA development libraries"
        echo "   - v4l-utils (for camera testing)"
    fi
}

# Function to setup permissions
setup_permissions() {
    echo "🔧 Setting up camera permissions..."
    
    # Add user to video group for camera access
    if ! groups $USER | grep -q "video"; then
        echo "   Adding $USER to video group..."
        sudo usermod -a -G video $USER
        echo "   ⚠️  You may need to log out and log back in for camera permissions to take effect"
    fi
    
    # Create udev rules for camera devices (optional)
    if [ ! -f /etc/udev/rules.d/99-camera-dashboard.rules ]; then
        echo "   Creating udev rules for camera devices..."
        sudo tee /etc/udev/rules.d/99-camera-dashboard.rules > /dev/null <<EOF
# Camera Dashboard - USB Camera Rules
KERNEL=="video[0-9]*", SUBSYSTEM=="video4linux", MODE="0664", GROUP="video"
EOF
        
        # Reload udev rules
        sudo udevadm control --reload-rules
        sudo udevadm trigger
    fi
    
    echo "✅ Camera permissions configured"
}

# Function to build the application
build_app() {
    echo "🔨 Building Camera Dashboard..."
    
    # Download dependencies
    echo "   Downloading Go modules..."
    go mod tidy
    
    # Build the application
    echo "   Compiling application..."
    go build -o camera-dashboard .
    
    # Make executable
    chmod +x camera-dashboard
    
    echo "✅ Build complete: ./camera-dashboard"
}

# Function to test camera devices
test_cameras() {
    echo "📷 Testing camera devices..."
    
    if command -v v4l2-ctl &> /dev/null; then
        for device in /dev/video*; do
            if [ -e "$device" ]; then
                echo "   Testing $device..."
                if v4l2-ctl --device="$device" --info &> /dev/null; then
                    echo "   ✅ $device is accessible"
                else
                    echo "   ❌ $device is not accessible"
                fi
            fi
        done
    else
        echo "   ⚠️  v4l2-ctl not available - skipping camera tests"
        echo "   Install with: sudo apt-get install v4l-utils"
    fi
}

# Function to create desktop entry
create_desktop_entry() {
    echo "🖥️  Creating desktop entry..."
    
    DESKTOP_DIR="$HOME/.local/share/applications"
    mkdir -p "$DESKTOP_DIR"
    
    cat > "$DESKTOP_DIR/camera-dashboard-go.desktop" <<EOF
[Desktop Entry]
Version=1.0
Type=Application
Name=Camera Dashboard Go
Comment=Multi-camera monitoring dashboard
Exec=$(pwd)/camera-dashboard
Icon=camera-video
Terminal=false
Categories=Video;AudioVideo;
EOF
    
    echo "✅ Desktop entry created"
}

# Main installation function
main() {
    echo "Starting installation..."
    echo ""
    
    check_linux
    install_go
    install_system_deps
    setup_permissions
    build_app
    test_cameras
    create_desktop_entry
    
    echo ""
    echo "🎉 Installation complete!"
    echo ""
    echo "To run the application:"
    echo "   ./camera-dashboard"
    echo ""
    echo "Or find it in your applications menu as 'Camera Dashboard Go'"
    echo ""
    echo "If camera permissions don't work, log out and log back in"
    echo ""
}

# Run main function
main