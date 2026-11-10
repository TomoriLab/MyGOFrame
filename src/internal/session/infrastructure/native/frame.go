package native

import (
	"image"
	"image/draw"
	"sync"
	"unsafe"
)

type frameState struct {
	mutex    sync.RWMutex
	once     sync.Once
	image    *image.RGBA
	dmaToken unsafe.Pointer
	isDMA    bool
	release  func()
}

// Frame shares its native lease state across value copies.
type Frame struct {
	Sequence             uint64
	TimestampNanoseconds uint64
	width                int
	height               int
	state                *frameState
}

func newFrame(value *image.RGBA, sequence, timestampNanoseconds uint64, release func()) *Frame {
	frame := &Frame{
		Sequence:             sequence,
		TimestampNanoseconds: timestampNanoseconds,
		state:                &frameState{image: value, release: release},
	}
	if value != nil {
		frame.width, frame.height = value.Rect.Dx(), value.Rect.Dy()
	}
	return frame
}

func newDMAFrame(token unsafe.Pointer, width, height int, sequence, timestampNanoseconds uint64, release func()) *Frame {
	return &Frame{
		Sequence: sequence, TimestampNanoseconds: timestampNanoseconds,
		width: width, height: height,
		state: &frameState{dmaToken: token, isDMA: true, release: release},
	}
}

// IsDMA reports whether this frame uses an opaque DMA-BUF lease instead of CPU pixels.
func (frame *Frame) IsDMA() bool {
	return frame != nil && frame.state != nil && frame.state.isDMA
}

// Size returns immutable frame dimensions independently of its release state.
func (frame *Frame) Size() (int, int) {
	if frame == nil {
		return 0, 0
	}
	return frame.width, frame.height
}

// NewImageFrame wraps a Go-owned image without a native release operation.
func NewImageFrame(value *image.RGBA, sequence uint64) *Frame {
	if value != nil {
		imageValue := *value
		value = &imageValue
	}
	return newFrame(value, sequence, 0, nil)
}

// UnsafeBorrowedImage exposes native pixels for the zero-copy presenter.
// The caller must stop all access, including retained Pix slices, before Release.
// Access must not race with Release; use CopyImage for an independently owned image.
func (frame *Frame) UnsafeBorrowedImage() *image.RGBA {
	if frame == nil || frame.state == nil {
		return nil
	}
	frame.state.mutex.RLock()
	defer frame.state.mutex.RUnlock()
	return frame.state.image
}

// CopyImage returns Go-owned CPU pixels that remain valid after Release.
// DMA-BUF frames have no CPU image and return nil.
func (frame *Frame) CopyImage() *image.RGBA {
	if frame == nil || frame.state == nil {
		return nil
	}
	frame.state.mutex.RLock()
	defer frame.state.mutex.RUnlock()
	value := frame.state.image
	if value == nil || value.Pix == nil {
		return nil
	}
	result := image.NewRGBA(value.Rect)
	draw.Draw(result, result.Rect, value, value.Rect.Min, draw.Src)
	return result
}

func (frame *Frame) Release() {
	if frame == nil || frame.state == nil {
		return
	}
	state := frame.state
	state.once.Do(func() {
		state.mutex.Lock()
		release := state.release
		state.release = nil
		state.dmaToken = nil
		if state.image != nil {
			state.image.Pix = nil
			state.image = nil
		}
		state.mutex.Unlock()
		if release != nil {
			release()
		}
	})
}

func (frame *Frame) SequenceNumber() uint64   { return frame.Sequence }
func (frame *Frame) CaptureTimestamp() uint64 { return frame.TimestampNanoseconds }
