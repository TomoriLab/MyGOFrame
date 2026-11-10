package presentation

import (
	"github.com/TomoriLab/MyGOFrame/src/internal/desktop/presentation/localization"
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestSidebarControlsAndCollapse(t *testing.T) {
	application := newTestApplication()
	tester := ui.NewTester(application.view, 1280, 800)
	if tester.HasText("Capture viewer") {
		t.Fatal("header subtitle remains")
	}
	settings, found := tester.Find("Capture settings")
	if !found {
		t.Fatal("settings missing")
	}
	start, found := tester.Find("Start capture")
	if !found || start.X < settings.X || start.X+start.W > settings.X+settings.W+1 {
		t.Fatal("capture action is outside sidebar")
	}
	if err := tester.Click("Toggle sidebar"); err != nil {
		t.Fatal(err)
	}
	if !application.sidebarCollapsed {
		t.Fatal("sidebar did not collapse")
	}
	viewport, found := tester.Find(localization.Text("en", "Video viewport"))
	if !found || viewport.X != 40 || viewport.Y != 0 || viewport.W != 1240 || viewport.H != 800 {
		t.Fatalf("collapsed viewport has unexpected bounds: %+v", viewport)
	}
	if tester.HasText("Capture controls") || tester.HasText("Capture information") {
		t.Fatal("collapsed sidebar retains sections")
	}
	if err := tester.Click("Toggle sidebar"); err != nil {
		t.Fatal(err)
	}
	if application.sidebarCollapsed {
		t.Fatal("sidebar did not expand")
	}
}

func TestSidebarActionButtonsHaveMatchingGeometry(t *testing.T) {
	for _, locale := range []string{"en", "ko", "ja"} {
		application := newTestApplication()
		tester := ui.NewTester(application.view, 1280, 800)
		tester.SetLocale(locale)
		for _, active := range []bool{false, true} {
			application.running = active
			if active {
				application.status.Recording.Phase = "recording"
			} else {
				application.status.Recording.Phase = "idle"
			}
			tester.Frame()
			record, found := tester.Find(localization.Text(locale, "Recording action"))
			if !found {
				t.Fatal("recording button missing")
			}
			capture, found := tester.Find(localization.Text(locale, "Preset video action"))
			if !found {
				t.Fatal("capture button missing")
			}
			if record.Y != capture.Y || record.H != 32 || capture.H != 32 || record.W != capture.W || record.X+record.W+8 != capture.X {
				t.Fatalf("%s active=%t: inconsistent action geometry: record=%+v capture=%+v", locale, active, record, capture)
			}
		}
	}
}

func TestSidebarSectionsAndPreviewBounds(t *testing.T) {
	for _, locale := range []string{"en", "ko", "ja"} {
		application := newTestApplication()
		tester := ui.NewTester(application.view, 1280, 800)
		tester.SetLocale(locale)
		viewport, found := tester.Find(localization.Text(locale, "Video viewport"))
		if !found || viewport.X != 330 || viewport.Y != 0 || viewport.W != 950 || viewport.H != 800 {
			t.Fatalf("%s: unexpected viewport %+v", locale, viewport)
		}
		for _, title := range []string{"Settings", "Capture controls", "Capture information"} {
			if err := tester.Click(localization.Text(locale, title)); err != nil {
				t.Fatal(err)
			}
		}
		if !application.settingsSectionClosed || !application.controlsSectionClosed || !application.informationSectionClosed {
			t.Fatal("section collapse failed")
		}
		if tester.HasText(localization.Text(locale, "Start video")) {
			t.Fatal("collapsed controls remain visible")
		}
		for _, title := range []string{"Settings", "Capture controls", "Capture information"} {
			if err := tester.Click(localization.Text(locale, title)); err != nil {
				t.Fatal(err)
			}
		}
		if application.settingsSectionClosed || application.controlsSectionClosed || application.informationSectionClosed {
			t.Fatal("section expansion failed")
		}
	}
}

func TestSidebarCompactToggleOpensSettings(t *testing.T) {
	application := newTestApplication()
	tester := ui.NewTester(application.view, 640, 480)
	if err := tester.Click("Toggle sidebar"); err != nil {
		t.Fatal(err)
	}
	if !application.settingsOpen {
		t.Fatal("compact settings did not open")
	}
	if _, found := tester.Find("Start capture"); !found {
		t.Fatal("compact capture action missing")
	}
}

func TestSidebarScrollDoesNotPaintOverHeader(t *testing.T) {
	application := newTestApplication()
	tester := ui.NewTester(application.view, 1280, 640)
	before := tester.Image()
	pixels := make([][4]uint32, 330*48)
	for y := 0; y < 48; y++ {
		for x := 0; x < 330; x++ {
			r, g, b, a := before.At(x, y).RGBA()
			pixels[y*330+x] = [4]uint32{r, g, b, a}
		}
	}
	for _, offset := range []float32{80, 160, -80, -160} {
		tester.Scroll(160, 200, 0, offset)
		tester.Frame()
		after := tester.Image()
		for y := 0; y < 48; y++ {
			for x := 0; x < 330; x++ {
				r, g, b, a := after.At(x, y).RGBA()
				if pixels[y*330+x] != [4]uint32{r, g, b, a} {
					t.Fatalf("scroll painted over header at %d,%d", x, y)
				}
			}
		}
	}
}
