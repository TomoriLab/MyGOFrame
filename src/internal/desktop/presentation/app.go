package presentation

import (
	"errors"
	"fmt"
	"image"
	"sync"
	"time"

	about "github.com/TomoriLab/MyGOFrame/src/internal/about/domain"
	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/application"
	desktopapp "github.com/TomoriLab/MyGOFrame/src/internal/desktop/application"
	recording "github.com/TomoriLab/MyGOFrame/src/internal/recording/domain"
	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"

	"github.com/TomoriLab/MyGOFrame/src/internal/desktop/presentation/localization"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const audioOff = "Off"
const defaultOutput = "System default"

type applicationState struct {
	services                 desktopapp.Services
	changeLocale             func(string)
	sidebarCollapsed         bool
	settingsOverviewScroll   ui.ScrollState
	settingsSectionClosed    bool
	controlsSectionClosed    bool
	informationSectionClosed bool
	debugEnabled             bool
	debugUpdating            bool
	debugDone                chan struct{}
	aboutOpen                bool
	noticesOpen              bool
	aboutCatalog             *about.Catalog
	aboutError               error
	licenseOpen              bool
	licenseComponent         about.Component
	licenseBody              string
	licenseSearch            string
	licenseNeedsReset        bool
	window                   *mygo.Window
	presentation             *presentationWindow
	closing                  bool
	worker                   *captureWorker
	devices                  []session.VideoDevice
	audioDevices             session.AudioDevices
	device                   string
	selection                modeSelection
	colorRange               string
	videoBackend             string
	audioSource              string
	audioOutput              string
	volume                   float64
	muted                    bool
	running                  bool
	busy                     bool
	fullscreen               bool
	settingsOpen             bool
	themeOverride            bool
	darkTheme                bool
	settingsPage             int
	recordingConfig          session.RecordingConfiguration
	recordingDevices         []session.RecordingDevice
	recordingGPU             string
	recordingProbeBusy       bool
	recordingProbeDone       chan struct{}
	monitorAudio             bool
	presets                  []session.CapturePreset
	selectedPreset           string
	presetName               string
	presetBusy               bool
	presetDone               chan struct{}
	presetError              string
	presetNotice             string
	presetDeletePending      bool
	notice                   string
	errorMessage             string
	status                   session.SessionSnapshot
	bitmap                   *ui.Bitmap
	displayedFrame           capture.Frame
	detachedPixels           []byte
	gpuImage                 capture.GPUImage
	gpuGeneration            uint64
	gpuFailure               error
	gpuFailureGeneration     uint64
	gpuFallbackPending       bool
	gpuMetrics               []capture.GPURenderMetrics
	measureLatency           bool
	gpuStartedAt             time.Time
	gpuPendingViews          uint32
}

func newApplication(services desktopapp.Services) *applicationState {
	application := &applicationState{services: services, selection: modeSelection{resolve: services.ResolveModes}, settingsPage: -1, recordingConfig: recording.DefaultRecordingConfiguration(), recordingGPU: "Automatic", monitorAudio: true, colorRange: "Auto", videoBackend: "auto", audioSource: audioOff, audioOutput: defaultOutput, volume: 50, measureLatency: services.MeasureLatency}
	application.darkTheme, application.themeOverride = loadThemePreference()
	if services.Diagnostics != nil {
		application.debugEnabled = services.Diagnostics.Enabled()
	}
	return application
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
	application.gpuFailure = nil
	if application.bitmap != nil {
		application.bitmap.SetGPUReadyCallback(nil)
	}
	application.detachPixels()
	application.retireGPUImage()
	application.bitmap = nil
	previous := application.displayedFrame
	application.displayedFrame = nil
	if previous != nil {
		previous.Release()
	}
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

func (application *applicationState) trackGPUReady(frame capture.Frame) {
	if application.bitmap == nil || !application.measureLatency {
		return
	}
	sequence, timestamp := frame.SequenceNumber(), frame.CaptureTimestamp()
	var mutex sync.Mutex
	seen := make(map[uint64]bool)
	application.bitmap.SetGPUReadyCallback(func(contextID uint64) {
		mutex.Lock()
		defer mutex.Unlock()
		if !seen[contextID] {
			seen[contextID] = true
			application.services.MarkGPUReady(timestamp, sequence, contextID)
		}
	})
}

func (application *applicationState) processGPUFailure() {
	if application.gpuImage != nil {
		if failure := application.gpuImage.TakeError(); failure != nil {
			application.gpuFailure = failure
			application.gpuFailureGeneration = application.gpuGeneration
		}
		if !application.gpuImage.Prepared() {
			application.gpuPendingViews++
			if application.gpuPendingViews >= 30 && time.Since(application.gpuStartedAt) >= time.Second {
				application.gpuFailure = errors.New("the selected renderer cannot prepare DMA-BUF images")
				application.gpuFailureGeneration = application.gpuGeneration
			}
		}
	}
	// A queued renderer error retains its original session while control requests finish.
	if application.gpuFailure != nil && application.gpuFailureGeneration != application.status.Generation {
		application.gpuFailure = nil
	}
	if application.gpuFailure == nil || application.worker == nil || application.busy || application.closing {
		return
	}
	failure := application.gpuFailure
	application.debugEvent("video.gpu-fallback", failure.Error())
	generation := application.gpuFailureGeneration
	application.gpuFailure = nil
	application.gpuFallbackPending = true
	application.clearPreview()
	application.submit("Switching to CPU video processing…", func(worker *captureWorker) (func(*applicationState), error) {
		worker.frames.clear()
		worker.afterSequence = 0
		err := worker.engine.ReportGPUFailure(generation, failure.Error())
		return func(application *applicationState) {
			application.gpuFallbackPending = false
			application.clearPreview()
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
			previousWidth, previousHeight := 0, 0
			if previous != nil {
				previousWidth, previousHeight = previous.Size()
			}
			if application.gpuImage == nil || application.bitmap == nil || previousWidth != width || previousHeight != height {
				application.detachPixels()
				application.retireGPUImage()
				application.gpuImage = application.services.NewGPUImage()
				application.gpuGeneration = application.status.Generation
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
		if previous != nil {
			previous.Release()
		}
	}
}

func (application *applicationState) gpuRenderMetrics() []capture.GPURenderMetrics {
	result := append([]capture.GPURenderMetrics(nil), application.gpuMetrics...)
	if application.gpuImage != nil {
		result = append(result, application.gpuImage.Metrics()...)
	}
	return result
}

func (application *applicationState) close() {
	if application.closing {
		return
	}
	application.debugEvent("app.closing", "")
	application.closing = true
	if application.debugDone != nil {
		<-application.debugDone
	}
	if application.recordingProbeDone != nil {
		<-application.recordingProbeDone
	}
	if application.presetDone != nil {
		<-application.presetDone
	}
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
		catalog, err := worker.engine.RefreshDevices(selectedPath, selectedSource, selectedOutput)
		return func(application *applicationState) {
			application.applyCatalog(catalog, selectedPath)
		}, err
	})
}

// Failed enumerations leave the last displayed list intact until a valid snapshot arrives.
func (application *applicationState) applyCatalog(catalog session.DeviceCatalog, previousPath string) {
	if catalog.VideoAvailable {
		application.devices = catalog.Devices
		application.device = ""
		for _, device := range catalog.Devices {
			if device.Path == catalog.SelectedPath {
				application.device = videoLabel(device)
			}
		}
	}
	if catalog.ModesAvailable || (catalog.VideoAvailable && catalog.SelectedPath != previousPath) {
		application.selection.setModes(catalog.Modes)
	}
	if catalog.AudioAvailable {
		application.audioDevices = catalog.AudioDevices
		application.audioSource, application.audioOutput = audioOff, defaultOutput
		for _, device := range catalog.AudioDevices.Sources {
			if device.ID == catalog.Source {
				application.audioSource = audioLabel(device)
			}
		}
		for _, device := range catalog.AudioDevices.Outputs {
			if device.ID == catalog.Output {
				application.audioOutput = audioLabel(device)
			}
		}
	}
}

func (application *applicationState) loadModes() {
	path := application.selectedDevice()
	application.selection.setModes(nil)
	application.submit("Reading video modes…", func(worker *captureWorker) (func(*applicationState), error) {
		modes, err := application.services.VideoModes(path)
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
	config := session.VideoConfig{Path: path, Mode: mode, ColorRange: rangeValue, Backend: application.videoBackend}
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
		if controls := worker.controls.Load(); controls != nil {
			_ = worker.engine.SetAudioControls(*controls)
		}
		err := worker.engine.StartAudio(source, output)
		snapshot, _ := worker.engine.Snapshot()
		return func(application *applicationState) {
			if err != nil {
				application.audioSource = audioOff
				application.audioOutput = defaultOutput
				for _, device := range application.audioDevices.Sources {
					if device.ID == snapshot.Audio.Source {
						application.audioSource = audioLabel(device)
					}
				}
				for _, device := range application.audioDevices.Outputs {
					if device.ID == snapshot.Audio.Output {
						application.audioOutput = audioLabel(device)
					}
				}
			}
		}, err
	})
}

func (application *applicationState) setVolume() {
	if application.worker != nil {
		application.worker.setAudioControls(session.AudioControls{Volume: application.volume / 100, Muted: application.muted})
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
	dark := context.Theme().Dark
	if application.themeOverride {
		dark = application.darkTheme
	} else {
		application.darkTheme = dark
	}
	appearance := fluentAppearance(dark)
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
	ui.Row(context).Fill().Gap(0).Background(appearance.theme.Background).Children(func() {
		application.sidebarLayout(context, compact, height)
		ui.Column(context).Grow(1).MinWidth(0).FillHeight().Children(func() {
			application.preview(context)
		})
	})

	ui.DialogBase(context, &application.settingsOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.45))
		panel.Column().Size(min(400, max(1, width-24)), min(680, max(1, height-24))).Background(appearance.card).Border(1, appearance.stroke).Radius(12).Label(text(context, "Capture settings dialog")).Children(func() {
			ui.Row(context).FillWidth().Shrink(0).Padding(12).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(context, text(context, "Settings")).FontSize(20).FontWeight(600).Grow(1).MinWidth(0).SingleLine()
				application.themeSwitch(context)

				if ui.Button(context.Key("close-settings"), text(context, "Close")).Clicked() {
					application.settingsOpen = false
				}
			})
			ui.Scroll(context.Key("compact-settings-scroll")).Label(text(context, "Settings panel content")).FillWidth().Grow(1).MinHeight(0).Children(func() {
				ui.Column(context).FillWidth().Children(func() {
					ui.Column(context).FillWidth().Height(280).Shrink(0).Children(func() { application.settingsBody(context) })
					application.sidebarActions(context)
					application.previewStatus(context, true, height, appearance)
				})
			})
		})
	})
	application.aboutDialogs(context, width, height)
}

func (application *applicationState) preview(context *ui.Context) {
	viewport := ui.Column(context).FillWidth().Grow(1).MinWidth(0).MinHeight(0).Clip().Label(text(context, "Video viewport"))
	if application.fullscreen {
		viewport.Fill()
	}
	viewport.Background(ui.RGB(0, 0, 0)).Center().Children(func() {
		if application.bitmap != nil {
			bitmap := application.bitmap
			// The video contributes no intrinsic layout size, so capture resolution cannot enlarge the viewport.
			ui.Box(context).Fill().MinWidth(0).MinHeight(0).Label(text(context, "Live capture preview")).Draw(func(painter *ui.Painter, bounds ui.Rect) {
				if !application.canPresentFrame(painter) {
					return
				}
				width, height := bitmap.Size()
				painter.ImageRegion(bitmap, ui.Rect{}, fitVideoRectangle(bounds, width, height), ui.ImageOptions{})
			})
		} else {
			ui.Text(context, text(context, "Capture preview")).FillWidth().MinWidth(0).TextAlign(ui.Center).FontSize(24).TextColor(ui.RGB(205, 209, 220))
			message := "Choose a video mode and start capture."
			if application.running {
				message = "Waiting for video frames…"
			}
			ui.Text(context, text(context, message)).FillWidth().MinWidth(0).TextAlign(ui.Center).FontSize(14).TextColor(ui.RGB(145, 152, 170))
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

func actualVideoSummary(status session.SessionSnapshot, contexts ...*ui.Context) string {
	video := status.Video
	if video.Width == 0 || video.Height == 0 {
		return ""
	}
	result := fmt.Sprintf("%s · %d × %d", video.Format, video.Width, video.Height)
	if video.IntervalNumerator > 0 {
		result += fmt.Sprintf(" · %.2f fps", float64(video.IntervalDenominator)/float64(video.IntervalNumerator))
	}
	if label := map[int32]string{0: "Auto", 1: "Limited", 2: "Full"}[video.ColorRange]; label != "" {
		result += " · " + localization.Text(messageLocale(contexts), label)
	}
	return result
}
