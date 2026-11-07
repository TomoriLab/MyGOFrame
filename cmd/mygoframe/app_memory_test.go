package main

import (
	"image"
	"testing"

	"github.com/TomoriLab/MyGOFrame/internal/core"
	"github.com/egoist/mygo/ui"
)

func TestDetachedPixelsReuseImmutableBackingAcrossResizes(t *testing.T) {
	application := newApplication()
	firstImage := image.NewRGBA(image.Rect(0, 0, 4, 4))
	firstImage.Pix[0] = 255
	firstImage.Pix[3] = 255
	first := core.NewImageFrame(firstImage, 1)
	application.displayedFrame = first
	application.bitmap = ui.NewBitmap(first.UnsafeBorrowedImage())
	application.detachPixels()
	firstBacking := application.detachedPixels
	if len(firstBacking) != 4*4*4 {
		t.Fatalf("first detached image has %d bytes", len(firstBacking))
	}
	firstAddress := &firstBacking[0]
	first.Release()

	second := core.NewImageFrame(image.NewRGBA(image.Rect(0, 0, 2, 2)), 2)
	application.bitmap.Update(second.UnsafeBorrowedImage())
	application.displayedFrame = second
	application.detachPixels()
	if &application.detachedPixels[0] != firstAddress {
		t.Fatal("smaller detached image allocated another backing")
	}
	second.Release()

	third := core.NewImageFrame(image.NewRGBA(image.Rect(0, 0, 8, 8)), 3)
	application.bitmap.Update(third.UnsafeBorrowedImage())
	application.displayedFrame = third
	application.detachPixels()
	third.Release()
	if len(application.detachedPixels) != 8*8*4 {
		t.Fatalf("grown detached image has %d bytes", len(application.detachedPixels))
	}
	for index, value := range firstBacking {
		if value != 0 {
			t.Fatalf("retained blank scene changed at byte %d", index)
		}
	}
}
