// Package helpers provides utility functions for the camera dashboard.
package helpers

import (
	"context"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// =============================================================================
// kill_device_holders — clear processes holding camera device files
// =============================================================================
// Matches Python's kill_device_holders() from utils/helpers.py.
// Called before camera capture starts to free /dev/video* devices
// that might be held by stale FFmpeg or other processes.
//
// Strategy:
//   1. Use lsof -t to find PIDs holding the device (primary)
//   2. Fall back to fuser -v if lsof returns nothing
//   3. Exclude our own PID
//   4. Send SIGTERM, wait grace period, then SIGKILL survivors
// =============================================================================

// KillDeviceHolders attempts to terminate any process holding a camera device.
// Returns true if any processes were killed.
// If enabled is false, the function is a no-op and returns false.
func KillDeviceHolders(devicePath string, enabled bool) bool {
	return KillDeviceHoldersWithGrace(devicePath, enabled, 400*time.Millisecond)
}

// KillDeviceHoldersWithGrace is like KillDeviceHolders but allows specifying
// the grace period between SIGTERM and SIGKILL.
func KillDeviceHoldersWithGrace(devicePath string, enabled bool, grace time.Duration) bool {
	if !enabled {
		return false
	}

	pids := getPIDsFromLsof(devicePath)
	if len(pids) == 0 {
		pids = getPIDsFromFuser(devicePath)
	}

	// Exclude our own PID
	myPID := os.Getpid()
	delete(pids, myPID)

	if len(pids) == 0 {
		return false
	}

	sortedPIDs := sortedKeys(pids)
	log.Printf("[KillHolders] Killing holders of %s: %v", devicePath, sortedPIDs)

	// Phase 1: SIGTERM
	for pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			if isPermissionError(err) {
				// Escalate to sudo fuser -k
				runCmd("sudo", "fuser", "-k", devicePath)
				break
			}
			log.Printf("[KillHolders] Failed to SIGTERM pid %d: %v", pid, err)
		}
	}

	// Grace period
	time.Sleep(grace)

	// Phase 2: SIGKILL survivors
	for pid := range pids {
		if !isPIDAlive(pid) {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			if isPermissionError(err) {
				runCmd("sudo", "fuser", "-k", devicePath)
			} else {
				log.Printf("[KillHolders] Failed to SIGKILL pid %d: %v", pid, err)
			}
		}
	}

	return true
}

// getPIDsFromLsof returns PIDs holding a device using lsof -t.
func getPIDsFromLsof(devicePath string) map[int]struct{} {
	out := runCmd("lsof", "-t", devicePath)
	pids := make(map[int]struct{})
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if pid, err := strconv.Atoi(line); err == nil && pid > 0 {
			pids[pid] = struct{}{}
		}
	}
	return pids
}

// getPIDsFromFuser returns PIDs holding a device using fuser -v (fallback).
var digitRegexp = regexp.MustCompile(`\b(\d+)\b`)

func getPIDsFromFuser(devicePath string) map[int]struct{} {
	out := runCmd("fuser", "-v", devicePath)
	pids := make(map[int]struct{})
	for _, match := range digitRegexp.FindAllString(out, -1) {
		if pid, err := strconv.Atoi(match); err == nil && pid > 0 {
			pids[pid] = struct{}{}
		}
	}
	return pids
}

// isPIDAlive checks if a PID exists by sending signal 0.
func isPIDAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// runCmd executes a command with a 2-second timeout and returns stdout.
// Errors (including timeout) are silently ignored (returns empty string).
func runCmd(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// isPermissionError checks if an error is a permission error.
func isPermissionError(err error) bool {
	return err == syscall.EPERM || err == syscall.EACCES
}

// sortedKeys returns the keys of a map as a sorted slice.
func sortedKeys(m map[int]struct{}) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort (typically < 5 PIDs)
	for i := 1; i < len(keys); i++ {
		j := i
		for j > 0 && keys[j-1] > keys[j] {
			keys[j-1], keys[j] = keys[j], keys[j-1]
			j--
		}
	}
	return keys
}
