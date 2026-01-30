package camera

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Camera represents a camera device
type Camera struct {
	DeviceID   string
	DevicePath string
	Name       string
	Available  bool
}

// DiscoverCameras finds all available camera devices on Linux
func DiscoverCameras() ([]Camera, error) {
	var cameras []Camera

	// Scan /dev/video* for camera devices
	devDir := "/dev"
	err := filepath.Walk(devDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		if strings.HasPrefix(info.Name(), "video") {
			devicePath := path
			cameraID := info.Name()

			// Check if it's actually a video device by trying to read basic info
			if info.Mode()&os.ModeDevice != 0 {
				camera := Camera{
					DeviceID:   cameraID,
					DevicePath: devicePath,
					Name:       fmt.Sprintf("Camera %s", cameraID),
					Available:  true,
				}
				cameras = append(cameras, camera)
			}
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to scan /dev directory: %w", err)
	}

	// For initial version, return first 3 cameras (like Python version)
	if len(cameras) > 3 {
		cameras = cameras[:3]
	}

	return cameras, nil
}
