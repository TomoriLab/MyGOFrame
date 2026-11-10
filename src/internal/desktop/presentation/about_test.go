package presentation

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func revealAboutControl(t *testing.T, tester *ui.Tester, scrollLabel, controlLabel string) {
	t.Helper()
	for range 50 {
		viewport, found := tester.Find(scrollLabel)
		if !found {
			t.Fatalf("missing scroll viewport %q", scrollLabel)
		}
		for _, label := range []string{"Settings panel content", "설정 패널 내용", "設定パネルの内容"} {
			if parent, exists := tester.Find(label); exists && (scrollLabel == "Settings detail" || scrollLabel == "설정 내용" || scrollLabel == "設定の内容") {
				top, bottom := max(viewport.Y, parent.Y), min(viewport.Y+viewport.H, parent.Y+parent.H)
				viewport.Y, viewport.H = top, max(float32(0), bottom-top)
			}
		}
		if bounds, exists := tester.Find(controlLabel); exists && bounds.Y >= viewport.Y && bounds.Y+bounds.H <= viewport.Y+viewport.H {
			return
		}
		tester.Scroll(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 0, 48)
		tester.Advance(time.Second)
	}
	t.Fatalf("cannot reveal %q in %q", controlLabel, scrollLabel)
}

func requireInside(t *testing.T, tester *ui.Tester, label string, outer ui.Rect) ui.Rect {
	t.Helper()
	bounds, found := tester.Find(label)
	if !found || bounds.W <= 0 || bounds.H <= 0 || bounds.X < outer.X || bounds.Y < outer.Y || bounds.X+bounds.W > outer.X+outer.W+1 || bounds.Y+bounds.H > outer.Y+outer.H+1 {
		t.Fatalf("%s exceeds container: control=%+v container=%+v", label, bounds, outer)
	}
	return bounds
}

func TestAboutNavigationAndNestedLicenseDialogs(t *testing.T) {
	for _, size := range [][2]int{{320, 240}, {640, 480}, {1280, 800}} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d_dark_%t", size[0], size[1], dark), func(t *testing.T) {
				application := newTestApplication()
				application.running = true
				tester := ui.NewTester(application.view, size[0], size[1])
				tester.SetDark(dark)
				compact := size[0] < 800
				if compact {
					if err := tester.Click("Toggle sidebar"); err != nil {
						t.Fatal(err)
					}
				}
				preset, _ := tester.Find("Presets settings")
				about, found := tester.Find("About settings")
				if !found || about.Y <= preset.Y {
					t.Fatal("About must follow Presets")
				}
				revealAboutControl(t, tester, "Settings overview", "About settings")
				if err := tester.Click("About settings"); err != nil {
					t.Fatal(err)
				}
				window := ui.Rect{W: float32(size[0]), H: float32(size[1])}
				panel := requireInside(t, tester, "About dialog", window)
				requireInside(t, tester, "Close app information", panel)
				logo, found := tester.Find("MyGOFrame logo")
				if !found || logo.W != 96 || logo.H != 96 || logo.X < panel.X || logo.X+logo.W > panel.X+panel.W {
					t.Fatalf("About logo has invalid geometry: %+v", logo)
				}
				if application.settingsPage != -1 || !application.aboutOpen || !tester.HasText("zeroday0619 / Tomori Lab") || !tester.HasText("zero@tomori.dev") {
					t.Fatal("About did not open a modal with repository developer metadata")
				}
				if size[1]-24 < 560 {
					revealAboutControl(t, tester, "About information", "View MyGOFrame license")
				} else {
					requireInside(t, tester, "View MyGOFrame license", panel)
				}
				if err := tester.Click("View MyGOFrame license"); err != nil {
					t.Fatal(err)
				}
				licensePanel := requireInside(t, tester, "License dialog", window)
				requireInside(t, tester, "Close license", licensePanel)
				reader := requireInside(t, tester, "License text", licensePanel)
				if reader.H < 80 || !strings.Contains(application.licenseBody, "Permission is hereby granted, free of charge") {
					t.Fatal("the full MIT text is not readable")
				}
				original := application.licenseBody
				if err := tester.Click("License text"); err != nil {
					t.Fatal(err)
				}
				tester.Type("This must not edit a notice.")
				if application.licenseBody != original {
					t.Fatal("read-only license text was modified")
				}
				if err := tester.Click("Copy license text"); err != nil {
					t.Fatal(err)
				}
				if tester.Clipboard() != original {
					t.Fatal("license copy omitted part of the text")
				}
				tester.Key(0, ui.KeyF11)
				if application.fullscreen {
					t.Fatal("a modal allowed the underlying fullscreen shortcut")
				}
				tester.Key(0, ui.KeyEscape)
				if application.licenseOpen || !application.aboutOpen || (compact && !application.settingsOpen) {
					t.Fatal("Escape did not close only the license modal")
				}
				if viewport, _ := tester.Find("About dialog"); viewport.H < 560 {
					revealAboutControl(t, tester, "About information", "Open source and runtime notices")
				}
				if err := tester.Click("Open source and runtime notices"); err != nil {
					t.Fatal(err)
				}
				requireInside(t, tester, "Open source and runtime notices", window)
				tester.Key(0, ui.KeyEscape)
				if application.noticesOpen || !application.aboutOpen {
					t.Fatal("notice Escape dismissed its parent")
				}
				tester.Key(0, ui.KeyEscape)
				if application.aboutOpen || (compact && !application.settingsOpen) || !application.running {
					t.Fatal("closing About changed its parent or capture state")
				}
				if compact {
					tester.Key(0, ui.KeyEscape)
					if application.settingsOpen {
						t.Fatal("Settings did not close after both child modals")
					}
				}
			})
		}
	}
}

func TestAboutLinksHaveReadableContrast(t *testing.T) {
	for _, dark := range []bool{false, true} {
		palette := fluentAppearance(dark)
		for _, background := range []ui.Color{palette.card, palette.theme.SurfaceHover} {
			first, second := colorLuminance(aboutLinkColor(dark)), colorLuminance(background)
			contrast := (math.Max(first, second) + .05) / (math.Min(first, second) + .05)
			if contrast < 4.5 {
				t.Fatalf("dark=%t link contrast %.2f is below 4.5:1", dark, contrast)
			}
		}
	}
}

func TestThirdPartySearchAndLongLicenseReader(t *testing.T) {
	application := newTestApplication()
	application.openAbout()
	application.licenseSearch = "MyGo"
	tester := ui.NewTester(application.view, 1280, 800)
	if viewport, _ := tester.Find("About dialog"); viewport.H < 560 {
		revealAboutControl(t, tester, "About information", "Open source and runtime notices")
	}
	if err := tester.Click("Open source and runtime notices"); err != nil {
		t.Fatal(err)
	}
	revealAboutControl(t, tester, "Search licenses", "License for MyGo")
	if err := tester.Click("License for MyGo"); err != nil {
		t.Fatal(err)
	}
	if !application.licenseOpen || !strings.Contains(application.licenseBody, "Permission is hereby granted") {
		t.Fatal("third-party license did not open its full notice")
	}
	if err := tester.Click("Close license"); err != nil {
		t.Fatal(err)
	}
	application.showComponentLicense(application.aboutCatalog.Components[0])
	tester.Frame()
	tester.ClickAt(2, 2)
	if application.licenseOpen || !application.aboutOpen {
		t.Fatal("a license backdrop click dismissed its parent")
	}
	application.licenseSearch = "a component that does not exist"
	tester.Frame()
	if !tester.HasText("No matching components.") {
		t.Fatal("empty search results are not explained")
	}
	for _, component := range application.aboutCatalog.Components {
		if component.ID != "runtime:rust" {
			continue
		}
		application.showComponentLicense(component)
		tester.Frame()
		if len(application.licenseBody) < 10000 {
			t.Fatal("Rust dependency notices were truncated")
		}
		for _, size := range [][2]int{{320, 240}, {640, 480}, {1280, 800}} {
			tester.SetSize(size[0], size[1])
			panel := requireInside(t, tester, "License dialog", ui.Rect{W: float32(size[0]), H: float32(size[1])})
			requireInside(t, tester, "License text", panel)
			requireInside(t, tester, "Copy license text", panel)
		}
		tester.Key(0, ui.KeyEscape)
		tester.ClickAt(2, 2)
		if application.noticesOpen || !application.aboutOpen {
			t.Fatal("notice backdrop dismissed its parent")
		}
		tester.ClickAt(2, 2)
		if application.aboutOpen {
			t.Fatal("About backdrop did not dismiss the dialog")
		}
		return
	}
	t.Fatal("Rust runtime notice is missing")
}
