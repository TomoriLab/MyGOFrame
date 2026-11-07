package main

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPreviewKeepsAllSourceCornersVisible(t *testing.T) {
	for _, source := range [][2]int{{1920, 1080}, {3840, 2160}, {640, 480}, {1080, 1920}} {
		for _, fullscreen := range []bool{false, true} {
			application := newApplication()
			application.bitmap = ui.NewBitmap(previewPattern(source[0], source[1]))
			application.fullscreen = fullscreen
			tester := ui.NewTester(application.view, 1280, 758)
			for _, scenario := range []struct {
				width, height int
				scale         float32
			}{
				{1280, 758, 1}, {800, 600, 1.25}, {600, 900, 1.5}, {1280, 360, 2}, {640, 360, 1}, {320, 240, 2}, {1920, 1080, 1},
			} {
				dimensions := [2]int{scenario.width, scenario.height}
				t.Run(fmt.Sprintf("%dx%d_in_%dx%d_scale%g_fullscreen_%t", source[0], source[1], dimensions[0], dimensions[1], scenario.scale, fullscreen), func(t *testing.T) {
					tester.SetScale(scenario.scale)
					tester.SetSize(dimensions[0], dimensions[1])
					viewport, found := tester.Find("Video viewport")
					if !found || viewport.W <= 0 || viewport.H <= 0 || viewport.X < 0 || viewport.Y < 0 || viewport.X+viewport.W > float32(dimensions[0])+1 || viewport.Y+viewport.H > float32(dimensions[1])+1 {
						t.Fatalf("video viewport exceeds window: %+v in %v", viewport, dimensions)
					}
					scale := math.Min(float64(viewport.W)/float64(source[0]), float64(viewport.H)/float64(source[1]))
					fittedWidth, fittedHeight := float64(source[0])*scale, float64(source[1])*scale
					left := float64(viewport.X) + (float64(viewport.W)-fittedWidth)/2
					top := float64(viewport.Y) + (float64(viewport.H)-fittedHeight)/2
					rendered := tester.Image()
					for _, corner := range []struct {
						x, y  float64
						color color.RGBA
					}{
						{1.0 / 16, 1.0 / 16, color.RGBA{255, 0, 0, 255}},
						{15.0 / 16, 1.0 / 16, color.RGBA{0, 255, 0, 255}},
						{1.0 / 16, 15.0 / 16, color.RGBA{0, 0, 255, 255}},
						{15.0 / 16, 15.0 / 16, color.RGBA{255, 255, 0, 255}},
						{0.005, 0.5, color.RGBA{255, 255, 255, 255}},
						{0.995, 0.5, color.RGBA{255, 255, 255, 255}},
						{0.5, 0.005, color.RGBA{255, 255, 255, 255}},
						{0.5, 0.995, color.RGBA{255, 255, 255, 255}},
					} {
						x, y := int((left+fittedWidth*corner.x)*float64(scenario.scale)), int((top+fittedHeight*corner.y)*float64(scenario.scale))
						if actual := rendered.RGBAAt(x, y); actual != corner.color {
							t.Errorf("corner (%0.2f,%0.2f) not fitted: pixel(%d,%d)=%v, want %v; viewport=%+v", corner.x, corner.y, x, y, actual, corner.color, viewport)
						}
					}
				})
			}
		}
	}
}

func TestCompactSettingsRemainAccessible(t *testing.T) {
	application := newApplication()
	tester := ui.NewTester(application.view, 320, 240)
	if _, found := tester.Find("Capture settings"); found {
		t.Fatal("fixed sidebar consumes the compact video viewport")
	}
	if err := tester.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	dialog, found := tester.Find("Capture settings dialog")
	if !found || dialog.X < 0 || dialog.Y < 0 || dialog.X+dialog.W > 320 || dialog.Y+dialog.H > 240 {
		t.Fatalf("settings dialog exceeds compact window: %+v", dialog)
	}
	tester.Key(0, ui.KeyEscape)
	if application.settingsOpen {
		t.Fatal("Escape did not close compact settings")
	}
}

func TestLongStatusDoesNotConsumePreview(t *testing.T) {
	application := newApplication()
	application.bitmap = ui.NewBitmap(previewPattern(1920, 1080))
	application.errorMessage = strings.Repeat("A disconnected capture device needs to be selected again. ", 12)
	tester := ui.NewTester(application.view, 320, 240)
	viewport, found := tester.Find("Video viewport")
	if !found || viewport.W <= 0 || viewport.H < 80 || viewport.Y+viewport.H > 240 {
		t.Fatalf("status consumed preview: %+v", viewport)
	}
}

func TestOpenCompactSettingsResizeToSidebar(t *testing.T) {
	application := newApplication()
	tester := ui.NewTester(application.view, 320, 240)
	if err := tester.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tester.SetSize(1280, 800)
	if _, found := tester.Find("Capture settings"); !found {
		t.Fatal("wide window did not restore the settings sidebar")
	}
	if application.settingsOpen {
		t.Fatal("compact settings dialog remained open over its restored sidebar")
	}
	if _, found := tester.Find("Capture settings dialog"); found {
		t.Fatal("compact settings dialog remained in the wide layout")
	}
	tester.SetSize(320, 240)
	if err := tester.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	if !application.settingsOpen {
		t.Fatal("compact settings could not reopen after a wide resize")
	}
}
