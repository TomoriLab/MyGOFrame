package presentation

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/TomoriLab/MyGOFrame/src/internal/desktop/presentation/localization"
	"github.com/TomoriLab/MyGOFrame/src/internal/diagnostics/infrastructure/memory"
	"github.com/egoist/mygo/ui"
)

func TestNativeDebugSwitchCopyClearAndCaptureIsolation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, locale := range []string{"en", "ko", "ja"} {
		for _, size := range [][2]int{{320, 240}, {640, 480}, {1280, 900}} {
			t.Run(fmt.Sprintf("%s/%dx%d", locale, size[0], size[1]), func(t *testing.T) {
				application := newTestApplication()
				recorder := memory.New(nil)
				application.services.Diagnostics = recorder
				application.running = true
				tester := ui.NewTester(application.view, size[0], size[1])
				tester.SetLocale(locale)
				translate := func(value string) string { return localization.Text(locale, value) }
				if size[0] < 800 {
					if err := tester.Click(translate("Toggle sidebar")); err != nil {
						t.Fatal(err)
					}
				}
				revealAboutControl(t, tester, translate("Settings overview"), translate("Open app settings"))
				if err := tester.Click(translate("Open app settings")); err != nil {
					t.Fatal(err)
				}
				revealAboutControl(t, tester, translate("Settings detail"), translate("Debug mode switch"))
				if err := tester.Click(translate("Debug mode switch")); err != nil {
					t.Fatal(err)
				}
				if !recorder.Enabled() || !application.debugEnabled || !application.running || application.measureLatency {
					t.Fatal("debug switch changed capture or profiling state")
				}
				application.debugEvent("test.command", "complete")
				revealAboutControl(t, tester, translate("Settings detail"), translate("Copy diagnostic report"))
				if err := tester.Click(translate("Copy diagnostic report")); err != nil {
					t.Fatal(err)
				}
				var report diagnosticReport
				if err := json.Unmarshal([]byte(tester.Clipboard()), &report); err != nil {
					t.Fatal(err)
				}
				if !report.Diagnostics.State.Enabled || len(report.Diagnostics.Events) == 0 {
					t.Fatal("debug report is empty")
				}
				revealAboutControl(t, tester, translate("Settings detail"), translate("Clear diagnostic history"))
				if err := tester.Click(translate("Clear diagnostic history")); err != nil {
					t.Fatal(err)
				}
				if recorder.State().EventCount != 0 {
					t.Fatal("clear did not remove diagnostic history")
				}
				viewport, _ := tester.Find(translate("Settings detail"))
				tester.Scroll(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 0, -10000)
				tester.Advance(time.Second)
				revealAboutControl(t, tester, translate("Settings detail"), translate("Debug mode switch"))
				if err := tester.Click(translate("Debug mode switch")); err != nil {
					t.Fatal(err)
				}
				if recorder.Enabled() || application.debugEnabled || !application.running {
					t.Fatal("disable did not preserve capture")
				}
				application.close()
			})
		}
	}
}
