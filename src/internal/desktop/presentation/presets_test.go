package presentation

import (
	"testing"
	"time"

	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"

	"github.com/egoist/mygo/ui"
)

func TestPresetNativeControlsAndCurrentSettings(t *testing.T) {
	application := newTestApplication()
	config, err := parseVideoConfig("/dev/video0", "NV12", 1920, 1080, "60000/1001")
	if err != nil {
		t.Fatal(err)
	}
	device := session.VideoDevice{Path: config.Path, Name: "Capture"}
	application.devices = []session.VideoDevice{device}
	application.device = videoLabel(device)
	application.selection.setModes([]session.VideoMode{config.Mode})
	application.audioDevices = session.AudioDevices{Sources: []session.AudioDevice{{ID: "input", Name: "Card"}}, Outputs: []session.AudioDevice{{ID: "output", Name: "Speaker"}}}
	application.audioSource, application.audioOutput = audioLabel(application.audioDevices.Sources[0]), audioLabel(application.audioDevices.Outputs[0])
	application.colorRange, application.videoBackend = "Full", "dma"
	application.volume, application.muted = 37, true
	preset, err := application.currentPreset("Console")
	if err != nil || preset.Mode.IntervalNumerator != 1001 || preset.Volume != .37 || !preset.Muted || preset.AudioSource != "input" || preset.AudioOutput != "output" {
		t.Fatalf("settings lost: %+v %v", preset, err)
	}
	tester := ui.NewTester(application.view, 1280, 1000)
	if err := tester.Click("Presets settings"); err != nil {
		t.Fatal(err)
	}
	if !tester.HasText("Settings presets") || !tester.HasText("No saved presets yet.") {
		t.Fatal("missing native preset controls")
	}
	if err := tester.Click("Preset name input"); err != nil {
		t.Fatal(err)
	}
	tester.Type("Console 한글")
	if application.presetName != "Console 한글" {
		t.Fatalf("native input failed: %q", application.presetName)
	}
	application.acceptPresets(session.PresetDocument{SchemaVersion: 1, Presets: []session.CapturePreset{preset}}, "Console")
	tester.Frame()
	if err := tester.Click("Delete preset"); err != nil {
		t.Fatal(err)
	}
	if !application.presetDeletePending || !tester.HasText("Confirm delete") {
		t.Fatal("deletion did not request confirmation")
	}
	if err := tester.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if application.presetDeletePending {
		t.Fatal("deletion was not cancelled")
	}
	application.volume, application.colorRange = 80, "Limited"
	application.applyPresetSelection(session.PresetResolution{Preset: preset, Devices: application.devices, AudioDevices: application.audioDevices, Modes: []session.VideoMode{config.Mode}, Mode: config.Mode})
	if application.volume != 37 || application.colorRange != "Full" || !application.muted || application.audioSource != audioLabel(application.audioDevices.Sources[0]) {
		t.Fatal("preset selection did not restore native controls")
	}
}

func TestCompactPresetTabRemainsReachable(t *testing.T) {
	application := newTestApplication()
	tester := ui.NewTester(application.view, 320, 240)
	if err := tester.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	revealAboutControl(t, tester, "Settings overview", "Presets settings")
	if err := tester.Click("Presets settings"); err != nil {
		t.Fatal(err)
	}
	if !tester.HasText("Settings presets") {
		t.Fatal("compact preset tab is unreachable")
	}
}

func TestCloseWaitsForPresetStorage(t *testing.T) {
	application := newTestApplication()
	application.presetDone = make(chan struct{})
	finished := make(chan struct{})
	go func() { application.close(); close(finished) }()
	select {
	case <-finished:
		t.Fatal("shutdown abandoned an unfinished preset operation")
	case <-time.After(20 * time.Millisecond):
	}
	close(application.presetDone)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after preset storage completed")
	}
}

func TestQuickPresetControlsStayInsideWindowAndLockDuringRecording(t *testing.T) {
	for _, size := range [][2]int{{320, 240}, {640, 480}, {800, 600}, {1000, 700}, {1120, 700}, {1280, 800}, {2060, 1000}} {
		application := newTestApplication()
		application.presets = []session.CapturePreset{{Name: "Console one"}, {Name: "Console two"}}
		application.selectedPreset = "Console one"
		tester := ui.NewTester(application.view, size[0], size[1])
		for _, label := range []string{"Quick capture controls", "Quick preset selection", "Start capture"} {
			bounds, found := tester.Find(label)
			if !found || bounds.X < 0 || bounds.X+bounds.W > float32(size[0])+1 || bounds.Y < 0 || bounds.Y+bounds.H > float32(size[1])+1 {
				t.Fatalf("%s exceeds quick controls at %v: %+v", label, size, bounds)
			}
		}
		selector, _ := tester.Find("Quick preset selection")
		start, _ := tester.Find("Preset capture action")
		recording, _ := tester.Find("Recording action")
		aligned := selector.Y == start.Y
		if size[0] < 560 {
			aligned = selector.Y+selector.H+8 == start.Y
		}
		if selector.W < 176 || selector.H != 32 || start.H != 32 || !aligned || recording.X+recording.W > start.X || recording.Y != start.Y {
			t.Fatalf("quick controls collapsed or lost alignment at %v: preset=%+v start=%+v", size, selector, start)
		}
		if size[0] >= 1120 {
			capture, _ := tester.Find("Open capture window action")
			if selector.Y != capture.Y || start.X+start.W > capture.X {
				t.Fatalf("preset group is not aligned with toolbar: preset=%+v start=%+v capture=%+v", selector, start, capture)
			}
		} else if size[0] >= 800 {
			settings, _ := tester.Find("Capture settings")
			if selector.X != settings.X || selector.W != settings.W || recording.X != settings.X+settings.W+16 {
				t.Fatalf("quick controls do not follow content columns: preset=%+v settings=%+v start=%+v", selector, settings, start)
			}
		}
		if err := tester.Click("Start capture"); err != nil {
			t.Fatal("saved preset did not enable quick start:", err)
		}
		application.status.Recording = session.RecordingSnapshot{Phase: "recording"}
		tester.Frame()
		_ = tester.Click("Quick preset selection")
		if tester.HasText("Console two") {
			t.Fatal("recording opened the preset selector")
		}

	}
}
