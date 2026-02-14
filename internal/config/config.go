// Package config manages configuration for Camera Dashboard.
//
// Handles loading config from INI files, environment variables,
// and provides default values for all settings.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// =============================================================================
// Configuration struct
// =============================================================================

// Config holds all runtime configuration values.
type Config struct {
	// Logging
	// LogLevel is parsed from the INI file but currently unused because Go's
	// standard log package does not support levelled logging. Retained for
	// forward-compatibility if a levelled logger (e.g. slog) is adopted.
	LogLevel       string
	LogFile        string
	LogMaxBytes    int
	LogBackupCount int
	LogToStdout    bool

	// Performance + Recovery
	DynamicFPSEnabled    bool
	PerfCheckIntervalMS  int
	MinDynamicFPS        int
	MinDynamicUIFPS      int
	UIFPSStep            int
	CPULoadThreshold     float64
	CPUTempThresholdC    float64
	StressHoldCount      int
	RecoverHoldCount     int
	StaleFrameTimeoutSec float64
	RestartCooldownSec   float64
	MaxRestartsPerWindow int
	RestartWindowSec     float64

	// Camera rescan (hot-plug)
	RescanIntervalMS      int
	FailedCameraCooldownS float64
	CameraSlotCount       int
	KillDeviceHolders     bool

	// Profile
	CaptureWidth  int
	CaptureHeight int
	CaptureFPS    int
	CaptureFormat string // "mjpeg" or "yuyv"; passed to FFmpeg as -input_format
	UIFPS         int

	// Health
	HealthLogIntervalSec float64

	// Render overhead (code-only, not in INI)
	RenderOverheadMS int

	// Debug flags (code-only)
	UIFPSLogging bool
}

// =============================================================================
// Defaults
// =============================================================================

// DefaultConfig returns a Config populated with all default values,
// matching the Python reference implementation.
func DefaultConfig() *Config {
	return &Config{
		// Logging
		LogLevel:       "INFO",
		LogFile:        "./logs/camera_dashboard.log",
		LogMaxBytes:    5 * 1024 * 1024, // 5 MB
		LogBackupCount: 3,
		LogToStdout:    true,

		// Performance + Recovery
		DynamicFPSEnabled:    true,
		PerfCheckIntervalMS:  2000,
		MinDynamicFPS:        10,
		MinDynamicUIFPS:      12,
		UIFPSStep:            2,
		CPULoadThreshold:     3.0,
		CPUTempThresholdC:    75.0,
		StressHoldCount:      3,
		RecoverHoldCount:     3,
		StaleFrameTimeoutSec: 1.5,
		RestartCooldownSec:   5.0,
		MaxRestartsPerWindow: 3,
		RestartWindowSec:     30.0,

		// Camera rescan
		RescanIntervalMS:      15000,
		FailedCameraCooldownS: 30.0,
		CameraSlotCount:       3,
		KillDeviceHolders:     true,

		// Profile
		CaptureWidth:  640,
		CaptureHeight: 480,
		CaptureFPS:    25,
		CaptureFormat: "mjpeg",
		UIFPS:         20,

		// Health
		HealthLogIntervalSec: 30.0,

		// Code-only defaults
		RenderOverheadMS: 3,
		UIFPSLogging:     false,
	}
}

// =============================================================================
// INI parser (minimal, no external deps)
// =============================================================================

// iniData stores parsed INI sections and their key-value pairs.
type iniData map[string]map[string]string

// parseINI reads an INI file and returns its sections and key-value pairs.
// Supports comments (# and ;), sections ([name]), and key = value lines.
func parseINI(path string) (iniData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	result := make(iniData)
	currentSection := ""

	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		// Section header
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := result[currentSection]; !ok {
				result[currentSection] = make(map[string]string)
			}
			continue
		}

		// Key = value
		if idx := strings.IndexByte(line, '='); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			value := strings.TrimSpace(line[idx+1:])
			if currentSection != "" {
				result[currentSection][key] = value
			}
		}
	}

	return result, nil
}

// get returns a value from the parsed INI data, or empty string if not found.
func (d iniData) get(section, key string) (string, bool) {
	if sec, ok := d[section]; ok {
		if val, ok := sec[key]; ok {
			return val, true
		}
	}
	return "", false
}

// hasSection returns true if the section exists in the INI data.
func (d iniData) hasSection(section string) bool {
	_, ok := d[section]
	return ok
}

// =============================================================================
// Type parsing helpers (match Python's _as_bool, _as_int, _as_float)
// =============================================================================

// asBool parses a string as boolean. Truthy: "1","true","yes","on".
// Falsy: "0","false","no","off". Returns fallback on empty/unrecognised.
func asBool(value string, fallback bool) bool {
	if value == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

// asInt parses a string as int with optional min/max clamping.
// Pass nil for unbounded. Returns fallback on parse error.
func asInt(value string, fallback int, minVal, maxVal *int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	if minVal != nil && parsed < *minVal {
		parsed = *minVal
	}
	if maxVal != nil && parsed > *maxVal {
		parsed = *maxVal
	}
	return parsed
}

// asFloat parses a string as float64 with optional min/max clamping.
// Pass nil for unbounded. Returns fallback on parse error.
func asFloat(value string, fallback float64, minVal, maxVal *float64) float64 {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return fallback
	}
	if minVal != nil && parsed < *minVal {
		parsed = *minVal
	}
	if maxVal != nil && parsed > *maxVal {
		parsed = *maxVal
	}
	return parsed
}

// Helper functions to create pointers for min/max bounds
func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }

// =============================================================================
// Load + Apply
// =============================================================================

// ConfigPath returns the INI file path to use, respecting env vars.
func ConfigPath() string {
	if p := os.Getenv("CAMERA_DASHBOARD_CONFIG"); p != "" {
		return p
	}
	return "./config.ini"
}

// Load reads the INI file at the given path (or the default/env path)
// and returns a fully populated Config. Missing sections or keys
// fall back to DefaultConfig() values.
func Load(path string) (*Config, error) {
	if path == "" {
		path = ConfigPath()
	}

	cfg := DefaultConfig()

	// If file doesn't exist, return defaults (not an error)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	}

	ini, err := parseINI(path)
	if err != nil {
		return cfg, fmt.Errorf("config: failed to parse %s: %w", path, err)
	}

	applyINI(cfg, ini)

	// Environment variable overrides
	if logFile := os.Getenv("CAMERA_DASHBOARD_LOG_FILE"); logFile != "" {
		cfg.LogFile = logFile
	}

	return cfg, nil
}

// applyINI maps INI key-value pairs onto the Config struct,
// matching Python's apply_config() exactly.
func applyINI(cfg *Config, ini iniData) {
	// [logging]
	if ini.hasSection("logging") {
		if v, ok := ini.get("logging", "level"); ok {
			cfg.LogLevel = strings.ToUpper(strings.TrimSpace(v))
		}
		if v, ok := ini.get("logging", "file"); ok {
			cfg.LogFile = v
		}
		if v, ok := ini.get("logging", "max_bytes"); ok {
			cfg.LogMaxBytes = asInt(v, cfg.LogMaxBytes, intPtr(1024), nil)
		}
		if v, ok := ini.get("logging", "backup_count"); ok {
			cfg.LogBackupCount = asInt(v, cfg.LogBackupCount, intPtr(1), nil)
		}
		if v, ok := ini.get("logging", "stdout"); ok {
			cfg.LogToStdout = asBool(v, cfg.LogToStdout)
		}
	}

	// [performance]
	if ini.hasSection("performance") {
		if v, ok := ini.get("performance", "dynamic_fps"); ok {
			cfg.DynamicFPSEnabled = asBool(v, cfg.DynamicFPSEnabled)
		}
		if v, ok := ini.get("performance", "perf_check_interval_ms"); ok {
			cfg.PerfCheckIntervalMS = asInt(v, cfg.PerfCheckIntervalMS, intPtr(250), nil)
		}
		if v, ok := ini.get("performance", "min_dynamic_fps"); ok {
			cfg.MinDynamicFPS = asInt(v, cfg.MinDynamicFPS, intPtr(1), nil)
		}
		if v, ok := ini.get("performance", "min_dynamic_ui_fps"); ok {
			cfg.MinDynamicUIFPS = asInt(v, cfg.MinDynamicUIFPS, intPtr(1), nil)
		}
		if v, ok := ini.get("performance", "ui_fps_step"); ok {
			cfg.UIFPSStep = asInt(v, cfg.UIFPSStep, intPtr(1), nil)
		}
		if v, ok := ini.get("performance", "cpu_load_threshold"); ok {
			cfg.CPULoadThreshold = asFloat(v, cfg.CPULoadThreshold, floatPtr(0.1), floatPtr(20.0))
		}
		if v, ok := ini.get("performance", "cpu_temp_threshold_c"); ok {
			cfg.CPUTempThresholdC = asFloat(v, cfg.CPUTempThresholdC, floatPtr(30.0), floatPtr(100.0))
		}
		if v, ok := ini.get("performance", "stress_hold_count"); ok {
			cfg.StressHoldCount = asInt(v, cfg.StressHoldCount, intPtr(1), nil)
		}
		if v, ok := ini.get("performance", "recover_hold_count"); ok {
			cfg.RecoverHoldCount = asInt(v, cfg.RecoverHoldCount, intPtr(1), nil)
		}
		if v, ok := ini.get("performance", "stale_frame_timeout_sec"); ok {
			cfg.StaleFrameTimeoutSec = asFloat(v, cfg.StaleFrameTimeoutSec, floatPtr(0.5), nil)
		}
		if v, ok := ini.get("performance", "restart_cooldown_sec"); ok {
			cfg.RestartCooldownSec = asFloat(v, cfg.RestartCooldownSec, floatPtr(1.0), nil)
		}
		if v, ok := ini.get("performance", "max_restarts_per_window"); ok {
			cfg.MaxRestartsPerWindow = asInt(v, cfg.MaxRestartsPerWindow, intPtr(1), nil)
		}
		if v, ok := ini.get("performance", "restart_window_sec"); ok {
			cfg.RestartWindowSec = asFloat(v, cfg.RestartWindowSec, floatPtr(5.0), nil)
		}
	}

	// [camera]
	if ini.hasSection("camera") {
		if v, ok := ini.get("camera", "rescan_interval_ms"); ok {
			cfg.RescanIntervalMS = asInt(v, cfg.RescanIntervalMS, intPtr(500), nil)
		}
		if v, ok := ini.get("camera", "failed_camera_cooldown_sec"); ok {
			cfg.FailedCameraCooldownS = asFloat(v, cfg.FailedCameraCooldownS, floatPtr(1.0), nil)
		}
		if v, ok := ini.get("camera", "slot_count"); ok {
			cfg.CameraSlotCount = asInt(v, cfg.CameraSlotCount, intPtr(1), intPtr(8))
		}
		if v, ok := ini.get("camera", "kill_device_holders"); ok {
			cfg.KillDeviceHolders = asBool(v, cfg.KillDeviceHolders)
		}
	}

	// [profile]
	if ini.hasSection("profile") {
		if v, ok := ini.get("profile", "capture_width"); ok {
			cfg.CaptureWidth = asInt(v, cfg.CaptureWidth, intPtr(160), intPtr(1920))
		}
		if v, ok := ini.get("profile", "capture_height"); ok {
			cfg.CaptureHeight = asInt(v, cfg.CaptureHeight, intPtr(120), intPtr(1080))
		}
		if v, ok := ini.get("profile", "capture_fps"); ok {
			cfg.CaptureFPS = asInt(v, cfg.CaptureFPS, intPtr(1), intPtr(60))
		}
		if v, ok := ini.get("profile", "capture_format"); ok {
			v = strings.ToLower(strings.TrimSpace(v))
			if v == "mjpeg" || v == "yuyv" {
				cfg.CaptureFormat = v
			}
		}
		if v, ok := ini.get("profile", "ui_fps"); ok {
			cfg.UIFPS = asInt(v, cfg.UIFPS, intPtr(1), intPtr(60))
		}
	}

	// [health]
	if ini.hasSection("health") {
		if v, ok := ini.get("health", "log_interval_sec"); ok {
			cfg.HealthLogIntervalSec = asFloat(v, cfg.HealthLogIntervalSec, floatPtr(5.0), nil)
		}
	}
}

// =============================================================================
// Profile scaling (choose_profile equivalent)
// =============================================================================

// ChooseProfile picks capture resolution and FPS based on camera count.
// Dynamically scales resolution down when more cameras are active
// to maintain smooth performance on resource-constrained devices.
//
// Returns (width, height, captureFPS, uiFPS).
func (c *Config) ChooseProfile(cameraCount int) (int, int, int, int) {
	baseW := c.CaptureWidth
	baseH := c.CaptureHeight
	baseFPS := c.CaptureFPS
	baseUIFPS := c.UIFPS

	var scale, fpsScale float64

	switch {
	case cameraCount >= 6:
		// 6+ cameras: drop to 50% res, 60% fps
		scale = 0.5
		fpsScale = 0.6
	case cameraCount >= 4:
		// 4-5 cameras: drop to 75% res, 75% fps
		scale = 0.75
		fpsScale = 0.75
	case cameraCount >= 2:
		// 2-3 cameras: full res, 90% fps
		scale = 1.0
		fpsScale = 0.9
	default:
		// 1 camera: full everything
		scale = 1.0
		fpsScale = 1.0
	}

	// Apply scaling; ensure dimensions are multiples of 16 for codec efficiency
	scaledW := max16(160, roundDown16(int(float64(baseW)*scale)))
	scaledH := max16(120, roundDown16(int(float64(baseH)*scale)))
	scaledFPS := intMax(c.MinDynamicFPS, int(float64(baseFPS)*fpsScale))
	scaledUIFPS := intMax(c.MinDynamicUIFPS, int(float64(baseUIFPS)*fpsScale))

	return scaledW, scaledH, scaledFPS, scaledUIFPS
}

// roundDown16 rounds n down to the nearest multiple of 16.
func roundDown16(n int) int {
	return (n / 16) * 16
}

// max16 returns the larger of a and b, both assumed >= 0.
func max16(minimum, value int) int {
	if value < minimum {
		return minimum
	}
	return value
}

// intMax returns the larger of a and b.
func intMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// =============================================================================
// Validate
// =============================================================================

// Validate checks whether the Config values are reasonable and returns
// warnings. Returns ok=false if any setting is critically problematic.
func (c *Config) Validate() (ok bool, warnings []string) {
	ok = true

	pixels := c.CaptureWidth * c.CaptureHeight
	if pixels > 480000 {
		warnings = append(warnings, "High resolution may cause USB bandwidth issues with multiple cameras")
	}

	if c.CaptureFPS > 20 {
		warnings = append(warnings, fmt.Sprintf("FPS %d > 20 may cause instability with 3+ cameras", c.CaptureFPS))
	}

	// Estimate bandwidth for CameraSlotCount cameras (MJPEG assumed)
	bandwidth := float64(c.CaptureWidth*c.CaptureHeight*c.CaptureFPS) * 0.15 * float64(c.CameraSlotCount) / 1024 / 1024
	if bandwidth > 30 {
		ok = false
		warnings = append(warnings, "Estimated USB bandwidth exceeds safe limits")
	} else if bandwidth > 20 {
		warnings = append(warnings, "Estimated USB bandwidth is high - may cause issues")
	}

	if c.MinDynamicFPS > c.CaptureFPS {
		warnings = append(warnings, fmt.Sprintf("MinDynamicFPS (%d) > CaptureFPS (%d)", c.MinDynamicFPS, c.CaptureFPS))
	}

	if c.UIFPS > 60 {
		warnings = append(warnings, "UI FPS > 60 is wasteful and likely unsupported")
	}

	return ok, warnings
}
