//go:build linux && cgo

package native

import "testing"

func TestRecordingPolicyRejectsInvalidConfigurationAndMissingCapture(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for _, mutate := range []func(*RecordingConfiguration){
		func(config *RecordingConfiguration) { config.Codec = "invalid" },
		func(config *RecordingConfiguration) { config.BitrateMbps = 0 },
		func(config *RecordingConfiguration) { config.BitrateMbps = 1001 },
		func(config *RecordingConfiguration) { config.RateControl = "invalid" },
		func(config *RecordingConfiguration) { config.Path = "relative.mkv" },
		func(config *RecordingConfiguration) { config.Path = "/absolute.webm" },
		func(config *RecordingConfiguration) {},
	} {
		config := DefaultRecordingConfiguration()
		mutate(&config)
		if err := engine.StartRecording(config); err == nil {
			t.Fatal("recording without valid configuration and capture succeeded")
		}
	}
	if err := engine.SetMonitoring(false); err != nil {
		t.Fatal(err)
	}
	state, err := engine.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.MonitorEnabled || state.Recording.Active() {
		t.Fatal("monitoring selection unexpectedly started recording")
	}
	if err := engine.StopRecording(); err != nil {
		t.Fatal(err)
	}
}
