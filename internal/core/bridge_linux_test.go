//go:build linux && cgo

package core

import (
	"math"
	"sync"
	"testing"
)

func TestBridgeFailureRecoveryAndShutdown(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	fourcc, err := FourCC("NV12")
	if err != nil {
		t.Fatal(err)
	}
	config := VideoConfig{
		Path: "/dev/mygoframe-nonexistent-test-device",
		Mode: VideoMode{FourCC: fourcc, Width: 640, Height: 480, IntervalNumerator: 1, IntervalDenominator: 60},
	}
	for range 8 {
		if err := engine.StartVideo(config); err == nil {
			t.Fatal("a missing device unexpectedly started")
		}
		status, err := engine.Status()
		if err != nil {
			t.Fatal(err)
		}
		if status["videoRunning"] != false || status["error"] == "" {
			t.Fatalf("failed startup did not publish its stopped state: %v", status)
		}
		engine.StopVideo()
	}
	if err := engine.StartAudio("", ""); err != nil {
		t.Fatalf("stopping audio without a server should succeed: %v", err)
	}
	engine.Close()
	engine.Close()
	if _, err := engine.Status(); err == nil {
		t.Fatal("status accepted a destroyed engine")
	}
	if err := engine.StartVideo(config); err == nil {
		t.Fatal("capture accepted a destroyed engine")
	}
	if frame, err := engine.AcquireFrame(0); frame != nil || err != nil {
		t.Fatalf("closed engine returned a frame: %v, %v", frame, err)
	}
}

func TestBridgeVolumeRoundTrip(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for _, test := range []struct{ input, expected float64 }{
		{0.25, 0.25}, {2, 1}, {-1, 0}, {math.NaN(), 0}, {math.Inf(1), 0},
	} {
		engine.SetVolume(test.input)
		status, err := engine.Status()
		if err != nil {
			t.Fatal(err)
		}
		if status["volume"] != test.expected {
			t.Fatalf("volume %v: got %v, want %v", test.input, status["volume"], test.expected)
		}
	}
}

func TestEngineValueCopiesShareShutdown(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	copyOfEngine := *engine
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 25 {
				if _, err := copyOfEngine.Status(); err != nil && err.Error() != "capture engine is closed" {
					t.Errorf("unexpected copied-engine status error: %v", err)
				}
			}
		})
	}
	workers.Go(engine.Close)
	workers.Wait()
	copyOfEngine.Close()
	if _, err := engine.Status(); err == nil {
		t.Fatal("original engine remained open after copied engine shutdown")
	}
	if _, err := copyOfEngine.Status(); err == nil {
		t.Fatal("copied engine remained open after shared shutdown")
	}
}

func TestZeroValueEngineIsClosed(t *testing.T) {
	var engine Engine
	engine.Close()
	if _, err := engine.Status(); err == nil {
		t.Fatal("zero-value engine reported an open native handle")
	}
	if err := engine.StartVideo(VideoConfig{}); err == nil {
		t.Fatal("zero-value engine accepted capture startup")
	}
	var nilEngine *Engine
	nilEngine.Close()
	if _, err := nilEngine.Status(); err == nil {
		t.Fatal("nil engine reported an open native handle")
	}
}
