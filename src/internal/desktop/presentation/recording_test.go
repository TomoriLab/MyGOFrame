package presentation

import (
	"testing"
	"time"

	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"

	"github.com/egoist/mygo/ui"
)

func TestSettingsOverviewAndDetailStayInsidePanel(t *testing.T) {
	for _, size := range [][2]int{{320, 240}, {640, 480}, {900, 600}, {1280, 800}, {1920, 1080}} {
		application := newTestApplication()
		tester := ui.NewTester(application.view, size[0], size[1])
		if tester.HasText("Settings") {
			if err := tester.Click("Settings"); err != nil {
				t.Fatal(err)
			}
		}
		if application.settingsPage != -1 {
			t.Fatal("settings did not start at the overview")
		}
		outer, found := tester.Find("Capture settings")
		if !found {
			outer, found = tester.Find("Capture settings dialog")
		}
		overview, exists := tester.Find("Settings overview")
		if !found || !exists || overview.X < outer.X || overview.X+overview.W > outer.X+outer.W+1 || overview.Y+overview.H > outer.Y+outer.H+1 {
			t.Fatalf("overview exceeds settings at %v: overview=%+v panel=%+v", size, overview, outer)
		}
		tester.Scroll(overview.X+overview.W/2, overview.Y+overview.H/2, 0, 168)
		tester.Advance(time.Second)
		card, found := tester.Find("Recording settings")
		if !found || card.X < outer.X || card.X+card.W > outer.X+outer.W+1 {
			t.Fatalf("recording card exceeds settings: %+v", card)
		}
		if err := tester.Click("Recording settings"); err != nil {
			t.Fatal(err)
		}
		if application.settingsPage != 2 || !tester.HasText("Capture recording") {
			t.Fatal("recording card did not open its detail")
		}
		if err := tester.Click("Back to settings"); err != nil {
			t.Fatal(err)
		}
		if application.settingsPage != -1 || !tester.HasText("Settings overview") {
			t.Fatal("back did not restore the overview")
		}
	}
}

func TestRecordingSummaryIncludesFailuresAndDrops(t *testing.T) {
	state := session.RecordingSnapshot{Phase: "failed", Error: "Disk full"}
	if recordingSummary(state) != "Recording failed: Disk full" {
		t.Fatal("recording error was hidden")
	}
	for _, phase := range []string{"starting", "recording", "stopping"} {
		state.Phase = phase
		if !state.Active() {
			t.Fatalf("%s did not lock recording configuration", phase)
		}
	}
	for _, phase := range []string{"idle", "completed", "failed", "cancelled"} {
		state.Phase = phase
		if state.Active() {
			t.Fatalf("%s retained the recording lock", phase)
		}
	}
}

func TestCloseWaitsForRecordingProbe(t *testing.T) {
	application := newTestApplication()
	application.recordingProbeDone = make(chan struct{})
	finished := make(chan struct{})
	go func() { application.close(); close(finished) }()
	select {
	case <-finished:
		t.Fatal("shutdown abandoned the hardware probe")
	case <-time.After(20 * time.Millisecond):
	}
	close(application.recordingProbeDone)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after the probe")
	}
}
