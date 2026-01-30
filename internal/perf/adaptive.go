package perf

import (
	"camera-dashboard-go/internal/camera"
	"sync"
	"time"
)

// AdaptiveController manages dynamic performance adjustments
type AdaptiveController struct {
	monitor   *Monitor
	manager   *camera.Manager
	fps       int
	targetFPS int
	minFPS    int
	maxFPS    int

	// Control state
	isUnderStress bool
	stressCount   int
	recoveryCount int

	mutex sync.RWMutex
}

// NewAdaptiveController creates a new adaptive performance controller
func NewAdaptiveController(manager *camera.Manager) *AdaptiveController {
	return &AdaptiveController{
		monitor:   NewMonitor(),
		manager:   manager,
		targetFPS: 30,
		minFPS:    5,
		maxFPS:    30,
		fps:       30,
	}
}

// Start begins performance monitoring and adaptation
func (ac *AdaptiveController) Start() {
	go ac.adaptationLoop()
}

// adaptationLoop runs the main performance adaptation logic
func (ac *AdaptiveController) adaptationLoop() {
	ticker := time.NewTicker(2 * time.Second) // Check every 2 seconds
	defer ticker.Stop()

	for range ticker.C {
		if err := ac.monitor.UpdateStats(); err != nil {
			continue // Skip this iteration on error
		}

		ac.adjustPerformance()
	}
}

// adjustPerformance adjusts FPS based on system load and temperature
func (ac *AdaptiveController) adjustPerformance() {
	ac.mutex.Lock()
	defer ac.mutex.Unlock()

	load := ac.monitor.GetLoadAverage()
	temp := ac.monitor.GetTemperature()

	// Determine if system is under stress
	currentlyStressed := load > 1.5 || temp > 70.0

	if currentlyStressed && !ac.isUnderStress {
		// System entered stress state
		ac.isUnderStress = true
		ac.stressCount++
		ac.reduceFPS()
	} else if currentlyStressed && ac.isUnderStress {
		// System remains under stress
		if ac.stressCount > 3 {
			ac.reduceFPS() // Further reduce if prolonged stress
		}
	} else if !currentlyStressed && ac.isUnderStress {
		// System recovering from stress
		ac.recoveryCount++
		if ac.recoveryCount > 2 {
			ac.isUnderStress = false
			ac.recoveryCount = 0
			ac.stressCount = 0
			ac.increaseFPS()
		}
	} else {
		// System is stable
		if ac.fps < ac.targetFPS {
			ac.increaseFPS()
		}
	}
}

// reduceFPS reduces the target FPS
func (ac *AdaptiveController) reduceFPS() {
	newFPS := ac.fps - 2
	if newFPS < ac.minFPS {
		newFPS = ac.minFPS
	}

	if newFPS != ac.fps {
		ac.fps = newFPS
		// TODO: Apply FPS changes to camera capture workers
	}
}

// increaseFPS increases the target FPS
func (ac *AdaptiveController) increaseFPS() {
	newFPS := ac.fps + 2
	if newFPS > ac.maxFPS {
		newFPS = ac.maxFPS
	}

	if newFPS != ac.fps {
		ac.fps = newFPS
		// TODO: Apply FPS changes to camera capture workers
	}
}

// GetCurrentFPS returns the current FPS setting
func (ac *AdaptiveController) GetCurrentFPS() int {
	ac.mutex.RLock()
	defer ac.mutex.RUnlock()
	return ac.fps
}

// GetSystemStatus returns current system status
func (ac *AdaptiveController) GetSystemStatus() (load, temp float64, stressed bool) {
	return ac.monitor.GetLoadAverage(), ac.monitor.GetTemperature(), ac.isUnderStress
}
