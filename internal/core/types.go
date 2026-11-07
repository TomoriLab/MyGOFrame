package core

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"sync"
)

type VideoDevice struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type VideoMode struct {
	FourCC              uint32 `json:"fourcc"`
	Format              string `json:"format"`
	Width               uint32 `json:"width"`
	Height              uint32 `json:"height"`
	IntervalNumerator   uint32 `json:"intervalNumerator"`
	IntervalDenominator uint32 `json:"intervalDenominator"`
}

func (mode VideoMode) Resolution() string { return fmt.Sprintf("%d × %d", mode.Width, mode.Height) }

func (mode VideoMode) FrameRate() string {
	if mode.IntervalNumerator == 0 {
		return "Unknown"
	}
	return fmt.Sprintf("%.3f fps (%d/%d)", float64(mode.IntervalDenominator)/float64(mode.IntervalNumerator), mode.IntervalDenominator, mode.IntervalNumerator)
}

type AudioDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type AudioDevices struct {
	Sources []AudioDevice `json:"sources"`
	Outputs []AudioDevice `json:"outputs"`
}

type VideoConfig struct {
	Path       string
	Mode       VideoMode
	ColorRange int32
}

func FourCC(value string) (uint32, error) {
	if len(value) != 4 {
		return 0, fmt.Errorf("pixel format must contain four ASCII characters")
	}
	for _, character := range []byte(value) {
		if character < 32 || character > 126 {
			return 0, fmt.Errorf("pixel format must contain four ASCII characters")
		}
	}
	return binary.LittleEndian.Uint32([]byte(value)), nil
}

type frameState struct {
	mutex   sync.RWMutex
	once    sync.Once
	image   *image.RGBA
	release func()
}

// Frame shares its native lease state across value copies.
type Frame struct {
	Sequence             uint64
	TimestampNanoseconds uint64
	state                *frameState
}

func newFrame(value *image.RGBA, sequence, timestampNanoseconds uint64, release func()) *Frame {
	return &Frame{
		Sequence:             sequence,
		TimestampNanoseconds: timestampNanoseconds,
		state:                &frameState{image: value, release: release},
	}
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

// CopyImage returns Go-owned pixels that remain valid after Release.
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
