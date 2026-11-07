package main

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsCategoriesPreserveSelections(t *testing.T) {
	application := newApplication()
	application.selection.setModes(fixtureModes())
	application.volume = 35
	application.muted = true
	tester := ui.NewTester(application.view, 1280, 800)
	for _, label := range []string{"Audio", "Sharing", "Video", "Audio"} {
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
		if label == "Audio" && (!tester.HasText("Audio monitoring") || !tester.HasText("Volume 35%")) {
			t.Fatal("audio tab lost its controls or selected volume")
		}
		if label == "Sharing" && !tester.HasText("Open capture window") {
			t.Fatal("sharing tab did not expose its native capture window action")
		}
	}
	if application.audioSource != audioOff || !application.muted || application.selection.format != "NV12" {
		t.Fatal("changing settings categories mutated capture choices")
	}
}

func TestCompactFluentActionsRemainInsideWindow(t *testing.T) {
	application := newApplication()
	tester := ui.NewTester(application.view, 320, 240)
	for _, label := range []string{"MyGOFrame", "Settings", "F11"} {
		bounds, found := tester.Find(label)
		if !found || bounds.X < 0 || bounds.X+bounds.W > 320 || bounds.Y < 0 || bounds.Y+bounds.H > 240 {
			t.Fatalf("compact action %q exceeds the window: %+v", label, bounds)
		}
	}
	if err := tester.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("Sharing"); err != nil {
		t.Fatal(err)
	}
	if !tester.HasText("Open capture window") {
		t.Fatal("compact settings do not expose the capture window")
	}
}

func TestFluentAppearanceFollowsSystemTheme(t *testing.T) {
	application := newApplication()
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
