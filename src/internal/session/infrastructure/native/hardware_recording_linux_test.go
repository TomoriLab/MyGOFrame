//go:build linux && cgo

package native

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHardwareRecordingThroughSessionPorts(t *testing.T) {
	output := os.Getenv("MYGOFRAME_RECORDING_TEST_OUTPUT")
	if output == "" {
		t.Skip("Set MYGOFRAME_RECORDING_TEST_OUTPUT to an exclusive directory for a capture-device test.")
	}
	if !filepath.IsAbs(output) {
		t.Fatal("the recording test output directory must be absolute")
	}
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	device := os.Getenv("MYGOFRAME_RECORDING_TEST_DEVICE")
	if device == "" {
		device = "/dev/video0"
	}
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	fourcc, _ := FourCC("NV12")
	if err := engine.StartVideo(VideoConfig{Path: device, Backend: "dma", Mode: VideoMode{
		FourCC: fourcc, Format: "NV12", Width: 3840, Height: 2160, IntervalNumerator: 1, IntervalDenominator: 60,
	}}); err != nil {
		t.Fatal(err)
	}
	for _, codec := range []string{"h264", "hevc", "av1"} {
		t.Run(codec, func(t *testing.T) {
			config := DefaultRecordingConfiguration()
			config.Codec, config.RecordAudio = codec, false
			config.Path = filepath.Join(output, "domain-"+codec+".mkv")
			if err := engine.StartRecording(config); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = engine.StopRecording() })
			var sequence uint64
			deadline := time.Now().Add(8 * time.Second)
			var snapshot SessionSnapshot
			for time.Now().Before(deadline) {
				frame, err := engine.AcquireFrame(sequence)
				if err != nil {
					t.Fatal(err)
				}
				if frame != nil {
					sequence = frame.SequenceNumber()
					frame.Release()
				}
				snapshot, err = engine.Snapshot()
				if err != nil || snapshot.Recording.Error != "" {
					t.Fatalf("recording failed: %+v, %v", snapshot.Recording, err)
				}
				if snapshot.Recording.Frames >= 30 {
					break
				}
				time.Sleep(4 * time.Millisecond)
			}
			if err := engine.StopRecording(); err != nil {
				t.Fatal(err)
			}
			deadline = time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				snapshot, err = engine.Snapshot()
				if err != nil || !snapshot.Recording.Active() {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil || snapshot.Recording.Phase != "completed" || snapshot.Recording.Frames < 30 || snapshot.Recording.Bytes == 0 {
				t.Fatalf("recording did not finalize: %+v, %v", snapshot.Recording, err)
			}
			data, err := json.MarshalIndent(snapshot, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config.Path+".json", data, 0o600); err != nil {
				t.Fatal(err)
			}
		})
		if t.Failed() {
			return
		}
	}
}
