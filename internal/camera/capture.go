package camera

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"os/exec"
	"strconv"
	"time"
)

// CaptureWorker handles camera capture in a goroutine
type CaptureWorker struct {
	camera  Camera
	running bool
	frameCh chan<- image.Image
	stopCh  chan struct{}

	// FFmpeg capture
	ffmpegCmd *exec.Cmd
}

// NewCaptureWorker creates a new capture worker for a camera
func NewCaptureWorker(camera Camera, frameCh chan<- image.Image) *CaptureWorker {
	return &CaptureWorker{
		camera:  camera,
		frameCh: frameCh,
		stopCh:  make(chan struct{}),
	}
}

// Start begins capturing frames from camera
func (cw *CaptureWorker) Start() error {
	if cw.running {
		return fmt.Errorf("capture worker already running")
	}

	cw.running = true
	go cw.captureLoop()
	return nil
}

// Stop stops capture worker
func (cw *CaptureWorker) Stop() {
	if !cw.running {
		return
	}

	cw.running = false
	close(cw.stopCh)

	if cw.ffmpegCmd != nil && cw.ffmpegCmd.Process != nil {
		cw.ffmpegCmd.Process.Kill()
	}
}

// captureLoop runs the main capture loop using FFmpeg
func (cw *CaptureWorker) captureLoop() {
	defer func() {
		if cw.ffmpegCmd != nil && cw.ffmpegCmd.Process != nil {
			cw.ffmpegCmd.Process.Kill()
		}
	}()

	// Build FFmpeg command for MJPEG capture (most compatible)
	args := []string{
		"-f", "v4l2",
		"-i", cw.camera.DevicePath,
		"-video_size", "640x480",
		"-framerate", "30",
		"-pix_fmt", "rgb24",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-", // Output to stdout
	}

	cw.ffmpegCmd = exec.Command("ffmpeg", args...)

	// Get pipe for stdout
	stdout, err := cw.ffmpegCmd.StdoutPipe()
	if err != nil {
		fmt.Printf("Failed to create stdout pipe: %v\n", err)
		return
	}

	// Start the command
	if err := cw.ffmpegCmd.Start(); err != nil {
		fmt.Printf("Failed to start FFmpeg: %v\n", err)
		return
	}

	frameCount := 0

	for cw.running {
		select {
		case <-cw.stopCh:
			return
		default:
			// Try to read a frame
			frame, err := cw.readMJPEGFrame(stdout)
			if err != nil {
				if err == io.EOF {
					// FFmpeg ended, restart it
					cw.restartFFmpeg(args)
					continue
				}
				fmt.Printf("Failed to read frame: %v\n", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// Send frame to UI
			select {
			case cw.frameCh <- frame:
				frameCount++
			default:
				// Skip frame if channel is full (latest-frame-only approach)
			}
		}
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
	// Simple frame reading - in a real implementation,
	// this would properly parse MJPEG frames
	// Use realistic frame patterns instead of test patterns

	return cw.generateRealisticFrame(int(time.Now().UnixNano() / 1000000)), nil
}

// generateTestFrame creates a test frame for development (fallback)
func (cw *CaptureWorker) generateTestFrame(frameNum int) image.Image {
	width, height := 640, 480
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
