package ui

import (
	"camera-dashboard-go/internal/camera"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// GridLayout manages the camera grid layout (similar to Python version)
type GridLayout struct {
	container *fyne.Container
	widgets   []*CameraWidget
}

// NewGridLayout creates a new grid layout for cameras
func NewGridLayout() *GridLayout {
	return &GridLayout{
		container: container.NewVBox(), // Will be updated with actual grid
		widgets:   make([]*CameraWidget, 0),
	}
}

// UpdateCameras updates the grid with new camera list
func (gl *GridLayout) UpdateCameras(cameras []camera.Camera) {
	// Clear existing widgets
	for _, widget := range gl.widgets {
		widget.Hide()
	}
	gl.widgets = gl.widgets[:0]

	// Create new camera widgets
	var cameraContainers []fyne.CanvasObject
	for _, cam := range cameras {
		cameraWidget := NewCameraWidget(cam)
		gl.widgets = append(gl.widgets, cameraWidget)
		cameraContainers = append(cameraContainers, cameraWidget)
	}

	// Determine grid layout based on camera count
	var grid *fyne.Container
	switch len(cameras) {
	case 1:
		grid = container.NewVBox(cameraContainers...)
	case 2:
		grid = container.NewHBox(cameraContainers...)
	case 3:
		// Two on top, one on bottom (like Python version)
		grid = container.NewVBox(
			container.NewHBox(cameraContainers[0], cameraContainers[1]),
			container.NewHBox(cameraContainers[2]),
		)
	default:
		// Fallback to grid layout for 4+ cameras
		grid = container.NewGridWithColumns(2, cameraContainers...)
	}

	// Add settings tile (top-left corner like Python version)
	settingsTile := widget.NewButton("Settings", func() {
		// TODO: Implement settings
	})
	settingsTile.Resize(fyne.NewSize(100, 100))

	// Create final container with settings overlay
	gl.container = container.NewStack(grid, settingsTile)
}

// GetContainer returns the Fyne container
func (gl *GridLayout) GetContainer() *fyne.Container {
	return gl.container
}

// GetWidgets returns all camera widgets
func (gl *GridLayout) GetWidgets() []*CameraWidget {
	return gl.widgets
}
