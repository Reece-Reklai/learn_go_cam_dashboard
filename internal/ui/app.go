package ui

import (
	"camera-dashboard-go/internal/camera"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"time"
)

// App represents the main camera dashboard application
type App struct {
	fyneApp fyne.App
	window  fyne.Window
	grid    *GridLayout
	manager *camera.Manager
	cameras []camera.Camera

	// Fullscreen overlay
	fullscreenWin fyne.Window
	fullscreenCam *CameraWidget
	isFullscreen  bool
}

// NewApp creates a new camera dashboard application
func NewApp() *App {
	fyneApp := app.New()
	window := fyneApp.NewWindow("Camera Dashboard - Go")
	window.Resize(fyne.NewSize(1280, 720))

	return &App{
		fyneApp: fyneApp,
		window:  window,
		grid:    NewGridLayout(),
	}
}

// Start initializes and starts the application
func (a *App) Start() {
	// Initialize cameras
	a.initializeCameras()

	// Setup UI
	a.setupUI()

	// Start camera refresh
	a.startCameraRefresh()

	// Show and run
	a.window.ShowAndRun()
}

// initializeCameras discovers and sets up cameras
func (a *App) initializeCameras() {
	// Initialize camera manager
	a.manager = camera.NewManager()

	err := a.manager.Initialize()
	if err != nil {
		// Show error dialog
		dialog := widget.NewModalPopUp(
			widget.NewLabel("Failed to initialize cameras: "+err.Error()),
			a.window.Canvas(),
		)
		dialog.Show()
		return
	}

	// Start camera capture
	err = a.manager.Start()
	if err != nil {
		dialog := widget.NewModalPopUp(
			widget.NewLabel("Failed to start cameras: "+err.Error()),
			a.window.Canvas(),
		)
		dialog.Show()
		return
	}

	a.cameras = a.manager.GetCameras()
	a.grid.UpdateCameras(a.cameras)

	// Setup click handlers for fullscreen
	a.setupFullscreenHandlers()
}

// setupFullscreenHandlers sets up click handlers for camera widgets
func (a *App) setupFullscreenHandlers() {
	widgets := a.grid.GetWidgets()
	for _, widget := range widgets {
		widget.SetClickHandler(func() {
			a.showFullscreen(widget)
		})
	}
}

// showFullscreen shows a single camera in fullscreen mode
func (a *App) showFullscreen(cameraWidget *CameraWidget) {
	if a.isFullscreen {
		return
	}

	a.isFullscreen = true

	// Create fullscreen window
	a.fullscreenWin = a.fyneApp.NewWindow("Fullscreen - " + cameraWidget.GetCamera().DeviceID)
	a.fullscreenWin.SetFullScreen(true)

	// Create fullscreen camera widget
	a.fullscreenCam = NewCameraWidget(cameraWidget.GetCamera())
	a.fullscreenCam.image = cameraWidget.image // Copy current frame

	// Click to exit fullscreen
	a.fullscreenCam.SetClickHandler(func() {
		a.hideFullscreen()
	})

	// Display in center
	content := container.NewCenter(a.fullscreenCam)
	a.fullscreenWin.SetContent(content)
	a.fullscreenWin.Show()

	// Update fullscreen widget with frames
	go a.updateFullscreenCamera(cameraWidget.GetCamera().DeviceID)
}

// hideFullscreen closes fullscreen mode
func (a *App) hideFullscreen() {
	if !a.isFullscreen {
		return
	}

	a.isFullscreen = false
	if a.fullscreenWin != nil {
		a.fullscreenWin.Close()
		a.fullscreenWin = nil
	}
	a.fullscreenCam = nil
}

// updateFullscreenCamera updates the fullscreen camera with new frames
func (a *App) updateFullscreenCamera(cameraID string) {
	for a.isFullscreen {
		frameCh := a.manager.GetFrameChannel(cameraID)
		if frameCh != nil {
			select {
			case frame := <-frameCh:
				if a.fullscreenCam != nil {
					a.fullscreenCam.UpdateFrame(frame)
				}
			default:
				time.Sleep(33 * time.Millisecond) // ~30 FPS
			}
		} else {
			break
		}
	}
}

// setupUI sets up the main UI
func (a *App) setupUI() {
	content := a.grid.GetContainer()

	// Add status bar at bottom
	statusBar := widget.NewLabel("Camera Dashboard - Initializing...")

	mainContainer := fyne.NewContainerWithLayout(
		&cameraDashboardLayout{},
		content,
		statusBar,
	)

	a.window.SetContent(mainContainer)
}

// startCameraRefresh starts periodic camera updates
func (a *App) startCameraRefresh() {
	go func() {
		ticker := time.NewTicker(33 * time.Millisecond) // ~30 FPS
		defer ticker.Stop()

		for range ticker.C {
			// Update camera widgets with new frames
			widgets := a.grid.GetWidgets()
			for i, widget := range widgets {
				if i < len(a.cameras) {
					cameraID := a.cameras[i].DeviceID
					frameCh := a.manager.GetFrameChannel(cameraID)

					// Update status
					widget.SetStatus("Connected - " + cameraID)

					// Try to get latest frame
					select {
					case frame := <-frameCh:
						widget.UpdateFrame(frame)
					default:
						// No new frame available
					}
				}
			}
		}
	}()
}

// cameraDashboardLayout is a simple layout manager
type cameraDashboardLayout struct{}

func (l *cameraDashboardLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) < 2 {
		return
	}

	// Main content takes most of the space
	objects[0].Resize(fyne.NewSize(size.Width, size.Height-30))
	objects[0].Move(fyne.NewPos(0, 0))

	// Status bar at bottom
	objects[1].Resize(fyne.NewSize(size.Width, 30))
	objects[1].Move(fyne.NewPos(0, size.Height-30))
}

func (l *cameraDashboardLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	minSize := fyne.NewSize(640, 480) // Minimum size

	// Add status bar height
	if len(objects) >= 2 {
		minSize.Height += 30
	}

	return minSize
}
