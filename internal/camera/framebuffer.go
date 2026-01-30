package camera

import (
	"image"
	"sync"
	"sync/atomic"
	"time"
)

// FrameBuffer provides lock-free access to the latest frame
// Capture writes at max speed, UI reads when ready
type FrameBuffer struct {
	// Double-buffering with atomic swap
	frames     [2]image.Image
	writeIndex atomic.Int32
	readIndex  atomic.Int32

	// Frame metadata
	frameCount   atomic.Uint64
	lastFrameAt  atomic.Int64 // Unix nano timestamp
	droppedCount atomic.Uint64

	// Stats for performance monitoring
	captureStartTime time.Time
	mu               sync.RWMutex
}

// NewFrameBuffer creates a new frame buffer
func NewFrameBuffer() *FrameBuffer {
	fb := &FrameBuffer{
		captureStartTime: time.Now(),
	}
	fb.writeIndex.Store(0)
	fb.readIndex.Store(1)
	return fb
}

// Write stores a new frame (called by capture goroutine)
// This is non-blocking and always succeeds
func (fb *FrameBuffer) Write(frame image.Image) {
	// Write to current write slot
	writeIdx := fb.writeIndex.Load()
	fb.frames[writeIdx] = frame

	// Atomic swap - make written frame available for reading
	fb.writeIndex.Store(1 - writeIdx)
	fb.readIndex.Store(writeIdx)

	fb.frameCount.Add(1)
	fb.lastFrameAt.Store(time.Now().UnixNano())
}

// Read returns the latest frame (called by UI goroutine)
// Returns nil if no frame available yet
func (fb *FrameBuffer) Read() image.Image {
	readIdx := fb.readIndex.Load()
	return fb.frames[readIdx]
}

// ReadIfNew returns the frame only if it's newer than lastRead
// Returns nil if no new frame, avoiding unnecessary UI refreshes
func (fb *FrameBuffer) ReadIfNew(lastRead uint64) (image.Image, uint64, bool) {
	currentCount := fb.frameCount.Load()
	if currentCount <= lastRead {
		return nil, lastRead, false
	}

	readIdx := fb.readIndex.Load()
	frame := fb.frames[readIdx]
	return frame, currentCount, true
}

// GetFrameCount returns total frames captured
func (fb *FrameBuffer) GetFrameCount() uint64 {
	return fb.frameCount.Load()
}

// GetLastFrameTime returns when the last frame was captured
func (fb *FrameBuffer) GetLastFrameTime() time.Time {
	nanos := fb.lastFrameAt.Load()
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

// GetCaptureStats returns capture performance stats
func (fb *FrameBuffer) GetCaptureStats() (fps float64, totalFrames uint64, uptime time.Duration) {
	fb.mu.RLock()
	startTime := fb.captureStartTime
	fb.mu.RUnlock()

	uptime = time.Since(startTime)
	totalFrames = fb.frameCount.Load()

	if uptime.Seconds() > 0 {
		fps = float64(totalFrames) / uptime.Seconds()
	}
	return
}

// GetActualFPS returns the measured FPS over last second
func (fb *FrameBuffer) GetActualFPS() float64 {
	lastTime := fb.GetLastFrameTime()
	if lastTime.IsZero() {
		return 0
	}

	// Measure based on recent frame times
	elapsed := time.Since(lastTime)
	if elapsed > time.Second {
		return 0 // No recent frames
	}

	fps, _, _ := fb.GetCaptureStats()
	return fps
}

// Reset clears the buffer and stats
func (fb *FrameBuffer) Reset() {
	fb.mu.Lock()
	fb.frames[0] = nil
	fb.frames[1] = nil
	fb.frameCount.Store(0)
	fb.droppedCount.Store(0)
	fb.lastFrameAt.Store(0)
	fb.captureStartTime = time.Now()
	fb.mu.Unlock()
}

// MarkDropped increments dropped frame counter
func (fb *FrameBuffer) MarkDropped() {
	fb.droppedCount.Add(1)
}

// GetDroppedCount returns number of dropped frames
func (fb *FrameBuffer) GetDroppedCount() uint64 {
	return fb.droppedCount.Load()
}
