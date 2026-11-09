//go:build linux && cgo

package core

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func TestSessionSnapshotPreservesCountersAndRejectsUnknownSchema(t *testing.T) {
	var snapshot SessionSnapshot
	if err := json.Unmarshal([]byte(`{"schemaVersion":1,"generation":9007199254740993,"commandID":18446744073709551614}`), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Generation != 9007199254740993 || snapshot.CommandID != 18446744073709551614 || snapshot.validate() != nil {
		t.Fatal("snapshot counters lost integer precision")
	}
	snapshot.SchemaVersion = 2
	if snapshot.validate() == nil {
		t.Fatal("accepted an unsupported snapshot schema")
	}
}

func TestSwiftAudioPolicyAndStaleRendererFailure(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := engine.SetAudioControls(AudioControls{Volume: .37, Muted: true}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := engine.Snapshot()
	if err != nil || !snapshot.Muted || snapshot.EffectiveGain != 0 || snapshot.Volume < .369 || snapshot.Volume > .371 {
		t.Fatalf("incorrect muted state: %+v, %v", snapshot, err)
	}
	if err := engine.ReportGPUFailure(snapshot.Generation, "retired renderer"); err != nil {
		t.Fatal(err)
	}
	after, err := engine.Snapshot()
	if err != nil || after.CommandID != snapshot.CommandID {
		t.Fatalf("inactive renderer restarted capture: %+v, %v", after, err)
	}
	if err := engine.SetAudioControls(AudioControls{Volume: .37}); err != nil {
		t.Fatal(err)
	}
	after, err = engine.Snapshot()
	if err != nil || after.Muted || after.EffectiveGain < .369 || after.EffectiveGain > .371 {
		t.Fatalf("unmute lost requested volume: %+v, %v", after, err)
	}
}

func TestFrameNotificationDescriptorSurvivesEngineAndSupportsCancellation(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	file, err := engine.FrameEvents()
	if err != nil {
		engine.Close()
		t.Fatal(err)
	}
	defer file.Close()
	engine.Close()
	if err := file.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var value [8]byte
	if _, err := file.Read(value[:]); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("descriptor did not remain pollable: %v", err)
	}
	if err := file.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := file.Read(value[:]); finished <- err }()
	file.Close()
	select {
	case err := <-finished:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("unexpected cancellation result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("descriptor close did not cancel the notification reader")
	}
}

func TestSessionNotificationDescriptorsReturnToBaseline(t *testing.T) {
	cycle := func() {
		engine, failure := New()
		if failure != nil {
			t.Fatal(failure)
		}
		file, failure := engine.FrameEvents()
		engine.Close()
		if failure != nil {
			t.Fatal(failure)
		}
		if failure := file.Close(); failure != nil {
			t.Fatal(failure)
		}
	}
	cycle()
	before, failure := os.ReadDir("/proc/self/fd")
	if failure != nil {
		t.Fatal(failure)
	}
	for range 100 {
		cycle()
	}
	after, failure := os.ReadDir("/proc/self/fd")
	if failure != nil {
		t.Fatal(failure)
	}
	if len(after) != len(before) {
		t.Fatalf("notification descriptors grew from %d to %d", len(before), len(after))
	}
}
