package camera

// =============================================================================
// VEHICLE CAMERA CONFIGURATION
// =============================================================================
// Modify these values to change camera resolution and FPS for all cameras.
// After changing, rebuild with: go build -o camera-dashboard .
//
// Run: ./camera-dashboard --query-cameras to see what your cameras support.
// =============================================================================

// -----------------------------------------------------------------------------
// RESOLUTION SETTINGS
// -----------------------------------------------------------------------------
// Common resolutions supported by most USB cameras:
//
//   Resolution   | Pixels    | Use Case
//   -------------|-----------|------------------------------------------
//   800x600      | 480,000   | High quality, single camera only
//   640x480      | 307,200   | Recommended for 2-3 cameras (balanced)
//   352x288      | 101,376   | Low bandwidth, very stable
//   320x240      | 76,800    | Minimum usable, ultra-low power
//
// For vehicle use with 3 cameras, 640x480 is optimal.
// -----------------------------------------------------------------------------

const (
	// CameraWidth is the capture width in pixels
	// Recommended: 640 for 3 cameras, 800 for 1 camera
	CameraWidth = 640

	// CameraHeight is the capture height in pixels
	// Recommended: 480 for 3 cameras, 600 for 1 camera
	CameraHeight = 480
)

// -----------------------------------------------------------------------------
// FPS (FRAMES PER SECOND) SETTINGS
// -----------------------------------------------------------------------------
// FPS affects smoothness, USB bandwidth, CPU load, and battery life.
//
//   FPS  | Frame Interval | USB Bandwidth* | Use Case
//   -----|----------------|----------------|--------------------------------
//   30   | 33ms           | ~9 MB/s        | Single camera, maximum smoothness
//   20   | 50ms           | ~6 MB/s        | 2 cameras, smooth
//   15   | 67ms           | ~4.5 MB/s      | 3 cameras, good for parking/reversing
//   10   | 100ms          | ~3 MB/s        | Ultra-stable, battery saver
//
//   * Bandwidth estimate for 3 cameras at 640x480 MJPEG
//
// For vehicle blind spot + reversing cameras, 15 FPS is optimal.
// -----------------------------------------------------------------------------

const (
	// CameraFPS is the target frames per second
	// Recommended: 15 for 3 cameras, 20-30 for 1-2 cameras
	CameraFPS = 15
)

// -----------------------------------------------------------------------------
// FORMAT SETTINGS
// -----------------------------------------------------------------------------
// MJPEG is strongly recommended - uses 10x less USB bandwidth than raw YUYV.
// Only change this if your cameras don't support MJPEG.
// -----------------------------------------------------------------------------

const (
	// CameraFormat is the preferred capture format
	// Options: "mjpeg" (recommended), "yuyv" (fallback)
	CameraFormat = "mjpeg"
)

// -----------------------------------------------------------------------------
// ADVANCED: USB BANDWIDTH CALCULATOR
// -----------------------------------------------------------------------------
// Use this to verify your settings won't exceed USB limits.
//
// MJPEG bandwidth ≈ Width × Height × FPS × 0.15 bytes/sec (compressed)
// YUYV bandwidth  = Width × Height × FPS × 2 bytes/sec (uncompressed)
//
// USB 2.0 practical limit: ~35 MB/s shared across all cameras
//
// Example calculations for 3 cameras:
//   640×480 @ 15 FPS MJPEG: 640×480×15×0.15×3 = 2.1 MB/s  ✓ Safe
//   640×480 @ 30 FPS MJPEG: 640×480×30×0.15×3 = 4.1 MB/s  ✓ Safe
//   640×480 @ 15 FPS YUYV:  640×480×15×2×3    = 27.6 MB/s ⚠️ Risky
//   800×600 @ 30 FPS YUYV:  800×600×30×2×3    = 86.4 MB/s ✗ Overload
// -----------------------------------------------------------------------------

// GetCameraConfig returns the current camera configuration
// Used by other packages to access settings
func GetCameraConfig() (width, height, fps int, format string) {
	return CameraWidth, CameraHeight, CameraFPS, CameraFormat
}

// ValidateConfig checks if the current config is reasonable
func ValidateConfig() (ok bool, warnings []string) {
	ok = true
	warnings = []string{}

	// Check resolution
	pixels := CameraWidth * CameraHeight
	if pixels > 480000 { // > 800x600
		warnings = append(warnings, "High resolution may cause USB bandwidth issues with multiple cameras")
	}

	// Check FPS
	if CameraFPS > 20 {
		warnings = append(warnings, "FPS > 20 may cause instability with 3 cameras")
	}

	// Estimate bandwidth for 3 cameras (worst case)
	var bandwidth float64
	if CameraFormat == "mjpeg" {
		bandwidth = float64(CameraWidth*CameraHeight*CameraFPS) * 0.15 * 3 / 1024 / 1024
	} else {
		bandwidth = float64(CameraWidth*CameraHeight*CameraFPS) * 2 * 3 / 1024 / 1024
	}

	if bandwidth > 30 {
		ok = false
		warnings = append(warnings, "Estimated USB bandwidth exceeds safe limits")
	} else if bandwidth > 20 {
		warnings = append(warnings, "Estimated USB bandwidth is high - may cause issues")
	}

	return ok, warnings
}
