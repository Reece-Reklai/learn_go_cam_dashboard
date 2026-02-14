package perf

import (
	"camera-dashboard-go/internal/camera"
	"camera-dashboard-go/internal/config"
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

// FPS limits - fallbacks if config is nil
const (
	MinFPS     = 10 // Absolute minimum for usability
	MaxFPS     = 30 // Absolute maximum
	DefaultFPS = 15 // Default if config unavailable
)

// SmartController manages dynamic FPS adjustment based on system thermals
// and CPU load. When dynamic FPS is enabled (via config), it uses a state
// machine (Probing -> Stable -> Recovering -> Emergency) to find and
// maintain the highest sustainable FPS. When disabled, it runs at fixed FPS.
type SmartController struct {
	monitor *Monitor
	manager *camera.Manager
	cfg     *config.Config

	// FPS control
	currentFPS   int
	sweetSpotFPS int // Best known stable FPS
	minFPS       int
	maxFPS       int

	// Dynamic FPS mode
	dynamicEnabled bool

	// State machine
	state          atomic.Int32
	stateEnterTime time.Time
	stabilityCount int
	lastChange     time.Time

	// Stress-based tracking (matches Python's stress_hold_count / recover_hold_count)
	stressCount  int // Consecutive ticks under stress
	recoverCount int // Consecutive ticks in recovery conditions

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

// NewSmartController creates a performance controller.
// If cfg is nil, uses built-in defaults with dynamic FPS disabled.
// If cfg.DynamicFPSEnabled is true, the controller actively adapts FPS
// based on CPU temperature and load.
func NewSmartController(manager *camera.Manager, cfg *config.Config) *SmartController {
	if cfg == nil {
		cfg = config.DefaultConfig()
		cfg.DynamicFPSEnabled = false // Safe default without config
	}

	captureFPS := cfg.CaptureFPS
	minFPS := cfg.MinDynamicFPS
	if minFPS < MinFPS {
		minFPS = MinFPS
	}
	if captureFPS < minFPS {
		captureFPS = minFPS
	}
	if captureFPS > MaxFPS {
		captureFPS = MaxFPS
	}

	numCameras := 0
	if manager != nil {
		cameras := manager.GetCameras()
		numCameras = len(cameras)
	}

	// Validate config
	ok, warnings := cfg.Validate()
	if !ok {
		log.Printf("[SmartCtrl] WARNING: Config validation failed!")
	}
	for _, w := range warnings {
		log.Printf("[SmartCtrl] WARNING: %s", w)
	}

	sc := &SmartController{
		monitor:        NewMonitor(),
		manager:        manager,
		cfg:            cfg,
		dynamicEnabled: cfg.DynamicFPSEnabled,
		tempHistory:    make([]float64, 0, 10),
		stopCh:         make(chan struct{}),
	}

	if cfg.DynamicFPSEnabled {
		// Dynamic mode: min and max differ, start with probing
		sc.minFPS = minFPS
		sc.maxFPS = captureFPS
		sc.currentFPS = captureFPS // Start at configured FPS, probe downward if needed
		sc.sweetSpotFPS = captureFPS
		log.Printf("[SmartCtrl] Config: %dx%d @ %d FPS for %d cameras (dynamic adaptation enabled, min=%d)",
			cfg.CaptureWidth, cfg.CaptureHeight, captureFPS, numCameras, minFPS)
	} else {
		// Fixed mode: no adaptation
		sc.minFPS = captureFPS
		sc.maxFPS = captureFPS
		sc.currentFPS = captureFPS
		sc.sweetSpotFPS = captureFPS
		log.Printf("[SmartCtrl] Config: %dx%d @ %d FPS for %d cameras (fixed, no adaptation)",
			cfg.CaptureWidth, cfg.CaptureHeight, captureFPS, numCameras)
	}

	return sc
}

// Start begins monitoring and optional FPS adaptation.
func (sc *SmartController) Start() {
	if sc.running.Swap(true) {
		return
	}

	sc.stateEnterTime = time.Now()
	sc.lastChange = time.Now()

	if sc.dynamicEnabled {
		sc.state.Store(StateProbing)
		log.Printf("[SmartCtrl] Started - dynamic FPS %d-%d, probing for sweet spot", sc.minFPS, sc.maxFPS)
	} else {
		sc.state.Store(StateStable)
		log.Printf("[SmartCtrl] Started - fixed %d FPS, monitoring only", sc.maxFPS)
	}

	// Apply initial FPS
	sc.applyFPS(sc.currentFPS)

	go sc.controlLoop()
}

// Stop halts the controller
func (sc *SmartController) Stop() {
	if !sc.running.Swap(false) {
		return
	}
	close(sc.stopCh)
}

// controlLoop runs the main control tick
func (sc *SmartController) controlLoop() {
	// Use config's perf check interval, defaulting to 1 second
	interval := time.Duration(sc.cfg.PerfCheckIntervalMS) * time.Millisecond
	if interval < 250*time.Millisecond {
		interval = 250 * time.Millisecond
	}

	ticker := time.NewTicker(interval)
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

// tick performs one monitoring + adaptation cycle
func (sc *SmartController) tick() {
	if err := sc.monitor.UpdateStats(); err != nil {
		return
	}

	sc.mutex.Lock()
	defer sc.mutex.Unlock()

	temp := sc.monitor.GetTemperature()
	load := sc.monitor.GetLoadAverage()

	sc.updateTempTrend(temp)

	if !sc.dynamicEnabled {
		// Fixed mode: monitor only, warn on critical temps
		if temp >= TempCritical {
			log.Printf("[SmartCtrl] WARNING: Temperature critical (%.1f°C) - consider improving ventilation", temp)
		}
		if sc.state.Load() != StateStable {
			sc.state.Store(StateStable)
		}
		sc.stableSeconds.Add(1)
		return
	}

	// Dynamic mode: dispatch to state handlers
	state := sc.state.Load()
	switch state {
	case StateProbing:
		sc.handleProbing(temp, load)
	case StateStable:
		sc.handleStable(temp, load)
	case StateRecovering:
		sc.handleRecovering(temp)
	case StateEmergency:
		sc.handleEmergency(temp)
	}
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

	// Is current FPS sustainable? Use config thresholds
	cpuLoadThresh := sc.cfg.CPULoadThreshold
	cpuTempThresh := sc.cfg.CPUTempThresholdC

	// Stress detection using config thresholds
	isUnderStress := temp >= cpuTempThresh || load >= cpuLoadThresh
	isLoadOK := load < LoadHigh

	// Check sustainability with thermal thresholds
	isSustainable := (temp < TempWarm) || (temp < TempHot && sc.tempTrend <= 0)

	if isSustainable && isLoadOK && !isUnderStress {
		sc.stabilityCount++
		sc.stressCount = 0

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
				sc.changeFPS(sc.currentFPS + sc.cfg.UIFPSStep)
			}
		}
	} else {
		sc.stabilityCount = 0
		sc.stressCount++

		// Use stress hold count from config before reducing
		if sc.stressCount >= sc.cfg.StressHoldCount {
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
				sc.stressCount = 0
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

	// Stress detection using config thresholds
	cpuLoadThresh := sc.cfg.CPULoadThreshold
	cpuTempThresh := sc.cfg.CPUTempThresholdC
	isUnderStress := temp >= cpuTempThresh || load >= cpuLoadThresh

	// Need to reduce?
	if temp >= TempHot || (temp >= TempWarm && sc.tempTrend > 0.5) || load >= LoadHigh || isUnderStress {
		sc.stressCount++

		if sc.stressCount >= sc.cfg.StressHoldCount {
			log.Printf("[SmartCtrl] Reducing FPS - temp: %.1f°C, load: %.2f (stress count: %d)",
				temp, load, sc.stressCount)
			newFPS := sc.currentFPS - sc.cfg.UIFPSStep
			if newFPS < sc.minFPS {
				newFPS = sc.minFPS
			}
			sc.changeFPS(newFPS)

			if newFPS < sc.sweetSpotFPS {
				sc.sweetSpotFPS = newFPS
				log.Printf("[SmartCtrl] Sweet spot lowered to %d FPS", sc.sweetSpotFPS)
			}
			sc.stressCount = 0
			return
		}
	} else {
		sc.stressCount = 0
		sc.recoverCount++
	}

	// Can we try higher? (after 30+ seconds stable, cooling, well under threshold)
	stableTime := sc.stableSeconds.Load()
	if stableTime > 30 && sc.currentFPS < sc.maxFPS &&
		temp < TempIdeal && sc.tempTrend < 0 && load < LoadIdeal &&
		sc.recoverCount >= sc.cfg.RecoverHoldCount {
		log.Printf("[SmartCtrl] Conditions excellent - trying higher FPS")
		sc.changeFPS(sc.currentFPS + sc.cfg.UIFPSStep)
		sc.stableSeconds.Store(0)
		sc.recoverCount = 0
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
		sc.recoverCount++
		if sc.recoverCount >= sc.cfg.RecoverHoldCount {
			if sc.currentFPS < sc.sweetSpotFPS {
				sc.changeFPS(sc.currentFPS + sc.cfg.UIFPSStep)
				sc.recoverCount = 0
			} else {
				log.Printf("[SmartCtrl] Recovered to sweet spot: %d FPS", sc.sweetSpotFPS)
				sc.enterState(StateStable)
			}
		}
	} else {
		sc.recoverCount = 0
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
	sc.stressCount = 0
	sc.recoverCount = 0

	log.Printf("[SmartCtrl] State: %s -> %s", stateName(oldState), stateName(int32(state)))

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

	if sc.dynamicEnabled {
		log.Printf("[SmartCtrl] %s | FPS: %d (sweet=%d, range %d-%d) | Temp: %.1f°C | Load: %.2f | Uptime: %ds",
			sc.GetState(), sc.currentFPS, sc.sweetSpotFPS, sc.minFPS, sc.maxFPS,
			temp, load, sc.stableSeconds.Load())
	} else {
		log.Printf("[SmartCtrl] Fixed mode | FPS: %d | Temp: %.1f°C | Load: %.2f | Uptime: %ds",
			sc.currentFPS, temp, load, sc.stableSeconds.Load())
	}
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
	return stateName(sc.state.Load())
}

// IsDynamic returns whether dynamic FPS adaptation is enabled
func (sc *SmartController) IsDynamic() bool {
	return sc.dynamicEnabled
}

// stateNames maps state constants to human-readable names.
var stateNames = []string{"Probing", "Stable", "Recovering", "Emergency"}

// stateName returns the name for a state value, or "Unknown" if out of range.
func stateName(state int32) string {
	if state >= 0 && int(state) < len(stateNames) {
		return stateNames[state]
	}
	return "Unknown"
}

type AdaptiveController = SmartController

func NewAdaptiveController(manager *camera.Manager, cfg *config.Config) *AdaptiveController {
	return NewSmartController(manager, cfg)
}
