package presentation

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TomoriLab/MyGOFrame/src/internal/desktop/presentation/localization"
	"github.com/TomoriLab/MyGOFrame/src/internal/diagnostics/infrastructure/memory"
	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"
	"github.com/egoist/mygo/ui"
)

func settingsCardLabel(title string) string {
	if title == "App settings" {
		return "Open app settings"
	}
	return title + " settings"
}

func TestLocalizedSettingsAndCanonicalSelections(t *testing.T) {
	for _, locale := range []string{"en", "ko", "ja"} {
		t.Run(locale, func(t *testing.T) {
			application := newTestApplication()
			application.selection.setModes(fixtureModes())
			application.devices = []session.VideoDevice{{Name: "Test capture", Path: "/dev/video0"}}
			application.device = videoLabel(application.devices[0])
			application.colorRange = "Full"
			application.volume = 35
			application.presets = []session.CapturePreset{{Name: "Console 한글 日本語"}}
			application.selectedPreset = application.presets[0].Name
			tester := ui.NewTester(application.view, 1280, 900)
			tester.SetLocale(locale)
			translate := func(value string) string { return localization.Text(locale, value) }
			if !tester.HasText(translate("Settings")) || !tester.HasText("Console 한글 日本語") {
				t.Fatal("overview or preset name missing")
			}
			for _, category := range []struct{ title, detail string }{{"Video", "Color range"}, {"Audio", "Audio monitoring"}, {"Recording", "Video recording"}, {"App settings", "Language"}, {"Presets", "Settings presets"}} {
				if err := tester.Click(translate(settingsCardLabel(category.title))); err != nil {
					t.Fatal(err)
				}
				if !tester.HasText(translate(category.detail)) {
					t.Fatalf("untranslated detail %s", category.title)
				}
				if category.title == "Video" {
					if err := tester.Click(translate("Full")); err != nil {
						t.Fatal(err)
					}
					if err := tester.Click(translate("Limited")); err != nil {
						t.Fatal(err)
					}
					if application.colorRange != "Limited" {
						t.Fatalf("localized selection stored %q", application.colorRange)
					}
				}
				if err := tester.Click(translate("Back to settings")); err != nil {
					t.Fatal(err)
				}
			}
			if application.audioSource != audioOff || application.audioOutput != defaultOutput || application.recordingGPU != "Automatic" || application.selectedPreset != "Console 한글 日本語" {
				t.Fatal("translation changed canonical configuration")
			}
			tester.SetLocale("ja")
			if !tester.HasText("設定") || application.colorRange != "Limited" {
				t.Fatal("locale transition changed capture choices")
			}
		})
	}
}

func TestLocalizedToolbarAndDialogsFit(t *testing.T) {
	for _, locale := range []string{"en", "ko", "ja"} {
		for _, size := range [][2]int{{320, 240}, {640, 480}, {800, 600}, {1120, 700}, {1280, 800}, {1920, 1080}} {
			t.Run(fmt.Sprintf("%s/%dx%d", locale, size[0], size[1]), func(t *testing.T) {
				application := newTestApplication()
				application.presets = []session.CapturePreset{{Name: "Console"}}
				application.selectedPreset = "Console"
				tester := ui.NewTester(application.view, size[0], size[1])
				tester.SetLocale(locale)
				translate := func(value string) string { return localization.Text(locale, value) }
				for _, label := range []string{"Quick capture controls", "Quick preset selection", "Preset video action", "Recording action", "Fullscreen action"} {
					bounds, found := tester.Find(translate(label))
					if !found || bounds.X < 0 || bounds.Y < 0 || bounds.X+bounds.W > float32(size[0])+1 || bounds.Y+bounds.H > float32(size[1])+1 {
						t.Fatalf("%s exceeds %v: %+v", label, size, bounds)
					}
				}
				if size[0] < 800 {
					if err := tester.Click(translate("Settings")); err != nil {
						t.Fatal(err)
					}
				}
				if _, found := tester.Find(translate("App language")); found {
					t.Fatal("language selector leaked into the overview")
				}
				revealAboutControl(t, tester, translate("Settings overview"), translate("Open app settings"))
				if err := tester.Click(translate("Open app settings")); err != nil {
					t.Fatal(err)
				}
				if _, found := tester.Find(translate("App language")); !found {
					t.Fatal("language selector unavailable")
				}
				application.aboutOpen = true
				application.aboutCatalog, _ = application.services.Notices()
				tester.Frame()
				if !tester.HasText(translate("Developer")) {
					t.Fatal("about dialog untranslated")
				}
				revealAboutControl(t, tester, translate("About information"), translate("View MyGOFrame license"))
				if err := tester.Click(translate("View MyGOFrame license")); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(application.licenseBody, "MIT License") {
					t.Fatal("license body changed")
				}
				for _, label := range []string{"License dialog", "Close license", "License text"} {
					bounds, found := tester.Find(translate(label))
					if !found || bounds.X < 0 || bounds.Y < 0 || bounds.X+bounds.W > float32(size[0])+1 || bounds.Y+bounds.H > float32(size[1])+1 {
						t.Fatalf("%s exceeds modal: %+v", label, bounds)
					}
				}
			})
		}
	}
}

func TestLanguageSelectorEmitsTagsAndViewsFollowLocale(t *testing.T) {
	application := newTestApplication()
	application.colorRange = "Full"
	application.running = true
	selected := "en"
	currentLocale := "en"
	tester := ui.NewTester(application.view, 1280, 900)
	if err := tester.Click("Open app settings"); err != nil {
		t.Fatal(err)
	}
	capture := &presentationWindow{application: application}
	captureTester := ui.NewTester(capture.view, 960, 540)
	application.changeLocale = func(tag string) { selected = tag }
	for _, sample := range []struct{ tag, name string }{{"ko", "한국어"}, {"ja", "日本語"}, {"en", "English"}} {
		if err := tester.Click(localization.Text(currentLocale, "App language")); err != nil {
			t.Fatal(err)
		}
		if err := tester.Click(sample.name); err != nil {
			t.Fatal(err)
		}
		tester.SetLocale(selected)
		captureTester.SetLocale(selected)
		currentLocale = selected
		if selected != sample.tag || !tester.HasText(localization.Text(sample.tag, "Settings")) {
			t.Fatalf("language selection did not apply: %q", selected)
		}
		if _, found := captureTester.Find(localization.Text(sample.tag, "Capture window viewport")); !found {
			t.Fatal("capture view did not switch language")
		}
		if !application.running || application.colorRange != "Full" {
			t.Fatal("locale change restarted the capture")
		}
	}
}

func TestWriteLocalizationScreenshots(t *testing.T) {
	output := os.Getenv("MYGOFRAME_L10N_SCREENSHOTS")
	if output == "" {
		t.Skip("screenshot output was not requested")
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, locale := range []string{"en", "ko", "ja"} {
		application := newTestApplication()
		application.services.Diagnostics = memory.New(nil)
		application.presets = []session.CapturePreset{{Name: "Console"}}
		application.selectedPreset = "Console"
		tester := ui.NewTester(application.view, 1280, 800)
		tester.SetLocale(locale)
		tester.Advance(time.Second)
		file, err := os.Create(filepath.Join(output, locale+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(file, tester.Image()); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := tester.Click(localization.Text(locale, "Open app settings")); err != nil {
			t.Fatal(err)
		}
		settingsFile, err := os.Create(filepath.Join(output, locale+"-app-settings.png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(settingsFile, tester.Image()); err != nil {
			t.Fatal(err)
		}
		if err := settingsFile.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLocalizedNativeTextMenus(t *testing.T) {
	for _, locale := range []string{"en", "ko", "ja"} {
		t.Run(locale, func(t *testing.T) {
			value := "Capture preset"
			tester := ui.NewTester(func(context *ui.Context) { ui.TextInput(context, &value).Width(300).Label("Localized text input") }, 640, 480)
			tester.SetLocale(locale)
			if err := tester.RightClick("Localized text input"); err != nil {
				t.Fatal(err)
			}
			labels := strings.Join(tester.Menu(), "\n")
			for _, key := range []string{"mygo.copy", "mygo.paste", "mygo.selectAll"} {
				if !strings.Contains(labels, localization.Text(locale, key)) {
					t.Fatalf("%s is not localized: %q", key, labels)
				}
			}
		})
	}
}

func TestTranslatedSelectorsKeepCanonicalValues(t *testing.T) {
	for _, locale := range []string{"en", "ko", "ja"} {
		for _, options := range [][]string{{"Auto", "Limited", "Full"}, {audioOff, defaultOutput}, {"Automatic", "Unavailable saved GPU"}, {"vbr", "cbr"}} {
			selected := options[0]
			tester := ui.NewTester(func(context *ui.Context) {
				localizedSelect(context, &selected, options).Width(300).Label("Canonical selector")
			}, 640, 480)
			tester.SetLocale(locale)
			if err := tester.Click("Canonical selector"); err != nil {
				t.Fatal(err)
			}
			if err := tester.Click(localization.Text(locale, options[1])); err != nil {
				t.Fatal(err)
			}
			if selected != options[1] {
				t.Fatalf("%s stored translated value %q instead of %q", locale, selected, options[1])
			}
			tester.SetLocale("ja")
			if selected != options[1] || !tester.HasText(localization.Text("ja", options[1])) {
				t.Fatal("language change lost the canonical choice")
			}
		}
	}
}
