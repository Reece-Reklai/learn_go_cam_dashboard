package perf

import (
	"camera-dashboard-go/internal/camera"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Controller states
const (
	StateProbing    = iota // Finding max sustainable FPS
	StateStable            // Running at sweet spot
	StateRecovering        // Coming back from thermal event
	StateEmergency         // Critical thermal - minimum FPS
)

// Thermal thresholds for Pi 4/5 (designed to run warm)
// Pi 5 throttles at 85°C, so we have headroom up to 83°C
const (
	TempIdeal    = 72.0 // Below this: can try increasing FPS
	TempComfort  = 78.0 // Sweet spot ceiling - Pi runs fine here
	TempWarm     = 82.0 // Start being cautious (still safe)
	TempHot      = 84.0 // Need to reduce FPS (approaching throttle)
	TempCritical = 86.0 // Emergency minimum FPS (throttling imminent)
)

// Load thresholds (Pi5 has 4 cores)
const (
	LoadIdeal = 2.5 // Comfortable load
	LoadHigh  = 3.8 // High but not overloaded
)

// FPS limits - uses settings from camera/config.go
// These are fallbacks if config can't be read
const (
	MinFPS     = 10 // Absolute minimum for usability
	MaxFPS     = 30 // Absolute maximum
	DefaultFPS = 15 // Default if config unavailable
)

// SmartController manages dynamic FPS adjustment
// Resolution is always max (640x480) - only FPS adapts
type SmartController struct {
	monitor *Monitor
	manager *camera.Manager

	// FPS control
	currentFPS   int
	sweetSpotFPS int // Best known stable FPS
	minFPS       int
	maxFPS       int

	// State machine
	state          atomic.Int32
	stateEnterTime time.Time
	stabilityCount int
	lastChange     time.Time

	// Thermal tracking
	tempHistory []float64
	tempTrend   float64 // Positive = heating, negative = cooling

	// Stats
	stableSeconds atomic.Int64
	adjustCount   int

	// Concurrency
	mutex   sync.RWMutex
	running atomic.Bool
	stopCh  chan struct{}
}

// NewSmartController creates a simple fixed-FPS controller
// Uses FPS from camera/config.go - no runtime adaptation
func NewSmartController(manager *camera.Manager) *SmartController {
	// Get FPS from config
	_, _, configFPS, _ := camera.GetCameraConfig()
	if configFPS < MinFPS {
		configFPS = MinFPS
	}
	if configFPS > MaxFPS {
		configFPS = MaxFPS
	}

	numCameras := 0
	if manager != nil {
		cameras := manager.GetCameras()
		numCameras = len(cameras)
	}

	// Validate config and log warnings
	ok, warnings := camera.ValidateConfig()
	if !ok {
		log.Printf("[SmartCtrl] WARNING: Config validation failed!")
	}
	for _, w := range warnings {
		log.Printf("[SmartCtrl] WARNING: %s", w)
	}

	sc := &SmartController{
		monitor:      NewMonitor(),
		manager:      manager,
		minFPS:       configFPS, // Same as max - no adaptation
		maxFPS:       configFPS, // Fixed at configured FPS
		currentFPS:   configFPS,
		sweetSpotFPS: configFPS,
		tempHistory:  make([]float64, 0, 10),
		stopCh:       make(chan struct{}),
	}

	w, h, fps, format := camera.GetCameraConfig()
	log.Printf("[SmartCtrl] Config: %dx%d @ %d FPS (%s) for %d cameras (fixed, no adaptation)",
		w, h, fps, format, numCameras)
	return sc
}

// Start begins monitoring (no FPS adaptation - uses config.go settings)
func (sc *SmartController) Start() {
	if sc.running.Swap(true) {
		return
	}

	sc.state.Store(StateStable) // Always stable - no adaptation
	sc.stateEnterTime = time.Now()
	sc.lastChange = time.Now()

	// Apply configured FPS
	sc.applyFPS(sc.maxFPS)

	log.Printf("[SmartCtrl] Started - fixed %d FPS, monitoring only (edit config.go to change)", sc.maxFPS)

	go sc.controlLoop()
}

// Stop halts the controller
func (sc *SmartController) Stop() {
	if !sc.running.Swap(false) {
		return
	}
	close(sc.stopCh)
}

// controlLoop runs every second
func (sc *SmartController) controlLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	logTicker := time.NewTicker(5 * time.Second)
	defer logTicker.Stop()

	for {
		select {
		case <-sc.stopCh:
			return
		case <-ticker.C:
			sc.tick()
		case <-logTicker.C:
			sc.logStatus()
		}
	}
}

// tick performs one monitoring cycle (no FPS changes for vehicle mode)
func (sc *SmartController) tick() {
	if err := sc.monitor.UpdateStats(); err != nil {
		return
	}

	sc.mutex.Lock()
	defer sc.mutex.Unlock()

	temp := sc.monitor.GetTemperature()

	sc.updateTempTrend(temp)

	// Vehicle mode: just monitor, don't change FPS
	// Only log warnings for extreme temperatures
	if temp >= TempCritical {
		log.Printf("[SmartCtrl] WARNING: Temperature critical (%.1f°C) - consider improving ventilation", temp)
	}

	// Always stay stable in vehicle mode
	if sc.state.Load() != StateStable {
		sc.state.Store(StateStable)
	}
	sc.stableSeconds.Add(1)
}

// updateTempTrend tracks temperature changes
func (sc *SmartController) updateTempTrend(temp float64) {
	sc.tempHistory = append(sc.tempHistory, temp)
	if len(sc.tempHistory) > 10 {
		sc.tempHistory = sc.tempHistory[1:]
	}

	if len(sc.tempHistory) >= 3 {
		n := len(sc.tempHistory)
		sc.tempTrend = (sc.tempHistory[n-1] - sc.tempHistory[0]) / float64(n)
	}
}

// handleEmergency - at minimum FPS, waiting for cooldown
func (sc *SmartController) handleEmergency(temp float64) {
	if sc.currentFPS != sc.minFPS {
		sc.applyFPS(sc.minFPS)
	}

	// Exit emergency when cooled down
	if temp < TempWarm && sc.tempTrend <= 0 && time.Since(sc.stateEnterTime) > 10*time.Second {
		log.Printf("[SmartCtrl] Exiting emergency - temp: %.1f°C", temp)
		sc.enterState(StateRecovering)
	}
}

// handleProbing - finding the max sustainable FPS
func (sc *SmartController) handleProbing(temp, load float64) {
	timeSinceChange := time.Since(sc.lastChange)

	// Emergency check
	if temp >= TempCritical {
		log.Printf("[SmartCtrl] EMERGENCY - temp: %.1f°C", temp)
		sc.enterState(StateEmergency)
		return
	}

	// Is current FPS sustainable?
	isSustainable := (temp < TempWarm) || (temp < TempHot && sc.tempTrend <= 0)
	isLoadOK := load < LoadHigh

	if isSustainable && isLoadOK {
		sc.stabilityCount++

		// Stable for 8+ seconds - this FPS works
		if sc.stabilityCount >= 8 {
			if sc.currentFPS > sc.sweetSpotFPS {
				sc.sweetSpotFPS = sc.currentFPS
				log.Printf("[SmartCtrl] New sweet spot: %d FPS @ %.1f°C", sc.sweetSpotFPS, temp)
			}

			// Very stable - enter stable state
			if sc.stabilityCount >= 12 {
				log.Printf("[SmartCtrl] Stable at %d FPS", sc.currentFPS)
				sc.enterState(StateStable)
				return
			}

			// Try higher FPS if cooling and stable
			if sc.currentFPS < sc.maxFPS && temp < TempComfort &&
				sc.tempTrend < 0 && timeSinceChange > 15*time.Second {
				sc.changeFPS(sc.currentFPS + 2)
			}
		}
	} else {
		sc.stabilityCount = 0

		// Too hot or overloaded - reduce FPS
		shouldReduce := temp >= TempHot || (temp >= TempWarm && sc.tempTrend > 0.3) || load >= LoadHigh

		if shouldReduce && timeSinceChange > 5*time.Second {
			newFPS := sc.currentFPS - 3
			if newFPS < sc.minFPS {
				newFPS = sc.minFPS
			}
			sc.changeFPS(newFPS)

			// Update sweet spot if we had to go lower
			if newFPS < sc.sweetSpotFPS {
				sc.sweetSpotFPS = newFPS
			}
		}
	}
}

// handleStable - maintaining the sweet spot FPS
func (sc *SmartController) handleStable(temp, load float64) {
	sc.stableSeconds.Add(1)

	// Check for emergency
	if temp >= TempCritical {
		log.Printf("[SmartCtrl] EMERGENCY in stable - temp: %.1f°C", temp)
		sc.enterState(StateEmergency)
		return
	}

	// Need to reduce?
	if temp >= TempHot || (temp >= TempWarm && sc.tempTrend > 0.5) || load >= LoadHigh {
		log.Printf("[SmartCtrl] Reducing FPS - temp: %.1f°C, load: %.2f", temp, load)
		newFPS := sc.currentFPS - 2
		if newFPS < sc.minFPS {
			newFPS = sc.minFPS
		}
		sc.changeFPS(newFPS)

		if newFPS < sc.sweetSpotFPS {
			sc.sweetSpotFPS = newFPS
			log.Printf("[SmartCtrl] Sweet spot lowered to %d FPS", sc.sweetSpotFPS)
		}
		return
	}

	// Can we try higher? (after 30+ seconds stable, cooling, well under threshold)
	stableTime := sc.stableSeconds.Load()
	if stableTime > 30 && sc.currentFPS < sc.maxFPS &&
		temp < TempIdeal && sc.tempTrend < 0 && load < LoadIdeal {
		log.Printf("[SmartCtrl] Conditions excellent - trying higher FPS")
		sc.changeFPS(sc.currentFPS + 2)
		sc.stableSeconds.Store(0)
	}
}

// handleRecovering - stepping back up to sweet spot
func (sc *SmartController) handleRecovering(temp float64) {
	if temp >= TempHot {
		if temp >= TempCritical {
			sc.enterState(StateEmergency)
		}
		return
	}

	// Gradually increase toward sweet spot
	if temp < TempComfort && sc.tempTrend <= 0 && time.Since(sc.lastChange) > 5*time.Second {
		if sc.currentFPS < sc.sweetSpotFPS {
			sc.changeFPS(sc.currentFPS + 2)
		} else {
			log.Printf("[SmartCtrl] Recovered to sweet spot: %d FPS", sc.sweetSpotFPS)
			sc.enterState(StateStable)
		}
	}
}

// changeFPS applies a new FPS value
func (sc *SmartController) changeFPS(fps int) {
	if fps < sc.minFPS {
		fps = sc.minFPS
	}
	if fps > sc.maxFPS {
		fps = sc.maxFPS
	}
	if fps == sc.currentFPS {
		return
	}

	oldFPS := sc.currentFPS
	sc.currentFPS = fps
	sc.lastChange = time.Now()
	sc.stabilityCount = 0
	sc.adjustCount++

	if sc.manager != nil {
		sc.manager.SetFPS(fps)
	}

	log.Printf("[SmartCtrl] FPS: %d -> %d", oldFPS, fps)
}

// applyFPS sets FPS without logging (for initial setup)
func (sc *SmartController) applyFPS(fps int) {
	sc.currentFPS = fps
	if sc.manager != nil {
		sc.manager.SetFPS(fps)
	}
}

// enterState transitions to a new state
func (sc *SmartController) enterState(state int) {
	oldState := sc.state.Swap(int32(state))
	sc.stateEnterTime = time.Now()
	sc.stabilityCount = 0

	names := []string{"Probing", "Stable", "Recovering", "Emergency"}
	log.Printf("[SmartCtrl] State: %s -> %s", names[oldState], names[state])

	if state == StateEmergency {
		sc.applyFPS(sc.minFPS)
	}
	if state == StateStable {
		sc.stableSeconds.Store(0)
	}
}

// logStatus outputs current state
func (sc *SmartController) logStatus() {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()

	temp := sc.monitor.GetTemperature()
	load := sc.monitor.GetLoadAverage()

	log.Printf("[SmartCtrl] Vehicle mode | FPS: %d (fixed) | Temp: %.1f°C | Load: %.2f | Uptime: %ds",
		sc.currentFPS, temp, load, sc.stableSeconds.Load())
}

// GetCurrentFPS returns current FPS
func (sc *SmartController) GetCurrentFPS() int {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return sc.currentFPS
}

// GetSweetSpotFPS returns the discovered sweet spot
func (sc *SmartController) GetSweetSpotFPS() int {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return sc.sweetSpotFPS
}

// GetState returns current state name
func (sc *SmartController) GetState() string {
	names := []string{"Probing", "Stable", "Recovering", "Emergency"}
	return names[sc.state.Load()]
}

// Legacy compatibility
type AdaptiveController = SmartController

func NewAdaptiveController(manager *camera.Manager) *AdaptiveController {
	return NewSmartController(manager)
}
