package perf

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Monitor tracks system performance metrics
type Monitor struct {
	lastCheck   time.Time
	loadAvg     float64
	temperature float64
	memoryUsage float64 // Percentage of memory used
}

// NewMonitor creates a new performance monitor
func NewMonitor() *Monitor {
	return &Monitor{
		lastCheck: time.Now(),
	}
}

// UpdateStats updates performance statistics
func (m *Monitor) UpdateStats() error {
	// Update CPU load
	if err := m.updateLoadAverage(); err != nil {
		return err
	}

	// Update temperature
	if err := m.updateTemperature(); err != nil {
		return err
	}

	// Update memory usage
	m.updateMemoryUsage() // Non-critical, ignore errors

	m.lastCheck = time.Now()
	return nil
}

// updateLoadAverage reads system load average
func (m *Monitor) updateLoadAverage() error {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return err
	}

	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return ErrInvalidLoadAverage
	}

	m.loadAvg, err = strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return err
	}

	return nil
}

// updateTemperature reads CPU temperature from thermal zones
func (m *Monitor) updateTemperature() error {
	// Try to read from common thermal zones
	thermalPaths := []string{
		"/sys/class/thermal/thermal_zone0/temp",
		"/sys/class/thermal/thermal_zone1/temp",
		"/sys/class/thermal/thermal_zone2/temp",
		"/sys/devices/virtual/thermal/thermal_zone0/temp",
	}

	var totalTemp float64
	var count int

	for _, path := range thermalPaths {
		if data, err := os.ReadFile(path); err == nil {
			if tempStr := strings.TrimSpace(string(data)); tempStr != "" {
				if temp, err := strconv.ParseFloat(tempStr, 64); err == nil {
					// Temperature is in millidegrees Celsius
					totalTemp += temp / 1000.0
					count++
				}
			}
		}
	}

	if count == 0 {
		return ErrTemperatureNotFound
	}

	m.temperature = totalTemp / float64(count)
	return nil
}

// GetLoadAverage returns current load average
func (m *Monitor) GetLoadAverage() float64 {
	return m.loadAvg
}

// GetTemperature returns current temperature in Celsius
func (m *Monitor) GetTemperature() float64 {
	return m.temperature
}

// GetMemoryUsage returns memory usage percentage (0-100)
func (m *Monitor) GetMemoryUsage() float64 {
	return m.memoryUsage
}

// updateMemoryUsage reads memory stats from /proc/meminfo
func (m *Monitor) updateMemoryUsage() error {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return err
	}

	var memTotal, memAvailable int64
	lines := strings.Split(string(data), "\n")

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		switch fields[0] {
		case "MemTotal:":
			memTotal, _ = strconv.ParseInt(fields[1], 10, 64)
		case "MemAvailable:":
			memAvailable, _ = strconv.ParseInt(fields[1], 10, 64)
		}
	}

	if memTotal > 0 {
		m.memoryUsage = 100.0 * float64(memTotal-memAvailable) / float64(memTotal)
	}

	return nil
}

// IsUnderStress returns true if system is under stress
func (m *Monitor) IsUnderStress() bool {
	return m.loadAvg > 1.5 || m.temperature > 70.0
}

// Errors
var (
	ErrInvalidLoadAverage  = fmt.Errorf("invalid load average format")
	ErrTemperatureNotFound = fmt.Errorf("temperature sensors not found")
)
