//go:build linux && cgo

package native

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
)

func testDMAFrame(sequence uint64, release func()) *Frame {
	return newDMAFrame(unsafe.Pointer(new(byte)), 4, 2, sequence, 100, release)
}

func fakeGPUImage() (*GPUImage, *atomic.Int32, *atomic.Int32) {
	created, destroyed := new(atomic.Int32), new(atomic.Int32)
	presenter := &GPUImage{gpuImageState: &gpuImageState{contexts: make(map[uint64]gpuContext)}}
	presenter.operations = gpuOperations{
		create: func() (unsafe.Pointer, error) {
			created.Add(1)
			return unsafe.Pointer(new(byte)), nil
		},
		render: func(_, _ unsafe.Pointer) (uint32, GPURenderMetrics, error) {
			return 7, GPURenderMetrics{Sequence: 1, Width: 4, Height: 2}, nil
		},
		destroy: func(unsafe.Pointer) { destroyed.Add(1) },
		abandon: func(unsafe.Pointer) { destroyed.Add(1) },
	}
	return presenter, created, destroyed
}

func TestDMAFrameCopiesShareReleaseAndKeepMetadata(t *testing.T) {
	var releases atomic.Int32
	frame := testDMAFrame(9, func() { releases.Add(1) })
	copyOfFrame := *frame
	if !frame.IsDMA() || frame.CopyImage() != nil || frame.UnsafeBorrowedImage() != nil {
		t.Fatal("DMA frame exposed CPU image storage")
	}
	copyOfFrame.Release()
	frame.Release()
	if releases.Load() != 1 {
		t.Fatalf("DMA token released %d times", releases.Load())
	}
	if width, height := frame.Size(); width != 4 || height != 2 || !frame.IsDMA() {
		t.Fatal("release invalidated immutable DMA metadata")
	}
	if frame.state.dmaToken != nil {
		t.Fatal("released DMA token remains accessible")
	}
}

func TestGPUImageSeparatesContextsAndDefersDestruction(t *testing.T) {
	presenter, created, destroyed := fakeGPUImage()
	frame := testDMAFrame(1, nil)
	defer frame.Release()
	presenter.SetFrame(frame)
	for _, context := range []uint64{10, 10, 20} {
		if texture := presenter.Texture(context); texture != 7 {
			t.Fatalf("context %d returned texture %d", context, texture)
		}
	}
	if created.Load() != 2 || len(presenter.Metrics()) != 2 {
		t.Fatal("GPU contexts or unique metrics were not isolated")
	}
	presenter.Close()
	if presenter.Texture(10) != 0 || destroyed.Load() != 0 {
		t.Fatal("Close issued context-dependent destruction")
	}
	presenter.Release(10)
	presenter.Release(10)
	presenter.Abandon(20)
	if destroyed.Load() != 2 {
		t.Fatalf("retired %d contexts", destroyed.Load())
	}
}

func TestGPUImageRejectsReleasedFrameAndReportsFailureOnce(t *testing.T) {
	presenter, created, _ := fakeGPUImage()
	frame := testDMAFrame(1, nil)
	frame.Release()
	presenter.SetFrame(frame)
	if presenter.Texture(1) != 0 || created.Load() != 0 {
		t.Fatal("released DMA frame was sent to the native renderer")
	}
	frame = testDMAFrame(2, nil)
	defer frame.Release()
	presenter.SetFrame(frame)
	presenter.operations.create = func() (unsafe.Pointer, error) { return nil, errors.New("unsupported import") }
	if presenter.Texture(1) != 0 || presenter.TakeError() == nil || presenter.TakeError() != nil {
		t.Fatal("GPU failure was not published once")
	}
	if presenter.Texture(2) != 0 {
		t.Fatal("failed presenter resumed rendering")
	}
}

func TestGPUImageRenderPinsNativeFrameUntilSubmissionReturns(t *testing.T) {
	presenter, _, _ := fakeGPUImage()
	var releases atomic.Int32
	frame := testDMAFrame(1, func() { releases.Add(1) })
	presenter.SetFrame(frame)
	entered, proceed := make(chan struct{}), make(chan struct{})
	presenter.operations.render = func(_, _ unsafe.Pointer) (uint32, GPURenderMetrics, error) {
		close(entered)
		<-proceed
		if releases.Load() != 0 {
			t.Error("native frame was released during submission")
		}
		return 7, GPURenderMetrics{Sequence: 1}, nil
	}
	var workers sync.WaitGroup
	workers.Go(func() { presenter.Texture(1) })
	<-entered
	workers.Go(frame.Release)
	close(proceed)
	workers.Wait()
	if releases.Load() != 1 {
		t.Fatal("frame release did not complete after submission")
	}
	presenter.Release(1)
}

func TestGPUImageCopiesShareContextRetirement(t *testing.T) {
	presenter, _, destroyed := fakeGPUImage()
	frame := testDMAFrame(1, nil)
	defer frame.Release()
	presenter.SetFrame(frame)
	copyOfPresenter := *presenter
	copyOfPresenter.Texture(1)
	presenter.Release(1)
	copyOfPresenter.Release(1)
	if destroyed.Load() != 1 {
		t.Fatal("copied presenter destroyed the same context twice")
	}
	var zero GPUImage
	zero.Close()
	zero.Release(1)
	zero.Abandon(1)
	if zero.Texture(1) != 0 || zero.Prepared() || zero.TakeError() != nil {
		t.Fatal("zero-value presenter exposed a native context")
	}
}
