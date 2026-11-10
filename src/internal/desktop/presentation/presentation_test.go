package presentation

import (
	"fmt"
	"image/color"
	"testing"

	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/application"

	"github.com/egoist/mygo/ui"
)

func TestCaptureWindowContainsOnlyCompleteVideo(t *testing.T) {
	for _, source := range [][2]int{{1920, 1080}, {1080, 1920}} {
		for _, dimensions := range [][2]int{{320, 180}, {320, 240}, {600, 900}, {1280, 360}} {
			t.Run(fmt.Sprintf("%dx%d_in_%dx%d", source[0], source[1], dimensions[0], dimensions[1]), func(t *testing.T) {
				application := newTestApplication()
				application.bitmap = ui.NewBitmap(previewPattern(source[0], source[1]))
				application.notice = "Private device information"
				application.errorMessage = "Private capture error"
				presentation := &presentationWindow{application: application}
				tester := ui.NewTester(presentation.view, dimensions[0], dimensions[1])
				if text := tester.Texts(); len(text) != 1 || text[0] != "Capture window viewport" {
					t.Fatalf("capture window exposed interface text: %v", text)
				}
				bounds, found := tester.Find("Capture window viewport")
				if !found || bounds.X != 0 || bounds.Y != 0 || bounds.W != float32(dimensions[0]) || bounds.H != float32(dimensions[1]) {
					t.Fatalf("capture viewport does not fill its window: %+v", bounds)
				}
				fitted := fitVideoRectangle(bounds, source[0], source[1])
				for _, corner := range []struct {
					x, y  float32
					color color.RGBA
				}{
					{1.0 / 16, 1.0 / 16, color.RGBA{255, 0, 0, 255}},
					{15.0 / 16, 1.0 / 16, color.RGBA{0, 255, 0, 255}},
					{1.0 / 16, 15.0 / 16, color.RGBA{0, 0, 255, 255}},
					{15.0 / 16, 15.0 / 16, color.RGBA{255, 255, 0, 255}},
				} {
					pixel := tester.Image().RGBAAt(int(fitted.X+fitted.W*corner.x), int(fitted.Y+fitted.H*corner.y))
					if pixel != corner.color {
						t.Fatalf("capture window lost a source corner: got %v, want %v", pixel, corner.color)
					}
				}
				if !presentation.displayedBitmap || presentation.renderCount == 0 {
					t.Fatal("capture window did not record its rendered content")
				}
			})
		}
	}
}

func TestCaptureWindowConsumesFramesWithoutControlWindow(t *testing.T) {
	application := newTestApplication()
	application.worker = &captureWorker{}
	frame := capture.NewImageFrame(previewPattern(640, 480), 41)
	application.worker.frames.replace(frame)
	presentation := &presentationWindow{application: application}
	tester := ui.NewTester(presentation.view, 640, 480)
	if application.displayedFrame != frame || presentation.displayedSequence != 41 {
		t.Fatal("capture window depends on the control view to consume frames")
	}
	if queued, available := application.worker.frames.take(); available {
		queued.Release()
		t.Fatal("rendered frame remained queued")
	}
	application.clearPreview()
	tester.Frame()
	if presentation.displayedBitmap || presentation.displayedSequence != 0 {
		t.Fatal("stopped capture still reports video content")
	}
	image := tester.Image()
	for y := range image.Rect.Dy() {
		for x := range image.Rect.Dx() {
			if pixel := image.RGBAAt(x, y); pixel != (color.RGBA{0, 0, 0, 255}) {
				t.Fatalf("stopped capture did not clear to black at %d,%d: %v", x, y, pixel)
			}
		}
	}
}

func TestCaptureWindowFullscreenIsIndependent(t *testing.T) {
	application := newTestApplication()
	presentation := &presentationWindow{application: application}
	tester := ui.NewTester(presentation.view, 320, 180)
	tester.Key(0, ui.KeyF11)
	if !presentation.fullscreen || application.fullscreen {
		t.Fatal("capture fullscreen changed the controls window")
	}
	tester.Key(0, ui.KeyEscape)
	if presentation.fullscreen {
		t.Fatal("Escape did not leave capture fullscreen")
	}
}
