package camera

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// CaptureWorker handles camera capture in a goroutine
type CaptureWorker struct {
	camera   Camera
	settings Settings // Camera settings from config
	running  atomic.Bool
	stopCh   chan struct{}
	wg       sync.WaitGroup // Tracks capture goroutine for clean shutdown

	// Frame output
	frameBuffer *FrameBuffer // Buffer mode for decoupled capture/render

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
}

// NewCaptureWorkerWithBuffer creates a capture worker using FrameBuffer
func NewCaptureWorkerWithBuffer(camera Camera, buffer *FrameBuffer, s Settings) *CaptureWorker {
	capW := camera.Capabilities.MaxWidth
	capH := camera.Capabilities.MaxHeight
	capFPS := camera.Capabilities.MaxFPS

	// Ensure we have valid defaults from settings
	if capW == 0 {
		capW = s.Width
	}
	if capH == 0 {
		capH = s.Height
	}
	if capFPS == 0 {
		capFPS = s.FPS
	}

	cw := &CaptureWorker{
		camera:      camera,
		settings:    s,
		frameBuffer: buffer,
		stopCh:      make(chan struct{}),
		captureW:    capW,
		captureH:    capH,
		captureFPS:  capFPS,
	}
	cw.targetFPS.Store(int32(capFPS))
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
	cw.wg.Add(1)
	go func() {
		defer cw.wg.Done()
		cw.captureLoop()
	}()
	return nil
}

// Stop stops capture worker and waits for goroutine to exit
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

	// Kill FFmpeg immediately to unblock any reads
	cw.ffmpegMu.Lock()
	if cw.ffmpegCmd != nil && cw.ffmpegCmd.Process != nil {
		cw.ffmpegCmd.Process.Kill()
		cw.ffmpegCmd.Wait() // Reap zombie process
	}
	cw.ffmpegMu.Unlock()

	// Wait for capture goroutine to fully exit (with timeout)
	done := make(chan struct{})
	go func() {
		cw.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// Goroutine exited cleanly
	case <-time.After(2 * time.Second):
		log.Printf("[Capture] %s: Warning - goroutine did not exit within 2s", cw.camera.DeviceID)
	}
}

// Restart stops the worker and starts it again with a fresh stopCh
// Used for hot-plug recovery without recreating the entire manager
func (cw *CaptureWorker) Restart() error {
	log.Printf("[Capture] %s: Restarting worker...", cw.camera.DeviceID)

	// Stop waits for goroutine to fully exit
	cw.Stop()

	// Reset stopCh (old one is closed)
	cw.stopCh = make(chan struct{})

	// Reset stats
	cw.frameCount.Store(0)
	cw.errorCount.Store(0)
	cw.skippedFrames.Store(0)

	// Start again
	return cw.Start()
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
		cw.ffmpegMu.Lock()
		if cw.ffmpegCmd != nil && cw.ffmpegCmd.Process != nil {
			cw.ffmpegCmd.Process.Kill()
		}
		cw.ffmpegMu.Unlock()
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
func (cw *CaptureWorker) tryRealCameraCapture() bool {
	videoSize := fmt.Sprintf("%dx%d", cw.captureW, cw.captureH)
	fps := cw.captureFPS
	format := cw.settings.Format

	log.Printf("[Capture] Camera %s: Vehicle mode - %s @ %d FPS (%s, fixed)",
		cw.camera.DeviceID, videoSize, fps, format)

	// Build format list based on configured format
	// The configured format is tried first, then fallbacks
	var formats [][]string

	// Common FFmpeg args for all formats
	commonArgs := []string{"-thread_queue_size", "512", "-probesize", "32", "-analyzeduration", "0"}
	outputArgs := []string{"-f", "image2pipe", "-vcodec", "mjpeg", "-q:v", "5", "-"}

	// buildArgs safely constructs FFmpeg args without mutating commonArgs/outputArgs.
	// Using append(append(commonArgs, ...), outputArgs...) would corrupt commonArgs
	// on subsequent calls if the first append didn't grow the backing array.
	buildArgs := func(inputArgs ...string) []string {
		args := make([]string, 0, len(commonArgs)+len(inputArgs)+len(outputArgs))
		args = append(args, commonArgs...)
		args = append(args, inputArgs...)
		args = append(args, outputArgs...)
		return args
	}

	fpsStr := fmt.Sprintf("%d", fps)

	// Primary format from config
	if format == "mjpeg" {
		formats = append(formats, buildArgs(
			"-f", "v4l2", "-input_format", "mjpeg", "-video_size", videoSize,
			"-framerate", fpsStr, "-i", cw.camera.DevicePath))
		// YUYV fallback
		formats = append(formats, buildArgs(
			"-f", "v4l2", "-input_format", "yuyv422", "-video_size", videoSize,
			"-framerate", fpsStr, "-i", cw.camera.DevicePath))
	} else if format == "yuyv" {
		// YUYV first if configured
		formats = append(formats, buildArgs(
			"-f", "v4l2", "-input_format", "yuyv422", "-video_size", videoSize,
			"-framerate", fpsStr, "-i", cw.camera.DevicePath))
		// MJPEG fallback
		formats = append(formats, buildArgs(
			"-f", "v4l2", "-input_format", "mjpeg", "-video_size", videoSize,
			"-framerate", fpsStr, "-i", cw.camera.DevicePath))
	}

	// Auto format detection as last resort
	formats = append(formats, buildArgs(
		"-f", "v4l2", "-video_size", videoSize,
		"-framerate", fpsStr, "-i", cw.camera.DevicePath))

	for _, args := range formats {
		if !cw.running.Load() {
			return false // Shutting down, don't try more formats
		}
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

	lastProcessedTime := time.Now()

	// Read frames from FFmpeg output - FFmpeg controls the rate
	// NO RESTART LOGIC - frame skipping handles FPS adaptation
	for cw.running.Load() {
		select {
		case <-cw.stopCh:
			return true
		default:
			// Re-read targetFPS each iteration so SetFPS() changes take effect
			targetFPS := int(cw.targetFPS.Load())
			if targetFPS <= 0 {
				targetFPS = cw.settings.FPS
			}
			minFrameInterval := time.Second / time.Duration(targetFPS)

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

			// Decode JPEG to image
			frame := cw.decodeJPEG(jpegData)
			if frame == nil {
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
	// Scale with FPS: at 30fps a frame is ~33ms, at 5fps ~200ms; add generous margin
	fps := int(cw.targetFPS.Load())
	if fps <= 0 {
		fps = cw.settings.FPS
	}
	frameTimeout := time.Duration(float64(time.Second)/float64(fps)*3) + 50*time.Millisecond
	if frameTimeout < 150*time.Millisecond {
		frameTimeout = 150 * time.Millisecond
	}
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
	// Track last-scanned position to avoid O(n^2) rescanning on each Read()
	scanFrom := 1
	for {
		// Check timeout
		if time.Since(frameStart) > frameTimeout {
			*frameData = (*frameData)[:0]
			return nil, fmt.Errorf("timeout finding EOI marker")
		}

		if len(*frameData) > 10 {
			for i := scanFrom; i < len(*frameData); i++ {
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
			// Next time, start scanning from where we left off minus 1
			// (minus 1 because the EOI marker spans two bytes)
			scanFrom = len(*frameData) - 1
			if scanFrom < 1 {
				scanFrom = 1
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

// decodeJPEG decodes raw JPEG bytes to image
// Returns nil on decode failure - caller should skip this frame
func (cw *CaptureWorker) decodeJPEG(jpegData []byte) image.Image {
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil
	}
	return img
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
			frame := cw.generateTestFrame(int(cw.frameCount.Load()))
			cw.frameCount.Add(1)
			cw.lastFrameTime.Store(time.Now().UnixNano())

			// Send frame
			cw.sendFrame(frame)

			time.Sleep(frameInterval)
		}
	}
}

// sendFrame sends frame to FrameBuffer
func (cw *CaptureWorker) sendFrame(frame image.Image) {
	if cw.frameBuffer != nil {
		cw.frameBuffer.Write(frame)
	}
}

// generateTestFrame creates a test frame for development (fallback)
func (cw *CaptureWorker) generateTestFrame(frameNum int) image.Image {
	width, height := cw.settings.Width, cw.settings.Height
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	stride := img.Stride

	// Create realistic patterns that simulate camera input
	cameraIDNum := 0
	if len(cw.camera.DeviceID) > 5 { // "videoX" format
		if num, err := strconv.Atoi(cw.camera.DeviceID[5:]); err == nil {
			cameraIDNum = num
		}
	}

	// Cache time.Now() once per frame instead of per-pixel
	now := time.Now()
	sec := now.Second()
	timestamp := int(now.Unix())

	for y := 0; y < height; y++ {
		rowOff := y * stride
		for x := 0; x < width; x++ {
			var r, g, b uint8

			switch cameraIDNum {
			case 0: // Camera 0 - Blue sky scene
				gradient := float64(y) / float64(height)
				r = uint8(135 * (1 - gradient))
				g = uint8(206 * (1 - gradient))
				b = uint8(250 * (1 - gradient))

				if x%80 < 20 && y%60 < 15 {
					white := uint8(200 + int(15*sec%55))
					r, g, b = white, white, white
				}

			case 1: // Camera 1 - Green landscape
				r = uint8(50 + 20*sec%30)
				g = uint8(120 + 30*sec%40)
				b = uint8(50)

				if x%100 < 10 && y%100 < 10 {
					r, g, b = 255, 100, 100
				}

			case 2: // Camera 2 - Urban scene
				gray := uint8(128 + 50*sec%80)
				r, g, b = gray, gray, gray

				if (x%40 < 5 || y%30 < 3) && x+y > 200 {
					r, g, b = 180, 180, 200
				}

			default: // Default multi-color pattern
				r = uint8((x + frameNum) % 256)
				g = uint8((y + frameNum/2) % 256)
				b = uint8((x + y + frameNum/3) % 256)
			}

			// Add time overlay to show "live" nature
			if (timestamp%100) < 50 && x < 50 && y < 20 {
				r, g, b = 255, 255, 255
			}

			off := rowOff + x*4
			img.Pix[off+0] = r
			img.Pix[off+1] = g
			img.Pix[off+2] = b
			img.Pix[off+3] = 255
		}
	}

	return img
}
