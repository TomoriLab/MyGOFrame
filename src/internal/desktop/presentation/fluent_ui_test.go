package presentation

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsCategoriesPreserveSelections(t *testing.T) {
	application := newTestApplication()
	application.selection.setModes(fixtureModes())
	application.volume = 35
	application.muted = true
	tester := ui.NewTester(application.view, 1280, 800)
	for _, label := range []string{"Audio", "Video", "Audio"} {
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
		if label == "Audio" && (!tester.HasText("Audio monitoring") || !tester.HasText("Volume 35%")) {
			t.Fatal("audio tab lost its controls or selected volume")
		}

		if err := tester.Click("Back to settings"); err != nil {
			t.Fatal(err)
		}
	}
	if application.audioSource != audioOff || !application.muted || application.selection.format != "NV12" {
		t.Fatal("changing settings categories mutated capture choices")
	}
}

func TestCompactFluentActionsRemainInsideWindow(t *testing.T) {
	application := newTestApplication()
	tester := ui.NewTester(application.view, 320, 240)
	for _, label := range []string{"MyGOFrame", "Settings", "F11"} {
		bounds, found := tester.Find(label)
		if !found || bounds.X < 0 || bounds.X+bounds.W > 320 || bounds.Y < 0 || bounds.Y+bounds.H > 240 {
			t.Fatalf("compact action %q exceeds the window: %+v", label, bounds)
		}
	}
	bounds, found := tester.Find("Capture window")
	if !found || bounds.X < 0 || bounds.X+bounds.W > 320 {
		t.Fatal("compact capture-window action is unavailable")
	}

}

func TestFluentAppearanceFollowsSystemTheme(t *testing.T) {
	application := newTestApplication()
	tester := ui.NewTester(application.view, 1280, 800)
	for _, dark := range []bool{false, true, false} {
		tester.SetDark(dark)
		expected := fluentAppearance(dark).theme.Background
		pixel := tester.Image().RGBAAt(0, 0)
		if pixel.R != expected.R || pixel.G != expected.G || pixel.B != expected.B {
			t.Fatalf("system dark=%t gave window background %v, want %v", dark, pixel, expected)
		}
	}
}

func TestFluentTextContrast(t *testing.T) {
	for _, dark := range []bool{false, true} {
		palette := fluentAppearance(dark)
		for _, pair := range [][2]ui.Color{
			{palette.theme.Text, palette.card}, {palette.theme.TextMuted, palette.card},
			{palette.theme.TextMuted, palette.theme.Background}, {palette.theme.Text, palette.theme.Surface},
			{palette.theme.AccentText, palette.theme.Accent}, {palette.theme.AccentText, palette.theme.AccentHover},
			{palette.theme.Danger, palette.card}, {palette.successForeground, palette.successBackground},
		} {
			first, second := colorLuminance(pair[0]), colorLuminance(pair[1])
			contrast := (math.Max(first, second) + 0.05) / (math.Min(first, second) + 0.05)
			if contrast < 4.5 {
				t.Errorf("dark=%t text contrast %.2f is below 4.5:1 for %v", dark, contrast, pair)
			}
		}
	}
}

func colorLuminance(color ui.Color) float64 {
	channel := func(value uint8) float64 {
		linear := float64(value) / 255
		if linear <= 0.04045 {
			return linear / 12.92
		}
		return math.Pow((linear+0.055)/1.055, 2.4)
	}
	return channel(color.R)*0.2126 + channel(color.G)*0.7152 + channel(color.B)*0.0722
}

func TestSettingsOrderToolbarGroupingAndThemeSwitch(t *testing.T) {
	application := newTestApplication()
	tester := ui.NewTester(application.view, 1280, 800)
	if tester.HasText("Sharing") {
		t.Fatal("redundant Sharing category remains")
	}
	previous := float32(-1)
	for _, label := range []string{"Video settings", "Audio settings", "Recording settings", "Presets settings"} {
		bounds, found := tester.Find(label)
		if !found || bounds.Y <= previous {
			t.Fatalf("incorrect category order for %s", label)
		}
		previous = bounds.Y
	}
	capture, _ := tester.Find("Capture window")
	fullscreen, _ := tester.Find("Fullscreen · F11")
	recording, _ := tester.Find("Record")
	if capture.X+capture.W > fullscreen.X || fullscreen.X+fullscreen.W > recording.X {
		t.Fatal("window actions and recording are not ordered correctly")
	}
	before := tester.Image().RGBAAt(0, 0)
	if err := tester.Click("Theme switch"); err != nil {
		t.Fatal(err)
	}
	if !application.themeOverride || tester.Image().RGBAAt(0, 0) == before {
		t.Fatal("native theme switch did not change appearance")
	}
	dark := application.darkTheme
	tester.SetDark(!dark)
	if application.darkTheme != dark {
		t.Fatal("system appearance overrode an explicit selection")
	}
	if err := tester.Click("Theme switch"); err != nil {
		t.Fatal(err)
	}
	if application.darkTheme == dark {
		t.Fatal("theme switch did not toggle back")
	}
}
