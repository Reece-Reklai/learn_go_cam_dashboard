#!/bin/bash
#
# Camera Dashboard Installer
# Installs dependencies and the pre-built camera-dashboard binary
#
# Usage:
#   ./install.sh          # Install with prompts
#   ./install.sh --yes    # Install without prompts (auto-yes)
#

set -e

APP_NAME="camera-dashboard"
INSTALL_DIR="/usr/local/bin"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AUTO_YES=false

# Parse arguments
if [ "$1" = "--yes" ] || [ "$1" = "-y" ]; then
    AUTO_YES=true
fi

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "======================================"
echo "  Camera Dashboard Installer"
echo "======================================"
echo ""

# Check if running on Linux ARM64
check_platform() {
    echo -n "Checking platform... "
    ARCH=$(uname -m)
    OS=$(uname -s)
    
    if [ "$OS" != "Linux" ]; then
        echo -e "${RED}FAILED${NC}"
        echo "Error: This installer only supports Linux (detected: $OS)"
        exit 1
    fi
    
    if [ "$ARCH" != "aarch64" ] && [ "$ARCH" != "arm64" ]; then
        echo -e "${RED}FAILED${NC}"
        echo "Error: This binary requires 64-bit ARM (aarch64)"
        echo "Detected: $ARCH"
        echo ""
        echo "If you're on 32-bit Pi OS, rebuild from source:"
        echo "  make build"
        exit 1
    fi
    
    echo -e "${GREEN}OK${NC} (Linux $ARCH)"
}

# Check for display server
check_display() {
    echo -n "Checking display server... "
    
    if [ -z "$DISPLAY" ] && [ -z "$WAYLAND_DISPLAY" ]; then
        if [ -e "/tmp/.X11-unix/X0" ]; then
            echo -e "${GREEN}OK${NC} (X11 available at :0)"
        else
            echo -e "${YELLOW}WARNING${NC}"
            echo "  No display detected. You need a desktop environment."
            echo "  Run with: DISPLAY=:0 $APP_NAME"
        fi
    elif [ -n "$WAYLAND_DISPLAY" ]; then
        echo -e "${YELLOW}WAYLAND${NC}"
        echo "  Wayland detected. App works best with X11."
    else
        echo -e "${GREEN}OK${NC} (DISPLAY=$DISPLAY)"
    fi
}

# Check/install dependencies
install_dependencies() {
    echo ""
    echo "Checking dependencies..."
    
    MISSING=""
    
    echo -n "  ffmpeg: "
    if command -v ffmpeg &> /dev/null; then
        VERSION=$(ffmpeg -version 2>&1 | head -1 | cut -d' ' -f3)
        echo -e "${GREEN}OK${NC} ($VERSION)"
    else
        echo -e "${RED}MISSING${NC}"
        MISSING="$MISSING ffmpeg"
    fi
    
    echo -n "  v4l2-ctl: "
    if command -v v4l2-ctl &> /dev/null; then
        echo -e "${GREEN}OK${NC}"
    else
        echo -e "${RED}MISSING${NC}"
        MISSING="$MISSING v4l-utils"
    fi
    
    if [ -n "$MISSING" ]; then
        echo ""
        echo "Missing packages:$MISSING"
        
        if [ "$AUTO_YES" = true ]; then
            REPLY="y"
        else
            read -p "Install missing packages? [Y/n] " -n 1 -r
            echo
        fi
        
        if [[ $REPLY =~ ^[Yy]$ ]] || [[ -z $REPLY ]]; then
            echo "Installing packages..."
            sudo apt update
            sudo apt install -y $MISSING
            echo -e "${GREEN}Dependencies installed${NC}"
        else
            echo -e "${YELLOW}Skipping - app may not work without dependencies${NC}"
        fi
    else
        echo -e "${GREEN}All dependencies present${NC}"
    fi
}

# Find and install the binary
install_binary() {
    echo ""
    echo "Installing $APP_NAME..."
    
    # Find the binary (check multiple locations)
    BINARY=""
    for loc in "$SCRIPT_DIR/$APP_NAME" "$SCRIPT_DIR/release/$APP_NAME"; do
        if [ -f "$loc" ] && [ -x "$loc" ]; then
            BINARY="$loc"
            break
        fi
    done
    
    if [ -z "$BINARY" ]; then
        echo -e "${RED}Error: $APP_NAME binary not found${NC}"
        echo ""
        echo "Expected locations:"
        echo "  $SCRIPT_DIR/$APP_NAME"
        echo "  $SCRIPT_DIR/release/$APP_NAME"
        echo ""
        echo "Build first with: make build"
        exit 1
    fi
    
    # Verify it's an ARM64 binary
    FILE_TYPE=$(file "$BINARY" 2>/dev/null || echo "unknown")
    if ! echo "$FILE_TYPE" | grep -q "aarch64\|ARM aarch64"; then
        echo -e "${YELLOW}Warning: Binary may not be ARM64${NC}"
        echo "  $FILE_TYPE"
    fi
    
    echo "  From: $BINARY"
    echo "  To:   $INSTALL_DIR/$APP_NAME"
    
    sudo cp "$BINARY" "$INSTALL_DIR/$APP_NAME"
    sudo chmod +x "$INSTALL_DIR/$APP_NAME"
    
    echo -e "${GREEN}Binary installed${NC}"
    
    # Show version
    echo ""
    "$INSTALL_DIR/$APP_NAME" -version 2>/dev/null || true
}

# Add user to video group if needed
setup_permissions() {
    echo ""
    echo -n "Checking camera permissions... "
    
    if groups $USER | grep -q "video"; then
        echo -e "${GREEN}OK${NC} (user in video group)"
    else
        echo -e "${YELLOW}ADDING${NC}"
        sudo usermod -a -G video $USER
        echo "  Added $USER to video group"
        echo "  NOTE: Log out and back in for this to take effect"
    fi
}

# Detect cameras
detect_cameras() {
    echo ""
    echo "Detecting cameras..."
    
    if command -v v4l2-ctl &> /dev/null; then
        USB_CAMS=$(v4l2-ctl --list-devices 2>/dev/null | grep -B1 "USB" | grep -A1 "USB" | grep "/dev/video" | head -5 || true)
        if [ -n "$USB_CAMS" ]; then
            echo -e "${GREEN}Found USB cameras:${NC}"
            echo "$USB_CAMS" | while read dev; do
                echo "  $dev"
            done
        else
            echo -e "${YELLOW}No USB cameras detected${NC}"
            echo "  Connect USB cameras before running the app"
        fi
    fi
}

# Main
main() {
    check_platform
    check_display
    install_dependencies
    install_binary
    setup_permissions
    detect_cameras
    
    echo ""
    echo "======================================"
    echo -e "${GREEN}  Installation Complete!${NC}"
    echo "======================================"
    echo ""
    echo "To run:"
    echo "  DISPLAY=:0 camera-dashboard"
    echo ""
}

main "$@"
