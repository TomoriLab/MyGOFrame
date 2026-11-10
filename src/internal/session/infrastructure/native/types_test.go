package native

import (
	"image"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFrameReleaseOnce(t *testing.T) {
	var releases atomic.Int32
	frame := newFrame(nil, 0, 0, func() { releases.Add(1) })
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(frame.Release)
	}
	workers.Wait()
	if releases.Load() != 1 {
		t.Fatalf("released %d times", releases.Load())
	}
}

func TestFrameValueCopiesShareRelease(t *testing.T) {
	var releases atomic.Int32
	value := image.NewRGBA(image.Rect(0, 0, 2, 1))
	value.Pix[0] = 73
	frame := newFrame(value, 1, 0, func() { releases.Add(1) })
	copyOfFrame := *frame
	snapshot := copyOfFrame.CopyImage()
	if snapshot == nil || snapshot.Pix[0] != 73 {
		t.Fatal("frame copy did not produce an independent image")
	}
	copyOfFrame.Release()
	frame.Release()
	if releases.Load() != 1 {
		t.Fatalf("native lease released %d times", releases.Load())
	}
	if frame.UnsafeBorrowedImage() != nil || copyOfFrame.CopyImage() != nil {
		t.Fatal("released frame still exposes its native image")
	}
	if value.Pix != nil {
		t.Fatal("released image still exposes its native pixel slice")
	}
	if snapshot.Pix[0] != 73 {
		t.Fatal("Go-owned snapshot changed after native release")
	}
}

func TestFrameCopyImageConcurrentWithRelease(t *testing.T) {
	var releases atomic.Int32
	value := image.NewRGBA(image.Rect(0, 0, 4, 4))
	frame := newFrame(value, 1, 0, func() { releases.Add(1) })
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 100 {
				if copied := frame.CopyImage(); copied != nil && len(copied.Pix) != 64 {
					t.Errorf("unexpected copied pixel length: %d", len(copied.Pix))
				}
			}
		})
	}
	frame.Release()
	workers.Wait()
	if releases.Load() != 1 {
		t.Fatalf("native lease released %d times", releases.Load())
	}
}
