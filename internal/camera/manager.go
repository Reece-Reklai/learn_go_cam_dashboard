package camera

import (
	"fmt"
	"image"
	"sync"
)

// Manager manages multiple cameras and capture workers
type Manager struct {
	cameras       []Camera
	workers       []*CaptureWorker
	frameChannels map[string]chan image.Image
	running       bool
	mutex         sync.RWMutex
}

// NewManager creates a new camera manager
func NewManager() *Manager {
	return &Manager{
		frameChannels: make(map[string]chan image.Image),
	}
}

// Initialize discovers and initializes cameras
func (m *Manager) Initialize() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Stop existing workers
	m.Stop()

	// Discover cameras
	cameras, err := DiscoverCameras()
	if err != nil {
		return err
	}

	m.cameras = cameras
	m.workers = make([]*CaptureWorker, len(cameras))
	m.frameChannels = make(map[string]chan image.Image)

	// Create capture workers for each camera
	for i, camera := range cameras {
		frameCh := make(chan image.Image, 1) // Latest-frame-only buffer
		worker := NewCaptureWorker(camera, frameCh)

		m.workers[i] = worker
		m.frameChannels[camera.DeviceID] = frameCh
	}

	m.running = true
	return nil
}

// Start starts all camera capture workers
func (m *Manager) Start() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if !m.running {
		return ErrManagerNotInitialized
	}

	for _, worker := range m.workers {
		if err := worker.Start(); err != nil {
			return err
		}
	}

	return nil
}

// Stop stops all camera capture workers
func (m *Manager) Stop() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.running = false

	// Stop all workers
	for _, worker := range m.workers {
		worker.Stop()
	}

	// Close all frame channels
	for _, ch := range m.frameChannels {
		close(ch)
	}

	m.workers = nil
	m.frameChannels = make(map[string]chan image.Image)
}

// GetCameras returns list of cameras
func (m *Manager) GetCameras() []Camera {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	cameras := make([]Camera, len(m.cameras))
	copy(cameras, m.cameras)
	return cameras
}

// GetFrameChannel returns frame channel for a specific camera
func (m *Manager) GetFrameChannel(cameraID string) <-chan image.Image {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if ch, exists := m.frameChannels[cameraID]; exists {
		return ch
	}

	return nil
}

// Errors
var (
	ErrManagerNotInitialized = fmt.Errorf("camera manager not initialized")
)
