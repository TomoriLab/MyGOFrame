package application

import (
	"image"
	"image/draw"
	"sync"
)

type imageFrame struct {
	mutex         sync.RWMutex
	image         *image.RGBA
	sequence      uint64
	width, height int
}

// NewImageFrame owns an image header for synthetic and independently owned previews.
func NewImageFrame(value *image.RGBA, sequence uint64) Frame {
	frame := &imageFrame{sequence: sequence}
	if value != nil {
		header := *value
		frame.image = &header
		frame.width, frame.height = value.Rect.Dx(), value.Rect.Dy()
	}
	return frame
}
func (frame *imageFrame) SequenceNumber() uint64   { return frame.sequence }
func (frame *imageFrame) CaptureTimestamp() uint64 { return 0 }
func (frame *imageFrame) IsDMA() bool              { return false }
func (frame *imageFrame) Size() (int, int)         { return frame.width, frame.height }
func (frame *imageFrame) UnsafeBorrowedImage() *image.RGBA {
	frame.mutex.RLock()
	defer frame.mutex.RUnlock()
	return frame.image
}
func (frame *imageFrame) CopyImage() *image.RGBA {
	frame.mutex.RLock()
	defer frame.mutex.RUnlock()
	if frame.image == nil {
		return nil
	}
	result := image.NewRGBA(frame.image.Rect)
	draw.Draw(result, result.Rect, frame.image, frame.image.Rect.Min, draw.Src)
	return result
}
func (frame *imageFrame) Release() {
	frame.mutex.Lock()
	defer frame.mutex.Unlock()
	if frame.image != nil {
		frame.image.Pix = nil
		frame.image = nil
	}
}
