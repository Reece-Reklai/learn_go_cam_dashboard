package ui

import (
	"camera-dashboard-go/internal/camera"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"image"
	"image/color"
	"sync"
)

// CameraWidget represents a single camera display
type CameraWidget struct {
	Camera      camera.Camera
	CanvasImage *canvas.Image
	Container   *fyne.Container
	Label       *widget.Label

	// Frame management
	frame     image.Image
	frameLock sync.RWMutex

	// Click handler
	onClick func()
}

// NewCameraWidget creates a new camera widget
func NewCameraWidget(cam camera.Camera) *CameraWidget {
	w := &CameraWidget{
		Camera: cam,
	}

	// Create placeholder image (bright red so we can see it)
	placeholder := image.NewRGBA(image.Rect(0, 0, 320, 240))
	red := color.RGBA{255, 0, 0, 255}
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			placeholder.Set(x, y, red)
		}
	}
	w.frame = placeholder

	// Create canvas image - use same approach as working test
	w.CanvasImage = canvas.NewImageFromImage(placeholder)
	w.CanvasImage.FillMode = canvas.ImageFillOriginal // Same as working test
	w.CanvasImage.SetMinSize(fyne.NewSize(320, 240))

	// Create status label
	w.Label = widget.NewLabel(cam.DeviceID)

	// Create container - use VBox with image on top, label at bottom
	w.Container = container.NewVBox(
		w.CanvasImage,
		w.Label,
	)

	return w
}

// UpdateFrame updates the displayed frame
func (w *CameraWidget) UpdateFrame(img image.Image) {
	if img == nil {
		return
	}

	w.frameLock.Lock()
	w.frame = img
	w.frameLock.Unlock()

	// Update canvas image and refresh
	w.CanvasImage.Image = img
	w.CanvasImage.Refresh()
}

// SetStatus updates the status text
func (w *CameraWidget) SetStatus(status string) {
	w.Label.SetText(status)
}

// GetCamera returns the camera
func (w *CameraWidget) GetCamera() camera.Camera {
	return w.Camera
}

// SetClickHandler sets the click handler (for fullscreen)
func (w *CameraWidget) SetClickHandler(handler func()) {
	w.onClick = handler
}
