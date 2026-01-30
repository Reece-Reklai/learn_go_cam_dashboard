# LEARN.md - Deep Dive into Camera Dashboard Architecture

This document explains the technical concepts, patterns, and decisions in this codebase. It's designed to teach you Go concurrency, systems programming, and real-time video processing.

---

## Table of Contents

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
11. [Performance Optimization](#11-performance-optimization)
12. [Error Handling Patterns](#12-error-handling-patterns)
13. [Key Go Concepts Used](#13-key-go-concepts-used)

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
  1234  Z (zombie)  ← FFmpeg exited but not reaped

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

## 11. Performance Optimization

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

## 12. Error Handling Patterns

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

## 13. Key Go Concepts Used

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

## Summary

This codebase demonstrates several advanced Go patterns:

1. **Concurrency** - Multiple goroutines with safe communication
2. **Lock-free programming** - Atomics and double buffering
3. **Process management** - FFmpeg child processes
4. **Stream parsing** - MJPEG frame extraction
5. **GUI programming** - Custom Fyne widgets
6. **Resource management** - Graceful shutdown, zombie prevention

Each pattern solves a real problem:
- Lock-free buffers → Maximum performance
- Frame skipping → CPU efficiency
- Zombie reaping → System stability
- Signal handling → Clean shutdown

Understanding these patterns will make you a better systems programmer and help you build robust, high-performance applications in Go.
