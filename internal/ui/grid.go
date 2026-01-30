package ui

import (
	"camera-dashboard-go/internal/camera"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// GridLayout manages the camera grid layout
// Layout: 2x2 grid with Settings in top-left, 3 cameras in remaining slots
type GridLayout struct {
	container      *fyne.Container
	widgets        []*CameraWidget
	settingsWidget *fyne.Container
	onSettings     func()
}

// NewGridLayout creates a new grid layout for cameras
func NewGridLayout() *GridLayout {
	gl := &GridLayout{
		container: container.NewVBox(),
		widgets:   make([]*CameraWidget, 0),
	}

	// Create settings widget
	gl.settingsWidget = gl.createSettingsWidget()

	return gl
}

// createSettingsWidget creates the settings tile for the grid
func (gl *GridLayout) createSettingsWidget() *fyne.Container {
	settingsBtn := widget.NewButton("Settings", func() {
		if gl.onSettings != nil {
			gl.onSettings()
		}
	})

	// Create a container with the button centered
	return container.NewCenter(settingsBtn)
}

// SetSettingsHandler sets the callback for settings button
func (gl *GridLayout) SetSettingsHandler(handler func()) {
	gl.onSettings = handler
}

// UpdateCameras updates the grid with new camera list
// Creates a 2x2 grid: [Settings, Cam1], [Cam2, Cam3]
func (gl *GridLayout) UpdateCameras(cameras []camera.Camera) {
	// Clear existing widgets
	gl.widgets = gl.widgets[:0]

	// Create camera widgets (up to 3 cameras)
	maxCameras := 3
	if len(cameras) < maxCameras {
		maxCameras = len(cameras)
	}

	for i := 0; i < maxCameras; i++ {
		cameraWidget := NewCameraWidget(cameras[i])
		gl.widgets = append(gl.widgets, cameraWidget)
	}

	// Build 2x2 grid layout
	// Top row: Settings | Camera 1 (or placeholder)
	// Bottom row: Camera 2 (or placeholder) | Camera 3 (or placeholder)

	var topLeft, topRight, bottomLeft, bottomRight fyne.CanvasObject

	// Top-left is always settings
	topLeft = gl.settingsWidget

	// Top-right: Camera 1
	if len(gl.widgets) >= 1 {
		topRight = gl.widgets[0].Container
	} else {
		topRight = widget.NewLabel("No Camera")
	}

	// Bottom-left: Camera 2
	if len(gl.widgets) >= 2 {
		bottomLeft = gl.widgets[1].Container
	} else {
		bottomLeft = widget.NewLabel("No Camera")
	}

	// Bottom-right: Camera 3
	if len(gl.widgets) >= 3 {
		bottomRight = gl.widgets[2].Container
	} else {
		bottomRight = widget.NewLabel("No Camera")
	}

	// Create 2x2 grid
	gl.container = container.NewGridWithColumns(2,
		topLeft, topRight,
		bottomLeft, bottomRight,
	)
}

// GetContainer returns the Fyne container
func (gl *GridLayout) GetContainer() *fyne.Container {
	return gl.container
}

// GetWidgets returns all camera widgets
func (gl *GridLayout) GetWidgets() []*CameraWidget {
	return gl.widgets
}
