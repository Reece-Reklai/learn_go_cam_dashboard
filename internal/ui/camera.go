package ui

import (
	"camera-dashboard-go/internal/camera"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
	"image"
	"sync"
	"time"
)

// CameraWidget represents a single camera display widget
type CameraWidget struct {
	widget.BaseWidget
	camera    camera.Camera
	image     *canvas.Image
	container *fyne.Container
	status    string

	// Frame management
	frame     image.Image
	frameLock sync.RWMutex

	// Touch/gesture handling
	pressTime   time.Time
	longPress   bool
	onClick     func()
	onLongPress func()
}

// NewCameraWidget creates a new camera widget
func NewCameraWidget(cam camera.Camera) *CameraWidget {
	w := &CameraWidget{
		camera: cam,
		status: "Initializing...",
	}

	w.ExtendBaseWidget(w)

	// Create image placeholder (will be updated with actual frames)
	w.image = canvas.NewImageFromResource(nil)
	w.image.FillMode = canvas.ImageFillStretch

	// Create container with border
	w.container = container.NewBorder(
		nil,                       // top
		widget.NewLabel(w.status), // bottom
		nil,                       // left
		nil,                       // right
		w.image,
	)

	return w
}

// CreateRenderer implements fyne.Widget
func (w *CameraWidget) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(w.container)
}

// Tapped handles single tap/click events
func (w *CameraWidget) Tapped(e *fyne.PointEvent) {
	duration := time.Since(w.pressTime)

	if duration < 400*time.Millisecond && !w.longPress {
		// Short click - trigger fullscreen
		if w.onClick != nil {
			w.onClick()
		}
	}

	w.longPress = false
}

// TappedSecondary handles right-click
func (w *CameraWidget) TappedSecondary(e *fyne.PointEvent) {
	// Alternative fullscreen trigger
	if w.onClick != nil {
		w.onClick()
	}
}

// MouseDown handles mouse/touch press
func (w *CameraWidget) MouseDown(*desktop.MouseEvent) {
	w.pressTime = time.Now()

	// Start long press timer
	go func() {
		time.Sleep(400 * time.Millisecond)
		if time.Since(w.pressTime) >= 400*time.Millisecond {
			w.longPress = true
			if w.onLongPress != nil {
				w.onLongPress()
			}
		}
	}()
}

// SetClickHandler sets the click handler
func (w *CameraWidget) SetClickHandler(handler func()) {
	w.onClick = handler
}

// SetLongPressHandler sets the long press handler
func (w *CameraWidget) SetLongPressHandler(handler func()) {
	w.onLongPress = handler
}

// UpdateFrame updates the displayed frame
func (w *CameraWidget) UpdateFrame(img image.Image) {
	w.frameLock.Lock()
	w.frame = img
	w.frameLock.Unlock()

	// Update UI on main thread
	w.image.Image = img
	w.image.Refresh()
}

// SetStatus updates the status text
func (w *CameraWidget) SetStatus(status string) {
	w.status = status
	if w.container.Objects[1] != nil {
		if label, ok := w.container.Objects[1].(*widget.Label); ok {
			label.SetText(status)
		}
	}
}

// GetCamera returns the camera associated with this widget
func (w *CameraWidget) GetCamera() camera.Camera {
	return w.camera
}
