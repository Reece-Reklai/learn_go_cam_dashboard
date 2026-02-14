package ui

import (
	"camera-dashboard-go/internal/camera"
	"camera-dashboard-go/internal/config"
	"camera-dashboard-go/internal/helpers"
	"camera-dashboard-go/internal/perf"
	"fmt"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
	"image"
	"image/color"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// App represents the main camera dashboard application
type App struct {
	fyneApp fyne.App
	window  fyne.Window
	manager *camera.Manager
	cameras []camera.Camera
	cfg     *config.Config

	// Grid positions (4 slots: 0=top-left, 1=top-right, 2=bottom-left, 3=bottom-right)
	// Each slot can contain: -1 = settings, 0-2 = camera index
	gridSlots [4]int // What content is in each grid position

	// Camera display
	cameraImages  [3]*canvas.Image
	cameraFrames  [3]image.Image
	cameraWidgets [3]*TappableImage // References to camera TappableImage widgets
	cameraStatus  [3]bool           // true = connected, false = disconnected
	lastFrameRead [3]uint64         // Last frame timestamp read from each buffer
	frameLock     sync.RWMutex      // Protects cameras, cameraFrames, cameraStatus, lastFrameTime

	// All 4 grid widgets (for highlighting during swap)
	gridWidgets [4]Highlightable

	// UI state
	swapMode          bool
	swapSourceSlot    int // Grid position (0-3)
	isFullscreen      atomic.Bool
	fullscreenSlot    int
	fullscreenImg     *canvas.Image
	fullscreenWidget  *TappableImage
	fullscreenContent *fyne.Container
	fullscreenStopCh  chan struct{} // Stops the fullscreen update goroutine
	fullscreenMu      sync.Mutex    // Protects fullscreen state transitions
	gridContent       *fyne.Container
	grid              *fyne.Container

	// Hot-plug detection
	hotplugStopCh      chan struct{}
	reinitInProgress   bool // Prevents concurrent reinitializations
	reinitLock         sync.Mutex
	lastDisconnectTime [3]time.Time // Per-camera debounce tracking
	cleanupOnce        sync.Once    // Prevents double close of hotplugStopCh

	// Stale frame detection + bounded auto-restart
	lastFrameTime   [3]time.Time   // When each camera last produced a frame
	restartEvents   [3][]time.Time // Sliding window of restart timestamps
	lastRestartTime [3]time.Time   // Last restart timestamp per camera
	restartLimitHit [3]bool        // Whether restart limit was reached

	// Night mode
	nightModeEnabled atomic.Bool
	nightModeBufs    [3]*image.RGBA // Reusable buffers for night mode (one per camera slot)
	nightModeFSBuf   *image.RGBA    // Reusable buffer for fullscreen night mode

	// Performance management
	perfController *perf.AdaptiveController
}

// Highlightable interface for widgets that can be highlighted during swap
type Highlightable interface {
	SetHighlight(on bool)
}

// NewApp creates a new camera dashboard application
func NewApp(cfg *config.Config) *App {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	fyneApp := app.New()
	window := fyneApp.NewWindow("Camera Dashboard - Go")

	window.Resize(fyne.NewSize(800, 480))
	window.SetFullScreen(true)

	a := &App{
		fyneApp:        fyneApp,
		window:         window,
		cfg:            cfg,
		swapSourceSlot: -1,
		hotplugStopCh:  make(chan struct{}),
	}

	// Initialize grid slot assignments:
	// Slot 0 (top-left) = -1 (settings)
	// Slot 1 (top-right) = 0 (camera 0)
	// Slot 2 (bottom-left) = 1 (camera 1)
	// Slot 3 (bottom-right) = 2 (camera 2 / placeholder)
	a.gridSlots[0] = -1 // Settings
	a.gridSlots[1] = 0  // Camera 0
	a.gridSlots[2] = 1  // Camera 1
	a.gridSlots[3] = 2  // Camera 2 (placeholder)

	// Initialize all cameras as disconnected initially
	a.cameraStatus[0] = false
	a.cameraStatus[1] = false
	a.cameraStatus[2] = false

	// Create camera images
	bgColor := color.RGBA{25, 25, 25, 255}
	for i := 0; i < 3; i++ {
		placeholder := createColoredImage(400, 240, bgColor)
		a.cameraFrames[i] = placeholder
		a.cameraImages[i] = canvas.NewImageFromImage(placeholder)
		a.cameraImages[i].FillMode = canvas.ImageFillStretch // Fill entire cell, no black bars
	}

	return a
}

func createColoredImage(width, height int, c color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	r, g, b, a := c.RGBA()
	r8, g8, b8, a8 := uint8(r>>8), uint8(g>>8), uint8(b>>8), uint8(a>>8)

	// Fill first row with direct Pix writes
	stride := img.Stride
	for x := 0; x < width; x++ {
		off := x * 4
		img.Pix[off+0] = r8
		img.Pix[off+1] = g8
		img.Pix[off+2] = b8
		img.Pix[off+3] = a8
	}
	// Copy first row to remaining rows
	firstRow := img.Pix[:stride]
	for y := 1; y < height; y++ {
		copy(img.Pix[y*stride:(y+1)*stride], firstRow)
	}
	return img
}

func (a *App) Start() {
	a.setupUI()
	a.window.Show()
	go a.initializeCamerasAsync()
	a.startCameraRefresh()
	go a.startHotplugDetection()
	go a.startStaleFrameDetection()
	go a.startHealthLogging()
	a.fyneApp.Run()
}

// TappableImage is an image that can be tapped and long-pressed
type TappableImage struct {
	widget.BaseWidget
	image           *canvas.Image
	bg              *canvas.Rectangle
	border          *canvas.Rectangle
	disconnectLabel *canvas.Text
	onTap           func()
	onLongTap       func()
	pressStart      time.Time
	longPressTimer  *time.Timer
	longPressFired  bool
	tapHandled      bool // Prevents double-firing from MouseUp + Tapped
	highlighted     bool
	disconnected    bool
	mu              sync.Mutex
}

func NewTappableImage(img *canvas.Image, bgColor color.Color, onTap, onLongTap func()) *TappableImage {
	t := &TappableImage{
		image:     img,
		bg:        canvas.NewRectangle(bgColor),
		border:    canvas.NewRectangle(color.Transparent),
		onTap:     onTap,
		onLongTap: onLongTap,
	}
	t.border.StrokeWidth = 4
	t.border.StrokeColor = color.Transparent

	// Create disconnected label (hidden by default)
	t.disconnectLabel = canvas.NewText("Disconnected", color.RGBA{180, 180, 180, 255})
	t.disconnectLabel.TextSize = 18
	t.disconnectLabel.Alignment = fyne.TextAlignCenter
	t.disconnectLabel.Hidden = true

	t.ExtendBaseWidget(t)
	return t
}

func (t *TappableImage) CreateRenderer() fyne.WidgetRenderer {
	// Stack: bg, image, disconnected label centered, border on top
	labelContainer := container.NewCenter(t.disconnectLabel)
	c := container.NewStack(t.bg, t.image, labelContainer, t.border)
	return widget.NewSimpleRenderer(c)
}

// SetHighlight sets the border highlight for swap mode
func (t *TappableImage) SetHighlight(on bool) {
	t.mu.Lock()
	t.highlighted = on
	t.mu.Unlock()

	if on {
		t.border.StrokeColor = color.RGBA{255, 200, 0, 255} // Yellow border
	} else {
		t.border.StrokeColor = color.Transparent
	}
	t.border.Refresh()
}

// SetDisconnected shows or hides the "Disconnected" label
func (t *TappableImage) SetDisconnected(disconnected bool) {
	t.mu.Lock()
	t.disconnected = disconnected
	t.mu.Unlock()

	if disconnected {
		t.disconnectLabel.Hidden = false
		// Show dark placeholder image
		t.image.Hidden = true
	} else {
		t.disconnectLabel.Hidden = true
		t.image.Hidden = false
	}
	t.disconnectLabel.Refresh()
	t.image.Refresh()
}

// IsDisconnected returns whether this camera slot is disconnected
func (t *TappableImage) IsDisconnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.disconnected
}

// MouseDown starts the long-press timer
func (t *TappableImage) MouseDown(ev *desktop.MouseEvent) {
	t.mu.Lock()
	t.pressStart = time.Now()
	t.longPressFired = false
	t.tapHandled = false

	// Cancel any existing timer
	if t.longPressTimer != nil {
		t.longPressTimer.Stop()
	}

	// Start long-press timer (500ms)
	t.longPressTimer = time.AfterFunc(500*time.Millisecond, func() {
		t.mu.Lock()
		t.longPressFired = true
		t.tapHandled = true // Don't fire tap after long press
		t.mu.Unlock()

		log.Println("[UI] Long press detected!")
		if t.onLongTap != nil {
			t.onLongTap()
		}
	})
	t.mu.Unlock()
}

// MouseUp cancels the long-press timer if not yet fired
func (t *TappableImage) MouseUp(ev *desktop.MouseEvent) {
	t.mu.Lock()
	if t.longPressTimer != nil {
		t.longPressTimer.Stop()
		t.longPressTimer = nil
	}
	fired := t.longPressFired
	handled := t.tapHandled
	if !fired && !handled {
		t.tapHandled = true // Mark as handled so Tapped doesn't fire again
	}
	t.mu.Unlock()

	// If long press wasn't fired and not yet handled, treat as regular tap
	if !fired && !handled {
		log.Println("[UI] Tapped!")
		if t.onTap != nil {
			t.onTap()
		}
	}
}

// Tapped handles touch taps (fallback for touch devices without mouse events)
func (t *TappableImage) Tapped(_ *fyne.PointEvent) {
	t.mu.Lock()
	handled := t.tapHandled
	fired := t.longPressFired
	if !handled && !fired {
		t.tapHandled = true
	}
	t.mu.Unlock()

	// Only fire if not already handled by MouseUp
	if !handled && !fired {
		log.Println("[UI] Tapped (touch)!")
		if t.onTap != nil {
			t.onTap()
		}
	}
}

func (t *TappableImage) TappedSecondary(_ *fyne.PointEvent) {
	// Right-click also triggers long-press action
	log.Println("[UI] Secondary tap (right-click)")
	if t.onLongTap != nil {
		t.onLongTap()
	}
}

// TappableSettings is the settings widget with swap support
type TappableSettings struct {
	widget.BaseWidget
	bg             *canvas.Rectangle
	border         *canvas.Rectangle
	content        *fyne.Container
	nightModeBtn   *widget.Button
	onTap          func()
	onLongTap      func()
	pressStart     time.Time
	longPressTimer *time.Timer
	longPressFired bool
	tapHandled     bool
	highlighted    bool
	mu             sync.Mutex
}

func NewTappableSettings(onRestart, onExit, onNightModeToggle, onTap, onLongTap func()) *TappableSettings {
	t := &TappableSettings{
		bg:        canvas.NewRectangle(color.RGBA{50, 50, 55, 255}),
		border:    canvas.NewRectangle(color.Transparent),
		onTap:     onTap,
		onLongTap: onLongTap,
	}
	t.border.StrokeWidth = 4
	t.border.StrokeColor = color.Transparent

	restartBtn := widget.NewButton("Restart", func() {
		if onRestart != nil {
			onRestart()
		}
	})

	t.nightModeBtn = widget.NewButton("Nightmode: Off", func() {
		if onNightModeToggle != nil {
			onNightModeToggle()
		}
	})

	exitBtn := widget.NewButton("Exit", func() {
		if onExit != nil {
			onExit()
		}
	})

	t.content = container.NewCenter(container.NewVBox(restartBtn, t.nightModeBtn, exitBtn))
	t.ExtendBaseWidget(t)
	return t
}

// SetNightModeLabel updates the night mode button label.
func (t *TappableSettings) SetNightModeLabel(enabled bool) {
	if t.nightModeBtn == nil {
		return
	}
	if enabled {
		t.nightModeBtn.SetText("Nightmode: On")
	} else {
		t.nightModeBtn.SetText("Nightmode: Off")
	}
}

func (t *TappableSettings) CreateRenderer() fyne.WidgetRenderer {
	c := container.NewStack(t.bg, t.content, t.border)
	return widget.NewSimpleRenderer(c)
}

// SetHighlight sets the border highlight for swap mode
func (t *TappableSettings) SetHighlight(on bool) {
	t.mu.Lock()
	t.highlighted = on
	t.mu.Unlock()

	if on {
		t.border.StrokeColor = color.RGBA{255, 200, 0, 255} // Yellow border
	} else {
		t.border.StrokeColor = color.Transparent
	}
	t.border.Refresh()
}

// MouseDown starts the long-press timer
func (t *TappableSettings) MouseDown(ev *desktop.MouseEvent) {
	t.mu.Lock()
	t.pressStart = time.Now()
	t.longPressFired = false
	t.tapHandled = false

	if t.longPressTimer != nil {
		t.longPressTimer.Stop()
	}

	t.longPressTimer = time.AfterFunc(500*time.Millisecond, func() {
		t.mu.Lock()
		t.longPressFired = true
		t.tapHandled = true
		t.mu.Unlock()

		log.Println("[UI] Settings: Long press detected!")
		if t.onLongTap != nil {
			t.onLongTap()
		}
	})
	t.mu.Unlock()
}

// MouseUp cancels the long-press timer if not yet fired
func (t *TappableSettings) MouseUp(ev *desktop.MouseEvent) {
	t.mu.Lock()
	if t.longPressTimer != nil {
		t.longPressTimer.Stop()
		t.longPressTimer = nil
	}
	fired := t.longPressFired
	handled := t.tapHandled
	if !fired && !handled {
		t.tapHandled = true
	}
	t.mu.Unlock()

	if !fired && !handled {
		log.Println("[UI] Settings: Tapped!")
		if t.onTap != nil {
			t.onTap()
		}
	}
}

// Tapped handles touch taps
func (t *TappableSettings) Tapped(_ *fyne.PointEvent) {
	t.mu.Lock()
	handled := t.tapHandled
	fired := t.longPressFired
	if !handled && !fired {
		t.tapHandled = true
	}
	t.mu.Unlock()

	if !handled && !fired {
		log.Println("[UI] Settings: Tapped (touch)!")
		if t.onTap != nil {
			t.onTap()
		}
	}
}

func (a *App) setupUI() {
	// Dark background
	background := canvas.NewRectangle(color.RGBA{20, 20, 20, 255})

	// Settings widget with Restart/Night Mode/Exit buttons and swap support
	var settingsWidget *TappableSettings
	settingsWidget = NewTappableSettings(
		func() {
			log.Println("[UI] Restart clicked")
			a.restart()
		},
		func() {
			log.Println("[UI] Exit clicked")
			a.cleanup()
		},
		func() {
			a.toggleNightMode()
			settingsWidget.SetNightModeLabel(a.nightModeEnabled.Load())
		},
		func() { a.onWidgetTap(settingsWidget) },
		func() { a.onWidgetLongPress(settingsWidget) },
	)
	a.gridWidgets[0] = settingsWidget

	// Camera widgets with tap handlers
	var cam1, cam2, cam3 *TappableImage

	cam1 = NewTappableImage(
		a.cameraImages[0],
		color.RGBA{25, 25, 25, 255},
		func() { a.onWidgetTap(cam1) },
		func() { a.onWidgetLongPress(cam1) },
	)
	a.gridWidgets[1] = cam1
	a.cameraWidgets[0] = cam1
	cam1.SetDisconnected(true) // Start disconnected until camera detected

	cam2 = NewTappableImage(
		a.cameraImages[1],
		color.RGBA{25, 25, 25, 255},
		func() { a.onWidgetTap(cam2) },
		func() { a.onWidgetLongPress(cam2) },
	)
	a.gridWidgets[2] = cam2
	a.cameraWidgets[1] = cam2
	cam2.SetDisconnected(true) // Start disconnected until camera detected

	cam3 = NewTappableImage(
		a.cameraImages[2],
		color.RGBA{25, 25, 25, 255},
		func() { a.onWidgetTap(cam3) },
		func() { a.onWidgetLongPress(cam3) },
	)
	a.gridWidgets[3] = cam3
	a.cameraWidgets[2] = cam3
	cam3.SetDisconnected(true) // Start disconnected until camera detected

	// Dynamic grid layout based on number of widgets (settings + cameras)
	totalSlots := 1 + a.cfg.CameraSlotCount // 1 settings panel + N camera slots
	gridRows, gridCols := helpers.GetSmartGrid(totalSlots)
	a.grid = container.New(&fillGridLayout{rows: gridRows, cols: gridCols},
		settingsWidget, cam1,
		cam2, cam3,
	)

	// Prepare fullscreen image (reused) - use Stretch to fill screen
	a.fullscreenImg = canvas.NewImageFromImage(createColoredImage(800, 480, color.RGBA{0, 0, 0, 255}))
	a.fullscreenImg.FillMode = canvas.ImageFillStretch

	// Fullscreen tappable widget
	a.fullscreenWidget = NewTappableImage(
		a.fullscreenImg,
		color.RGBA{0, 0, 0, 255},
		func() { a.hideFullscreen() },
		nil,
	)

	// Fullscreen content (black bg + image)
	fsBg := canvas.NewRectangle(color.RGBA{0, 0, 0, 255})
	a.fullscreenContent = container.NewStack(fsBg, a.fullscreenWidget)
	a.fullscreenContent.Hide()

	// Grid content
	a.gridContent = container.NewStack(background, a.grid)

	// Main content with both layers
	content := container.NewStack(a.gridContent, a.fullscreenContent)
	a.window.SetContent(content)
}

// fillGridLayout is a custom layout that fills all available space in a grid
type fillGridLayout struct {
	rows, cols int
}

func (g *fillGridLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(100, 100)
}

func (g *fillGridLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) == 0 {
		return
	}

	cellWidth := size.Width / float32(g.cols)
	cellHeight := size.Height / float32(g.rows)

	for i, obj := range objects {
		row := i / g.cols
		col := i % g.cols

		x := float32(col) * cellWidth
		y := float32(row) * cellHeight

		obj.Move(fyne.NewPos(x, y))
		obj.Resize(fyne.NewSize(cellWidth, cellHeight))
	}
}

// onGridTap handles tap on any grid position (0-3)
func (a *App) onGridTap(gridPos int) {
	log.Printf("[UI] Grid tap on position %d, swapMode=%v", gridPos, a.swapMode)

	if a.swapMode {
		a.handleSwapTap(gridPos)
	} else {
		a.showFullscreen(gridPos)
	}
}

// onGridLongPress handles long-press on any grid position (0-3)
func (a *App) onGridLongPress(gridPos int) {
	log.Printf("[UI] Long press on grid position %d", gridPos)
	a.swapMode = true
	a.swapSourceSlot = gridPos

	// Highlight the selected widget
	if a.gridWidgets[gridPos] != nil {
		a.gridWidgets[gridPos].SetHighlight(true)
	}

	log.Printf("[UI] Swap mode - selected position %d, tap another to swap", gridPos)
}

// findWidgetPosition finds the current grid position of a widget
func (a *App) findWidgetPosition(widget Highlightable) int {
	for i := 0; i < 4; i++ {
		if a.gridWidgets[i] == widget {
			return i
		}
	}
	return -1
}

// onWidgetTap handles tap on a widget, finding its current position dynamically
func (a *App) onWidgetTap(widget Highlightable) {
	gridPos := a.findWidgetPosition(widget)
	if gridPos < 0 {
		log.Println("[UI] Widget tap: widget not found in grid")
		return
	}
	a.onGridTap(gridPos)
}

// onWidgetLongPress handles long-press on a widget, finding its current position dynamically
func (a *App) onWidgetLongPress(widget Highlightable) {
	gridPos := a.findWidgetPosition(widget)
	if gridPos < 0 {
		log.Println("[UI] Widget long-press: widget not found in grid")
		return
	}
	a.onGridLongPress(gridPos)
}

func (a *App) handleSwapTap(gridPos int) {
	if a.swapSourceSlot < 0 {
		a.swapSourceSlot = gridPos
		// Highlight selected widget
		if a.gridWidgets[gridPos] != nil {
			a.gridWidgets[gridPos].SetHighlight(true)
		}
		log.Printf("[UI] Swap: selected position %d", gridPos)
	} else if a.swapSourceSlot == gridPos {
		// Cancel selection
		if a.gridWidgets[gridPos] != nil {
			a.gridWidgets[gridPos].SetHighlight(false)
		}
		a.swapSourceSlot = -1
		a.swapMode = false
		log.Println("[UI] Swap: cancelled")
	} else {
		// Perform swap
		a.swapGridPositions(a.swapSourceSlot, gridPos)

		// Clear all highlights and exit swap mode
		for i := 0; i < 4; i++ {
			if a.gridWidgets[i] != nil {
				a.gridWidgets[i].SetHighlight(false)
			}
		}
		a.swapMode = false
		a.swapSourceSlot = -1
		log.Println("[UI] Swap completed")
	}
}

// swapGridPositions swaps the content assignments of two grid positions
func (a *App) swapGridPositions(pos1, pos2 int) {
	log.Printf("[UI] Swapping grid positions %d and %d", pos1, pos2)

	// Swap the content assignments
	a.gridSlots[pos1], a.gridSlots[pos2] = a.gridSlots[pos2], a.gridSlots[pos1]

	// Swap the actual widgets in the grid
	objects := a.grid.Objects
	objects[pos1], objects[pos2] = objects[pos2], objects[pos1]

	// Swap widget references
	a.gridWidgets[pos1], a.gridWidgets[pos2] = a.gridWidgets[pos2], a.gridWidgets[pos1]

	// Refresh the grid layout
	a.grid.Refresh()
}

func (a *App) showFullscreen(gridPos int) {
	if a.isFullscreen.Load() {
		return
	}

	// Get the content type at this grid position
	contentType := a.gridSlots[gridPos]

	// Settings widget (-1) doesn't go fullscreen
	if contentType == -1 {
		log.Printf("[UI] Settings widget tapped - no fullscreen")
		return
	}

	// Camera index
	camIndex := contentType
	a.frameLock.RLock()
	camCount := len(a.cameras)
	a.frameLock.RUnlock()
	if camIndex >= camCount {
		log.Printf("[UI] No camera at grid position %d (camera index %d)", gridPos, camIndex)
		return
	}

	a.isFullscreen.Store(true)
	a.fullscreenSlot = gridPos
	log.Printf("[UI] Fullscreen: camera %d from grid position %d", camIndex, gridPos)

	// Get current frame and set it
	a.frameLock.RLock()
	currentFrame := a.cameraFrames[camIndex]
	a.frameLock.RUnlock()

	if currentFrame != nil {
		displayFrame := currentFrame
		if a.nightModeEnabled.Load() {
			displayFrame = applyNightMode(currentFrame)
		}
		a.fullscreenImg.Image = displayFrame
		a.fullscreenImg.Refresh()
	}

	// Show fullscreen, hide grid
	a.gridContent.Hide()
	a.fullscreenContent.Show()

	// Stop any previous fullscreen update goroutine (mutex prevents double-close)
	a.fullscreenMu.Lock()
	if a.fullscreenStopCh != nil {
		close(a.fullscreenStopCh)
	}
	a.fullscreenStopCh = make(chan struct{})
	stopCh := a.fullscreenStopCh
	a.fullscreenMu.Unlock()

	// Start fullscreen update loop
	go a.updateFullscreenLoop(camIndex, stopCh)
}

func (a *App) hideFullscreen() {
	if !a.isFullscreen.Load() {
		return
	}
	log.Println("[UI] Exiting fullscreen")
	a.isFullscreen.Store(false)

	// Stop fullscreen update goroutine (mutex prevents double-close)
	a.fullscreenMu.Lock()
	if a.fullscreenStopCh != nil {
		close(a.fullscreenStopCh)
		a.fullscreenStopCh = nil
	}
	a.fullscreenMu.Unlock()

	// Hide fullscreen, show grid
	a.fullscreenContent.Hide()
	a.gridContent.Show()
}

func (a *App) updateFullscreenLoop(camIndex int, stopCh chan struct{}) {
	uiFPS := a.cfg.UIFPS
	if uiFPS <= 0 {
		uiFPS = 15
	}
	ticker := time.NewTicker(time.Second / time.Duration(uiFPS))
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			if !a.isFullscreen.Load() {
				return
			}

			a.frameLock.RLock()
			frame := a.cameraFrames[camIndex]
			a.frameLock.RUnlock()

			if frame != nil && a.fullscreenImg != nil {
				// Apply night mode filter if enabled
				displayFrame := frame
				if a.nightModeEnabled.Load() {
					a.nightModeFSBuf = applyNightModeReuse(frame, a.nightModeFSBuf)
					displayFrame = a.nightModeFSBuf
				}

				a.fullscreenImg.Image = displayFrame
				a.fullscreenImg.Refresh()
			}
		}
	}
}

func (a *App) initializeCamerasAsync() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[UI] PANIC in camera init: %v", r)
		}
	}()

	log.Println("[UI] Starting camera initialization...")

	// Kill any processes holding camera devices (e.g., stale FFmpeg from previous run)
	if a.cfg.KillDeviceHolders {
		for _, devNum := range []int{0, 2, 4, 6, 8, 10} {
			devPath := fmt.Sprintf("/dev/video%d", devNum)
			if _, err := os.Stat(devPath); err == nil {
				helpers.KillDeviceHolders(devPath, true)
			}
		}
	}

	// Use buffer mode for decoupled capture/render with config-driven settings
	a.manager = camera.NewManagerWithSettings(camera.Settings{
		Width:  a.cfg.CaptureWidth,
		Height: a.cfg.CaptureHeight,
		FPS:    a.cfg.CaptureFPS,
		Format: a.cfg.CaptureFormat,
	}, true)

	if err := a.manager.Initialize(); err != nil {
		log.Printf("[UI] Camera init error: %v", err)
		return
	}
	log.Println("[UI] Manager initialized (buffer mode, config-driven settings)")

	if err := a.manager.Start(); err != nil {
		log.Printf("[UI] Camera start error: %v", err)
		return
	}

	cams := a.manager.GetCameras()
	a.frameLock.Lock()
	a.cameras = cams
	a.frameLock.Unlock()
	log.Printf("[UI] Discovered %d cameras", len(cams))
	for i, cam := range cams {
		log.Printf("[UI]   - %s: %s", cam.DeviceID, cam.DevicePath)
		// Mark camera as connected and update UI
		if i < 3 {
			a.updateCameraStatus(i, true)
		}
	}

	a.perfController = perf.NewAdaptiveController(a.manager, a.cfg)
	a.perfController.Start()
}

func (a *App) startCameraRefresh() {
	go func() {
		// UI refresh rate from config
		uiFPS := a.cfg.UIFPS
		if uiFPS <= 0 {
			uiFPS = 30
		}
		refreshInterval := time.Second / time.Duration(uiFPS)
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()

		frameCounters := make(map[string]uint64)

		for {
			select {
			case <-a.hotplugStopCh:
				return
			case <-ticker.C:
			}

			if a.manager == nil {
				continue
			}

			a.frameLock.RLock()
			camCount := len(a.cameras)
			cameras := make([]camera.Camera, camCount)
			copy(cameras, a.cameras)
			a.frameLock.RUnlock()

			if camCount == 0 {
				continue
			}

			// Update each camera's image (camera indices 0, 1, 2)
			for camIndex := 0; camIndex < 3 && camIndex < camCount; camIndex++ {
				cameraID := cameras[camIndex].DeviceID

				// Try buffer mode first (preferred)
				buffer := a.manager.GetFrameBuffer(cameraID)
				if buffer == nil {
					continue
				}

				// Only update if there's a new frame (avoids unnecessary refreshes)
				frame, frameNum, hasNew := buffer.ReadIfNew(a.lastFrameRead[camIndex])
				if !hasNew || frame == nil {
					continue // No new frame
				}

				a.lastFrameRead[camIndex] = frameNum

				// Track frame arrival time for stale detection
				a.frameLock.Lock()
				a.cameraFrames[camIndex] = frame
				a.lastFrameTime[camIndex] = time.Now()
				a.frameLock.Unlock()

				// Apply night mode filter if enabled
				displayFrame := frame
				if a.nightModeEnabled.Load() {
					a.nightModeBufs[camIndex] = applyNightModeReuse(frame, a.nightModeBufs[camIndex])
					displayFrame = a.nightModeBufs[camIndex]
				}

				// Update the camera image widget
				// Fyne's Refresh is thread-safe but can be slow if backed up
				a.cameraImages[camIndex].Image = displayFrame
				a.cameraImages[camIndex].Refresh()

				frameCounters[cameraID]++
				if frameCounters[cameraID]%90 == 1 { // Log every 90 frames (~3 sec at 30fps)
					fps, totalFrames, _ := buffer.GetCaptureStats()
					droppedCount := buffer.GetDroppedCount()
					log.Printf("[UI] Camera %s: frame #%d, buffer stats: %d captured, %d dropped, %.1f fps",
						cameraID, frameCounters[cameraID], totalFrames, droppedCount, fps)
				}
			}
		}
	}()
}

// updateCameraStatus updates the connected/disconnected status for a camera slot
func (a *App) updateCameraStatus(camIndex int, connected bool) {
	if camIndex < 0 || camIndex >= 3 {
		return
	}

	a.frameLock.Lock()
	previousStatus := a.cameraStatus[camIndex]
	a.cameraStatus[camIndex] = connected
	a.frameLock.Unlock()

	if previousStatus != connected {
		log.Printf("[UI] Camera %d status changed: connected=%v", camIndex, connected)
	}

	// Update the widget UI
	if a.cameraWidgets[camIndex] != nil {
		a.cameraWidgets[camIndex].SetDisconnected(!connected)
	}
}

// =============================================================================
// Night Mode
// =============================================================================

// toggleNightMode toggles the night mode state and logs the change.
func (a *App) toggleNightMode() {
	wasEnabled := a.nightModeEnabled.Load()
	a.nightModeEnabled.Store(!wasEnabled)
	if !wasEnabled {
		log.Println("[UI] Night mode enabled")
	} else {
		log.Println("[UI] Night mode disabled")
	}
}

// =============================================================================
// Health Logging
// =============================================================================
// Periodic summary of camera health: online, stale, and disconnected counts.
// Matches Python's log_health_summary() from utils/helpers.py.
// =============================================================================

// startHealthLogging periodically logs camera health status.
// Disabled when HealthLogIntervalSec <= 0.
func (a *App) startHealthLogging() {
	interval := a.cfg.HealthLogIntervalSec
	if interval <= 0 {
		log.Println("[Health] Health logging disabled (interval <= 0)")
		return
	}

	log.Printf("[Health] Starting health logging (every %.0fs)...", interval)

	ticker := time.NewTicker(time.Duration(interval * float64(time.Second)))
	defer ticker.Stop()

	for {
		select {
		case <-a.hotplugStopCh:
			return
		case <-ticker.C:
			a.logHealthSummary()
		}
	}
}

// logHealthSummary logs the current health status of all camera slots.
// Counts cameras as online (fresh frame), stale (frame older than threshold),
// or disconnected (not connected).
func (a *App) logHealthSummary() {
	if a.manager == nil {
		return
	}

	now := time.Now()
	staleThreshold := a.cfg.StaleFrameTimeoutSec // H7: use config instead of hardcoded 10.0
	online := 0
	stale := 0
	disconnected := 0
	totalSlots := a.cfg.CameraSlotCount

	for camIndex := 0; camIndex < totalSlots && camIndex < 3; camIndex++ {
		a.frameLock.RLock()
		connected := a.cameraStatus[camIndex]
		lastFrame := a.lastFrameTime[camIndex]
		a.frameLock.RUnlock()

		if !connected {
			disconnected++
			continue
		}

		if lastFrame.IsZero() {
			// Never received a frame — treat as stale
			stale++
			log.Printf("[Health] WARNING: camera %d has never produced a frame", camIndex)
			continue
		}

		age := now.Sub(lastFrame).Seconds()
		if age > staleThreshold {
			stale++
			log.Printf("[Health] WARNING: camera %d frame is stale (%.1fs old)", camIndex, age)
		} else {
			online++
		}
	}

	log.Printf("[Health] cameras online=%d stale=%d disconnected=%d total_slots=%d",
		online, stale, disconnected, totalSlots)
}

// =============================================================================
// Stale Frame Detection + Bounded Auto-Restart
// =============================================================================
// Matches Python's _restart_capture_if_stale() policy:
//   - STALE_FRAME_TIMEOUT_SEC: time before a frame is considered stale
//   - RESTART_COOLDOWN_SEC: minimum time between restarts for one camera
//   - MAX_RESTARTS_PER_WINDOW: max restarts allowed in RESTART_WINDOW_SEC
//   - Extended cooldown (2x window) when limit is reached
// =============================================================================

// startStaleFrameDetection periodically checks for cameras that have stopped
// producing frames and restarts their capture workers.
func (a *App) startStaleFrameDetection() {
	log.Println("[Stale] Starting stale frame detection...")

	// Check every 500ms for responsiveness
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-a.hotplugStopCh:
			return
		case <-ticker.C:
			a.checkStaleFrames()
		}
	}
}

// checkStaleFrames checks each connected camera for stale frames
func (a *App) checkStaleFrames() {
	if a.manager == nil {
		return
	}

	a.frameLock.RLock()
	camCount := len(a.cameras)
	a.frameLock.RUnlock()

	if camCount == 0 {
		return
	}

	now := time.Now()
	staleTimeout := time.Duration(a.cfg.StaleFrameTimeoutSec * float64(time.Second))

	for camIndex := 0; camIndex < 3 && camIndex < camCount; camIndex++ {
		a.frameLock.RLock()
		connected := a.cameraStatus[camIndex]
		lastFrame := a.lastFrameTime[camIndex]
		a.frameLock.RUnlock()

		if !connected {
			continue // Skip disconnected cameras
		}

		// Skip if we haven't received any frames yet (still initializing)
		if lastFrame.IsZero() {
			continue
		}

		// Check if frame is stale
		staleDuration := now.Sub(lastFrame)
		if staleDuration <= staleTimeout {
			continue // Frame is fresh
		}

		log.Printf("[Stale] Camera %d: stale frame detected (no frames for %.1fs)",
			camIndex, staleDuration.Seconds())

		// Mark as disconnected in UI
		a.updateCameraStatus(camIndex, false)

		// Attempt bounded auto-restart
		a.restartCaptureIfStale(camIndex)
	}
}

// restartCaptureIfStale implements the bounded restart policy matching Python's
// _restart_capture_if_stale(). Enforces:
//   - Cooldown between restarts (RESTART_COOLDOWN_SEC)
//   - Sliding window restart limit (MAX_RESTARTS_PER_WINDOW in RESTART_WINDOW_SEC)
//   - Extended cooldown (2x window) when limit is reached
func (a *App) restartCaptureIfStale(camIndex int) {
	now := time.Now()
	cooldown := time.Duration(a.cfg.RestartCooldownSec * float64(time.Second))
	window := time.Duration(a.cfg.RestartWindowSec * float64(time.Second))
	extendedCooldown := window * 2

	// Check cooldown
	if !a.lastRestartTime[camIndex].IsZero() && now.Sub(a.lastRestartTime[camIndex]) < cooldown {
		return // Still in cooldown
	}

	// Count recent restarts in the sliding window
	recentCount := 0
	for _, t := range a.restartEvents[camIndex] {
		if now.Sub(t) <= window {
			recentCount++
		}
	}

	if recentCount >= a.cfg.MaxRestartsPerWindow {
		// Restart limit reached - check extended cooldown
		if !a.lastRestartTime[camIndex].IsZero() && now.Sub(a.lastRestartTime[camIndex]) < extendedCooldown {
			if !a.restartLimitHit[camIndex] {
				log.Printf("[Stale] Camera %d: restart limit reached (%d/%d in %.0fs), will retry in %.0fs",
					camIndex, recentCount, a.cfg.MaxRestartsPerWindow,
					a.cfg.RestartWindowSec, extendedCooldown.Seconds())
				a.restartLimitHit[camIndex] = true
			}
			return
		}

		// Extended cooldown passed - clear events and allow restart
		log.Printf("[Stale] Camera %d: extended cooldown passed, attempting recovery", camIndex)
		a.restartEvents[camIndex] = nil
		a.restartLimitHit[camIndex] = false
	}

	// Record this restart event
	a.restartEvents[camIndex] = append(a.restartEvents[camIndex], now)
	a.lastRestartTime[camIndex] = now

	// Clean up old events outside the window
	var filtered []time.Time
	for _, t := range a.restartEvents[camIndex] {
		if now.Sub(t) <= window*2 { // Keep slightly more history
			filtered = append(filtered, t)
		}
	}
	a.restartEvents[camIndex] = filtered

	log.Printf("[Stale] Camera %d: restarting capture worker after stale frames", camIndex)

	go func(idx int) {
		if a.manager == nil {
			return
		}

		// Kill any processes holding this camera device before restart
		a.frameLock.RLock()
		var devPath string
		if idx < len(a.cameras) {
			devPath = a.cameras[idx].DevicePath
		}
		a.frameLock.RUnlock()
		if devPath != "" {
			helpers.KillDeviceHolders(devPath, a.cfg.KillDeviceHolders)
		}

		if err := a.manager.RestartCameraByIndex(idx); err != nil {
			log.Printf("[Stale] Camera %d: failed to restart: %v", idx, err)
			return
		}

		// Reset frame time so we don't immediately re-trigger
		a.frameLock.Lock()
		a.lastFrameTime[idx] = time.Now()
		a.frameLock.Unlock()

		// Mark as connected again
		a.updateCameraStatus(idx, true)
		log.Printf("[Stale] Camera %d: successfully restarted", idx)
	}(camIndex)
}

// startHotplugDetection starts a goroutine that polls for camera connect/disconnect
func (a *App) startHotplugDetection() {
	log.Println("[Hotplug] Starting camera hot-plug detection...")

	ticker := time.NewTicker(2 * time.Second) // Poll every 2 seconds
	defer ticker.Stop()

	for {
		select {
		case <-a.hotplugStopCh:
			log.Println("[Hotplug] Stopping hot-plug detection")
			return
		case <-ticker.C:
			a.checkCameraChanges()
		}
	}
}

// checkCameraChanges polls for camera connect/disconnect events
func (a *App) checkCameraChanges() {
	// Simple check: just verify device files exist (don't use v4l2-ctl to avoid conflicts with FFmpeg)
	// Check for disconnections in our existing cameras
	a.frameLock.RLock()
	cameras := make([]camera.Camera, len(a.cameras))
	copy(cameras, a.cameras)
	var statusSnapshot [3]bool
	for i := 0; i < 3 && i < len(cameras); i++ {
		statusSnapshot[i] = a.cameraStatus[i]
	}
	a.frameLock.RUnlock()

	for i, cam := range cameras {
		if i >= 3 {
			break
		}

		// Check if device file still exists
		_, err := os.Stat(cam.DevicePath)
		deviceExists := err == nil

		wasConnected := statusSnapshot[i]

		if wasConnected && !deviceExists {
			// Camera disconnected - record time for debouncing
			a.reinitLock.Lock()
			a.lastDisconnectTime[i] = time.Now()
			a.reinitLock.Unlock()
			log.Printf("[Hotplug] Camera %d (%s) disconnected", i, cam.DevicePath)
			a.updateCameraStatus(i, false)
		} else if !wasConnected && deviceExists {
			// Camera reconnected
			log.Printf("[Hotplug] Camera %d (%s) reconnected", i, cam.DevicePath)
			a.handleCameraReconnect(i)
		}
	}

	// Check for new cameras at common device paths
	a.checkForNewCameras()
}

// checkForNewCameras looks for new cameras dynamically
func (a *App) checkForNewCameras() {
	// Skip if reinit is already in progress
	a.reinitLock.Lock()
	if a.reinitInProgress {
		a.reinitLock.Unlock()
		return
	}
	a.reinitLock.Unlock()

	// Only check if we have empty slots (fewer than 3 connected cameras)
	connectedCount := 0
	a.frameLock.RLock()
	camCount := len(a.cameras)
	for i := 0; i < 3; i++ {
		if i < camCount && a.cameraStatus[i] {
			connectedCount++
		}
	}
	a.frameLock.RUnlock()
	if connectedCount >= 3 {
		return // All slots full
	}

	// Build set of existing device paths we're already tracking
	existingPaths := make(map[string]bool)
	a.frameLock.RLock()
	for _, cam := range a.cameras {
		existingPaths[cam.DevicePath] = true
	}
	a.frameLock.RUnlock()

	// Scan /dev/video* for potential new USB cameras
	// USB cameras on Pi typically use even numbers (video0, video2, video4, etc.)
	// Odd numbers (video1, video3, etc.) are usually metadata devices
	for i := 0; i <= 10; i += 2 {
		devPath := fmt.Sprintf("/dev/video%d", i)

		if existingPaths[devPath] {
			continue // Already tracking this device
		}

		// Check if device exists
		if _, err := os.Stat(devPath); err == nil {
			// Verify it's a USB camera by checking if it's a capture device
			if a.isUSBCaptureDevice(devPath, existingPaths) {
				log.Printf("[Hotplug] New USB camera detected at %s", devPath)
				a.handleNewCameraDevice(devPath)
				return // Only handle one at a time
			}
		}
	}
}

// getUSBParent returns the sysfs USB parent path for a /dev/videoX device.
// Two video nodes from the same physical USB camera share the same parent.
// Returns "" if the parent cannot be determined.
func getUSBParent(devPath string) string {
	var videoNum int
	_, err := fmt.Sscanf(devPath, "/dev/video%d", &videoNum)
	if err != nil {
		return ""
	}
	// Resolve the physical device symlink and go up one level to the USB device
	symlinkPath := fmt.Sprintf("/sys/class/video4linux/video%d/device", videoNum)
	resolved, err := filepath.EvalSymlinks(symlinkPath)
	if err != nil {
		return ""
	}
	return filepath.Dir(resolved)
}

// isUSBCaptureDevice checks if a device path is a USB video capture device
// that is NOT a secondary node of an already-tracked camera.
// Uses sysfs instead of v4l2-ctl to avoid conflicts with active FFmpeg capture.
func (a *App) isUSBCaptureDevice(devPath string, existingPaths map[string]bool) bool {
	// Extract video number from path (e.g., /dev/video0 -> 0)
	var videoNum int
	_, err := fmt.Sscanf(devPath, "/dev/video%d", &videoNum)
	if err != nil {
		return false
	}

	// Check sysfs for device type - USB capture devices have specific characteristics
	// USB cameras typically create even-numbered video devices (video0, video2, video4)
	// Odd numbers are usually metadata devices
	if videoNum%2 != 0 {
		return false // Skip odd-numbered devices (metadata)
	}

	// Check if it's a capture device by looking at sysfs
	sysfsPath := fmt.Sprintf("/sys/class/video4linux/video%d/device/modalias", videoNum)
	data, err := os.ReadFile(sysfsPath)
	if err != nil {
		return false
	}

	// USB devices have modalias starting with "usb:"
	if !strings.HasPrefix(string(data), "usb:") {
		return false
	}

	// Reject secondary nodes that share a USB parent with an already-tracked camera.
	// Multi-function USB cameras (e.g. UVC webcams) register multiple /dev/videoX nodes
	// under the same physical USB device. Only the primary capture node (typically the
	// lowest-numbered) should be treated as a camera.
	candidateParent := getUSBParent(devPath)
	if candidateParent == "" {
		return false
	}
	for existingPath := range existingPaths {
		if getUSBParent(existingPath) == candidateParent {
			return false // Same physical device as an already-tracked camera
		}
	}

	return true
}

// handleNewCameraDevice handles a newly detected camera device
func (a *App) handleNewCameraDevice(devPath string) {
	a.reinitLock.Lock()
	if a.reinitInProgress {
		a.reinitLock.Unlock()
		log.Printf("[Hotplug] Reinit already in progress, skipping new camera %s", devPath)
		return
	}
	a.reinitInProgress = true
	a.reinitLock.Unlock()

	// Find an empty/disconnected slot
	emptySlot := -1
	a.frameLock.RLock()
	for i := 0; i < 3; i++ {
		if i >= len(a.cameras) || !a.cameraStatus[i] {
			emptySlot = i
			break
		}
	}
	a.frameLock.RUnlock()

	if emptySlot < 0 {
		log.Printf("[Hotplug] New camera detected (%s) but no empty slots available", devPath)
		a.reinitLock.Lock()
		a.reinitInProgress = false
		a.reinitLock.Unlock()
		return
	}

	log.Printf("[Hotplug] Assigning new camera (%s) to slot %d", devPath, emptySlot)

	go func() {
		defer func() {
			a.reinitLock.Lock()
			a.reinitInProgress = false
			a.reinitLock.Unlock()
		}()

		// Let device settle
		time.Sleep(1500 * time.Millisecond)

		// Stop existing manager and wait for cleanup
		if a.manager != nil {
			a.manager.Stop()
			time.Sleep(500 * time.Millisecond)
		}

		// Use buffer mode for decoupled capture/render with config-driven settings
		a.manager = camera.NewManagerWithSettings(camera.Settings{
			Width:  a.cfg.CaptureWidth,
			Height: a.cfg.CaptureHeight,
			FPS:    a.cfg.CaptureFPS,
			Format: a.cfg.CaptureFormat,
		}, true)
		if err := a.manager.Initialize(); err != nil {
			log.Printf("[Hotplug] Failed to reinitialize manager: %v", err)
			return
		}
		if err := a.manager.Start(); err != nil {
			log.Printf("[Hotplug] Failed to start manager: %v", err)
			return
		}

		cams := a.manager.GetCameras()
		a.frameLock.Lock()
		a.cameras = cams
		a.frameLock.Unlock()
		log.Printf("[Hotplug] Reinitialized with %d cameras", len(cams))

		for i := range cams {
			if i < 3 {
				a.updateCameraStatus(i, true)
			}
		}
	}()
}

// handleCameraReconnect handles a camera that was disconnected and is now reconnected
// Uses per-camera restart to avoid disrupting other cameras
func (a *App) handleCameraReconnect(camIndex int) {
	// Debounce: ignore reconnects within 3 seconds of disconnect
	a.reinitLock.Lock()
	timeSinceDisconnect := time.Since(a.lastDisconnectTime[camIndex])
	if timeSinceDisconnect < 3*time.Second {
		a.reinitLock.Unlock()
		log.Printf("[Hotplug] Camera %d: Ignoring reconnect (%.1fs since disconnect, need 3s debounce)",
			camIndex, timeSinceDisconnect.Seconds())
		return
	}

	if a.reinitInProgress {
		a.reinitLock.Unlock()
		log.Printf("[Hotplug] Reinit already in progress, skipping reconnect for camera %d", camIndex)
		return
	}
	a.reinitInProgress = true
	a.reinitLock.Unlock()

	log.Printf("[Hotplug] Camera %d: Attempting per-camera restart (other cameras unaffected)...", camIndex)

	go func() {
		defer func() {
			a.reinitLock.Lock()
			a.reinitInProgress = false
			a.reinitLock.Unlock()
		}()

		// Let the device settle after reconnection
		time.Sleep(1500 * time.Millisecond)

		// Kill any stale processes holding the device before restart
		a.frameLock.RLock()
		var devPath string
		if camIndex < len(a.cameras) {
			devPath = a.cameras[camIndex].DevicePath
		}
		a.frameLock.RUnlock()
		if devPath != "" {
			helpers.KillDeviceHolders(devPath, a.cfg.KillDeviceHolders)
		}

		// Restart only this camera's worker
		if a.manager != nil {
			if err := a.manager.RestartCameraByIndex(camIndex); err != nil {
				log.Printf("[Hotplug] Camera %d: Failed to restart: %v", camIndex, err)
				return
			}
		}

		// Mark camera as connected
		a.updateCameraStatus(camIndex, true)
		log.Printf("[Hotplug] Camera %d: Successfully restarted", camIndex)
	}()
}

// cleanup stops all processes and exits cleanly
func (a *App) cleanup() {
	a.cleanupOnce.Do(func() {
		log.Println("[UI] Cleanup: stopping all processes...")

		// Stop hot-plug detection
		close(a.hotplugStopCh)

		// Stop performance controller
		if a.perfController != nil {
			a.perfController.Stop()
		}

		// Stop camera manager (kills FFmpeg processes)
		if a.manager != nil {
			a.manager.Stop()
			log.Println("[UI] Cleanup: stopped camera manager")
		}

		log.Println("[UI] Cleanup: complete, exiting...")
		a.fyneApp.Quit()
	})
}

// restart stops all processes and restarts the application
func (a *App) restart() {
	log.Println("[UI] Restart: stopping all processes...")

	// Stop performance controller
	if a.perfController != nil {
		a.perfController.Stop()
	}

	// Stop camera manager
	if a.manager != nil {
		a.manager.Stop()
	}

	// Stop all background goroutines (hotplug, stale detection, health, refresh)
	a.cleanupOnce.Do(func() {
		close(a.hotplugStopCh)
	})

	log.Println("[UI] Restart: relaunching application...")

	// Get the path to the current executable
	executable, err := os.Executable()
	if err != nil {
		log.Printf("[UI] Restart: failed to get executable path: %v", err)
		return
	}

	// Launch new instance
	cmd := exec.Command(executable)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		log.Printf("[UI] Restart: failed to start new instance: %v", err)
		return
	}

	log.Println("[UI] Restart: new instance started, exiting current...")
	a.fyneApp.Quit()
}

// Cleanup is exported for external use (e.g., from main)
func (a *App) Cleanup() {
	a.cleanup()
}
