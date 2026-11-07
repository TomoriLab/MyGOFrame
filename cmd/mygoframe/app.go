package main

import (
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/TomoriLab/MyGOFrame/internal/core"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const audioOff = "Off"
const defaultOutput = "System default"

type applicationState struct {
	window             *mygo.Window
	presentation       *presentationWindow
	closing            bool
	worker             *captureWorker
	devices            []core.VideoDevice
	audioDevices       core.AudioDevices
	device             string
	selection          modeSelection
	colorRange         string
	videoBackend       string
	lastVideoConfig    core.VideoConfig
	audioSource        string
	audioOutput        string
	volume             float64
	muted              bool
	running            bool
	busy               bool
	fullscreen         bool
	settingsOpen       bool
	settingsPage       int
	notice             string
	errorMessage       string
	status             map[string]any
	bitmap             *ui.Bitmap
	displayedFrame     *core.Frame
	detachedPixels     []byte
	gpuImage           *core.GPUImage
	gpuFailure         error
	gpuFallbackPending bool
	gpuMetrics         []core.GPURenderMetrics
	measureLatency     bool
	gpuStartedAt       time.Time
	gpuPendingViews    uint32
}

func newApplication() *applicationState {
	return &applicationState{colorRange: "Auto", videoBackend: "auto", audioSource: audioOff, audioOutput: defaultOutput, volume: 50, measureLatency: os.Getenv("MYGOFRAME_MEASURE_LATENCY") == "1"}
}

func (application *applicationState) retireGPUImage() {
	if application.gpuImage != nil {
		application.gpuImage.Close()
		metrics := application.gpuImage.Metrics()
		if remaining := 7200 - len(application.gpuMetrics); remaining > 0 {
			if len(metrics) > remaining {
				metrics = metrics[:remaining]
			}
			application.gpuMetrics = append(application.gpuMetrics, metrics...)
		}
		application.gpuImage = nil
		application.gpuStartedAt = time.Time{}
		application.gpuPendingViews = 0
	}
	if application.bitmap != nil && application.displayedFrame != nil && application.displayedFrame.IsDMA() {
		application.bitmap.ReleaseExternal()
	}
}

func (application *applicationState) clearPreview() {
	if application.bitmap != nil {
		application.bitmap.SetGPUReadyCallback(nil)
	}
	application.detachPixels()
	application.retireGPUImage()
	application.bitmap = nil
	previous := application.displayedFrame
	application.displayedFrame = nil
	previous.Release()
	application.invalidatePresentation()
}

// Retained render scenes can outlive a view rebuild, so their image must stop
// referencing native pixels before the final frame lease is released.
func (application *applicationState) detachPixels() {
	if application.bitmap != nil && application.displayedFrame != nil && !application.displayedFrame.IsDMA() {
		value := application.displayedFrame.UnsafeBorrowedImage()
		if value == nil {
			return
		}
		bounds := value.Rect
		width, height := bounds.Dx(), bounds.Dy()
		required := width * height * 4
		if cap(application.detachedPixels) < required {
			application.detachedPixels = make([]byte, required)
		}
		application.detachedPixels = application.detachedPixels[:required]
		// The immutable zero backing can be shared by old scenes after the next resize.
		application.bitmap.Update(&image.RGBA{
			Pix: application.detachedPixels, Stride: width * 4, Rect: bounds,
		})
	}
}

func (application *applicationState) trackGPUReady(frame *core.Frame) {
	if application.bitmap == nil || !application.measureLatency {
		return
	}
	sequence, timestamp := frame.Sequence, frame.TimestampNanoseconds
	var mutex sync.Mutex
	seen := make(map[uint64]bool)
	application.bitmap.SetGPUReadyCallback(func(contextID uint64) {
		mutex.Lock()
		defer mutex.Unlock()
		if !seen[contextID] {
			seen[contextID] = true
			core.MarkGPUReady(timestamp, sequence, contextID)
		}
	})
}

func (application *applicationState) processGPUFailure() {
	if application.gpuImage != nil {
		if failure := application.gpuImage.TakeError(); failure != nil {
			application.gpuFailure = failure
		}
		if !application.gpuImage.Prepared() {
			application.gpuPendingViews++
			if application.gpuPendingViews >= 30 && time.Since(application.gpuStartedAt) >= time.Second {
				application.gpuFailure = errors.New("the selected renderer cannot prepare DMA-BUF images")
			}
		}
	}
	if application.gpuFailure == nil || application.worker == nil || application.busy || application.closing {
		return
	}
	configuration := application.lastVideoConfig
	failure := application.gpuFailure
	application.gpuFailure = nil
	if configuration.Path == "" {
		application.errorMessage = failure.Error()
		return
	}
	configuration.Backend = "cpu"
	configuration.FallbackReason = failure.Error()
	application.gpuFallbackPending = true
	application.clearPreview()
	application.submit("Switching to CPU video processing…", func(worker *captureWorker) (func(*applicationState), error) {
		worker.frames.clear()
		worker.afterSequence = 0
		err := worker.engine.StartVideo(configuration)
		return func(application *applicationState) {
			application.gpuFallbackPending = false
			application.clearPreview()
			application.running = err == nil
		}, err
	})
}

func (application *applicationState) takeFrame() {
	application.processGPUFailure()
	if application.worker == nil || application.gpuFallbackPending {
		return
	}
	if frame, available := application.worker.frames.take(); available {
		if application.gpuFailure != nil {
			frame.Release()
			return
		}
		previous := application.displayedFrame
		if frame.IsDMA() {
			width, height := frame.Size()
			previousWidth, previousHeight := previous.Size()
			if application.gpuImage == nil || application.bitmap == nil || previousWidth != width || previousHeight != height {
				application.detachPixels()
				application.retireGPUImage()
				application.gpuImage = core.NewGPUImage()
				application.gpuStartedAt = time.Now()
				application.gpuPendingViews = 0
				application.bitmap = ui.NewExternalBitmap(width, height, application.gpuImage)
			}
			application.gpuImage.SetFrame(frame)
			application.bitmap.InvalidateExternal()
		} else {
			frameImage := frame.UnsafeBorrowedImage()
			if frameImage == nil {
				frame.Release()
				return
			}
			if previous != nil && previous.IsDMA() {
				application.retireGPUImage()
				application.bitmap = nil
			}
			if application.bitmap == nil {
				application.bitmap = ui.NewBitmap(frameImage)
			} else {
				if previous != nil {
					previousImage := previous.UnsafeBorrowedImage()
					if previousImage != nil && previousImage.Rect != frameImage.Rect {
						application.detachPixels()
					}
				}
				application.bitmap.Update(frameImage)
			}
		}
		application.displayedFrame = frame
		application.trackGPUReady(frame)
		previous.Release()
	}
}

func (application *applicationState) gpuRenderMetrics() []core.GPURenderMetrics {
	result := append([]core.GPURenderMetrics(nil), application.gpuMetrics...)
	if application.gpuImage != nil {
		result = append(result, application.gpuImage.Metrics()...)
	}
	return result
}

func (application *applicationState) close() {
	if application.closing {
		return
	}
	application.closing = true
	if application.worker != nil {
		application.worker.close()
	}
	application.closePresentation()
	application.clearPreview()
}

func (application *applicationState) selectedDevice() string {
	for _, device := range application.devices {
		if videoLabel(device) == application.device {
			return device.Path
		}
	}
	return ""
}

func (application *applicationState) refresh() {
	selectedPath := application.selectedDevice()
	selectedSource, selectedOutput := application.audioIDs()
	application.submit("Discovering capture devices…", func(worker *captureWorker) (func(*applicationState), error) {
		devices, videoError := core.VideoDevices()
		audioDevices, audioError := core.ListAudioDevices()
		var selected core.VideoDevice
		for _, device := range devices {
			if device.Path == selectedPath {
				selected = device
			}
		}
		if selected.Path == "" && len(devices) > 0 {
			selected = devices[0]
		}
		var modes []core.VideoMode
		var modesError error
		if selected.Path != "" {
			modes, modesError = core.VideoModes(selected.Path)
		}
		sourcePresent, outputPresent := selectedSource == "", selectedOutput == ""
		for _, device := range audioDevices.Sources {
			if device.ID == selectedSource {
				sourcePresent = true
			}
		}
		for _, device := range audioDevices.Outputs {
			if device.ID == selectedOutput {
				outputPresent = true
			}
		}
		if !sourcePresent || !outputPresent {
			worker.engine.StopAudio()
		}
		return func(application *applicationState) {
			application.devices, application.audioDevices = devices, audioDevices
			application.device = ""
			if selected.Path != "" {
				application.device = videoLabel(selected)
			}
			application.selection.setModes(modes)
			if !sourcePresent || !outputPresent {
				application.audioSource = audioOff
			}
			if !outputPresent {
				application.audioOutput = defaultOutput
			}
		}, errors.Join(videoError, audioError, modesError)
	})
}

func (application *applicationState) loadModes() {
	path := application.selectedDevice()
	application.selection.setModes(nil)
	application.submit("Reading video modes…", func(worker *captureWorker) (func(*applicationState), error) {
		modes, err := core.VideoModes(path)
		return func(application *applicationState) { application.selection.setModes(modes) }, err
	})
}

func (application *applicationState) startVideo() {
	mode, valid := application.selection.selected()
	path := application.selectedDevice()
	if !valid || path == "" {
		application.errorMessage = "Select a capture device and an available video mode."
		return
	}
	rangeValue := int32(0)
	if application.colorRange == "Limited" {
		rangeValue = 1
	}
	if application.colorRange == "Full" {
		rangeValue = 2
	}
	config := core.VideoConfig{Path: path, Mode: mode, ColorRange: rangeValue, Backend: application.videoBackend}
	application.lastVideoConfig = config
	application.gpuFailure = nil
	application.gpuFallbackPending = false
	application.submit("Starting video capture…", func(worker *captureWorker) (func(*applicationState), error) {
		worker.frames.clear()
		worker.afterSequence = 0
		err := worker.engine.StartVideo(config)
		return func(application *applicationState) { application.clearPreview(); application.running = err == nil }, err
	})
}

func (application *applicationState) stopVideo() {
	application.gpuFailure = nil
	application.gpuFallbackPending = false
	application.submit("Stopping video capture…", func(worker *captureWorker) (func(*applicationState), error) {
		worker.engine.StopVideo()
		worker.frames.clear()
		return func(application *applicationState) { application.clearPreview(); application.running = false }, nil
	})
}

func (application *applicationState) audioIDs() (string, string) {
	var source, output string
	for _, device := range application.audioDevices.Sources {
		if audioLabel(device) == application.audioSource {
			source = device.ID
		}
	}
	for _, device := range application.audioDevices.Outputs {
		if audioLabel(device) == application.audioOutput {
			output = device.ID
		}
	}
	return source, output
}

func (application *applicationState) changeAudio() {
	source, output := application.audioIDs()
	application.submit("Updating audio monitoring…", func(worker *captureWorker) (func(*applicationState), error) {
		worker.engine.SetVolume(math.Float64frombits(worker.volume.Load()))
		err := worker.engine.StartAudio(source, output)
		if err != nil {
			worker.engine.StopAudio()
		}
		return func(application *applicationState) {
			if err != nil {
				application.audioSource = audioOff
			}
		}, err
	})
}

func (application *applicationState) setVolume() {
	volume := application.volume / 100
	if application.muted {
		volume = 0
	}
	if application.worker != nil {
		application.worker.volume.Store(math.Float64bits(volume))
	}
}

func (application *applicationState) setFullscreen(value bool) {
	application.fullscreen = value
	if value {
		application.settingsOpen = false
	}
	if application.window != nil {
		application.window.SetFullScreen(value)
	}
}

func (application *applicationState) view(context *ui.Context) {
	application.takeFrame()
	appearance := fluentAppearance(context.Theme().Dark)
	context.SetTheme(appearance.theme)
	if context.Shortcut(0, ui.KeyF11) {
		application.setFullscreen(!application.fullscreen)
	}
	if context.Shortcut(0, ui.KeyEscape) && application.fullscreen {
		application.setFullscreen(false)
	}
	if application.fullscreen {
		application.preview(context)
		return
	}
	width, height := context.Size()
	compact := width < 800 || height < 480
	if !compact {
		application.settingsOpen = false
	}
	padding := float32(16)
	if compact {
		padding = 8
	}
	ui.Column(context).Fill().Background(appearance.theme.Background).Children(func() {
		application.commandBar(context, compact, width)
		ui.Row(context).FillWidth().Grow(1).MinHeight(0).Padding(0, padding, padding).Gap(16).Children(func() {
			if !compact {
				ui.Column(context).Width(330).Shrink(0).FillHeight().Background(appearance.card).Border(1, appearance.stroke).Radius(8).Label("Capture settings").Children(func() {
					ui.Row(context).FillWidth().Shrink(0).Padding(16, 16, 8).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(context, "Settings").FontSize(20).FontWeight(600).Grow(1)
						if ui.Button(context.Key("refresh"), "Refresh").Tooltip("Refresh capture and audio devices").Disabled(application.busy || application.running).Clicked() {
							application.refresh()
						}
					})
					application.settingsBody(context)
				})
			}
			ui.Column(context).Grow(1).MinWidth(0).FillHeight().Background(appearance.card).Border(1, appearance.stroke).Radius(8).Children(func() {
				if !compact {
					ui.Row(context).FillWidth().Shrink(0).Padding(12, 16).Gap(12).AlignItems(ui.Center).Children(func() {
						ui.Text(context, "Preview").FontSize(16).FontWeight(600).Grow(1)
						application.captureStatus(context, appearance)
					})
				}
				application.preview(context)
				application.previewStatus(context, compact, height, appearance)
			})
		})
	})
	ui.DialogBase(context, &application.settingsOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.45))
		panel.Column().Size(min(400, max(1, width-24)), min(680, max(1, height-24))).Background(appearance.card).Border(1, appearance.stroke).Radius(12).Label("Capture settings dialog").Children(func() {
			ui.Row(context).FillWidth().Shrink(0).Padding(12).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(context, "Settings").FontSize(20).FontWeight(600).Grow(1)
				if ui.Button(context.Key("dialog-refresh"), "Refresh").Disabled(application.busy || application.running).Clicked() {
					application.refresh()
				}
				if ui.Button(context.Key("close-settings"), "Close").Clicked() {
					application.settingsOpen = false
				}
			})
			application.settingsBody(context)
		})
	})
}

func (application *applicationState) preview(context *ui.Context) {
	viewport := ui.Column(context).FillWidth().Grow(1).MinWidth(0).MinHeight(0).Clip().Label("Video viewport")
	if application.fullscreen {
		viewport.Fill()
	}
	viewport.Background(ui.RGB(0, 0, 0)).Center().Children(func() {
		if application.bitmap != nil {
			bitmap := application.bitmap
			// The video contributes no intrinsic layout size, so capture resolution cannot enlarge the viewport.
			ui.Box(context).Fill().MinWidth(0).MinHeight(0).Label("Live capture preview").Draw(func(painter *ui.Painter, bounds ui.Rect) {
				if !application.canPresentFrame(painter) {
					return
				}
				width, height := bitmap.Size()
				painter.ImageRegion(bitmap, ui.Rect{}, fitVideoRectangle(bounds, width, height), ui.ImageOptions{})
			})
		} else {
			ui.Text(context, "Capture preview").FontSize(24).TextColor(ui.RGB(205, 209, 220))
			message := "Choose a video mode and start capture."
			if application.running {
				message = "Waiting for video frames…"
			}
			ui.Text(context, message).FontSize(14).TextColor(ui.RGB(145, 152, 170))
		}
	})
}

func (application *applicationState) canPresentFrame(painter *ui.Painter) bool {
	if application.displayedFrame != nil && application.displayedFrame.IsDMA() && !painter.Accelerated() {
		if time.Since(application.gpuStartedAt) >= 250*time.Millisecond {
			application.gpuFailure = errors.New("the selected renderer does not provide GPU acceleration")
		}
		return false
	}
	return true
}

func fitVideoRectangle(bounds ui.Rect, width, height int) ui.Rect {
	if width <= 0 || height <= 0 || bounds.W <= 0 || bounds.H <= 0 {
		return ui.Rect{}
	}
	scale := min(bounds.W/float32(width), bounds.H/float32(height))
	fittedWidth, fittedHeight := float32(width)*scale, float32(height)*scale
	return ui.Rect{X: bounds.X + (bounds.W-fittedWidth)/2, Y: bounds.Y + (bounds.H-fittedHeight)/2, W: fittedWidth, H: fittedHeight}
}

func actualVideoSummary(status map[string]any) string {
	video, ok := status["video"].(map[string]any)
	if !ok || len(video) == 0 {
		return ""
	}
	var parts []string
	if format, _ := video["format"].(string); format != "" {
		parts = append(parts, format)
	}
	width, _ := video["width"].(float64)
	height, _ := video["height"].(float64)
	if width > 0 && height > 0 {
		parts = append(parts, fmt.Sprintf("%.0f × %.0f", width, height))
	}
	numerator, _ := video["intervalNumerator"].(float64)
	denominator, _ := video["intervalDenominator"].(float64)
	if numerator > 0 {
		parts = append(parts, fmt.Sprintf("%.3f fps", denominator/numerator))
	}
	if colorRange, ok := video["colorRange"].(float64); ok {
		label := map[float64]string{0: "Auto", 1: "Limited", 2: "Full"}[colorRange]
		if label != "" {
			parts = append(parts, label+" range")
		}
	}
	return strings.Join(parts, " · ")
}
