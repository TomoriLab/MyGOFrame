//go:build linux && cgo

package presentation

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/application"
	graphicsruntime "github.com/TomoriLab/MyGOFrame/src/internal/platform/infrastructure/graphics"
	nativeruntime "github.com/TomoriLab/MyGOFrame/src/internal/platform/infrastructure/native"
	recordingdomain "github.com/TomoriLab/MyGOFrame/src/internal/recording/domain"
	sessionapp "github.com/TomoriLab/MyGOFrame/src/internal/session/application"
	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"
)

type recordingSmokeGPU struct {
	capture.GPUImage
	samples      []capture.GPURenderMetrics
	mutex        sync.Mutex
	sequence     uint64
	lastSequence map[uint64]uint64
	frames       map[uint64]uint64
}

// Independent counters preserve recreation coverage after the bounded diagnostic samples fill.
func (image *recordingSmokeGPU) SetFrame(frame capture.Frame) {
	image.mutex.Lock()
	defer image.mutex.Unlock()
	image.sequence = 0
	if frame != nil {
		image.sequence = frame.SequenceNumber()
	}
	image.GPUImage.SetFrame(frame)
}

func (image *recordingSmokeGPU) Texture(context uint64) uint32 {
	image.mutex.Lock()
	defer image.mutex.Unlock()
	texture := image.GPUImage.Texture(context)
	if texture != 0 && image.sequence != 0 && image.lastSequence[context] != image.sequence {
		image.lastSequence[context] = image.sequence
		image.frames[context]++
	}
	return texture
}

func (image *recordingSmokeGPU) Close() {
	image.samples = image.GPUImage.Metrics()
	image.GPUImage.Close()
}

func checkRecordingPreviewRates(samples []capture.GPURenderMetrics, minimum float64) error {
	contexts := make(map[uint64][]capture.GPURenderMetrics)
	for _, sample := range samples {
		contexts[sample.ContextID] = append(contexts[sample.ContextID], sample)
	}
	if len(contexts) != 5 {
		return fmt.Errorf("expected main and four capture contexts, got %d", len(contexts))
	}
	for context, rows := range contexts {
		if len(rows) < 120 {
			return fmt.Errorf("context %d rendered only %d distinct frames", context, len(rows))
		}
		// Initial imports are excluded; subsequent duplicate frames cannot inflate this rate.
		rows = rows[30:]
		duration := rows[len(rows)-1].SubmitTimestampNS - rows[0].SubmitTimestampNS
		if duration == 0 {
			return fmt.Errorf("context %d has no measurable duration", context)
		}
		rate := float64(len(rows)-1) * 1e9 / float64(duration)
		if rate < minimum {
			return fmt.Errorf("context %d rendered %.2f fps, below %.2f", context, rate, minimum)
		}
	}
	return nil
}

type recordingSmokeEngine struct {
	sessionapp.Engine
	path      string
	final     session.SessionSnapshot
	failure   error
	closeOnce sync.Once
	startOnce sync.Once
}

func (engine *recordingSmokeEngine) StartVideo(config session.VideoConfig) error {
	if err := engine.Engine.StartVideo(config); err != nil {
		return err
	}
	if os.Getenv("MYGOFRAME_RECORDING_GUI_SIMULTANEOUS") == "1" {
		recording := recordingdomain.DefaultRecordingConfiguration()
		recording.Codec, recording.RecordAudio, recording.Path = "hevc", false, engine.path
		var err error
		engine.startOnce.Do(func() { err = engine.Engine.StartRecording(recording) })
		return err
	}
	return nil
}

func (engine *recordingSmokeEngine) Snapshot() (session.SessionSnapshot, error) {
	snapshot, err := engine.Engine.Snapshot()
	if err != nil || !snapshot.VideoRunning || snapshot.Frames < 120 {
		return snapshot, err
	}
	var startError error
	engine.startOnce.Do(func() {
		recording := recordingdomain.DefaultRecordingConfiguration()
		recording.Codec, recording.RecordAudio, recording.Path = "hevc", false, engine.path
		startError = engine.Engine.StartRecording(recording)
	})
	if startError != nil {
		return snapshot, startError
	}
	return engine.Engine.Snapshot()
}
func (engine *recordingSmokeEngine) Close() {
	engine.closeOnce.Do(func() {
		engine.failure = engine.Engine.StopRecording()
		deadline := time.Now().Add(10 * time.Second)
		for {
			snapshot, err := engine.Engine.Snapshot()
			engine.final = snapshot
			if err != nil {
				engine.failure = err
				break
			}
			if !snapshot.Recording.Active() || time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		engine.Engine.Close()
	})
}

var recordingGUIResult error

// MyGo requires its event loop on the process main goroutine, outside testing.T workers.
func TestMain(tests *testing.M) {
	if path := os.Getenv("MYGOFRAME_RECORDING_GUI_TEST_OUTPUT"); path != "" {
		recordingGUIResult = runRecordingGUI(path)
	}
	os.Exit(tests.Run())
}

// Run this test alone because the native application lifecycle is process-global.
func TestExperimentalRecordingTwoWindows(t *testing.T) {
	if os.Getenv("MYGOFRAME_RECORDING_GUI_TEST_OUTPUT") == "" {
		t.Skip("Set MYGOFRAME_RECORDING_GUI_TEST_OUTPUT to an unused absolute MKV path.")
	}
	if recordingGUIResult != nil {
		t.Fatal(recordingGUIResult)
	}
}

func runRecordingGUI(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("output path must be absolute")
	}
	if err := nativeruntime.Prepare(); err != nil {
		return err
	}
	if _, err := graphicsruntime.Prepare(); err != nil {
		return err
	}
	if os.Getenv("MYGO_GL_API") == "" {
		if err := os.Setenv("MYGO_GL_API", "gles"); err != nil {
			return err
		}
	}
	services := testServices()
	createGPU := services.NewGPUImage
	var preview *recordingSmokeGPU
	services.NewGPUImage = func() capture.GPUImage {
		preview = &recordingSmokeGPU{GPUImage: createGPU(), lastSequence: make(map[uint64]uint64), frames: make(map[uint64]uint64)}
		return preview
	}
	create := services.NewEngine
	var engine *recordingSmokeEngine
	services.NewEngine = func() (sessionapp.Engine, error) {
		inner, err := create()
		if err != nil {
			return nil, err
		}
		engine = &recordingSmokeEngine{Engine: inner, path: path}
		return engine, nil
	}
	config := session.VideoConfig{Path: "/dev/video0", Backend: "dma", Mode: session.VideoMode{
		FourCC: uint32('N') | uint32('V')<<8 | uint32('1')<<16 | uint32('2')<<24,
		Format: "NV12", Width: 3840, Height: 2160, IntervalNumerator: 1, IntervalDenominator: 60,
	}}
	duration := 24*time.Second + 24*time.Millisecond
	if configured := os.Getenv("MYGOFRAME_RECORDING_GUI_DURATION"); configured != "" {
		parsed, err := time.ParseDuration(configured)
		if err != nil || parsed < duration {
			return fmt.Errorf("invalid GUI recording duration: %q", configured)
		}
		duration = parsed
	}
	if err := runWindow(services, duration, &config, windowConfiguration{
		width: 1280, height: 800, captureWindow: true, captureWindowCycles: 3, videoBackend: "dma",
	}); err != nil {
		return err
	}
	if engine == nil {
		return fmt.Errorf("recording engine was not created")
	}
	if engine.failure != nil {
		return engine.failure
	}
	if engine.final.VideoBackend != "dmabuf" || engine.final.FallbackReason != "" {
		return fmt.Errorf("DMA preview was not retained: %+v", engine.final)
	}
	state := engine.final.Recording
	if state.Phase != "completed" || state.Frames < 120 || state.Error != "" || state.DroppedFrames != 0 {
		return fmt.Errorf("recording did not complete: %+v", state)
	}
	if os.Getenv("MYGOFRAME_EXPERIMENTAL_RECORDING_TRANSFER") == "vulkan" && !strings.HasPrefix(state.Transfer, "vulkan-") {
		return fmt.Errorf("Vulkan recording silently fell back: %s", state.Transfer)
	}
	minimum := 58.0
	if configured := os.Getenv("MYGOFRAME_RECORDING_GUI_MIN_FPS"); configured != "" {
		parsed, err := strconv.ParseFloat(configured, 64)
		if err != nil || math.IsNaN(parsed) || parsed < 0 || parsed > 60 {
			return fmt.Errorf("invalid GUI minimum frame rate: %q", configured)
		}
		minimum = parsed
	}
	if preview == nil {
		return fmt.Errorf("GPU preview was not created")
	}
	if len(preview.frames) != 5 {
		return fmt.Errorf("expected five rendered contexts, got %d", len(preview.frames))
	}
	for context, frames := range preview.frames {
		if frames < 120 {
			return fmt.Errorf("context %d produced only %d distinct textures", context, frames)
		}
	}
	if minimum > 0 {
		if err := checkRecordingPreviewRates(preview.samples, minimum); err != nil {
			return err
		}
	}
	if err := checkRecordingFile(path, state.Frames, minimum > 0); err != nil {
		return err
	}
	fmt.Printf("Distinct textures by context: %v\n", preview.frames)
	fmt.Printf("Recording result: %+v\n", state)
	return nil
}

// File timestamps expose encoder-side losses that cannot appear in queue overflow counters.
func checkRecordingFile(path string, frames uint64, checkCadence bool) error {
	commandContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(commandContext, "ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height,codec_name,avg_frame_rate:packet=pts_time", "-of", "json", path).Output()
	if err != nil {
		return fmt.Errorf("probe completed recording: %w", err)
	}
	var report struct {
		Packets []struct {
			PresentationTime string `json:"pts_time"`
		} `json:"packets"`
		Streams []struct {
			Width  int    `json:"width"`
			Height int    `json:"height"`
			Codec  string `json:"codec_name"`
			Rate   string `json:"avg_frame_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		return err
	}
	if len(report.Streams) != 1 || report.Streams[0].Width != 3840 || report.Streams[0].Height != 2160 || report.Streams[0].Codec != "hevc" || report.Streams[0].Rate != "60/1" {
		return fmt.Errorf("unexpected recording format: %+v", report.Streams)
	}
	if uint64(len(report.Packets)) != frames || frames < 120 {
		return fmt.Errorf("recording packets %d disagree with encoded frames %d", len(report.Packets), frames)
	}
	previous, maximum := -1.0, 0.0
	for index, packet := range report.Packets {
		timestamp, err := strconv.ParseFloat(packet.PresentationTime, 64)
		if err != nil || math.IsNaN(timestamp) || math.IsInf(timestamp, 0) || timestamp < 0 {
			return fmt.Errorf("invalid recording timestamp %q", packet.PresentationTime)
		}
		if index > 0 {
			gap := timestamp - previous
			if gap <= 0 {
				return fmt.Errorf("non-increasing recording timestamp at packet %d", index)
			}
			maximum = math.Max(maximum, gap)
		}
		previous = timestamp
	}
	if checkCadence && maximum > 0.020 {
		return fmt.Errorf("4K60 recording packet gap %.3f ms exceeds 20 ms", maximum*1000)
	}
	fmt.Printf("Recording packets: %d; maximum PTS gap: %.3f ms\n", len(report.Packets), maximum*1000)
	return nil
}
