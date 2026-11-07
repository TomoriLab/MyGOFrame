package main

import (
	"image"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/TomoriLab/MyGOFrame/internal/core"
	"github.com/egoist/mygo/ui"
)

func fixtureModes() []core.VideoMode {
	return []core.VideoMode{
		{FourCC: 1, Format: "NV12", Width: 1920, Height: 1080, IntervalNumerator: 1, IntervalDenominator: 60},
		{FourCC: 1, Format: "NV12", Width: 1920, Height: 1080, IntervalNumerator: 1001, IntervalDenominator: 60000},
		{FourCC: 1, Format: "NV12", Width: 1280, Height: 720, IntervalNumerator: 1, IntervalDenominator: 120},
		{FourCC: 2, Format: "YUYV", Width: 640, Height: 480, IntervalNumerator: 1, IntervalDenominator: 30},
	}
}

func TestVideoSelectionCascade(t *testing.T) {
	var selection modeSelection
	selection.setModes(fixtureModes())
	if len(selection.formats()) != 2 || len(selection.resolutions()) != 2 || len(selection.frameRates()) != 2 {
		t.Fatalf("incorrect mode grouping: %#v", selection)
	}
	selection.resolution = "1280 × 720"
	selection.reconcile()
	mode, valid := selection.selected()
	if !valid || mode.IntervalDenominator != 120 {
		t.Fatalf("resolution change retained invalid frame rate: %#v", selection)
	}
	selection.format = "YUYV"
	selection.reconcile()
	mode, valid = selection.selected()
	if !valid || mode.Width != 640 || mode.IntervalDenominator != 30 {
		t.Fatalf("format change retained invalid mode: %#v", selection)
	}
	selection.setModes(nil)
	if _, valid := selection.selected(); valid || selection.format != "" || selection.resolution != "" || selection.frameRate != "" {
		t.Fatalf("device removal retained mode: %#v", selection)
	}
}

func TestRationalFrameRate(t *testing.T) {
	for _, test := range []struct {
		value                  string
		numerator, denominator uint32
	}{{"60", 1, 60}, {"59.94", 50, 2997}, {"60000/1001", 1001, 60000}} {
		numerator, denominator, err := parseFrameRate(test.value)
		if err != nil || numerator != test.numerator || denominator != test.denominator {
			t.Fatalf("%s: %d/%d, %v", test.value, numerator, denominator, err)
		}
	}
	for _, value := range []string{"0", "-1", "NaN", "1/0", "4294967296"} {
		if _, _, err := parseFrameRate(value); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}

type testLease struct{ releases atomic.Int32 }

func (lease *testLease) Release() { lease.releases.Add(1) }

func TestLatestFrameSlotReleasesSupersededFrames(t *testing.T) {
	var slot latestSlot[*testLease]
	first, second := &testLease{}, &testLease{}
	slot.replace(first)
	slot.replace(second)
	if first.releases.Load() != 1 || second.releases.Load() != 0 {
		t.Fatal("replaced frame ownership was not released correctly")
	}
	frame, present := slot.take()
	if !present || frame != second {
		t.Fatal("latest frame was not retained")
	}
	slot.clear()
	if second.releases.Load() != 0 {
		t.Fatal("slot released a frame transferred to the consumer")
	}
	frame.Release()
	if second.releases.Load() != 1 {
		t.Fatal("consumer failed to release the frame")
	}
}

func TestLatestFrameSlotConcurrentReplaceAndTake(t *testing.T) {
	var slot latestSlot[*testLease]
	frames := make([]*testLease, 1000)
	for index := range frames {
		frames[index] = &testLease{}
	}
	var workers sync.WaitGroup
	workers.Go(func() {
		for _, frame := range frames {
			slot.replace(frame)
		}
	})
	workers.Go(func() {
		for range 1000 {
			if frame, present := slot.take(); present {
				frame.Release()
			}
		}
	})
	workers.Wait()
	slot.clear()
	for index, frame := range frames {
		if frame.releases.Load() != 1 {
			t.Fatalf("frame %d released %d times", index, frame.releases.Load())
		}
	}
}

func TestViewStartsWithAudioOffAndHandlesFullscreen(t *testing.T) {
	application := newApplication()
	application.devices = []core.VideoDevice{{Path: "/dev/video0", Name: "Capture"}}
	application.device = videoLabel(application.devices[0])
	application.selection.setModes(fixtureModes())
	application.audioDevices.Sources = []core.AudioDevice{{ID: "capture.input", Name: "Capture audio"}}
	tester := ui.NewTester(application.view, 1280, 800)
	if application.audioSource != audioOff || !tester.HasText("Video") || !tester.HasText("Audio") {
		t.Fatalf("incorrect initial controls: %v", tester.Texts())
	}
	if err := tester.Click("Fullscreen · F11"); err != nil {
		t.Fatal(err)
	}
	if !application.fullscreen || tester.HasText("Audio") {
		t.Fatal("fullscreen did not hide controls")
	}
	tester.Key(0, ui.KeyEscape)
	if application.fullscreen || !tester.HasText("Audio") {
		t.Fatal("Escape did not restore controls")
	}
}

func TestViewFormatSelectionUpdatesResolutionAndFrameRate(t *testing.T) {
	application := newApplication()
	application.selection.setModes(fixtureModes())
	tester := ui.NewTester(application.view, 1280, 800)
	if err := tester.Click("NV12"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("YUYV"); err != nil {
		t.Fatal(err)
	}
	mode, valid := application.selection.selected()
	if !valid || mode.Format != "YUYV" || mode.Width != 640 || mode.IntervalDenominator != 30 {
		t.Fatalf("UI format selection did not reset its dependent controls: %#v", application.selection)
	}
}

func TestViewReservesSettingsWidthAndVisibleStatus(t *testing.T) {
	for _, dimensions := range [][2]int{{1280, 758}, {800, 600}} {
		application := newApplication()
		tester := ui.NewTester(application.view, dimensions[0], dimensions[1])
		settings, found := tester.Find("Capture settings")
		if !found || settings.W != 330 {
			t.Fatalf("settings width %v at %v", settings, dimensions)
		}
		status, found := tester.Find("Ready")
		if !found || status.Y < 0 || status.Y+status.H > float32(dimensions[1]) {
			t.Fatalf("status outside viewport: %v at %v", status, dimensions)
		}
	}
}

func TestSmokeRejectsStoppedOrSilentStreams(t *testing.T) {
	for _, status := range []map[string]any{
		{"videoRunning": false},
		{"videoRunning": true, "error": "Device disconnected"},
	} {
		if err := validateSmokeStatus(status, 30, true); err == nil {
			t.Fatalf("accepted failed video: %v", status)
		}
	}
	if err := validateSmokeStatus(map[string]any{"videoRunning": true}, 30, true); err != nil {
		t.Fatal(err)
	}
	for _, audio := range []map[string]any{
		{"running": false, "capturedFrames": float64(64), "playedFrames": float64(64)},
		{"running": true, "capturedFrames": float64(0), "playedFrames": float64(64)},
		{"running": true, "capturedFrames": float64(64), "playedFrames": float64(0)},
		{"running": true, "capturedFrames": float64(64), "playedFrames": float64(64), "error": "Stream disconnected"},
	} {
		if err := validateSmokeStatus(map[string]any{"audio": audio}, 0, false); err == nil {
			t.Fatalf("accepted failed audio: %v", audio)
		}
	}
	if err := validateSmokeStatus(map[string]any{"audio": map[string]any{"running": true, "capturedFrames": float64(64), "playedFrames": float64(64)}}, 0, false); err != nil {
		t.Fatal(err)
	}
}

func TestVideoConfigValidation(t *testing.T) {
	config, err := parseVideoConfig("/dev/video9", "NV12", 1920, 1080, "60000/1001")
	if err != nil || config.Path != "/dev/video9" || config.Mode.Width != 1920 || config.Mode.Height != 1080 || config.Mode.IntervalNumerator != 1001 || config.Mode.IntervalDenominator != 60000 {
		t.Fatalf("unexpected configuration: %+v, %v", config, err)
	}
	for _, dimensions := range [][2]uint{{0, 1080}, {1920, 0}, {16385, 1080}, {1920, 16385}} {
		if _, err := parseVideoConfig("/dev/video9", "NV12", dimensions[0], dimensions[1], "60"); err == nil {
			t.Fatalf("accepted dimensions %v", dimensions)
		}
	}
	if _, err := parseVideoConfig("/dev/video9", "RGB", 1920, 1080, "60"); err == nil {
		t.Fatal("accepted invalid format")
	}
	if _, err := parseVideoConfig("/dev/video9", "NV12", 1920, 1080, "0"); err == nil {
		t.Fatal("accepted invalid frame rate")
	}
}

func TestPreviewSmokeRequiresDisplayedLiveFrame(t *testing.T) {
	application := newApplication()
	application.running = true
	application.status = map[string]any{"videoRunning": true}
	if err := validatePreview(application); err == nil {
		t.Fatal("accepted capture without a displayed frame")
	}
	frame := core.NewImageFrame(image.NewRGBA(image.Rect(0, 0, 2, 2)), 1)
	application.displayedFrame = frame
	application.bitmap = ui.NewBitmap(frame.UnsafeBorrowedImage())
	if err := validatePreview(application); err != nil {
		t.Fatal(err)
	}
	application.running = false
	if err := validatePreview(application); err == nil {
		t.Fatal("accepted a stopped preview")
	}
	application.running = true
	application.errorMessage = "Capture device disconnected"
	if err := validatePreview(application); err == nil {
		t.Fatal("accepted a preview with a runtime error")
	}
	application.errorMessage = ""
	application.status["error"] = "Capture failed"
	if err := validatePreview(application); err == nil {
		t.Fatal("accepted failed native capture")
	}
}
