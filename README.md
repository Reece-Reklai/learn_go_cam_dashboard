# Camera Dashboard

A high-performance multi-camera monitoring system for Raspberry Pi, designed for vehicle use (rear camera, blind spot monitoring). Built with Go and the Fyne GUI framework.

## Features

- **Multi-Camera Support** - Up to 3 USB cameras in a 2x2 grid layout
- **Real-time Video** - 640x480 @ 15 FPS, optimized for vehicle monitoring
- **Touch Interface** - Tap for fullscreen, long-press to swap camera positions
- **Hot-plug Detection** - Cameras auto-detect when connected/disconnected
- **Low Power** - Optimized for battery-powered operation (~100% CPU for 2 cameras)
- **Single Binary** - No Python, no runtime dependencies

## Quick Start

### On a Raspberry Pi (64-bit OS)

```bash
# Download and extract
tar -xzvf camera-dashboard-*.tar.gz

# Install (installs dependencies + binary)
./install.sh

# Run
DISPLAY=:0 camera-dashboard
```

### Build from Source

```bash
# Install dependencies
sudo apt install ffmpeg v4l-utils

# Build
make build

# Run
make run
```

## Requirements

| Requirement | Details |
|-------------|---------|
| **OS** | Raspberry Pi OS (64-bit) or Ubuntu ARM64 |
| **Hardware** | Raspberry Pi 3/4/5 |
| **Display** | X11 desktop environment |
| **Cameras** | USB cameras with V4L2 support |
| **Dependencies** | ffmpeg, v4l-utils |

## Usage

| Action | Result |
|--------|--------|
| **Tap camera** | Fullscreen view |
| **Tap fullscreen** | Exit fullscreen |
| **Long-press camera** | Enter swap mode |
| **Tap another slot** | Swap positions |
| **Restart button** | Reinitialize cameras |
| **Exit button** | Clean shutdown |

## Configuration

Edit `internal/camera/config.go` to change settings:

```go
const (
    CameraWidth  = 640   // Resolution width
    CameraHeight = 480   // Resolution height
    CameraFPS    = 15    // Frames per second
    CameraFormat = "mjpeg" // or "yuyv"
)
```

Then rebuild: `make build`

## Makefile Targets

```bash
make build      # Development build
make release    # Optimized build
make package    # Create deployment tarball
make run        # Build and run
make status     # Show CPU/temp/memory
make clean      # Remove build artifacts
make help       # Show all targets
```

## Project Structure

```
.
├── main.go                 # Entry point, signal handling
├── internal/
│   ├── camera/
│   │   ├── config.go       # Resolution/FPS settings
│   │   ├── manager.go      # Camera lifecycle management
│   │   ├── capture.go      # FFmpeg capture, frame decoding
│   │   ├── framebuffer.go  # Lock-free frame buffer
│   │   └── device.go       # Camera discovery
│   ├── ui/
│   │   ├── app.go          # Fyne application, UI setup
│   │   ├── camera.go       # Camera widget
│   │   └── grid.go         # Grid layout
│   └── perf/
│       ├── adaptive.go     # Performance controller
│       └── monitor.go      # CPU/temperature monitoring
├── Makefile                # Build system
├── install.sh              # Deployment installer
└── LEARN.md                # Technical deep-dive
```

## Troubleshooting

### No cameras detected
```bash
v4l2-ctl --list-devices  # Check if cameras are visible
ls -la /dev/video*       # Check device files
sudo usermod -a -G video $USER  # Add user to video group
```

### High CPU usage
- Reduce FPS in `config.go` (try 10 FPS)
- Use MJPEG format (not YUYV)
- Check for zombie processes: `ps aux | awk '$8 == "Z"'`

### Display issues
```bash
echo $DISPLAY  # Should be :0
DISPLAY=:0 camera-dashboard  # Set explicitly
```

## License

MIT License - see LICENSE.MIT

## Learn More

See [LEARN.md](LEARN.md) for a comprehensive technical deep-dive into:
- Concurrency patterns (goroutines, channels, atomics)
- Lock-free data structures (FrameBuffer)
- FFmpeg integration and MJPEG streaming
- GUI architecture with Fyne
- Performance optimization techniques
