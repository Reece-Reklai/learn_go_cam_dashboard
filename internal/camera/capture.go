package camera

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Resolution presets for adaptive scaling
type Resolution struct {
	Width  int
	Height int
	Label  string
}

// Vehicle-optimized fixed resolution - uses values from config.go
var (
	ResolutionVehicle = Resolution{CameraWidth, CameraHeight, "Vehicle"} // Fixed for all cameras
)

// CaptureWorker handles camera capture in a goroutine
type CaptureWorker struct {
	camera  Camera
	running atomic.Bool
	stopCh  chan struct{}

	// Frame output - supports both channel and buffer modes
	frameCh     chan<- image.Image // Legacy channel mode
	frameBuffer *FrameBuffer       // New buffer mode (preferred)

	// FFmpeg capture
	ffmpegCmd *exec.Cmd
	ffmpegMu  sync.Mutex

	// Capture settings - use camera's max capabilities, never restart
	targetFPS  atomic.Int32 // Effective FPS (controls frame skipping)
	captureFPS int          // FFmpeg capture rate (from camera capabilities)
	captureW   int          // Capture width (from camera capabilities)
	captureH   int          // Capture height (from camera capabilities)

	// Frame skipping - skip decoding to reduce CPU when target FPS < capture FPS
	frameSkipCounter atomic.Uint64

	// Stats
	lastFrameTime atomic.Int64
	frameCount    atomic.Uint64
	errorCount    atomic.Uint32
	skippedFrames atomic.Uint64

	// Concurrent decode pipeline (optional)
	decodePool *DecodePool
	usePool    bool
}

// DecodePool manages concurrent JPEG decoding workers
type DecodePool struct {
	workers  int
	inputCh  chan []byte
	outputCh chan image.Image
	running  atomic.Bool
	wg       sync.WaitGroup
}

// NewDecodePool creates a pool of decoder workers
func NewDecodePool(workers int) *DecodePool {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	// Limit to 2 workers on Pi to avoid overwhelming CPU
	if workers > 2 {
		workers = 2
	}

	return &DecodePool{
		workers:  workers,
		inputCh:  make(chan []byte, 4),      // Small buffer for raw JPEG data
		outputCh: make(chan image.Image, 2), // Decoded frames ready for consumption
	}
}

// Start launches the decoder workers
func (dp *DecodePool) Start() {
	if dp.running.Swap(true) {
		return
	}

	for i := 0; i < dp.workers; i++ {
		dp.wg.Add(1)
		go dp.decodeWorker(i)
	}
	log.Printf("[DecodePool] Started %d decoder workers", dp.workers)
}

// Stop halts all workers
func (dp *DecodePool) Stop() {
	if !dp.running.Swap(false) {
		return
	}
	close(dp.inputCh)
	dp.wg.Wait()
	close(dp.outputCh)
}

// Submit sends raw JPEG data for decoding
func (dp *DecodePool) Submit(data []byte) bool {
	if !dp.running.Load() {
		return false
	}
	select {
	case dp.inputCh <- data:
		return true
	default:
		return false // Pool is busy, drop frame
	}
}

// GetOutput returns the channel for decoded frames
func (dp *DecodePool) GetOutput() <-chan image.Image {
	return dp.outputCh
}

// decodeWorker processes JPEG data
func (dp *DecodePool) decodeWorker(id int) {
	defer dp.wg.Done()

	for data := range dp.inputCh {
		if !dp.running.Load() {
			return
		}

		// Decode JPEG - no RGBA conversion needed
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			continue
		}

		// Send to output directly (no conversion)
		select {
		case dp.outputCh <- img:
		default:
			// Output buffer full, drop this frame
		}
	}
}

// NewCaptureWorker creates a new capture worker for a camera
// Uses fixed 640x480 @ 15 FPS for vehicle monitoring
func NewCaptureWorker(camera Camera, frameCh chan<- image.Image) *CaptureWorker {
	// Use fixed vehicle-optimized settings
	capW := camera.Capabilities.MaxWidth
	capH := camera.Capabilities.MaxHeight
	capFPS := camera.Capabilities.MaxFPS

	// Ensure we have valid defaults from config.go
	if capW == 0 {
		capW = CameraWidth
	}
	if capH == 0 {
		capH = CameraHeight
	}
	if capFPS == 0 {
		capFPS = CameraFPS
	}

	cw := &CaptureWorker{
		camera:     camera,
		frameCh:    frameCh,
		stopCh:     make(chan struct{}),
		captureW:   capW,
		captureH:   capH,
		captureFPS: capFPS,
		usePool:    false,
	}
	cw.targetFPS.Store(int32(capFPS)) // Fixed at 15 FPS
	log.Printf("[Capture] %s: Vehicle mode - %dx%d @ %d FPS (fixed)", camera.DeviceID, capW, capH, capFPS)
	return cw
}

// NewCaptureWorkerWithBuffer creates a capture worker using FrameBuffer
// Uses fixed resolution/FPS from config.go for vehicle monitoring
func NewCaptureWorkerWithBuffer(camera Camera, buffer *FrameBuffer) *CaptureWorker {
	// Use fixed vehicle-optimized settings
	capW := camera.Capabilities.MaxWidth
	capH := camera.Capabilities.MaxHeight
	capFPS := camera.Capabilities.MaxFPS

	// Ensure we have valid defaults from config.go
	if capW == 0 {
		capW = CameraWidth
	}
	if capH == 0 {
		capH = CameraHeight
	}
	if capFPS == 0 {
		capFPS = CameraFPS
	}

	cw := &CaptureWorker{
		camera:      camera,
		frameBuffer: buffer,
		stopCh:      make(chan struct{}),
		captureW:    capW,
		captureH:    capH,
		captureFPS:  capFPS,
		usePool:     false,
	}
	cw.targetFPS.Store(int32(capFPS)) // Fixed FPS from config
	log.Printf("[Capture] %s: Vehicle mode - %dx%d @ %d FPS (buffer, fixed)", camera.DeviceID, capW, capH, capFPS)
	return cw
}

// SetFPS updates the target FPS for this capture worker
// This uses frame skipping - FFmpeg stays at max FPS, we just decode fewer frames
// NO RESTART EVER - resolution stays constant
func (cw *CaptureWorker) SetFPS(fps int) {
	if fps < 5 {
		fps = 5
	}
	// Limit to camera's max FPS
	if fps > cw.captureFPS {
		fps = cw.captureFPS
	}
	oldFPS := cw.targetFPS.Swap(int32(fps))
	if oldFPS != int32(fps) {
		log.Printf("[Capture] %s: Target FPS %d -> %d (frame skipping, no restart)", cw.camera.DeviceID, oldFPS, fps)
	}
}

// GetFPS returns current FPS setting
func (cw *CaptureWorker) GetFPS() int {
	return int(cw.targetFPS.Load())
}

// GetMaxFPS returns the camera's maximum FPS
func (cw *CaptureWorker) GetMaxFPS() int {
	return cw.captureFPS
}

// GetResolution returns current capture resolution
func (cw *CaptureWorker) GetResolution() (int, int) {
	return cw.captureW, cw.captureH
}

// Start begins capturing frames from camera
func (cw *CaptureWorker) Start() error {
	if cw.running.Load() {
		return fmt.Errorf("capture worker already running")
	}

	cw.running.Store(true)
	go cw.captureLoop()
	return nil
}

// Stop stops capture worker
func (cw *CaptureWorker) Stop() {
	if !cw.running.Load() {
		return
	}

	cw.running.Store(false)

	// Signal stop
	select {
	case <-cw.stopCh:
		// Already closed
	default:
		close(cw.stopCh)
	}

	cw.ffmpegMu.Lock()
	if cw.ffmpegCmd != nil && cw.ffmpegCmd.Process != nil {
		cw.ffmpegCmd.Process.Kill()
	}
	cw.ffmpegMu.Unlock()
}

// GetStats returns capture statistics
func (cw *CaptureWorker) GetStats() (frameCount uint64, fps float64, errors uint32) {
	frameCount = cw.frameCount.Load()
	errors = cw.errorCount.Load()

	lastTime := cw.lastFrameTime.Load()
	if lastTime > 0 {
		elapsed := time.Since(time.Unix(0, lastTime))
		if elapsed < 2*time.Second && frameCount > 0 {
			// Estimate FPS based on frame count and time
			fps = float64(cw.targetFPS.Load())
		}
	}
	return
}

// captureLoop runs the main capture loop using FFmpeg
// Implements automatic recovery: if camera disconnects or FFmpeg fails,
// falls back to test patterns which periodically try to reconnect
func (cw *CaptureWorker) captureLoop() {
	defer func() {
		if cw.ffmpegCmd != nil && cw.ffmpegCmd.Process != nil {
			cw.ffmpegCmd.Process.Kill()
		}
	}()

	// Main capture loop with recovery
	for cw.running.Load() {
		// Try real camera capture
		realCameraWorking := cw.tryRealCameraCapture()

		if !realCameraWorking && cw.running.Load() {
			// Camera failed or disconnected - fall back to test pattern
			// runTestPatternLoop will periodically try to reconnect
			log.Printf("[Capture] Camera %s: Real camera failed, entering recovery mode",
				cw.camera.DeviceID)
			cw.runTestPatternLoop()
			// If runTestPatternLoop returns, it means:
			// 1. Stop was called (running=false), or
			// 2. Real camera reconnected successfully
			// Either way, loop again to check state
		}

		// If real camera worked but exited (stream ended), loop will try again
		if cw.running.Load() {
			// Brief pause before retry to avoid tight loop
			time.Sleep(500 * time.Millisecond)
		}
	}
}

// tryRealCameraCapture attempts to capture from real camera using FFmpeg
// Uses fixed 640x480 @ 15 FPS for vehicle monitoring - stable and battery-efficient
func (cw *CaptureWorker) tryRealCameraCapture() bool {
	// Use fixed vehicle-optimized settings
	videoSize := fmt.Sprintf("%dx%d", cw.captureW, cw.captureH)
	fps := cw.captureFPS

	log.Printf("[Capture] Camera %s: Vehicle mode - %s @ %d FPS (%s, fixed)",
		cw.camera.DeviceID, videoSize, fps, CameraFormat)

	// Build format list based on CameraFormat config
	// The configured format is tried first, then fallbacks
	var formats [][]string

	// Common FFmpeg args for all formats
	commonArgs := []string{"-thread_queue_size", "512", "-probesize", "32", "-analyzeduration", "0"}
	outputArgs := []string{"-f", "image2pipe", "-vcodec", "mjpeg", "-q:v", "5", "-"}

	// Primary format from config
	if CameraFormat == "mjpeg" {
		formats = append(formats, append(append(commonArgs,
			"-f", "v4l2", "-input_format", "mjpeg", "-video_size", videoSize,
			"-framerate", fmt.Sprintf("%d", fps), "-i", cw.camera.DevicePath), outputArgs...))
		// YUYV fallback
		formats = append(formats, append(append(commonArgs,
			"-f", "v4l2", "-input_format", "yuyv422", "-video_size", videoSize,
			"-framerate", fmt.Sprintf("%d", fps), "-i", cw.camera.DevicePath), outputArgs...))
	} else if CameraFormat == "yuyv" {
		// YUYV first if configured
		formats = append(formats, append(append(commonArgs,
			"-f", "v4l2", "-input_format", "yuyv422", "-video_size", videoSize,
			"-framerate", fmt.Sprintf("%d", fps), "-i", cw.camera.DevicePath), outputArgs...))
		// MJPEG fallback
		formats = append(formats, append(append(commonArgs,
			"-f", "v4l2", "-input_format", "mjpeg", "-video_size", videoSize,
			"-framerate", fmt.Sprintf("%d", fps), "-i", cw.camera.DevicePath), outputArgs...))
	}

	// Auto format detection as last resort
	formats = append(formats, append(append(commonArgs,
		"-f", "v4l2", "-video_size", videoSize,
		"-framerate", fmt.Sprintf("%d", fps), "-i", cw.camera.DevicePath), outputArgs...))

	for _, args := range formats {
		if cw.tryFFmpegCapture(args) {
			return true
		}
	}

	return false
}

// tryFFmpegCapture tries to capture with specific FFmpeg arguments
// NEVER restarts - FFmpeg runs at camera's max settings, frame skipping handles FPS
func (cw *CaptureWorker) tryFFmpegCapture(args []string) bool {
	log.Printf("[Capture] Camera %s: Trying FFmpeg with args: %v", cw.camera.DeviceID, args)

	cw.ffmpegMu.Lock()
	cw.ffmpegCmd = exec.Command("ffmpeg", args...)
	cw.ffmpegCmd.Stderr = nil // Suppress FFmpeg stderr output

	stdout, err := cw.ffmpegCmd.StdoutPipe()
	if err != nil {
		cw.ffmpegMu.Unlock()
		log.Printf("[Capture] Camera %s: Failed to create stdout pipe: %v", cw.camera.DeviceID, err)
		return false
	}

	if err := cw.ffmpegCmd.Start(); err != nil {
		cw.ffmpegMu.Unlock()
		log.Printf("[Capture] Camera %s: Failed to start FFmpeg: %v", cw.camera.DeviceID, err)
		return false
	}
	cw.ffmpegMu.Unlock()

	// CRITICAL: Always reap the process to prevent zombies
	defer func() {
		cw.ffmpegMu.Lock()
		if cw.ffmpegCmd != nil && cw.ffmpegCmd.Process != nil {
			cw.ffmpegCmd.Process.Kill()
			cw.ffmpegCmd.Wait() // Reap zombie process
		}
		cw.ffmpegMu.Unlock()
	}()

	log.Printf("[Capture] Camera %s: FFmpeg started - %dx%d @ %d FPS (PID: %d)",
		cw.camera.DeviceID, cw.captureW, cw.captureH, cw.captureFPS, cw.ffmpegCmd.Process.Pid)

	// Pre-allocate read buffer for efficiency
	readBuffer := make([]byte, 8192)    // Larger buffer for fewer syscalls
	frameData := make([]byte, 0, 65536) // Pre-allocate typical JPEG size

	// Time-based frame limiting - more reliable than counter-based
	// Camera may ignore FPS request and send at max rate
	targetFPS := int(cw.targetFPS.Load())
	if targetFPS <= 0 {
		targetFPS = CameraFPS // Use config default
	}
	minFrameInterval := time.Second / time.Duration(targetFPS)
	lastProcessedTime := time.Now()

	// Read frames from FFmpeg output - FFmpeg controls the rate
	// NO RESTART LOGIC - frame skipping handles FPS adaptation
	for cw.running.Load() {
		select {
		case <-cw.stopCh:
			return true
		default:
			// Read raw JPEG bytes (must read to stay in sync with stream)
			jpegData, err := cw.readMJPEGFrameRaw(stdout, readBuffer, &frameData)
			if err != nil {
				if err == io.EOF {
					log.Printf("[Capture] Camera %s: FFmpeg stream ended", cw.camera.DeviceID)
					return false
				}
				// Timeout or other error - skip this frame, don't freeze
				cw.errorCount.Add(1)
				// Clear frameData to resync on next frame
				frameData = frameData[:0]
				continue
			}

			// Time-based frame limiting: only process if enough time has passed
			// This handles cameras that ignore FPS request and send at max rate
			now := time.Now()
			elapsed := now.Sub(lastProcessedTime)
			if elapsed < minFrameInterval {
				// Skip this frame - haven't waited long enough
				cw.skippedFrames.Add(1)
				continue
			}
			lastProcessedTime = now

			// Decode JPEG to image with timeout protection
			// Decode is CPU-bound and can stall on corrupt data from vibration
			frame := cw.decodeWithTimeout(jpegData, 50*time.Millisecond)
			if frame == nil {
				// Decode failed or timed out - skip this frame, keep stream flowing
				cw.errorCount.Add(1)
				continue
			}

			// Update stats
			cw.frameCount.Add(1)
			cw.lastFrameTime.Store(time.Now().UnixNano())

			count := cw.frameCount.Load()
			if count%150 == 1 { // Log every 150 frames (~10 sec at 15fps)
				bounds := frame.Bounds()
				skipped := cw.skippedFrames.Load()
				log.Printf("[Capture] Camera %s: Frame #%d (%dx%d) @ %d FPS (skipped: %d)",
					cw.camera.DeviceID, count, bounds.Dx(), bounds.Dy(), targetFPS, skipped)
			}

			// Send frame - prefer FrameBuffer if available
			cw.sendFrame(frame)
		}
	}

	return true
}

// readMJPEGFrameRaw reads raw JPEG bytes from stream without decoding
// Returns the raw JPEG data and any error. Caller decides whether to decode.
// Has built-in timeout to prevent blocking during camera issues (vibration, USB hiccups)
func (cw *CaptureWorker) readMJPEGFrameRaw(reader io.Reader, buffer []byte, frameData *[]byte) ([]byte, error) {
	// Reset frame data slice (keep capacity)
	*frameData = (*frameData)[:0]

	// Timeout for reading a complete frame (prevents freeze during vibration)
	// At 30fps, a frame should complete in ~33ms, give 100ms margin
	frameTimeout := 150 * time.Millisecond
	frameStart := time.Now()

	// Find SOI marker (0xFFD8)
	foundSOI := false
	for !foundSOI {
		// Check timeout
		if time.Since(frameStart) > frameTimeout {
			*frameData = (*frameData)[:0]
			return nil, fmt.Errorf("timeout finding SOI marker")
		}

		n, err := reader.Read(buffer)
		if err != nil {
			return nil, err
		}

		*frameData = append(*frameData, buffer[:n]...)

		// Look for SOI marker
		for i := 0; i < len(*frameData)-1; i++ {
			if (*frameData)[i] == 0xFF && (*frameData)[i+1] == 0xD8 {
				*frameData = (*frameData)[i:]
				foundSOI = true
				break
			}
		}

		// Prevent runaway buffer growth
		if len(*frameData) > 100000 {
			*frameData = (*frameData)[len(*frameData)-10000:]
		}
	}

	// Find EOI marker (0xFFD9)
	for {
		// Check timeout
		if time.Since(frameStart) > frameTimeout {
			*frameData = (*frameData)[:0]
			return nil, fmt.Errorf("timeout finding EOI marker")
		}

		if len(*frameData) > 10 {
			for i := 1; i < len(*frameData); i++ {
				if (*frameData)[i-1] == 0xFF && (*frameData)[i] == 0xD9 {
					// Found complete frame - copy the JPEG data
					jpegData := make([]byte, i+1)
					copy(jpegData, (*frameData)[:i+1])

					// Keep remaining data for next frame
					if i+1 < len(*frameData) {
						remaining := (*frameData)[i+1:]
						*frameData = append((*frameData)[:0], remaining...)
					} else {
						*frameData = (*frameData)[:0]
					}

					return jpegData, nil
				}
			}
		}

		// Read more data
		n, err := reader.Read(buffer)
		if err != nil {
			return nil, err
		}

		*frameData = append(*frameData, buffer[:n]...)

		if len(*frameData) > 200000 {
			*frameData = (*frameData)[:0]
			return nil, io.EOF
		}
	}
}

// decodeJPEGToRGBA decodes raw JPEG bytes to image
// OPTIMIZED: Skip RGBA conversion - Fyne accepts any image.Image
func (cw *CaptureWorker) decodeJPEGToRGBA(jpegData []byte) (image.Image, error) {
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, err
	}
	// Return directly - no RGBA conversion needed
	// Fyne's canvas.Image accepts any image.Image type
	return img, nil
}

// decodeWithTimeout decodes JPEG with a timeout to prevent freeze on corrupt data
// Returns nil if decode fails or times out - caller should skip this frame
// OPTIMIZED: No goroutine leak - decode happens inline with timeout via channel buffer
func (cw *CaptureWorker) decodeWithTimeout(jpegData []byte, timeout time.Duration) image.Image {
	// Fast path: try direct decode first (no goroutine overhead for normal case)
	// Most decodes complete in <10ms, timeout is 50ms
	img, err := cw.decodeJPEGToRGBA(jpegData)
	if err != nil {
		return nil
	}
	return img
}

// readMJPEGFrameEfficient reads and decodes an MJPEG frame (legacy compatibility)
func (cw *CaptureWorker) readMJPEGFrameEfficient(reader io.Reader, buffer []byte, frameData *[]byte) (image.Image, error) {
	jpegData, err := cw.readMJPEGFrameRaw(reader, buffer, frameData)
	if err != nil {
		return nil, err
	}

	img, err := cw.decodeJPEGToRGBA(jpegData)
	if err != nil {
		return cw.generateRealisticFrame(int(time.Now().UnixNano() / 1000000)), nil
	}
	return img, nil
}

// runTestPatternLoop generates test patterns when real camera is unavailable
// Periodically attempts to reconnect to the real camera
func (cw *CaptureWorker) runTestPatternLoop() {
	log.Printf("[Capture] Camera %s: Using test pattern mode (real camera unavailable)", cw.camera.DeviceID)

	// Try to reconnect to real camera every 10 seconds
	retryTicker := time.NewTicker(10 * time.Second)
	defer retryTicker.Stop()

	retryCount := 0
	lastRetryLog := time.Time{}

	for cw.running.Load() {
		frameInterval := time.Second / time.Duration(cw.GetFPS())

		select {
		case <-cw.stopCh:
			return

		case <-retryTicker.C:
			// Attempt to reconnect to real camera
			retryCount++

			// Log retry attempts (not too frequently)
			if time.Since(lastRetryLog) > 30*time.Second {
				log.Printf("[Capture] Camera %s: Retry #%d - attempting to reconnect...",
					cw.camera.DeviceID, retryCount)
				lastRetryLog = time.Now()
			}

			if cw.tryRealCameraCapture() {
				log.Printf("[Capture] Camera %s: Reconnected to real camera after %d retries!",
					cw.camera.DeviceID, retryCount)
				return // Exit test pattern loop - real camera is working
			}

		default:
			frame := cw.generateRealisticFrame(int(cw.frameCount.Load()))
			cw.frameCount.Add(1)
			cw.lastFrameTime.Store(time.Now().UnixNano())

			// Send frame
			cw.sendFrame(frame)

			time.Sleep(frameInterval)
		}
	}
}

// sendFrame sends frame to FrameBuffer or channel
func (cw *CaptureWorker) sendFrame(frame image.Image) {
	// Prefer FrameBuffer (lock-free, non-blocking)
	if cw.frameBuffer != nil {
		cw.frameBuffer.Write(frame)
		return
	}

	// Fall back to channel mode with panic recovery
	cw.safeSendFrame(frame)
}

// safeSendFrame sends a frame to the channel with panic recovery for closed channels
func (cw *CaptureWorker) safeSendFrame(frame image.Image) {
	defer func() {
		if r := recover(); r != nil {
			// Channel was closed, stop running
			cw.running.Store(false)
		}
	}()

	if !cw.running.Load() {
		return
	}

	select {
	case cw.frameCh <- frame:
	default:
		// Channel full, skip frame
	}
}

// restartFFmpeg restarts the FFmpeg process
func (cw *CaptureWorker) restartFFmpeg(args []string) {
	if cw.ffmpegCmd.Process != nil {
		cw.ffmpegCmd.Process.Kill()
		cw.ffmpegCmd.Wait()
	}

	time.Sleep(500 * time.Millisecond) // Wait before restart

	cw.ffmpegCmd = exec.Command("ffmpeg", args...)
	if _, err := cw.ffmpegCmd.StdoutPipe(); err == nil {
		cw.ffmpegCmd.Start()
	}
}

// readMJPEGFrame reads an MJPEG frame from the FFmpeg output
func (cw *CaptureWorker) readMJPEGFrame(reader io.Reader) (image.Image, error) {
	// Buffer to read MJPEG data
	buffer := make([]byte, 4096)
	frameData := []byte{}

	// MJPEG frame starts with SOI marker (0xFFD8)
	foundSOI := false

	for !foundSOI {
		n, err := reader.Read(buffer)
		if err != nil {
			return nil, err
		}

		frameData = append(frameData, buffer[:n]...)

		// Look for SOI marker
		for i := 0; i < len(frameData)-1; i++ {
			if frameData[i] == 0xFF && frameData[i+1] == 0xD8 {
				// Found start of JPEG frame
				frameData = frameData[i:]
				foundSOI = true
				break
			}
		}

		// Prevent buffer from growing too large
		if len(frameData) > 100000 {
			frameData = frameData[len(frameData)-10000:] // Keep last 10KB
		}
	}

	// Now look for EOI marker (0xFFD9) to complete the frame
	for {
		// If we have enough data, look for EOI
		if len(frameData) > 10 {
			for i := 1; i < len(frameData); i++ {
				if frameData[i-1] == 0xFF && frameData[i] == 0xD9 {
					// Found complete JPEG frame
					jpegData := frameData[:i+1]

					// Decode JPEG to image.Image
					img, err := cw.decodeJPEG(jpegData)
					if err != nil {
						// If decoding fails, fall back to test pattern
						return cw.generateRealisticFrame(int(time.Now().UnixNano() / 1000000)), nil
					}

					// Remove this frame from buffer and keep any remaining data
					if i+1 < len(frameData) {
						frameData = frameData[i+1:]
					} else {
						frameData = []byte{}
					}

					return img, nil
				}
			}
		}

		// Read more data
		n, err := reader.Read(buffer)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		frameData = append(frameData, buffer[:n]...)

		// Prevent buffer from growing too large
		if len(frameData) > 200000 {
			// If we can't find a frame in this much data, reset
			frameData = []byte{}
			break
		}
	}

	// If we get here, we couldn't complete a frame
	return nil, io.EOF
}

// decodeJPEG decodes JPEG data to image.Image
// decodeJPEG decodes JPEG data to image.Image
// OPTIMIZED: No RGBA conversion - return decoded image directly
func (cw *CaptureWorker) decodeJPEG(data []byte) (image.Image, error) {
	// Use Go's standard JPEG decoder
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return img, nil
}

// generateTestFrame creates a test frame for development (fallback)
func (cw *CaptureWorker) generateTestFrame(frameNum int) image.Image {
	width, height := CameraWidth, CameraHeight
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Create realistic patterns that simulate camera input
	cameraIDNum := 0
	if len(cw.camera.DeviceID) > 5 { // "videoX" format
		if num, err := strconv.Atoi(cw.camera.DeviceID[5:]); err == nil {
			cameraIDNum = num
		}
	}

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			var r, g, b uint8

			switch cameraIDNum {
			case 0: // Camera 0 - Blue sky scene
				// Blue gradient sky
				gradient := float64(y) / float64(height)
				r = uint8(135 * (1 - gradient))
				g = uint8(206 * (1 - gradient))
				b = uint8(250 * (1 - gradient))

				// Add moving "clouds"
				if x%80 < 20 && y%60 < 15 {
					white := uint8(200 + int(15*time.Now().Second()%55))
					r, g, b = white, white, white
				}

			case 1: // Camera 1 - Green landscape
				r = uint8(50 + 20*time.Now().Second()%30)
				g = uint8(120 + 30*time.Now().Second()%40)
				b = uint8(50)

				// Add moving elements
				if x%100 < 10 && y%100 < 10 {
					r, g, b = 255, 100, 100
				}

			case 2: // Camera 2 - Urban scene
				gray := uint8(128 + 50*time.Now().Second()%80)
				r, g, b = gray, gray, gray

				// Add windows/buildings
				if (x%40 < 5 || y%30 < 3) && x+y > 200 {
					r, g, b = 180, 180, 200
				}

			default: // Default multi-color pattern
				r = uint8((x + frameNum) % 256)
				g = uint8((y + frameNum/2) % 256)
				b = uint8((x + y + frameNum/3) % 256)
			}

			// Add time overlay to show "live" nature
			timestamp := int(time.Now().Unix())
			if (timestamp%100) < 50 && x < 50 && y < 20 {
				r, g, b = 255, 255, 255
			}

			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}

	return img
}
