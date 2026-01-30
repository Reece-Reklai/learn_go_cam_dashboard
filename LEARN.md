# LEARN.md - Deep Dive into Camera Dashboard Architecture

This document explains the technical concepts, patterns, and decisions in this codebase. It's designed to teach you Go concurrency, systems programming, real-time video processing, and **how to think like a senior engineer**.

---

## Table of Contents

### Part 1: Technical Implementation
1. [Architecture Overview](#1-architecture-overview)
2. [Concurrency Model](#2-concurrency-model)
3. [The Producer-Consumer Pattern](#3-the-producer-consumer-pattern)
4. [Lock-Free Programming with Atomics](#4-lock-free-programming-with-atomics)
5. [The FrameBuffer: A Lock-Free Double Buffer](#5-the-framebuffer-a-lock-free-double-buffer)
6. [FFmpeg Integration](#6-ffmpeg-integration)
7. [MJPEG Stream Parsing](#7-mjpeg-stream-parsing)
8. [GUI Architecture with Fyne](#8-gui-architecture-with-fyne)
9. [Signal Handling and Graceful Shutdown](#9-signal-handling-and-graceful-shutdown)
10. [Zombie Process Prevention](#10-zombie-process-prevention)
11. [Hot-Plug Detection and Per-Camera Restart](#11-hot-plug-detection-and-per-camera-restart)
12. [Performance Optimization](#12-performance-optimization)
13. [Error Handling Patterns](#13-error-handling-patterns)
14. [Key Go Concepts Used](#14-key-go-concepts-used)

### Part 2: Debugging & Contributing
15. [Debugging This Project](#15-debugging-this-project)
16. [Contributing to This Project](#16-contributing-to-this-project)
17. [Transferable Patterns for Your Projects](#17-transferable-patterns-for-your-projects)
18. [System Programming Concepts](#18-system-programming-concepts)

### Part 3: Growing as a Senior Engineer
19. [The Senior Engineer Mindset](#19-the-senior-engineer-mindset)
20. [Problem-Solving Framework](#20-problem-solving-framework)
21. [Debugging Like a Detective](#21-debugging-like-a-detective)
22. [Reading Code Effectively](#22-reading-code-effectively)
23. [Making Technical Decisions](#23-making-technical-decisions)
24. [Learning From This Project's Mistakes](#24-learning-from-this-projects-mistakes)
25. [Building Your Engineering Toolbox](#25-building-your-engineering-toolbox)
26. [The Art of Problem Deconstruction](#26-the-art-of-problem-deconstruction)

---

## 1. Architecture Overview

### High-Level Data Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              CAMERA DASHBOARD                                │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌──────────┐    ┌──────────────┐    ┌─────────────┐    ┌──────────────┐  │
│  │  USB     │    │   FFmpeg     │    │  Capture    │    │   Frame      │  │
│  │  Camera  │───▶│  Process     │───▶│  Worker     │───▶│   Buffer     │  │
│  │ /dev/vid │    │  (MJPEG)     │    │  (Goroutine)│    │  (Lock-free) │  │
│  └──────────┘    └──────────────┘    └─────────────┘    └──────┬───────┘  │
│                                                                  │          │
│       ┌──────────────────────────────────────────────────────────┘          │
│       │                                                                     │
│       ▼                                                                     │
│  ┌─────────────┐    ┌──────────────┐    ┌─────────────────────────────┐   │
│  │  UI Refresh │    │  Fyne        │    │         Display             │   │
│  │  (30 FPS)   │───▶│  canvas.Image│───▶│  (800x480 touchscreen)     │   │
│  │  (Goroutine)│    │              │    │                             │   │
│  └─────────────┘    └──────────────┘    └─────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Component Responsibilities

| Component | File | Responsibility |
|-----------|------|----------------|
| **main** | `main.go` | Entry point, signal handling |
| **App** | `ui/app.go` | GUI setup, user interaction |
| **Manager** | `camera/manager.go` | Camera lifecycle, worker orchestration |
| **CaptureWorker** | `camera/capture.go` | FFmpeg control, frame decoding |
| **FrameBuffer** | `camera/framebuffer.go` | Lock-free frame storage |
| **SmartController** | `perf/adaptive.go` | Temperature/load monitoring |

---

## 2. Concurrency Model

### The Goroutine Pattern

Go uses **goroutines** - lightweight threads managed by the Go runtime. They're cheap to create (2KB stack) and the runtime multiplexes them onto OS threads.

```go
// Launch a goroutine - runs concurrently with main
go func() {
    // This runs in parallel
    for {
        doWork()
    }
}()
```

### Our Goroutine Structure

```
main goroutine
    │
    ├── UI Event Loop (Fyne's internal)
    │
    ├── Camera Refresh goroutine (startCameraRefresh)
    │       └── Polls FrameBuffers at 30 FPS
    │
    ├── Hotplug Detection goroutine (startHotplugDetection)
    │       └── Checks for camera connect/disconnect
    │
    ├── Performance Controller goroutine (controlLoop)
    │       └── Monitors temperature and load
    │
    └── Per-Camera Capture goroutines (one per camera)
            └── Reads from FFmpeg, writes to FrameBuffer
```

### Why Multiple Goroutines?

1. **Capture is blocking** - Reading from FFmpeg blocks until data arrives
2. **UI must stay responsive** - Can't block the main thread
3. **Independent rates** - Cameras run at 15 FPS, UI refreshes at 30 FPS
4. **Isolation** - One camera failing doesn't affect others

---

## 3. The Producer-Consumer Pattern

This is the most important pattern in the codebase. Multiple producers (capture workers) generate frames, and one consumer (UI) displays them.

### Classic Channel-Based Approach

```go
// Producer
frameCh := make(chan image.Image, 1)

go func() {
    for {
        frame := captureFrame()
        select {
        case frameCh <- frame:  // Try to send
        default:                // Channel full, drop frame
        }
    }
}()

// Consumer
go func() {
    for frame := range frameCh {
        display(frame)
    }
}()
```

**Problem**: Channels have overhead (locks, memory barriers) and can cause goroutine blocking.

### Our Lock-Free Approach

Instead of channels, we use a `FrameBuffer` with atomic operations:

```go
// Producer writes latest frame (never blocks)
buffer.Write(frame)

// Consumer reads latest frame (never blocks)
frame := buffer.Read()
```

This is faster and simpler for our use case where we only care about the **latest** frame.

---

## 4. Lock-Free Programming with Atomics

### What Are Atomics?

Atomic operations are **indivisible** - they complete entirely or not at all, with no intermediate states visible to other goroutines.

```go
import "sync/atomic"

var counter atomic.Int64

// These are thread-safe without locks
counter.Add(1)
counter.Store(42)
value := counter.Load()
```

### Why Atomics Over Mutexes?

| Aspect | Mutex | Atomic |
|--------|-------|--------|
| **Speed** | Slower (syscalls) | Faster (CPU instructions) |
| **Blocking** | Can block | Never blocks |
| **Deadlocks** | Possible | Impossible |
| **Complexity** | More code | Less code |
| **Use case** | Complex operations | Simple read/write |

### Atomics in Our Code

```go
// capture.go
type CaptureWorker struct {
    running atomic.Bool     // Is the worker active?
    targetFPS atomic.Int32  // Current FPS setting
    frameCount atomic.Uint64 // Statistics counter
    // ...
}

// Safe to call from any goroutine
func (cw *CaptureWorker) Stop() {
    cw.running.Store(false)  // Atomic write
}

func (cw *CaptureWorker) captureLoop() {
    for cw.running.Load() {  // Atomic read
        // Continue capturing
    }
}
```

---

## 5. The FrameBuffer: A Lock-Free Double Buffer

This is the most sophisticated concurrent data structure in the codebase.

### The Problem

- **Producer** (capture) runs at 15 FPS, writing frames
- **Consumer** (UI) runs at 30 FPS, reading frames
- They run on different goroutines
- We can't have them accessing the same memory simultaneously

### The Solution: Double Buffering

```
┌─────────────────────────────────────────────────────────────┐
│                     FrameBuffer                              │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│   frames[0]  ◄─── writeIndex points here (being written)    │
│   ┌─────────────────────┐                                   │
│   │  [Camera Frame A]   │                                   │
│   └─────────────────────┘                                   │
│                                                              │
│   frames[1]  ◄─── readIndex points here (being read)        │
│   ┌─────────────────────┐                                   │
│   │  [Camera Frame B]   │                                   │
│   └─────────────────────┘                                   │
│                                                              │
│   After write completes, indices swap atomically            │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

### The Code Explained

```go
type FrameBuffer struct {
    frames     [2]image.Image    // Two slots for double buffering
    writeIndex atomic.Int32      // Which slot to write to
    readIndex  atomic.Int32      // Which slot to read from
    frameCount atomic.Uint64     // Total frames captured
    // ...
}

// Producer calls this
func (fb *FrameBuffer) Write(frame image.Image) {
    // Step 1: Get current write slot
    writeIdx := fb.writeIndex.Load()
    
    // Step 2: Write the frame
    fb.frames[writeIdx] = frame
    
    // Step 3: Atomic swap - make this frame available for reading
    fb.writeIndex.Store(1 - writeIdx)  // 0→1 or 1→0
    fb.readIndex.Store(writeIdx)       // Reader now sees this frame
    
    fb.frameCount.Add(1)
}

// Consumer calls this
func (fb *FrameBuffer) Read() image.Image {
    readIdx := fb.readIndex.Load()
    return fb.frames[readIdx]
}
```

### Why This Works

1. **Writer and reader access different slots** - No race condition
2. **Swap is atomic** - Reader never sees half-written frame
3. **Non-blocking** - Neither side ever waits
4. **No locks** - Maximum performance

### The ReadIfNew Optimization

```go
func (fb *FrameBuffer) ReadIfNew(lastRead uint64) (image.Image, uint64, bool) {
    currentCount := fb.frameCount.Load()
    if currentCount <= lastRead {
        return nil, lastRead, false  // No new frame
    }
    return fb.frames[fb.readIndex.Load()], currentCount, true
}
```

This prevents unnecessary UI refreshes when no new frame has arrived.

---

## 6. FFmpeg Integration

### Why FFmpeg?

- **Hardware support** - Uses V4L2 for direct camera access
- **Format handling** - Decodes MJPEG/YUYV automatically
- **Stability** - Battle-tested, handles edge cases

### Process Architecture

```
┌─────────────┐    pipe     ┌─────────────┐
│ Go Program  │◄───────────│   FFmpeg    │
│ (parent)    │   stdout   │  (child)    │
└─────────────┘             └─────────────┘
       │                           │
       │ exec.Command()            │ reads from
       └───────────────────────────┘ /dev/video0
```

### Starting FFmpeg

```go
func (cw *CaptureWorker) tryFFmpegCapture(args []string) bool {
    // Create the command
    cw.ffmpegCmd = exec.Command("ffmpeg", args...)
    
    // Get stdout pipe to read frames
    stdout, err := cw.ffmpegCmd.StdoutPipe()
    if err != nil {
        return false
    }
    
    // Start the process
    if err := cw.ffmpegCmd.Start(); err != nil {
        return false
    }
    
    // Read frames in a loop
    for cw.running.Load() {
        jpegData, err := cw.readMJPEGFrameRaw(stdout, ...)
        if err != nil {
            break
        }
        // Process frame...
    }
    
    return true
}
```

### FFmpeg Arguments Explained

```bash
ffmpeg \
    -thread_queue_size 512 \    # Buffer for input
    -probesize 32 \             # Minimal probing (faster start)
    -analyzeduration 0 \        # Skip stream analysis
    -f v4l2 \                   # Linux video input format
    -input_format mjpeg \       # Request MJPEG from camera
    -video_size 640x480 \       # Resolution
    -framerate 15 \             # FPS (camera may ignore this)
    -i /dev/video0 \            # Input device
    -f image2pipe \             # Output as raw images
    -vcodec mjpeg \             # Output MJPEG frames
    -q:v 5 \                    # Quality (1-31, lower=better)
    -                           # Output to stdout
```

---

## 7. MJPEG Stream Parsing

### What is MJPEG?

MJPEG (Motion JPEG) is a video format where each frame is a separate JPEG image. It's simple and widely supported by USB cameras.

### JPEG Markers

Every JPEG image has these markers:
- **SOI** (Start of Image): `0xFF 0xD8`
- **EOI** (End of Image): `0xFF 0xD9`

### Parsing Algorithm

```
Stream:  ... garbage ... [0xFF 0xD8] ... JPEG DATA ... [0xFF 0xD9] ... next frame ...
                              ▲                             ▲
                              │                             │
                            SOI                           EOI
                          (start)                        (end)
```

### The Code

```go
func (cw *CaptureWorker) readMJPEGFrameRaw(reader io.Reader, ...) ([]byte, error) {
    // Step 1: Find SOI marker (0xFF 0xD8)
    foundSOI := false
    for !foundSOI {
        n, err := reader.Read(buffer)
        if err != nil { return nil, err }
        
        frameData = append(frameData, buffer[:n]...)
        
        // Search for SOI
        for i := 0; i < len(frameData)-1; i++ {
            if frameData[i] == 0xFF && frameData[i+1] == 0xD8 {
                frameData = frameData[i:]  // Discard garbage before SOI
                foundSOI = true
                break
            }
        }
    }
    
    // Step 2: Find EOI marker (0xFF 0xD9)
    for {
        for i := 1; i < len(frameData); i++ {
            if frameData[i-1] == 0xFF && frameData[i] == 0xD9 {
                // Found complete JPEG!
                return frameData[:i+1], nil
            }
        }
        
        // Need more data
        n, err := reader.Read(buffer)
        if err != nil { return nil, err }
        frameData = append(frameData, buffer[:n]...)
    }
}
```

### Time-Based Frame Skipping

Cameras often ignore the FPS request and send at their max rate (30 FPS). We skip frames to achieve our target:

```go
// Target: 15 FPS = 66.7ms between frames
minFrameInterval := time.Second / time.Duration(targetFPS)
lastProcessedTime := time.Now()

for {
    jpegData := readFrame()
    
    // Check if enough time has passed
    elapsed := time.Since(lastProcessedTime)
    if elapsed < minFrameInterval {
        // Skip this frame - too soon
        continue
    }
    lastProcessedTime = time.Now()
    
    // Process this frame
    processFrame(jpegData)
}
```

---

## 8. GUI Architecture with Fyne

### What is Fyne?

Fyne is a Go-native GUI toolkit that uses OpenGL for rendering. It's cross-platform and designed for touch interfaces.

### Key Concepts

```go
// App and Window
fyneApp := app.New()
window := fyneApp.NewWindow("Camera Dashboard")

// Widgets are UI elements
button := widget.NewButton("Click Me", func() {
    // Handle click
})

// Containers arrange widgets
grid := container.NewGridWithColumns(2, widget1, widget2)

// canvas.Image displays images
img := canvas.NewImageFromImage(myImage)
img.FillMode = canvas.ImageFillStretch

// Setting content and running
window.SetContent(grid)
fyneApp.Run()  // Blocks until window closes
```

### Custom Widgets

We create custom widgets by embedding `widget.BaseWidget`:

```go
type TappableImage struct {
    widget.BaseWidget        // Embed base widget
    image     *canvas.Image
    onTap     func()
    onLongTap func()
}

// Must implement CreateRenderer
func (t *TappableImage) CreateRenderer() fyne.WidgetRenderer {
    c := container.NewStack(t.image)
    return widget.NewSimpleRenderer(c)
}

// Handle taps
func (t *TappableImage) Tapped(_ *fyne.PointEvent) {
    if t.onTap != nil {
        t.onTap()
    }
}
```

### Long Press Detection

```go
func (t *TappableImage) MouseDown(ev *desktop.MouseEvent) {
    t.pressStart = time.Now()
    
    // Start a timer for long press
    t.longPressTimer = time.AfterFunc(500*time.Millisecond, func() {
        t.longPressFired = true
        if t.onLongTap != nil {
            t.onLongTap()
        }
    })
}

func (t *TappableImage) MouseUp(ev *desktop.MouseEvent) {
    t.longPressTimer.Stop()
    
    if !t.longPressFired {
        // Was a short tap, not long press
        if t.onTap != nil {
            t.onTap()
        }
    }
}
```

### UI Update Pattern

```go
func (a *App) startCameraRefresh() {
    go func() {
        ticker := time.NewTicker(33 * time.Millisecond)  // 30 FPS
        defer ticker.Stop()
        
        for range ticker.C {
            for camIndex := 0; camIndex < 3; camIndex++ {
                buffer := a.manager.GetFrameBuffer(cameraID)
                
                // Only update if new frame available
                frame, _, hasNew := buffer.ReadIfNew(a.lastFrameRead[camIndex])
                if !hasNew {
                    continue
                }
                
                // Update UI (thread-safe in Fyne)
                a.cameraImages[camIndex].Image = frame
                a.cameraImages[camIndex].Refresh()
            }
        }
    }()
}
```

---

## 9. Signal Handling and Graceful Shutdown

### The Problem

When the user presses Ctrl+C or the system sends SIGTERM, we need to:
1. Stop all capture workers
2. Kill FFmpeg processes
3. Clean up resources
4. Exit cleanly

### Signal Handling

```go
func main() {
    app := ui.NewApp()
    
    // Create channel to receive signals
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    
    // Handle signals in a goroutine
    go func() {
        sig := <-sigCh  // Block until signal received
        log.Printf("Received signal %v, cleaning up...", sig)
        app.Cleanup()
        os.Exit(0)
    }()
    
    app.Start()  // Run the app
}
```

### Cleanup Order

```go
func (a *App) Cleanup() {
    // 1. Stop hotplug detection
    close(a.hotplugStopCh)
    
    // 2. Stop camera manager (stops workers)
    if a.manager != nil {
        a.manager.Stop()
    }
    
    // 3. Kill any remaining FFmpeg processes
    killFFmpegProcesses()
}

func killFFmpegProcesses() {
    exec.Command("pkill", "-9", "-f", "ffmpeg").Run()
}
```

---

## 10. Zombie Process Prevention

### What is a Zombie Process?

When a child process (FFmpeg) exits, it becomes a **zombie** until the parent calls `Wait()` to collect its exit status.

```
BEFORE Wait():
  PID   STATE
  1234  Z (zombie)  <- FFmpeg exited but not reaped

AFTER Wait():
  Process entry removed from system
```

### The Problem We Had

FFmpeg processes were becoming zombies because we killed them but didn't wait:

```go
// BAD: Creates zombies
cw.ffmpegCmd.Process.Kill()
// Zombie remains!
```

### The Solution

Always call `Wait()` after killing:

```go
// GOOD: No zombies
defer func() {
    if cw.ffmpegCmd != nil && cw.ffmpegCmd.Process != nil {
        cw.ffmpegCmd.Process.Kill()
        cw.ffmpegCmd.Wait()  // Reap the zombie
    }
}()
```

### Checking for Zombies

```bash
# List zombie processes
ps aux | awk '$8 == "Z" {print}'

# Count zombies
ps aux | awk '$8 == "Z" {count++} END {print count ? count : 0}'
```

---

## 11. Hot-Plug Detection and Per-Camera Restart

### The Problem

USB cameras can disconnect unexpectedly (loose cable, USB hub issues, power glitches). The system needs to:
1. Detect when a camera disconnects
2. Show a placeholder (test pattern) for the disconnected camera
3. Automatically restart the camera when it reconnects
4. **Not disrupt other cameras** during this process

### Initial Approach (Problematic)

Our first implementation restarted ALL cameras when one disconnected:

```go
// BAD: Disrupts all cameras
func handleCameraReconnect(camIndex int) {
    a.manager.Stop()          // Stops ALL cameras!
    a.manager = NewManager()  // Recreates everything
    a.manager.Initialize()    // Re-discovers all cameras
    a.manager.Start()         // Restarts all cameras
}
```

This caused:
- Brief interruption on ALL camera feeds (even healthy ones)
- USB bandwidth contention when restarting multiple cameras
- Potential race conditions with rapid reconnects

### The Solution: Per-Camera Restart

We added methods to restart individual cameras without affecting others:

```go
// capture.go - Worker can restart itself
func (cw *CaptureWorker) Restart() error {
    cw.Stop()                           // Stop this worker only
    cw.stopCh = make(chan struct{})     // Fresh stop channel
    return cw.Start()                   // Start new capture loop
}

// manager.go - Manager can restart specific camera
func (m *Manager) RestartCameraByIndex(index int) error {
    worker := m.workers[index]
    return worker.Restart()  // Only affects this camera
}
```

### Proper Goroutine Cleanup with WaitGroup

The tricky part is ensuring the old capture goroutine fully exits before starting a new one. We use a `sync.WaitGroup`:

```go
type CaptureWorker struct {
    wg sync.WaitGroup  // Tracks capture goroutine
    // ...
}

func (cw *CaptureWorker) Start() error {
    cw.wg.Add(1)
    go func() {
        defer cw.wg.Done()
        cw.captureLoop()
    }()
    return nil
}

func (cw *CaptureWorker) Stop() {
    cw.running.Store(false)
    close(cw.stopCh)
    
    // Kill FFmpeg to unblock reads
    if cw.ffmpegCmd != nil {
        cw.ffmpegCmd.Process.Kill()
        cw.ffmpegCmd.Wait()
    }
    
    // Wait for goroutine to exit (with timeout)
    done := make(chan struct{})
    go func() {
        cw.wg.Wait()
        close(done)
    }()
    
    select {
    case <-done:
        // Clean exit
    case <-time.After(2 * time.Second):
        log.Printf("Warning: goroutine did not exit in time")
    }
}
```

### Why WaitGroup is Critical

Without proper waiting, you get **two goroutines fighting** for the same camera:

```
Time     Old Goroutine              New Goroutine
─────────────────────────────────────────────────
T0       captureLoop() running
T1       Stop() called
T2       running=false, still in    
         retry loop (10s ticker)
T3                                  Start() called
T4       tries FFmpeg...            tries FFmpeg...
                                    ↑ CONFLICT!
```

With WaitGroup:

```
Time     Old Goroutine              New Goroutine
─────────────────────────────────────────────────
T0       captureLoop() running
T1       Stop() called
T2       running=false, exits
T3       wg.Done() called
T4       Stop() returns             
T5                                  Start() called
                                    ↑ Safe!
```

### Debouncing Reconnection Events

USB re-enumeration can cause rapid disconnect/reconnect events. We debounce to prevent multiple restart attempts:

```go
type App struct {
    lastDisconnectTime [3]time.Time  // Per-camera tracking
    // ...
}

func (a *App) handleCameraReconnect(camIndex int) {
    // Ignore reconnects within 3 seconds of disconnect
    timeSinceDisconnect := time.Since(a.lastDisconnectTime[camIndex])
    if timeSinceDisconnect < 3*time.Second {
        log.Printf("Ignoring reconnect (%.1fs since disconnect)", 
            timeSinceDisconnect.Seconds())
        return
    }
    
    // Proceed with restart...
}
```

### Avoiding v4l2-ctl Conflicts

Originally, we used `v4l2-ctl --info` to check if a device was a USB camera. But this conflicts with active FFmpeg capture:

```go
// BAD: Conflicts with FFmpeg
func isUSBCaptureDevice(devPath string) bool {
    cmd := exec.Command("v4l2-ctl", "--device="+devPath, "--info")
    output, _ := cmd.Output()  // May block or fail
    return strings.Contains(string(output), "USB")
}
```

We switched to reading sysfs directly (no device locking):

```go
// GOOD: No conflicts
func isUSBCaptureDevice(devPath string) bool {
    var videoNum int
    fmt.Sscanf(devPath, "/dev/video%d", &videoNum)
    
    // Read from sysfs (always available, no locking)
    sysfsPath := fmt.Sprintf("/sys/class/video4linux/video%d/device/modalias", videoNum)
    data, err := os.ReadFile(sysfsPath)
    if err != nil {
        return false
    }
    
    // USB devices have modalias starting with "usb:"
    return strings.HasPrefix(string(data), "usb:")
}
```

### Hot-Plug Architecture

```
                    ┌─────────────────────────────────────────────────────────┐
                    │                  Hot-Plug Detection                      │
                    │                 (polls every 2 seconds)                  │
                    └──────────────────────────┬──────────────────────────────┘
                                               │
                                               ▼
                    ┌──────────────────────────────────────────────────────────┐
                    │              Check each camera's device file              │
                    │                                                           │
                    │   for i, cam := range cameras:                           │
                    │       exists := os.Stat(cam.DevicePath) == nil           │
                    │       if wasConnected && !exists: DISCONNECT             │
                    │       if !wasConnected && exists: RECONNECT              │
                    └──────────────────────────────────────────────────────────┘
                                               │
                          ┌────────────────────┴────────────────────┐
                          │                                         │
                          ▼                                         ▼
               ┌──────────────────────┐              ┌─────────────────────────┐
               │     DISCONNECT       │              │       RECONNECT         │
               │                      │              │                         │
               │  - Record timestamp  │              │  - Check debounce (3s)  │
               │  - Update UI status  │              │  - Per-camera restart   │
               │  - Show test pattern │              │  - Update UI status     │
               └──────────────────────┘              └─────────────────────────┘
                          │                                         │
                          │                                         ▼
                          │                          ┌─────────────────────────┐
                          │                          │  RestartCameraByIndex() │
                          │                          │                         │
                          │                          │  1. Stop worker (wait)  │
                          │                          │  2. Reset stopCh        │
                          │                          │  3. Start new goroutine │
                          │                          │                         │
                          │                          │  Other cameras:         │
                          │                          │  UNAFFECTED             │
                          │                          └─────────────────────────┘
                          │                                         │
                          └────────────────────┬────────────────────┘
                                               │
                                               ▼
                    ┌──────────────────────────────────────────────────────────┐
                    │                    Camera Grid UI                         │
                    │                                                           │
                    │  ┌──────────┐ ┌──────────┐     Connected cameras show    │
                    │  │ Camera 0 │ │ Camera 1 │     live video feed            │
                    │  │ (live)   │ │(restart) │                                │
                    │  └──────────┘ └──────────┘     Disconnected cameras show  │
                    │  ┌──────────┐ ┌──────────┐     test pattern + status      │
                    │  │ Settings │ │ Camera 2 │                                │
                    │  │          │ │ (disc.)  │                                │
                    │  └──────────┘ └──────────┘                                │
                    └──────────────────────────────────────────────────────────┘
```

### Key Takeaways

1. **Per-camera isolation** - One camera's issues don't affect others
2. **WaitGroup for cleanup** - Ensures old goroutines fully exit before starting new ones
3. **Debouncing** - Prevents rapid restart cycles during USB re-enumeration
4. **Sysfs over v4l2-ctl** - Avoids device locking conflicts
5. **Timeout on wait** - Prevents indefinite hangs if goroutine is stuck

---

## 12. Performance Optimization

### CPU Optimization

1. **Frame Skipping** - Don't decode every frame
   ```go
   if elapsed < minFrameInterval {
       continue  // Skip decoding
   }
   ```

2. **No RGBA Conversion** - Fyne accepts any `image.Image`
   ```go
   // BAD: Wastes CPU
   func decodeJPEG(data []byte) *image.RGBA {
       img, _ := jpeg.Decode(bytes.NewReader(data))
       rgba := image.NewRGBA(img.Bounds())
       draw.Draw(rgba, rgba.Bounds(), img, image.Point{}, draw.Src)
       return rgba
   }
   
   // GOOD: Direct return
   func decodeJPEG(data []byte) image.Image {
       img, _ := jpeg.Decode(bytes.NewReader(data))
       return img
   }
   ```

3. **Buffer Reuse** - Pre-allocate and reuse buffers
   ```go
   readBuffer := make([]byte, 8192)      // Reuse across reads
   frameData := make([]byte, 0, 65536)   // Pre-allocated capacity
   ```

### Memory Optimization

1. **Lock-Free FrameBuffer** - Only 2 frames stored per camera
2. **No channel buffers** - Channels can accumulate frames
3. **Efficient slice operations**
   ```go
   // Reset slice but keep capacity
   frameData = frameData[:0]
   
   // Instead of creating new slice
   frameData = []byte{}  // Loses capacity
   ```

### USB Bandwidth

```
USB 2.0 Limit: ~35 MB/s (practical)

MJPEG @ 640x480:
  ~30 KB per frame × 15 FPS × 3 cameras = 1.3 MB/s ✓

YUYV @ 640x480:
  640×480×2 = 614 KB per frame × 15 FPS × 3 cameras = 27 MB/s ⚠️
```

Always use MJPEG format when available!

---

## 13. Error Handling Patterns

### Recover from Panics

```go
func (a *App) initializeCamerasAsync() {
    defer func() {
        if r := recover(); r != nil {
            log.Printf("PANIC in camera init: %v", r)
        }
    }()
    
    // Risky code...
}
```

### Safe Channel Send

```go
func (cw *CaptureWorker) safeSendFrame(frame image.Image) {
    defer func() {
        if r := recover(); r != nil {
            // Channel was closed
            cw.running.Store(false)
        }
    }()
    
    select {
    case cw.frameCh <- frame:
    default:
        // Channel full, drop frame
    }
}
```

### Timeout Protection

```go
func (cw *CaptureWorker) readMJPEGFrameRaw(...) ([]byte, error) {
    frameTimeout := 150 * time.Millisecond
    frameStart := time.Now()
    
    for {
        if time.Since(frameStart) > frameTimeout {
            return nil, fmt.Errorf("timeout finding SOI marker")
        }
        // Read data...
    }
}
```

---

## 14. Key Go Concepts Used

### Interfaces

```go
// Highlightable - any widget that can be highlighted
type Highlightable interface {
    SetHighlight(on bool)
}

// Both TappableImage and TappableSettings implement this
type TappableImage struct { ... }
func (t *TappableImage) SetHighlight(on bool) { ... }

type TappableSettings struct { ... }
func (t *TappableSettings) SetHighlight(on bool) { ... }

// Can use either type where Highlightable is expected
var widget Highlightable = cam1  // or settingsWidget
widget.SetHighlight(true)
```

### Embedding

```go
type TappableImage struct {
    widget.BaseWidget  // Embedded - TappableImage "is a" BaseWidget
    image *canvas.Image
}

// Can call BaseWidget methods directly
t.ExtendBaseWidget(t)  // From embedded type
```

### Defer

```go
func doWork() {
    file := openFile()
    defer file.Close()  // Runs when function returns
    
    mutex.Lock()
    defer mutex.Unlock()  // Always unlocks, even on panic
    
    // Work...
}
```

### Select

```go
select {
case frame := <-frameCh:
    display(frame)
case <-stopCh:
    return  // Stop requested
case <-time.After(5 * time.Second):
    log.Println("Timeout!")
default:
    // Non-blocking - runs if nothing else ready
}
```

### Goroutine Lifecycle

```go
type Worker struct {
    running atomic.Bool
    stopCh  chan struct{}
}

func (w *Worker) Start() {
    w.running.Store(true)
    w.stopCh = make(chan struct{})
    go w.loop()
}

func (w *Worker) Stop() {
    w.running.Store(false)
    close(w.stopCh)  // Unblocks any select waiting on stopCh
}

func (w *Worker) loop() {
    for w.running.Load() {
        select {
        case <-w.stopCh:
            return
        default:
            doWork()
        }
    }
}
```

---

## 15. Debugging This Project

### Essential Debugging Commands

```bash
# Run with full logging
DISPLAY=:0 ./camera-dashboard 2>&1 | tee /tmp/camera.log

# Monitor logs in real-time
tail -f /tmp/camera.log

# Filter for specific components
tail -f /tmp/camera.log | grep -E "Hotplug|Capture|Manager"

# Check system status
make status

# Check for zombie processes
ps aux | awk '$8 == "Z" {print}'

# Monitor USB events (camera connect/disconnect)
dmesg -w | grep -i "usb\|video"

# Check camera devices
v4l2-ctl --list-devices
ls -la /dev/video*

# Check what's using a camera
fuser /dev/video0

# Kill stuck processes
pkill -9 -f camera-dashboard; pkill -9 -f ffmpeg
```

### Reading the Logs

The log format tells you which component is speaking:

```
[Component] Camera ID: Message

Examples:
[Manager] Creating worker for camera video0     <- Manager lifecycle
[Capture] Camera video0: FFmpeg started         <- FFmpeg process control
[Hotplug] Camera 1 (/dev/video2) disconnected   <- Hot-plug detection
[UI] Camera video0: frame #91, buffer stats...  <- UI frame updates
[SmartCtrl] Vehicle mode | FPS: 15 | Temp: 80°C <- Performance monitoring
```

### Common Issues and Solutions

#### Issue: "No cameras detected"
```bash
# Check if cameras are visible to the system
v4l2-ctl --list-devices

# Check permissions
ls -la /dev/video*
groups $USER  # Should include 'video'

# Fix permissions
sudo usermod -a -G video $USER
# Then log out and back in
```

#### Issue: "FFmpeg stream ended" repeatedly
```bash
# Check kernel logs for USB errors
dmesg | tail -30 | grep -i "usb\|error"

# Common causes:
# - USB bandwidth exceeded (too many cameras)
# - Loose cable
# - Camera overheating
# - Power delivery issues (use powered hub)
```

#### Issue: High CPU usage
```bash
# Check what's consuming CPU
top -p $(pgrep -d, -f "camera-dashboard|ffmpeg")

# Common causes:
# - YUYV format instead of MJPEG (10x more CPU)
# - Too high FPS
# - RGBA conversion (we removed this)

# Fix: Edit config.go
CameraFormat = "mjpeg"  # Not "yuyv"
CameraFPS = 15          # Not 30
```

#### Issue: Memory growing over time
```bash
# Monitor memory
watch -n 1 'ps aux | grep camera-dashboard | grep -v grep'

# Check for goroutine leaks
# Add this to main.go temporarily:
go func() {
    for {
        time.Sleep(10 * time.Second)
        log.Printf("Goroutines: %d", runtime.NumGoroutine())
    }
}()
```

### Debugging Concurrency Issues

#### Finding Race Conditions
```bash
# Build with race detector (slow, but catches races)
go build -race -o camera-dashboard-race .
DISPLAY=:0 ./camera-dashboard-race

# Output shows exact line numbers of races
```

#### Debugging Deadlocks
```bash
# If app hangs, send SIGQUIT to dump goroutine stacks
kill -QUIT $(pgrep camera-dashboard)

# Or add this to main.go:
import _ "net/http/pprof"
go func() {
    log.Println(http.ListenAndServe("localhost:6060", nil))
}()

# Then visit: http://localhost:6060/debug/pprof/goroutine?debug=2
```

#### Adding Debug Logging

```go
// Temporary debug logging pattern
func (cw *CaptureWorker) debugCapture() {
    log.Printf("[DEBUG] %s: running=%v, ffmpeg=%v, buffer=%v",
        cw.camera.DeviceID,
        cw.running.Load(),
        cw.ffmpegCmd != nil,
        cw.frameBuffer != nil)
}
```

### Using Delve Debugger

```bash
# Install delve
go install github.com/go-delve/delve/cmd/dlv@latest

# Debug (headless for remote)
dlv debug . -- 

# Set breakpoints
(dlv) break internal/camera/capture.go:340
(dlv) continue
(dlv) print cw.camera.DeviceID
(dlv) goroutines  # List all goroutines
(dlv) goroutine 5 # Switch to goroutine 5
```

---

## 16. Contributing to This Project

### Project Structure Deep Dive

```
camera_dashboard_go_version/
├── main.go                     # Entry point - START HERE
│   └── Sets up signal handling, creates App, runs
│
├── internal/                   # Private packages (can't be imported externally)
│   │
│   ├── camera/                 # Camera subsystem
│   │   ├── config.go           # ← EDIT THIS for resolution/FPS
│   │   ├── device.go           # Camera discovery (v4l2)
│   │   ├── manager.go          # Orchestrates multiple cameras
│   │   ├── capture.go          # FFmpeg control, frame decoding (MOST COMPLEX)
│   │   ├── framebuffer.go      # Lock-free double buffer
│   │   ├── real_capture.go     # Real camera capture helpers
│   │   └── simple_capture.go   # Simplified capture mode
│   │
│   ├── ui/                     # User interface
│   │   ├── app.go              # Main application (HOT-PLUG LOGIC HERE)
│   │   ├── camera.go           # TappableImage widget
│   │   └── grid.go             # Grid layout helpers
│   │
│   └── perf/                   # Performance monitoring
│       ├── adaptive.go         # SmartController (temp/load monitoring)
│       └── monitor.go          # CPU/memory/temp reading
│
├── Makefile                    # Build system
├── install.sh                  # Deployment script
├── README.md                   # User documentation
└── LEARN.md                    # This file
```

### How to Add a Feature

#### Example: Add a "Screenshot" button

1. **Understand the data flow**
   ```
   User taps button → UI handler → Get frame from buffer → Save to file
   ```

2. **Find where to add UI** (`internal/ui/app.go`)
   ```go
   // In createSettingsPanel() or similar
   screenshotBtn := widget.NewButton("Screenshot", func() {
       a.takeScreenshot()
   })
   ```

3. **Implement the logic**
   ```go
   func (a *App) takeScreenshot() {
       for i, cam := range a.cameras {
           buffer := a.manager.GetFrameBuffer(cam.DeviceID)
           if buffer == nil {
               continue
           }
           
           frame := buffer.Read()
           if frame == nil {
               continue
           }
           
           filename := fmt.Sprintf("screenshot_%s_%d.jpg", 
               time.Now().Format("20060102_150405"), i)
           
           file, err := os.Create(filename)
           if err != nil {
               log.Printf("Failed to create %s: %v", filename, err)
               continue
           }
           
           jpeg.Encode(file, frame, &jpeg.Options{Quality: 90})
           file.Close()
           log.Printf("Saved screenshot: %s", filename)
       }
   }
   ```

4. **Test thoroughly**
   ```bash
   make build && make run
   # Test the button
   # Check logs for errors
   ```

### How to Fix a Bug

1. **Reproduce the bug**
   ```bash
   # Run with logging
   DISPLAY=:0 ./camera-dashboard 2>&1 | tee /tmp/camera.log
   # Trigger the bug
   # Check logs
   ```

2. **Find the relevant code**
   ```bash
   # Search for related terms
   grep -r "disconnect" internal/
   grep -r "FFmpeg" internal/
   ```

3. **Add debug logging**
   ```go
   log.Printf("[DEBUG] Before: %v", someValue)
   // ... code ...
   log.Printf("[DEBUG] After: %v", someValue)
   ```

4. **Fix and test**
   ```bash
   make build && make run
   # Verify fix
   # Check for regressions
   ```

5. **Clean up debug logging**

### Code Style Guidelines

```go
// 1. Log messages use [Component] prefix
log.Printf("[Capture] Camera %s: Starting FFmpeg", cam.DeviceID)

// 2. Errors are returned, not logged and ignored
if err != nil {
    return fmt.Errorf("failed to start camera: %w", err)
}

// 3. Use atomic operations for cross-goroutine state
running atomic.Bool  // Not: running bool with mutex

// 4. Defer cleanup immediately after resource acquisition
file, err := os.Open(path)
if err != nil {
    return err
}
defer file.Close()  // Immediately after successful open

// 5. Check running state in loops
for cw.running.Load() {  // Not: for { if !running { break } }
    // work
}

// 6. Use select with stopCh for interruptible waits
select {
case <-time.After(1 * time.Second):
    // timeout
case <-cw.stopCh:
    return  // Stop requested
}
```

### Testing Changes

```bash
# Build and run
make build && make run

# Check status while running
make status

# Run with race detector (catches concurrency bugs)
go build -race -o camera-dashboard-race .
DISPLAY=:0 ./camera-dashboard-race

# Check for compilation errors without building
go vet ./...

# Format code
go fmt ./...
```

### Commit Guidelines

```bash
# Good commit messages
git commit -m "Fix hot-plug: per-camera restart without disrupting other cameras"
git commit -m "Add screenshot button to settings panel"
git commit -m "Reduce CPU usage by skipping RGBA conversion"

# Bad commit messages
git commit -m "fix bug"
git commit -m "update"
git commit -m "wip"
```

---

## 17. Transferable Patterns for Your Projects

These patterns from this codebase can be applied to many other projects.

### Pattern 1: Worker Pool with Graceful Shutdown

**Use when:** You have multiple concurrent tasks that need clean shutdown.

```go
type Worker struct {
    id      int
    running atomic.Bool
    stopCh  chan struct{}
    wg      sync.WaitGroup
}

func (w *Worker) Start() {
    w.running.Store(true)
    w.stopCh = make(chan struct{})
    w.wg.Add(1)
    go func() {
        defer w.wg.Done()
        w.run()
    }()
}

func (w *Worker) Stop() {
    w.running.Store(false)
    close(w.stopCh)
    w.wg.Wait()  // Block until goroutine exits
}

func (w *Worker) run() {
    for w.running.Load() {
        select {
        case <-w.stopCh:
            return
        default:
            w.doWork()
        }
    }
}

// Manager controls multiple workers
type Manager struct {
    workers []*Worker
}

func (m *Manager) Start(n int) {
    m.workers = make([]*Worker, n)
    for i := 0; i < n; i++ {
        m.workers[i] = &Worker{id: i}
        m.workers[i].Start()
    }
}

func (m *Manager) Stop() {
    for _, w := range m.workers {
        w.Stop()
    }
}
```

**Apply to:**
- Web scrapers with multiple concurrent requests
- File processors handling multiple files
- API clients with parallel requests
- Any producer-consumer system

### Pattern 2: Lock-Free Latest-Value Buffer

**Use when:** Producer is faster than consumer, and you only care about the latest value.

```go
type LatestValue[T any] struct {
    values     [2]T
    writeIndex atomic.Int32
    readIndex  atomic.Int32
}

func (lv *LatestValue[T]) Write(value T) {
    writeIdx := lv.writeIndex.Load()
    lv.values[writeIdx] = value
    lv.writeIndex.Store(1 - writeIdx)
    lv.readIndex.Store(writeIdx)
}

func (lv *LatestValue[T]) Read() T {
    return lv.values[lv.readIndex.Load()]
}
```

**Apply to:**
- Sensor readings (temperature, GPS, etc.)
- Stock price tickers
- Game state updates
- Any "latest value wins" scenario

### Pattern 3: Debounced Event Handler

**Use when:** Events come in bursts but you only want to act once per burst.

```go
type Debouncer struct {
    delay     time.Duration
    lastEvent time.Time
    mu        sync.Mutex
}

func NewDebouncer(delay time.Duration) *Debouncer {
    return &Debouncer{delay: delay}
}

func (d *Debouncer) ShouldProcess() bool {
    d.mu.Lock()
    defer d.mu.Unlock()
    
    if time.Since(d.lastEvent) < d.delay {
        return false
    }
    d.lastEvent = time.Now()
    return true
}

// Usage
debouncer := NewDebouncer(3 * time.Second)

func onEvent() {
    if !debouncer.ShouldProcess() {
        log.Println("Debounced, ignoring")
        return
    }
    // Handle event
}
```

**Apply to:**
- File system watchers (avoid duplicate events)
- Button click handlers
- Search-as-you-type
- Window resize handlers

### Pattern 4: Process Manager with Zombie Prevention

**Use when:** You spawn child processes and need clean lifecycle management.

```go
type ProcessManager struct {
    cmd *exec.Cmd
    mu  sync.Mutex
}

func (pm *ProcessManager) Start(name string, args ...string) error {
    pm.mu.Lock()
    defer pm.mu.Unlock()
    
    pm.cmd = exec.Command(name, args...)
    pm.cmd.Stdout = os.Stdout
    pm.cmd.Stderr = os.Stderr
    
    return pm.cmd.Start()
}

func (pm *ProcessManager) Stop() {
    pm.mu.Lock()
    defer pm.mu.Unlock()
    
    if pm.cmd == nil || pm.cmd.Process == nil {
        return
    }
    
    // Kill and reap (prevent zombie)
    pm.cmd.Process.Kill()
    pm.cmd.Wait()
    pm.cmd = nil
}

func (pm *ProcessManager) Restart(name string, args ...string) error {
    pm.Stop()
    return pm.Start(name, args...)
}
```

**Apply to:**
- Running external tools (ffmpeg, imagemagick, etc.)
- Microservice orchestration
- Test harnesses
- Development servers

### Pattern 5: Periodic Task with Stop Channel

**Use when:** You need a background task that runs periodically and can be stopped.

```go
type PeriodicTask struct {
    interval time.Duration
    task     func()
    stopCh   chan struct{}
    running  atomic.Bool
}

func NewPeriodicTask(interval time.Duration, task func()) *PeriodicTask {
    return &PeriodicTask{
        interval: interval,
        task:     task,
        stopCh:   make(chan struct{}),
    }
}

func (pt *PeriodicTask) Start() {
    if pt.running.Swap(true) {
        return  // Already running
    }
    
    go func() {
        ticker := time.NewTicker(pt.interval)
        defer ticker.Stop()
        
        for {
            select {
            case <-pt.stopCh:
                return
            case <-ticker.C:
                pt.task()
            }
        }
    }()
}

func (pt *PeriodicTask) Stop() {
    if !pt.running.Swap(false) {
        return  // Not running
    }
    close(pt.stopCh)
}

// Usage
healthCheck := NewPeriodicTask(30*time.Second, func() {
    log.Println("Health check...")
    // Check system health
})
healthCheck.Start()
// Later...
healthCheck.Stop()
```

**Apply to:**
- Health checks
- Cache refresh
- Metrics collection
- Auto-save
- Polling APIs

### Pattern 6: Safe Channel Operations

**Use when:** Channels might be closed and you need to handle it gracefully.

```go
// Safe send - doesn't panic on closed channel
func safeSend[T any](ch chan<- T, value T) (sent bool) {
    defer func() {
        if recover() != nil {
            sent = false
        }
    }()
    
    select {
    case ch <- value:
        return true
    default:
        return false  // Channel full
    }
}

// Safe receive with timeout
func receiveWithTimeout[T any](ch <-chan T, timeout time.Duration) (T, bool) {
    select {
    case value, ok := <-ch:
        return value, ok
    case <-time.After(timeout):
        var zero T
        return zero, false
    }
}

// Drain channel (useful before closing)
func drain[T any](ch <-chan T) {
    for {
        select {
        case <-ch:
        default:
            return
        }
    }
}
```

### Pattern 7: Atomic State Machine

**Use when:** You have state transitions that need to be thread-safe.

```go
type State int32

const (
    StateIdle State = iota
    StateStarting
    StateRunning
    StateStopping
    StateStopped
)

type StateMachine struct {
    state atomic.Int32
}

func (sm *StateMachine) GetState() State {
    return State(sm.state.Load())
}

func (sm *StateMachine) Transition(from, to State) bool {
    return sm.state.CompareAndSwap(int32(from), int32(to))
}

// Usage
func (w *Worker) Start() error {
    // Only start if idle
    if !w.state.Transition(StateIdle, StateStarting) {
        return fmt.Errorf("cannot start: current state is %v", w.state.GetState())
    }
    
    // Do startup...
    
    w.state.Transition(StateStarting, StateRunning)
    return nil
}
```

**Apply to:**
- Connection states (disconnected → connecting → connected)
- Order states (pending → processing → shipped)
- Game states (menu → playing → paused)

### Pattern 8: Resource Pool with Reuse

**Use when:** Creating resources is expensive and you want to reuse them.

```go
type Pool[T any] struct {
    pool    chan T
    factory func() T
    reset   func(T)
}

func NewPool[T any](size int, factory func() T, reset func(T)) *Pool[T] {
    p := &Pool[T]{
        pool:    make(chan T, size),
        factory: factory,
        reset:   reset,
    }
    // Pre-populate
    for i := 0; i < size; i++ {
        p.pool <- factory()
    }
    return p
}

func (p *Pool[T]) Get() T {
    select {
    case item := <-p.pool:
        return item
    default:
        return p.factory()  // Pool empty, create new
    }
}

func (p *Pool[T]) Put(item T) {
    p.reset(item)
    select {
    case p.pool <- item:
    default:
        // Pool full, discard
    }
}

// Usage: Buffer pool
bufferPool := NewPool(
    10,
    func() []byte { return make([]byte, 8192) },
    func(b []byte) { /* clear if needed */ },
)

buf := bufferPool.Get()
// Use buf...
bufferPool.Put(buf)
```

**Apply to:**
- Buffer reuse (like we do for MJPEG parsing)
- Database connection pools
- HTTP client pools
- Object pools in games

---

## 18. System Programming Concepts

### Understanding File Descriptors

```bash
# See what files a process has open
ls -la /proc/$(pgrep camera-dashboard)/fd

# Common file descriptors:
# 0 = stdin
# 1 = stdout  
# 2 = stderr
# 3+ = opened files, sockets, devices
```

In Go:
```go
// os.File wraps a file descriptor
file, _ := os.Open("/dev/video0")
log.Printf("FD: %d", file.Fd())  // Prints the file descriptor number
```

### Understanding Pipes

FFmpeg writes to stdout, which we read through a pipe:

```go
cmd := exec.Command("ffmpeg", args...)
stdout, _ := cmd.StdoutPipe()  // Creates a pipe

// This creates:
// [FFmpeg process] --writes--> [pipe buffer] --reads--> [Go program]

// The pipe has a buffer (usually 64KB on Linux)
// If buffer fills up, FFmpeg blocks until we read
```

### Understanding Signals

```go
// Common signals:
// SIGINT  (2)  - Ctrl+C
// SIGTERM (15) - Polite "please stop"
// SIGKILL (9)  - Immediate termination (can't catch)
// SIGQUIT (3)  - Quit with core dump

// Handling signals in Go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

go func() {
    sig := <-sigCh
    log.Printf("Received %v, shutting down...", sig)
    cleanup()
    os.Exit(0)
}()
```

### Understanding /dev/video*

```bash
# Video devices in Linux follow V4L2 (Video4Linux2) API
# Each camera creates multiple device nodes:
# /dev/video0 - Main capture device
# /dev/video1 - Metadata device (often unused)

# Check device capabilities
v4l2-ctl --device=/dev/video0 --all

# List supported formats
v4l2-ctl --device=/dev/video0 --list-formats-ext
```

### Understanding sysfs

```bash
# sysfs exposes kernel information as files
# We use it to detect USB cameras without locking the device

# Camera info
cat /sys/class/video4linux/video0/name
cat /sys/class/video4linux/video0/device/modalias

# CPU temperature
cat /sys/class/thermal/thermal_zone0/temp

# CPU frequency
cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq
```

---

## 19. The Senior Engineer Mindset

### What Separates Junior from Senior?

It's not years of experience. It's **how you think**.

| Junior Approach | Senior Approach |
|-----------------|-----------------|
| "It works on my machine" | "How will this fail in production?" |
| "I'll fix it when it breaks" | "I'll design it so it can't break that way" |
| "I need to learn X framework" | "I need to understand the underlying principles" |
| "The code is done" | "The code is never done, only good enough for now" |
| "This is a coding problem" | "This is a systems problem with human factors" |
| "I'll figure it out as I go" | "Let me understand the problem first" |

### The Three Pillars of Senior Engineering

#### 1. Systems Thinking

See the whole picture, not just your code:

```
Your Code
    ↓
Interacts with other code
    ↓
Running on hardware with limits
    ↓
In an environment with constraints
    ↓
Used by humans with expectations
    ↓
Maintained by a team over years
```

**Example from this project:**
We don't just capture camera frames. We manage:
- USB bandwidth limits
- CPU thermal throttling
- Memory pressure
- FFmpeg child processes
- X11 display server
- User expectations for responsiveness
- Future maintainers reading our code

#### 2. Failure-First Thinking

Senior engineers ask "how can this fail?" before "how do I make it work?"

```go
// Junior: Happy path only
func getFrame() image.Image {
    frame := camera.Capture()
    return frame
}

// Senior: What can go wrong?
func getFrame() (image.Image, error) {
    // Camera could be disconnected
    if !camera.IsConnected() {
        return nil, ErrCameraDisconnected
    }
    
    // Capture could timeout
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()
    
    frame, err := camera.CaptureWithContext(ctx)
    if err != nil {
        // Log for debugging, return for handling
        log.Printf("[Capture] Failed: %v", err)
        return nil, fmt.Errorf("capture failed: %w", err)
    }
    
    // Frame could be corrupted
    if frame.Bounds().Empty() {
        return nil, ErrInvalidFrame
    }
    
    return frame, nil
}
```

#### 3. Empathy-Driven Development

Think about:
- **Users**: Will they understand what happened when it fails?
- **Future you**: Will you understand this code in 6 months?
- **Teammates**: Can someone else debug this at 3 AM?
- **Operators**: Can they monitor and troubleshoot this?

```go
// Bad: Who knows what happened?
log.Println("error")

// Good: Tells a story
log.Printf("[Hotplug] Camera %d (%s) disconnected after %v uptime, entering recovery mode",
    camIndex, cam.DevicePath, time.Since(cam.StartTime))
```

### Questions Senior Engineers Ask

Before writing code:
- What problem am I actually solving?
- Who will be affected by this?
- What are the constraints (time, memory, CPU, etc.)?
- How will I know if it's working?
- How will it fail? What happens then?

While writing code:
- Is this the simplest solution that works?
- Am I making assumptions I should validate?
- Will someone else understand this?
- Am I handling errors properly?

After writing code:
- How do I test this?
- How do I monitor this in production?
- What documentation is needed?
- What technical debt am I leaving?

---

## 20. Problem-Solving Framework

### The PAUSE Method

When you hit a problem, **PAUSE** before diving in:

```
P - Problem: What exactly is the problem? (Be specific)
A - Assumptions: What am I assuming? (Challenge them)
U - Understand: Do I understand the system? (Draw it out)
S - Simplify: Can I reproduce this minimally? (Isolate)
E - Experiment: What's the smallest test I can run? (Verify)
```

### Real Example: The Hot-Plug Bug

Let's apply PAUSE to the hot-plug stuttering issue we fixed:

#### P - Problem
"Camera stutters after reconnection."

Too vague. Be specific:
"After USB camera disconnects and reconnects, the video feed for that camera shows intermittent frames mixed with test patterns, even though the camera is working."

#### A - Assumptions
- The camera is actually working ← Verify with `v4l2-ctl`
- FFmpeg is starting correctly ← Check logs
- Only one FFmpeg process per camera ← Check `ps aux`
- The reconnect logic is being triggered ← Add logging

#### U - Understand
Draw the system:
```
Camera disconnect
    ↓
Hot-plug detector sees /dev/video2 missing
    ↓
Marks camera as disconnected
    ↓
Capture worker enters test pattern mode
    ↓
Camera reconnects, /dev/video2 reappears
    ↓
Hot-plug detector triggers handleCameraReconnect()
    ↓
??? What happens here ???
```

#### S - Simplify
Create minimal reproduction:
1. Start app with 2 cameras
2. Unplug camera 2
3. Wait 5 seconds
4. Plug camera 2 back in
5. Observe behavior

#### E - Experiment
Add logging to understand what's happening:
```go
log.Printf("[DEBUG] handleCameraReconnect called for camera %d", camIndex)
log.Printf("[DEBUG] Stopping manager...")
// ... 
log.Printf("[DEBUG] Manager stopped, creating new manager...")
```

Result: We discovered the old capture goroutine was still running when the new one started, causing two goroutines to fight over the same camera.

### The Five Whys

When you find a bug, ask "why" five times to find the root cause:

```
Bug: Camera stutters after reconnection

Why? Two FFmpeg processes are running for the same camera.

Why? The old capture goroutine didn't stop before the new one started.

Why? We called Stop() but didn't wait for the goroutine to exit.

Why? We used time.Sleep(100ms) instead of proper synchronization.

Why? We didn't think about goroutine lifecycle when writing Restart().

Root cause: Missing WaitGroup for goroutine synchronization.
```

### The Debugging Checklist

When stuck, go through this checklist:

```
□ Can I reproduce the problem consistently?
□ Have I checked the logs?
□ Have I added more logging to the suspicious area?
□ Have I checked the system state (ps, top, dmesg)?
□ Have I isolated the problem to a specific component?
□ Have I searched for similar issues online?
□ Have I explained the problem to someone (or a rubber duck)?
□ Have I taken a break and come back with fresh eyes?
□ Have I questioned my assumptions?
□ Have I looked at recent changes (git diff, git log)?
```

---

## 21. Debugging Like a Detective

### The Crime Scene Mindset

Think of bugs as crimes. You're the detective.

```
Crime:        The program crashed
Victim:       The user
Suspect:      Some code
Evidence:     Logs, core dumps, reproduction steps
Witnesses:    Error messages, stack traces
Motive:       Race condition? Null pointer? Resource exhaustion?
```

### Gathering Evidence

#### Level 1: Observation
```bash
# What's the program doing right now?
make status
top -p $(pgrep camera-dashboard)
ps aux | grep -E "camera|ffmpeg"

# What happened recently?
tail -100 /tmp/camera.log

# What's the system state?
df -h          # Disk full?
free -m        # Memory exhausted?
dmesg | tail   # Kernel complaints?
```

#### Level 2: Interrogation (Add Logging)
```go
// Bracket the suspicious area
log.Printf("[DEBUG] Entering handleReconnect, camIndex=%d", camIndex)
// ... suspicious code ...
log.Printf("[DEBUG] Exiting handleReconnect, success=%v", success)

// Log state at key points
log.Printf("[DEBUG] State: running=%v, ffmpegPID=%d, bufferSize=%d",
    cw.running.Load(),
    cw.ffmpegCmd.Process.Pid,
    cw.frameBuffer.GetStats())
```

#### Level 3: Forensics (Deep Analysis)
```bash
# Trace system calls
strace -p $(pgrep camera-dashboard) -e trace=open,read,write

# Profile CPU usage
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30

# Analyze goroutine states
curl http://localhost:6060/debug/pprof/goroutine?debug=2

# Check for race conditions
go build -race && ./camera-dashboard-race
```

### Common Bug Patterns and How to Spot Them

#### Pattern 1: Race Condition
**Symptoms:** Intermittent failures, "works sometimes", different behavior under load
**Clues:** Multiple goroutines, shared state without synchronization
**Detection:** Run with `-race` flag
**Fix:** Use atomics, mutexes, or channels

```go
// Buggy
var counter int
go func() { counter++ }()
go func() { counter++ }()

// Fixed
var counter atomic.Int64
go func() { counter.Add(1) }()
go func() { counter.Add(1) }()
```

#### Pattern 2: Goroutine Leak
**Symptoms:** Memory grows over time, too many goroutines
**Clues:** Goroutines waiting on channels that never receive
**Detection:** Monitor `runtime.NumGoroutine()`
**Fix:** Always have a way to exit goroutines

```go
// Buggy - goroutine never exits if stopCh isn't closed
go func() {
    for {
        select {
        case <-stopCh:
            return
        case data := <-dataCh:  // What if dataCh is never closed?
            process(data)
        }
    }
}()

// Fixed - timeout ensures eventual exit
go func() {
    for {
        select {
        case <-stopCh:
            return
        case data := <-dataCh:
            process(data)
        case <-time.After(30 * time.Second):
            if !running.Load() {
                return
            }
        }
    }
}()
```

#### Pattern 3: Deadlock
**Symptoms:** Program hangs, goroutines blocked forever
**Clues:** Multiple locks, circular dependencies
**Detection:** Send SIGQUIT to dump goroutine stacks
**Fix:** Consistent lock ordering, timeouts, avoid nested locks

```go
// Buggy - can deadlock if A and B acquired in different order
func transfer(from, to *Account, amount int) {
    from.mu.Lock()
    to.mu.Lock()  // If another goroutine does transfer(to, from, x), deadlock!
    // ...
}

// Fixed - consistent ordering
func transfer(from, to *Account, amount int) {
    // Always lock lower ID first
    first, second := from, to
    if from.ID > to.ID {
        first, second = to, from
    }
    first.mu.Lock()
    second.mu.Lock()
    // ...
}
```

#### Pattern 4: Resource Leak
**Symptoms:** File descriptors grow, "too many open files" error
**Clues:** Missing Close() calls, especially in error paths
**Detection:** `ls /proc/$(pgrep app)/fd | wc -l`
**Fix:** Always defer Close() after successful Open()

```go
// Buggy - leaks on error
func process(path string) error {
    file, err := os.Open(path)
    if err != nil {
        return err
    }
    data, err := io.ReadAll(file)
    if err != nil {
        return err  // Leak! file not closed
    }
    file.Close()
    return nil
}

// Fixed
func process(path string) error {
    file, err := os.Open(path)
    if err != nil {
        return err
    }
    defer file.Close()  // Always runs
    
    data, err := io.ReadAll(file)
    if err != nil {
        return err  // file.Close() will still run
    }
    return nil
}
```

#### Pattern 5: Zombie Process
**Symptoms:** Defunct processes in `ps`, PID table fills up
**Clues:** exec.Command without Wait()
**Detection:** `ps aux | awk '$8 == "Z"'`
**Fix:** Always call Wait() after Kill()

```go
// Buggy
cmd.Process.Kill()  // Zombie remains!

// Fixed
cmd.Process.Kill()
cmd.Wait()  // Reaps the zombie
```

### The Rubber Duck Method

When stuck, explain the problem out loud:

1. Get a rubber duck (or any object)
2. Explain what the code is supposed to do
3. Explain what it's actually doing
4. Explain it line by line

Often, you'll find the bug while explaining. This works because:
- Forces you to slow down
- Makes you question assumptions
- Engages different parts of your brain
- Catches things you "knew" but didn't verify

---

## 22. Reading Code Effectively

### The Three-Pass Method

#### Pass 1: Bird's Eye View (5 minutes)
- What is this project about?
- What are the main packages/directories?
- Where's the entry point?
- What are the dependencies?

```bash
# Quick overview
tree -L 2 -d                    # Directory structure
head -50 README.md              # What it does
cat go.mod                      # Dependencies
head -100 main.go               # Entry point
```

#### Pass 2: Follow the Flow (30 minutes)
- Start at main() and trace the happy path
- Identify the major components
- Understand data flow

```
main.go
  ↓ creates
App (ui/app.go)
  ↓ creates
Manager (camera/manager.go)
  ↓ creates
CaptureWorker (camera/capture.go)
  ↓ uses
FrameBuffer (camera/framebuffer.go)
```

#### Pass 3: Deep Dive (as needed)
- Focus on the specific area you need to understand
- Read tests to understand expected behavior
- Add logging to verify understanding

### Reading Unfamiliar Code

#### Strategy 1: Start with Types
```go
// Types tell you what the system is modeling
type CaptureWorker struct {
    camera      Camera          // Has a camera
    running     atomic.Bool     // Has state
    frameBuffer *FrameBuffer    // Produces frames
    ffmpegCmd   *exec.Cmd       // Uses external process
}

// From this you can infer:
// - Worker captures from a camera
// - Can be started/stopped (running)
// - Outputs to a buffer
// - Uses FFmpeg for capture
```

#### Strategy 2: Start with Public Interface
```go
// Public methods tell you what the component does
func (m *Manager) Initialize() error    // Setup
func (m *Manager) Start() error         // Begin operation
func (m *Manager) Stop()                // End operation
func (m *Manager) GetCameras() []Camera // Query state

// This is a lifecycle manager for cameras
```

#### Strategy 3: Start with Tests
```go
func TestCaptureWorker_StartsAndStops(t *testing.T) {
    worker := NewCaptureWorker(testCamera, nil)
    
    err := worker.Start()
    assert.NoError(t, err)
    assert.True(t, worker.running.Load())
    
    worker.Stop()
    assert.False(t, worker.running.Load())
}

// Tests document expected behavior
```

#### Strategy 4: Add Tracing
```go
// When you don't understand flow, add breadcrumbs
func (cw *CaptureWorker) Start() error {
    log.Printf("[TRACE] CaptureWorker.Start() called")
    // ...
}

func (cw *CaptureWorker) captureLoop() {
    log.Printf("[TRACE] captureLoop() entered")
    for cw.running.Load() {
        log.Printf("[TRACE] captureLoop() iteration")
        // ...
    }
    log.Printf("[TRACE] captureLoop() exiting")
}
```

### Asking Good Questions About Code

Instead of: "How does this work?"

Ask:
- "What is the input and output of this function?"
- "What state does this modify?"
- "What can cause this to fail?"
- "Why was it done this way instead of X?"
- "What would break if I changed Y?"

---

## 23. Making Technical Decisions

### The Decision Framework

For any significant technical decision:

```
1. CONTEXT: What are we trying to achieve?
2. OPTIONS: What are the possible approaches?
3. TRADEOFFS: What does each option cost/give?
4. DECISION: Which option and why?
5. CONSEQUENCES: What follows from this decision?
```

### Real Example: Per-Camera vs Full Restart

**Context:** When a camera disconnects and reconnects, we need to restart capture.

**Options:**

| Option | Description |
|--------|-------------|
| A. Full restart | Stop all cameras, reinitialize everything |
| B. Per-camera restart | Only restart the affected camera |
| C. No restart | Let built-in retry handle it |

**Tradeoffs:**

| Aspect | Full Restart | Per-Camera | No Restart |
|--------|--------------|------------|------------|
| Complexity | Low | Medium | Low |
| Disruption | High (all cameras) | Low (one camera) | None |
| Reliability | High | High | Medium |
| Implementation time | 1 hour | 4 hours | 0 |

**Decision:** Per-camera restart (Option B)

**Why:**
- Disrupting all cameras is unacceptable for vehicle monitoring
- Built-in retry doesn't handle the goroutine lifecycle correctly
- The added complexity is worth the improved user experience

**Consequences:**
- Need to implement WaitGroup for goroutine cleanup
- Need to add Restart() method to CaptureWorker
- Need debouncing to prevent rapid restarts
- Code is more complex but behavior is more robust

### Common Decision Tradeoffs

#### Build vs Buy
```
Build when:                    Buy/Use library when:
- Core to your business        - Commodity functionality
- Specific requirements        - Standard requirements
- Need full control            - Maintenance cost acceptable
- Team has expertise           - Library is well-maintained
```

#### Optimize Now vs Later
```
Optimize now when:             Optimize later when:
- You have measured a problem  - You're guessing it might be slow
- Cost of being slow is high   - Premature optimization
- You understand the bottleneck- You don't understand the system yet
```

#### Simple vs Flexible
```
Choose simple when:            Choose flexible when:
- Requirements are stable      - Requirements will change
- Single use case              - Multiple use cases
- Maintenance burden matters   - Extensibility matters
```

### The Reversibility Principle

Prefer reversible decisions. Ask: "How hard is it to change this later?"

```
Easy to change (prefer these):
- Log format
- Configuration values
- Internal implementation

Hard to change (think carefully):
- Public API
- Database schema
- File formats
- Protocol design
```

---

## 24. Learning From This Project's Mistakes

Every bug is a lesson. Here's what we learned:

### Mistake 1: Not Waiting for Goroutines

**What happened:**
```go
func (cw *CaptureWorker) Restart() error {
    cw.Stop()
    time.Sleep(100 * time.Millisecond)  // "Should be enough"
    return cw.Start()
}
```

**The problem:** 100ms wasn't always enough. Old goroutine was sometimes still running when new one started.

**The lesson:** Never use `time.Sleep()` for synchronization. Use proper primitives:
```go
func (cw *CaptureWorker) Restart() error {
    cw.Stop()      // Now uses WaitGroup internally
    cw.wg.Wait()   // Blocks until goroutine actually exits
    return cw.Start()
}
```

**Apply to your projects:**
- Always have explicit synchronization for goroutine lifecycle
- `time.Sleep()` in concurrent code is a code smell
- If you're sleeping to "wait for something," use channels or WaitGroups

### Mistake 2: Restarting Everything When One Thing Fails

**What happened:**
```go
func handleCameraReconnect(camIndex int) {
    a.manager.Stop()           // Stops ALL cameras
    a.manager = NewManager()   // Recreates everything
    a.manager.Start()          // Restarts ALL cameras
}
```

**The problem:** One flaky camera caused all cameras to restart constantly.

**The lesson:** Isolate failures. One component's problems shouldn't affect others.

**Apply to your projects:**
- Design for partial failure
- Each worker/component should be independently restartable
- Use circuit breakers for external dependencies

### Mistake 3: Using External Tools in Hot Paths

**What happened:**
```go
func isUSBCaptureDevice(devPath string) bool {
    cmd := exec.Command("v4l2-ctl", "--device="+devPath, "--info")
    output, _ := cmd.Output()
    // ...
}
```

**The problem:** `v4l2-ctl` locks the device, conflicting with FFmpeg capture.

**The lesson:** External commands in hot paths cause problems:
- They're slow
- They can fail
- They can conflict with other processes
- They can hang

**Apply to your projects:**
- Use libraries instead of shelling out when possible
- Read from `/proc` or `/sys` instead of running commands
- If you must shell out, do it infrequently and with timeouts

### Mistake 4: Not Considering USB Behavior

**What happened:**
We assumed camera disconnect → device file disappears immediately.

**Reality:**
```
USB disconnect
    ↓ (0-500ms)
Kernel notices
    ↓ (100-200ms)
Driver cleans up
    ↓ (variable)
Device file removed
    ↓ (0-1000ms)
Device file reappears (re-enumeration)
```

**The lesson:** Hardware has latency and quirks. Always debounce external events.

**Apply to your projects:**
- Network connections: Retry with backoff
- File system: Watch for multiple events per change
- USB/hardware: Debounce and verify state

### Mistake 5: Inadequate Logging

**What happened:**
```go
if err != nil {
    log.Println("error")  // What error? Where? Why?
}
```

**The lesson:** Logs are your only window into production. Make them useful.

**Apply to your projects:**
```go
// Include: component, context, error, and state
log.Printf("[%s] Failed to start capture for camera %s (%s): %v (retry %d/%d)",
    "Capture",
    cam.DeviceID,
    cam.DevicePath,
    err,
    retryCount,
    maxRetries)
```

---

## 25. Building Your Engineering Toolbox

### Mental Models

These are thinking tools that help you reason about systems:

#### 1. State Machines
Everything is a state machine:
```
Camera States: Disconnected → Connecting → Connected → Capturing → Error
                    ↑                                        │
                    └────────────────────────────────────────┘
```

When debugging, ask: "What state is this in? What state should it be in?"

#### 2. Producer-Consumer
Most systems move data between components:
```
[Producer] → [Buffer/Queue] → [Consumer]
   Camera →    FrameBuffer  →    UI
   API     →      Cache     →   Database
   User    →   Event Queue  →   Handler
```

When designing, ask: "Who produces? Who consumes? What happens if producer is faster/slower?"

#### 3. Layers
Systems have layers, each hiding complexity:
```
Your Code
    ↓
Standard Library (Go runtime, packages)
    ↓
Operating System (Linux kernel)
    ↓
Hardware (CPU, memory, USB)
```

When debugging, ask: "Which layer is the problem in?"

#### 4. Resources and Lifecycles
Everything is a resource with a lifecycle:
```
Create → Configure → Use → Cleanup → Destroy
Open   → Read/Write → Close
Acquire Lock → Critical Section → Release Lock
Start Goroutine → Work → Signal Done
```

When coding, ask: "How is this created? How must it be cleaned up?"

### Problem-Solving Techniques

#### Binary Search Debugging
When you don't know where a bug is:
1. Find a point where it works
2. Find a point where it's broken
3. Check the middle
4. Repeat until you find the exact line

```bash
# Works in version A, broken in version B
git bisect start
git bisect bad HEAD
git bisect good v1.0
# Git will binary search through commits
```

#### Differential Debugging
Compare working vs non-working:
```bash
# What's different between working and broken state?
diff working.log broken.log

# What changed since it last worked?
git diff HEAD~10..HEAD

# What's different between two runs?
./app > run1.log 2>&1
./app > run2.log 2>&1
diff run1.log run2.log
```

#### Reduction
Simplify until the bug is obvious:
1. Remove features until bug disappears
2. Add back one at a time until bug returns
3. The last thing you added contains the bug

#### Rubber Ducking
Explain the problem out loud. Your brain processes information differently when you verbalize it.

### Learning Strategies

#### 1. Learn by Building
Don't just read tutorials. Build things:
- Clone a project, break it, fix it
- Implement a simplified version from scratch
- Add features to existing projects

#### 2. Learn by Debugging
When something breaks, dig deep:
- Don't just fix it, understand why
- Document what you learned
- Create a reproduction case

#### 3. Learn by Teaching
Teaching forces understanding:
- Write documentation (like this file)
- Explain to teammates
- Answer Stack Overflow questions
- Write blog posts

#### 4. Learn by Reading
Read code from good projects:
- Standard library source
- Popular open source projects
- Your company's critical systems

### Technical Habits

#### Daily
- [ ] Read logs of your running services
- [ ] Review your own code before pushing
- [ ] Write tests for new code
- [ ] Update documentation as you learn

#### Weekly
- [ ] Read code from a project you admire
- [ ] Learn one new tool or technique
- [ ] Review and organize your notes
- [ ] Reflect on what problems you solved

#### Monthly
- [ ] Deep dive into an unfamiliar part of your stack
- [ ] Contribute to an open source project
- [ ] Write about something you learned
- [ ] Clean up technical debt

---

## 26. The Art of Problem Deconstruction

The most powerful skill in software engineering isn't knowing a language or framework—it's the ability to take a messy, complex problem and break it into pieces you can actually solve.

This section teaches you the mental process that experienced engineers use, often unconsciously. By making it explicit, you can practice and improve it deliberately.

### The Three Phases

Every problem-solving process follows three phases:

```
┌─────────────────────────────────────────────────────────────┐
│                                                             │
│   DECONSTRUCT → SOLVE → RECONSTRUCT                        │
│                                                             │
│   Break apart    Work on     Assemble                       │
│   the problem    the pieces  the solution                   │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

Most junior engineers skip deconstruction and jump straight to solving. This leads to:
- Solutions that don't fit the actual problem
- Missing edge cases
- Overcomplicated code
- Rework when assumptions prove wrong

### Phase 1: Deconstruction

Deconstruction is the art of breaking a complex problem into smaller, manageable pieces.

#### The Onion Method

Problems have layers. Peel them one at a time:

```
Layer 1: What the user said
         "The camera dashboard is slow"

Layer 2: What they meant
         "Video feeds freeze sometimes"

Layer 3: The observable symptom
         "Frame rate drops from 15 FPS to 2 FPS occasionally"

Layer 4: The technical symptom
         "CPU spikes to 100% during drops"

Layer 5: The root cause
         "Garbage collection pauses from image allocation"
```

Each layer gets you closer to a solvable problem.

#### Divide and Isolate

Break the system into components and test each:

```go
// Problem: "Camera doesn't work"
// Decompose into testable components:

// 1. Hardware: Does the camera work at all?
//    Test: ffplay /dev/video0

// 2. Permissions: Can our app access it?
//    Test: ls -la /dev/video0

// 3. FFmpeg: Can we get frames?
//    Test: ffmpeg -f v4l2 -i /dev/video0 -frames:v 1 test.jpg

// 4. Pipe: Is data flowing to our app?
//    Test: Add logging after pipe read

// 5. Parsing: Are we extracting frames correctly?
//    Test: Log frame boundaries

// 6. Display: Is rendering working?
//    Test: Display a static test image
```

Now you have 6 small problems instead of 1 big one.

#### The Dependency Chain

Map what depends on what:

```
                    ┌──────────────┐
                    │   Display    │
                    │   (Fyne)     │
                    └──────┬───────┘
                           │ needs frames
                           ▼
                    ┌──────────────┐
                    │  FrameBuffer │
                    └──────┬───────┘
                           │ needs parsed images
                           ▼
                    ┌──────────────┐
                    │ MJPEG Parser │
                    └──────┬───────┘
                           │ needs byte stream
                           ▼
                    ┌──────────────┐
                    │   FFmpeg     │
                    └──────┬───────┘
                           │ needs device access
                           ▼
                    ┌──────────────┐
                    │    Camera    │
                    └──────────────┘
```

If something breaks, start from the bottom and work up.

#### Identify the Core Problem

Often, what seems like multiple problems is actually one core problem with many symptoms:

```
Symptoms:                          Core Problem:
- Camera 1 stutters                ┐
- Camera 2 shows test pattern      ├─→ Hot-plug restarts all cameras
- CPU spikes during reconnect      ┘   instead of just the one that
- Audio from other app glitches        reconnected
```

Find the core problem, and multiple symptoms disappear.

#### Ask Clarifying Questions

Before solving, make sure you understand:

```
What?     - What exactly is happening? (Be precise)
When?     - When does it happen? (Triggers, timing)
Where?    - Where in the system? (Component, file, line)
How?      - How do we reproduce it? (Steps)
Why?      - Why might this be happening? (Hypotheses)
Who?      - Who is affected? (Users, systems)
```

### Phase 2: Systematic Solving

Once you've deconstructed the problem, solve each piece methodically.

#### The Scientific Method for Code

```
1. OBSERVE    - What exactly is happening?
2. HYPOTHESIZE - What might cause this?
3. PREDICT    - If my hypothesis is correct, what should I see?
4. TEST       - Run an experiment to verify
5. ITERATE    - Update hypothesis based on results
```

Real example from this codebase:

```
1. OBSERVE
   Camera stutters after reconnection

2. HYPOTHESIZE
   Maybe two FFmpeg processes are running

3. PREDICT  
   If true, `ps aux | grep ffmpeg` will show 2 processes
   per camera after reconnect

4. TEST
   Run the command → See 2 processes!

5. ITERATE
   New hypothesis: The old capture goroutine isn't stopping
   Test: Add logging → Confirmed!
   Solution: Add WaitGroup to ensure old goroutine exits
```

#### Work Small to Big

Solve in order of increasing complexity:

```
1. Fix the simplest case first
   "Make it work with one camera"

2. Handle the common case
   "Make it work with two cameras"

3. Handle edge cases
   "Make it work when cameras disconnect"

4. Handle failure cases
   "Make it gracefully degrade when cameras fail"
```

#### One Change at a Time

When debugging, change only ONE thing, then test:

```
❌ WRONG:
   "I'll add a WaitGroup, fix the buffer size, and
   change the timeout all at once"
   
   (If it still fails, which change was wrong?)

✓ RIGHT:
   "I'll add the WaitGroup and test"
   (Works? Good. Doesn't work? I know what broke it.)
```

#### The Binary Search Debug

When you don't know where the bug is, use binary search:

```
Problem somewhere in 1000 lines of code

Add log at line 500:
  - Bug appears before line 500? Search 1-500
  - Bug appears after line 500? Search 500-1000

Add log at line 250 (or 750):
  - Continue halving until found

10 iterations = any bug in 1024 lines
```

In code:
```go
func processData(data []byte) error {
    log.Println("DEBUG: Entering processData")  // Checkpoint 1
    
    result := transform(data)
    log.Println("DEBUG: After transform")       // Checkpoint 2
    
    if err := validate(result); err != nil {
        return err
    }
    log.Println("DEBUG: After validate")        // Checkpoint 3
    
    return store(result)
}
// If crash happens after "After transform" but before "After validate",
// the bug is in validate()
```

### Phase 3: Reconstruction

After solving the pieces, assemble them into a complete solution.

#### Bottom-Up Assembly

Build from the foundation up:

```
1. First: Core data structures
   - FrameBuffer with atomic operations
   - Test it in isolation

2. Second: Core logic
   - MJPEG parser
   - Test with sample data

3. Third: Integration
   - Connect parser to buffer
   - Test data flow

4. Fourth: Interface
   - Add UI rendering
   - Test visual output

5. Fifth: Error handling
   - Add recovery paths
   - Test failure modes
```

#### The Integration Checklist

When connecting components, verify:

```
□ Data format matches between components
□ Error handling exists at boundaries
□ Resources are properly passed/cleaned up
□ Concurrency is safe at interfaces
□ Logging exists to debug issues
```

#### Test the Seams

Bugs love to hide at component boundaries:

```go
// Seam between FFmpeg and Parser
func (p *Parser) ReadFrame() (*image.Image, error) {
    data, err := p.ffmpegPipe.Read()  // ← Seam: What if pipe breaks?
    if err != nil {
        return nil, err  // ← Don't just pass through, add context!
    }
    
    frame, err := p.decode(data)      // ← Seam: What if data is corrupt?
    if err != nil {
        return nil, fmt.Errorf("decode failed after %d bytes: %w", 
            len(data), err)           // ← Add context for debugging
    }
    
    return frame, nil
}
```

#### Verify End-to-End

After assembly, test the complete flow:

```
Camera → FFmpeg → Pipe → Parser → Buffer → UI → Display
   ✓       ✓       ✓       ✓        ✓      ✓      ✓

Then test failure modes:
- Unplug camera mid-stream → Graceful recovery?
- Corrupt frame data → No crash?
- CPU overload → Graceful degradation?
```

### Training Your Problem-Solving Brain

Problem-solving is a skill. Like any skill, it improves with deliberate practice.

#### Mental Models to Internalize

**1. Everything is a System**
```
Inputs → Processing → Outputs
         ↑
         └── State
         
Ask: What are the inputs? What state exists?
     What processing happens? What outputs result?
```

**2. Bugs are Clues, Not Enemies**
```
A bug tells you: "Your mental model is wrong HERE"
It's pointing to exactly where you need to learn.
```

**3. The Map is Not the Territory**
```
Your code is a model of reality.
When bugs appear, reality disagrees with your model.
Update your model, not your anger.
```

**4. Occam's Razor**
```
The simplest explanation is usually correct.
Before suspecting compiler bugs, check your typos.
```

**5. Chesterton's Fence**
```
Before removing code you don't understand, understand why it exists.
That "useless" sleep(100ms) might be preventing a race condition.
```

#### Daily Exercises

**Exercise 1: Explain the Bug**
When you fix a bug, write down:
- What was the symptom?
- What was the root cause?
- Why did the bug exist?
- How could it have been prevented?

**Exercise 2: Predict Before Testing**
Before running code, write down what you expect to happen.
When wrong, analyze why your mental model failed.

**Exercise 3: Read Code Backwards**
Start from the output and trace backwards to the input.
This reveals dependencies you might miss going forward.

**Exercise 4: The Rubber Duck Debug**
Explain your problem out loud, step by step.
You'll often find the bug while explaining.

**Exercise 5: Time-Box Your Approach**
"I'll try this approach for 30 minutes."
If no progress, switch approaches. Don't get stuck in ruts.

#### Weekly Practices

**Practice 1: Study a Bug You Didn't Write**
Find a bug report in an open source project.
Try to understand and fix it before reading the solution.

**Practice 2: Break Something on Purpose**
In a test environment, intentionally introduce bugs.
Practice diagnosing them with different techniques.

**Practice 3: Code Review Deeply**
Don't just skim. For each piece of code ask:
- What could go wrong?
- What assumptions is this making?
- How would I test this?

**Practice 4: Teach Someone**
Explain a bug you fixed to a colleague.
Teaching forces clarity of understanding.

#### Building Intuition

Intuition isn't magic—it's pattern recognition from experience.

```
Beginner:   "It's not working" → Random changes
            (No patterns yet)

Intermediate: "It's not working" → Check logs
              (One pattern)

Senior:     "It's not working" → 
            - Timing issue? (Pattern: race condition)
            - Resource issue? (Pattern: memory/handles)
            - State issue? (Pattern: initialization)
            - Boundary issue? (Pattern: off-by-one)
            (Many patterns to check)

Expert:     "That symptom + this context = probably X"
            (Pattern matching happens unconsciously)
```

Build patterns by:
1. Solving many diverse problems
2. Categorizing bugs you encounter
3. Noting which approaches work for which patterns
4. Teaching patterns to others

#### The Problem-Solving Journal

Keep a log of problems you solve:

```markdown
## Date: 2024-01-15
### Problem
Camera feeds stuttering after hot-plug reconnection

### Symptoms
- Intermittent frames mixed with test patterns
- Only on reconnected camera
- CPU spike during reconnection

### Root Cause
Old capture goroutine not stopped before new one started.
Two goroutines fighting for same camera.

### Solution
Added WaitGroup to ensure goroutine exits before restart.

### Lesson
Never assume goroutine stopped just because you closed a channel.
Use WaitGroup to confirm exit.

### Pattern
Category: Concurrency - Goroutine lifecycle
Symptom: Duplicate work / resource conflict
Solution: Synchronization primitive (WaitGroup)
```

After a year, you'll have a personal debugging handbook.

#### The Meta-Skill: Knowing When You're Stuck

Recognize these warning signs:
- Same thing for >30 minutes with no progress
- Trying the same approach repeatedly
- Getting frustrated instead of curious
- Making random changes hoping something works

When stuck:
1. **Stop** - Take a break (seriously)
2. **Step back** - Am I solving the right problem?
3. **Seek help** - Explain to someone else
4. **Simplify** - What's the smallest reproduction?
5. **Sleep on it** - Unconscious mind processes overnight

### Real-World Application: Debugging This Project

Let's apply everything to a real bug from this codebase.

**The Bug:** "App crashes when both cameras are unplugged simultaneously"

**Deconstruction:**
```
Layer 1: App crashes
Layer 2: Crash during hot-plug handling
Layer 3: Panic in manager.go when accessing camera list
Layer 4: Index out of range - camera list was modified during iteration
Layer 5: Race condition between hot-plug handler and camera manager
```

**Isolate Components:**
```
□ Hot-plug detector (udev/sysfs)
□ Camera manager (manager.go)
□ Camera list (slice of cameras)
□ UI update (app.go)

Test each: Where does the panic originate?
Answer: manager.go line 89, accessing cameras[i]
```

**Hypothesize and Test:**
```
Hypothesis 1: Cameras slice being modified during iteration
Test: Add mutex lock around camera access
Result: Still crashes (different panic)

Hypothesis 2: Multiple hot-plug events firing simultaneously  
Test: Add logging with timestamps
Result: Confirmed! Two events 3ms apart

Hypothesis 3: Debounce not working for simultaneous events
Test: Check debounce logic
Result: Debounce was per-camera, not global
```

**Solution:**
```go
// Before: Per-camera debounce
lastReconnect map[string]time.Time  // Per device path

// After: Global debounce + per-camera debounce
lastHotPlugEvent time.Time          // Global
lastReconnect    map[string]time.Time  // Per device
```

**Reconstruct:**
1. Add global debounce timer
2. Add mutex around camera list access
3. Test with single unplug
4. Test with dual unplug
5. Test with rapid plug/unplug cycles

**Document the Pattern:**
```
Category: Concurrency - Shared state modification
Symptom: Panic on slice access, index out of range
Trigger: Multiple events modifying same data structure
Solution: Mutex + debouncing to serialize access
```

### Summary: The Problem-Solving Mindset

```
┌────────────────────────────────────────────────────────────────┐
│                  THE PROBLEM-SOLVING LOOP                      │
│                                                                │
│     ┌──────────┐                                               │
│     │          │                                               │
│     │ OBSERVE  │◄─────────────────────────┐                    │
│     │          │                          │                    │
│     └────┬─────┘                          │                    │
│          │                                │                    │
│          ▼                                │                    │
│     ┌──────────┐                          │                    │
│     │          │                          │                    │
│     │ DECONSTRUCT                         │                    │
│     │          │                          │                    │
│     └────┬─────┘                          │                    │
│          │                                │                    │
│          ▼                                │                    │
│     ┌──────────┐                          │                    │
│     │          │        ┌────────────┐    │                    │
│     │  SOLVE   │───────►│  DIDN'T    │────┘                    │
│     │          │        │  WORK?     │                         │
│     └────┬─────┘        └────────────┘                         │
│          │                                                     │
│          │ WORKED                                              │
│          ▼                                                     │
│     ┌──────────┐                                               │
│     │          │                                               │
│     │RECONSTRUCT                                               │
│     │          │                                               │
│     └────┬─────┘                                               │
│          │                                                     │
│          ▼                                                     │
│     ┌──────────┐                                               │
│     │          │                                               │
│     │ DOCUMENT │                                               │
│     │          │                                               │
│     └──────────┘                                               │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

The best engineers aren't the ones who never get stuck. They're the ones who get unstuck efficiently because they:

1. **Deconstruct** problems into manageable pieces
2. **Solve** systematically, one piece at a time
3. **Reconstruct** carefully, testing at each step
4. **Learn** from each problem to build pattern recognition

This is a trainable skill. Every bug you fix, every system you understand, every problem you solve—they all add patterns to your mental library.

Start today. Pick a bug. Deconstruct it. Solve it. Document it.

That's how you build a problem-solving brain.

---

## Summary

This codebase demonstrates several advanced patterns:

1. **Concurrency** - Multiple goroutines with safe communication
2. **Lock-free programming** - Atomics and double buffering
3. **Process management** - FFmpeg child processes with zombie prevention
4. **Stream parsing** - MJPEG frame extraction
5. **Hot-plug handling** - Per-camera restart with debouncing
6. **GUI programming** - Custom Fyne widgets
7. **Resource management** - Graceful shutdown, proper cleanup

### Key Takeaways

| Pattern | Problem Solved | Apply To |
|---------|---------------|----------|
| WaitGroup | Ensure goroutines exit before restart | Any worker pool |
| Atomic double buffer | Fast producer-consumer without locks | Sensor data, game state |
| Debouncing | Prevent rapid event handling | File watchers, UI events |
| Stop channel | Interrupt blocking operations | Any background task |
| Process reaping | Prevent zombie processes | Any exec.Command usage |
| sysfs access | Non-blocking device detection | Hardware monitoring |

### The Senior Engineer's Checklist

Before you code:
- [ ] Do I understand the problem?
- [ ] Have I considered how this will fail?
- [ ] Am I making the simplest solution that works?

While you code:
- [ ] Am I handling errors properly?
- [ ] Will someone else understand this?
- [ ] Are my logs useful for debugging?

After you code:
- [ ] Have I tested the failure cases?
- [ ] Have I documented the non-obvious parts?
- [ ] Would I be happy to debug this at 3 AM?

### Your Learning Path

1. **Start simple** - Understand `main.go` and the overall flow
2. **Follow the data** - Camera → FFmpeg → Buffer → UI
3. **Study concurrency** - Focus on `capture.go` and `framebuffer.go`
4. **Debug actively** - Add logging, use `make status`
5. **Break things** - Change settings in `config.go`, see what breaks
6. **Fix bugs** - Apply the debugging techniques from this guide
7. **Add features** - Implement something new
8. **Teach others** - Explain what you learned

### Final Thought

The difference between a junior and senior engineer isn't knowledge—it's judgment. Judgment comes from experience, and experience comes from making mistakes and learning from them.

Every bug you fix, every system you debug, every decision you make—these all build your judgment. This project is a small but complete system where you can safely make mistakes and learn from them.

Go break something. Then fix it. That's how you grow.
