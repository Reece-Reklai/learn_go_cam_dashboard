package camera

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// CameraCapabilities holds the camera's maximum capabilities
type CameraCapabilities struct {
	MaxWidth  int
	MaxHeight int
	MaxFPS    int
	Format    string // "mjpeg" or "yuyv"
}

// Camera represents a camera device
type Camera struct {
	DeviceID     string
	DevicePath   string
	Name         string
	Available    bool
	Capabilities CameraCapabilities
}

// DiscoverCameras finds all available USB camera devices on Linux
func DiscoverCameras() ([]Camera, error) {
	log.Println("[Discovery] Starting camera discovery...")
	var cameras []Camera

	// Use v4l2-ctl to get actual video capture devices
	cmd := exec.Command("v4l2-ctl", "--list-devices")
	output, err := cmd.Output()
	if err != nil {
		log.Printf("[Discovery] v4l2-ctl failed: %v, falling back to simple discovery", err)
		// Fall back to simple discovery
		return discoverCamerasSimple()
	}

	log.Printf("[Discovery] v4l2-ctl output:\n%s", string(output))

	// Parse v4l2-ctl output to find USB cameras - first pass: just find devices
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	var currentName string
	var devices []string
	var devicePaths []struct {
		path string
		name string
	}

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "\t") && line != "" {
			// This is a device name line
			if currentName != "" && len(devices) > 0 {
				// Check if previous device was a USB camera
				if isUSBCamera(currentName) {
					devicePaths = append(devicePaths, struct {
						path string
						name string
					}{devices[0], currentName})
				}
			}
			currentName = line
			devices = []string{}
		} else if strings.HasPrefix(line, "\t") {
			// This is a device path
			device := strings.TrimSpace(line)
			if strings.HasPrefix(device, "/dev/video") {
				devices = append(devices, device)
			}
		}
	}

	// Handle last device
	if currentName != "" && len(devices) > 0 {
		if isUSBCamera(currentName) {
			devicePaths = append(devicePaths, struct {
				path string
				name string
			}{devices[0], currentName})
		}
	}

	// Limit to 3 cameras
	if len(devicePaths) > 3 {
		devicePaths = devicePaths[:3]
	}

	numCameras := len(devicePaths)
	log.Printf("[Discovery] Found %d USB cameras, querying capabilities...", numCameras)

	// Second pass: query capabilities with camera count for optimal resolution
	for _, dev := range devicePaths {
		cam := Camera{
			DeviceID:   filepath.Base(dev.path),
			DevicePath: dev.path,
			Name:       cleanCameraName(dev.name),
			Available:  true,
		}
		cam.Capabilities = queryCameraCapabilities(dev.path, numCameras)
		cameras = append(cameras, cam)
	}

	// Sort cameras by device number
	sort.Slice(cameras, func(i, j int) bool {
		numI := extractVideoNumber(cameras[i].DeviceID)
		numJ := extractVideoNumber(cameras[j].DeviceID)
		return numI < numJ
	})

	// If no cameras found, fall back to simple discovery
	if len(cameras) == 0 {
		log.Println("[Discovery] No USB cameras found, falling back to simple discovery")
		return discoverCamerasSimple()
	}

	log.Printf("[Discovery] Found %d cameras", len(cameras))
	for _, cam := range cameras {
		log.Printf("[Discovery]   %s: %dx%d @ %dfps (%s)",
			cam.DeviceID, cam.Capabilities.MaxWidth, cam.Capabilities.MaxHeight,
			cam.Capabilities.MaxFPS, cam.Capabilities.Format)
	}
	return cameras, nil
}

// isUSBCamera checks if the device name indicates a USB camera
func isUSBCamera(name string) bool {
	nameLower := strings.ToLower(name)
	// Include USB cameras, exclude Pi ISP and decoder devices
	if strings.Contains(nameLower, "usb") {
		return true
	}
	if strings.Contains(nameLower, "camera") && !strings.Contains(nameLower, "pispbe") {
		return true
	}
	if strings.Contains(nameLower, "webcam") {
		return true
	}
	return false
}

// cleanCameraName cleans up the camera name
func cleanCameraName(name string) string {
	// Remove trailing colon and parenthetical info
	name = strings.TrimSuffix(name, ":")
	if idx := strings.Index(name, "("); idx > 0 {
		name = strings.TrimSpace(name[:idx])
	}
	return name
}

// extractVideoNumber extracts the number from "videoX"
func extractVideoNumber(deviceID string) int {
	numStr := strings.TrimPrefix(deviceID, "video")
	num, _ := strconv.Atoi(numStr)
	return num
}

// ResolutionPreset defines preferred resolutions for different scenarios
type ResolutionPreset struct {
	Width    int
	Height   int
	Priority int // Higher = better match
}

// Vehicle-optimized fixed resolution
// Settings are defined in config.go - edit that file to change resolution/FPS
// Run: ./camera-dashboard --query-cameras to see what your cameras support

// getOptimalResolution returns the configured resolution from config.go
// Falls back to closest available if camera doesn't support exact resolution
func getOptimalResolution(availableResolutions []ResolutionPreset, numCameras int) (int, int) {
	// Use settings from config.go
	targetW, targetH := CameraWidth, CameraHeight

	// Check if exact resolution is available
	for _, res := range availableResolutions {
		if res.Width == targetW && res.Height == targetH {
			return targetW, targetH
		}
	}

	// Fallback: find closest resolution
	bestW, bestH := targetW, targetH
	bestDiff := 999999

	for _, res := range availableResolutions {
		diff := abs(res.Width-targetW) + abs(res.Height-targetH)
		if diff < bestDiff {
			bestDiff = diff
			bestW, bestH = res.Width, res.Height
		}
	}

	if bestDiff > 0 {
		log.Printf("[Discovery] Camera doesn't support %dx%d, using closest: %dx%d",
			targetW, targetH, bestW, bestH)
	}

	return bestW, bestH
}

// abs returns absolute value of integer
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// queryCameraCapabilities queries the camera's resolution and FPS capabilities
// Returns optimal settings based on camera, display, and Pi constraints
func queryCameraCapabilities(devicePath string, numCameras int) CameraCapabilities {
	// Use config.go values as defaults
	caps := CameraCapabilities{
		MaxWidth:  CameraWidth,
		MaxHeight: CameraHeight,
		MaxFPS:    CameraFPS,
		Format:    CameraFormat,
	}

	cmd := exec.Command("v4l2-ctl", "-d", devicePath, "--list-formats-ext")
	output, err := cmd.Output()
	if err != nil {
		log.Printf("[Discovery] Failed to query capabilities for %s: %v", devicePath, err)
		return caps
	}

	lines := strings.Split(string(output), "\n")
	inMJPEG := false

	// Regex patterns
	sizeRegex := regexp.MustCompile(`Size: Discrete (\d+)x(\d+)`)
	fpsRegex := regexp.MustCompile(`(\d+)\.(\d+) fps`)

	var availableResolutions []ResolutionPreset

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Check for MJPEG format section (preferred - low CPU)
		if strings.Contains(line, "'MJPG'") || strings.Contains(line, "Motion-JPEG") {
			inMJPEG = true
			continue
		}
		// Check for other format (exit MJPEG section)
		if strings.Contains(line, "'YUYV'") || strings.Contains(line, "'H264'") {
			inMJPEG = false
			continue
		}

		if inMJPEG {
			// Parse resolution
			if matches := sizeRegex.FindStringSubmatch(line); len(matches) == 3 {
				width, _ := strconv.Atoi(matches[1])
				height, _ := strconv.Atoi(matches[2])
				availableResolutions = append(availableResolutions, ResolutionPreset{
					Width:  width,
					Height: height,
				})
			}

			// Parse FPS - take the highest available
			if matches := fpsRegex.FindStringSubmatch(line); len(matches) == 3 {
				fps, _ := strconv.Atoi(matches[1])
				if fps > caps.MaxFPS {
					caps.MaxFPS = fps
				}
			}
		}
	}

	// Pick optimal resolution
	if len(availableResolutions) > 0 {
		caps.MaxWidth, caps.MaxHeight = getOptimalResolution(availableResolutions, numCameras)
	}

	// Limit FPS based on number of cameras to reduce USB bandwidth contention
	// USB 2.0 bandwidth is limited (~35MB/s real-world shared across all devices)
	// At 640x480 MJPEG, each frame is ~20-40KB, so 30fps = 0.6-1.2MB/s per camera
	// With 2+ cameras, we can get USB buffer overruns causing stream failures
	caps.MaxFPS = getOptimalFPS(caps.MaxFPS, numCameras)

	return caps
}

// getOptimalFPS returns the configured FPS from config.go
// Caps at camera's max FPS if camera doesn't support the configured value
func getOptimalFPS(cameraMaxFPS int, numCameras int) int {
	// Use settings from config.go
	targetFPS := CameraFPS

	if targetFPS <= cameraMaxFPS {
		return targetFPS
	}

	// Camera doesn't support configured FPS, use its max
	log.Printf("[Discovery] Camera max FPS (%d) is lower than configured (%d), using %d",
		cameraMaxFPS, targetFPS, cameraMaxFPS)
	return cameraMaxFPS
}

// discoverCamerasSimple is a fallback discovery method
func discoverCamerasSimple() ([]Camera, error) {
	var cameras []Camera
	var devicePaths []string

	// First pass: find devices
	for _, num := range []int{0, 2, 4} {
		devicePath := fmt.Sprintf("/dev/video%d", num)

		// Check if device exists
		if _, err := os.Stat(devicePath); os.IsNotExist(err) {
			continue
		}

		// Verify it's a video capture device using v4l2-ctl
		cmd := exec.Command("v4l2-ctl", "--device="+devicePath, "--info")
		output, err := cmd.Output()
		if err != nil {
			continue
		}

		// Check if it supports video capture
		if strings.Contains(string(output), "Video Capture") {
			devicePaths = append(devicePaths, devicePath)
		}

		if len(devicePaths) >= 3 {
			break
		}
	}

	numCameras := len(devicePaths)

	// Second pass: create cameras with capabilities
	for i, devicePath := range devicePaths {
		cam := Camera{
			DeviceID:   fmt.Sprintf("video%d", i*2),
			DevicePath: devicePath,
			Name:       fmt.Sprintf("Camera %d", i+1),
			Available:  true,
		}
		cam.Capabilities = queryCameraCapabilities(devicePath, numCameras)
		cameras = append(cameras, cam)
	}

	return cameras, nil
}
