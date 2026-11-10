package application

import "image"

// Frame exposes a leased image without exposing a native token or allocation address.
type Frame interface {
	SequenceNumber() uint64
	CaptureTimestamp() uint64
	IsDMA() bool
	Size() (int, int)
	UnsafeBorrowedImage() *image.RGBA
	CopyImage() *image.RGBA
	Release()
}

// GPUImage is implemented by a context-bound native presenter.
type GPUImage interface {
	SetFrame(Frame)
	Texture(uint64) uint32
	Release(uint64)
	Abandon(uint64)
	Close()
	TakeError() error
	Metrics() []GPURenderMetrics
	Prepared() bool
}
