package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"math"
	"math/big"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/TomoriLab/MyGOFrame/internal/core"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	list := flag.Bool("list", false, "List V4L2 video devices and PipeWire audio sources and outputs as JSON.")
	probe := flag.String("probe", "", "List the supported video modes of a V4L2 device as JSON.")
	video := flag.String("smoke-video", "", "Capture a V4L2 device without opening a window.")
	uiVideo := flag.String("smoke-ui-video", "", "Display a V4L2 device in the native window; requires --smoke-ui-seconds.")
	uiPattern := flag.Bool("smoke-ui-pattern", false, "Display a synthetic border and colored corners; requires --smoke-ui-seconds.")
	captureWindow := flag.Bool("capture-window", false, "Open a separate native video-only window for application sharing.")
	captureWindowCycles := flag.Int("smoke-capture-window-cycles", 0, "Close and reopen the capture window during a UI check; requires --capture-window and --smoke-ui-seconds.")
	windowWidth := flag.Int("window-width", 1280, "Set the initial window content width in logical pixels.")
	windowHeight := flag.Int("window-height", 800, "Set the initial window content height in logical pixels.")
	audio := flag.String("smoke-audio", "", "Monitor a PipeWire source without opening a window.")
	output := flag.String("output", "", "Select a PipeWire output node; an empty value uses the system default.")
	format := flag.String("format", "NV12", "Select a four-character V4L2 pixel format for the video check.")
	width := flag.Uint("width", 640, "Select the video check width in pixels.")
	height := flag.Uint("height", 480, "Select the video check height in pixels.")
	frameRate := flag.String("fps", "60", "Select frames per second, as a decimal or fraction such as 60000/1001.")
	seconds := flag.Float64("seconds", 2, "Run a video or audio check for this many seconds.")
	volume := flag.Float64("volume", 0, "Set audio check volume from 0 to 100; the default is silent.")
	uiSeconds := flag.Float64("smoke-ui-seconds", 0, "Close the native window after this many seconds for a UI check.")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flag.Args())
	}
	modes := 0
	for _, enabled := range []bool{*list, *probe != "", *video != "", *audio != "", *uiVideo != "", *uiPattern} {
		if enabled {
			modes++
		}
	}
	if modes > 1 {
		return errors.New("choose only one of --list, --probe, --smoke-video, --smoke-audio, --smoke-ui-video, or --smoke-ui-pattern")
	}
	if (*captureWindow || *captureWindowCycles != 0) && (*list || *probe != "" || *video != "" || *audio != "") {
		return errors.New("capture window options require a graphical UI mode")
	}
	if *list {
		return listDevices()
	}
	if *probe != "" {
		modes, err := core.VideoModes(*probe)
		if err != nil {
			return err
		}
		return writeJSON(modes)
	}
	if *video != "" || *audio != "" {
		if math.IsNaN(*seconds) || math.IsInf(*seconds, 0) || *seconds <= 0 || *seconds > 3600 {
			return errors.New("--seconds must be greater than zero and no more than 3600")
		}
		if math.IsNaN(*volume) || math.IsInf(*volume, 0) || *volume < 0 || *volume > 100 {
			return errors.New("--volume must be from 0 to 100")
		}
		engine, err := core.New()
		if err != nil {
			return err
		}
		defer engine.Close()
		if *video != "" {
			config, err := parseVideoConfig(*video, *format, *width, *height, *frameRate)
			if err != nil {
				return err
			}
			if err := engine.StartVideo(config); err != nil {
				return err
			}
			defer engine.StopVideo()
		} else {
			engine.SetVolume(*volume / 100)
			if err := engine.StartAudio(*audio, *output); err != nil {
				return err
			}
			defer engine.StopAudio()
		}
		return smoke(engine, time.Duration(*seconds*float64(time.Second)), *video != "")
	}
	if math.IsNaN(*uiSeconds) || math.IsInf(*uiSeconds, 0) || *uiSeconds < 0 || *uiSeconds > 3600 {
		return errors.New("--smoke-ui-seconds must be from 0 to 3600")
	}
	if *windowWidth < 320 || *windowWidth > 16384 || *windowHeight < 240 || *windowHeight > 16384 {
		return errors.New("window width must be from 320 to 16384 and height from 240 to 16384")
	}
	if *uiPattern && (*uiSeconds <= 0 || *width == 0 || *height == 0 || *width > 8192 || *height > 8192) {
		return errors.New("--smoke-ui-pattern requires a positive --smoke-ui-seconds and source dimensions from 1 to 8192")
	}
	if *captureWindowCycles < 0 || *captureWindowCycles > 16 || (*captureWindowCycles > 0 && (!*captureWindow || *uiSeconds < float64(*captureWindowCycles+1))) {
		return errors.New("--smoke-capture-window-cycles must be from 0 to 16 and requires --capture-window with at least one UI second per cycle plus one")
	}
	var uiConfig *core.VideoConfig
	if *uiVideo != "" {
		if *uiSeconds <= 0 {
			return errors.New("--smoke-ui-video requires a positive --smoke-ui-seconds")
		}
		config, err := parseVideoConfig(*uiVideo, *format, *width, *height, *frameRate)
		if err != nil {
			return err
		}
		uiConfig = &config
	}
	return runWindow(time.Duration(*uiSeconds*float64(time.Second)), uiConfig, windowConfiguration{
		width: *windowWidth, height: *windowHeight, pattern: *uiPattern, patternWidth: int(*width), patternHeight: int(*height),
		captureWindow: *captureWindow, captureWindowCycles: *captureWindowCycles,
	})
}

func parseVideoConfig(path, format string, width, height uint, frameRate string) (core.VideoConfig, error) {
	if width == 0 || height == 0 || width > 16384 || height > 16384 {
		return core.VideoConfig{}, errors.New("video dimensions must be from 1 to 16384")
	}
	fourcc, err := core.FourCC(format)
	if err != nil {
		return core.VideoConfig{}, err
	}
	numerator, denominator, err := parseFrameRate(frameRate)
	if err != nil {
		return core.VideoConfig{}, err
	}
	return core.VideoConfig{Path: path, Mode: core.VideoMode{FourCC: fourcc, Format: format, Width: uint32(width), Height: uint32(height), IntervalNumerator: numerator, IntervalDenominator: denominator}}, nil
}

func parseFrameRate(value string) (uint32, uint32, error) {
	rate, ok := new(big.Rat).SetString(value)
	if !ok || rate.Sign() <= 0 || !rate.Num().IsUint64() || !rate.Denom().IsUint64() || rate.Num().Uint64() > math.MaxUint32 || rate.Denom().Uint64() > math.MaxUint32 {
		return 0, 0, errors.New("--fps must be a positive decimal or fraction representable by 32-bit integers")
	}
	return uint32(rate.Denom().Uint64()), uint32(rate.Num().Uint64()), nil
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func listDevices() error {
	video, videoError := core.VideoDevices()
	audio, audioError := core.ListAudioDevices()
	result := map[string]any{"video": video, "audio": audio}
	if err := errors.Join(videoError, audioError); err != nil {
		result["error"] = err.Error()
	}
	if err := writeJSON(result); err != nil {
		return err
	}
	return errors.Join(videoError, audioError)
}

func smoke(engine *core.Engine, duration time.Duration, video bool) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	var sequence, acquired uint64
	running := true
	for running {
		select {
		case <-timer.C:
			running = false
		case <-interrupt:
			running = false
		case <-ticker.C:
			if video {
				frame, err := engine.AcquireFrame(sequence)
				if err != nil {
					return err
				}
				if frame != nil {
					sequence = frame.Sequence
					acquired++
					frame.Release()
				}
			}
		}
	}
	status, err := engine.Status()
	if err != nil {
		return err
	}
	status["acquiredFrames"] = acquired
	if err := writeJSON(status); err != nil {
		return err
	}
	return validateSmokeStatus(status, acquired, video)
}

func validateSmokeStatus(status map[string]any, acquired uint64, video bool) error {
	if message, _ := status["error"].(string); message != "" {
		return errors.New(message)
	}
	if video {
		if acquired == 0 {
			return errors.New("video check completed without receiving a frame")
		}
		if running, _ := status["videoRunning"].(bool); !running {
			return errors.New("video capture stopped before the check completed")
		}
		return nil
	}
	audio, ok := status["audio"].(map[string]any)
	if !ok {
		return errors.New("audio check returned no stream status")
	}
	if message, _ := audio["error"].(string); message != "" {
		return errors.New(message)
	}
	if running, _ := audio["running"].(bool); !running {
		return errors.New("audio streams are not running")
	}
	captured, _ := audio["capturedFrames"].(float64)
	played, _ := audio["playedFrames"].(float64)
	if captured <= 0 || played <= 0 {
		return errors.New("audio check completed without captured and played frames")
	}
	return nil
}

func validatePreview(application *applicationState) error {
	if application.errorMessage != "" {
		return errors.New(application.errorMessage)
	}
	if !application.running {
		return errors.New("UI video capture is not running")
	}
	if application.displayedFrame == nil || application.bitmap == nil {
		return errors.New("UI check completed without displaying a video frame")
	}
	return validateSmokeStatus(application.status, 1, true)
}

type windowConfiguration struct {
	width, height               int
	pattern                     bool
	patternWidth, patternHeight int
	captureWindow               bool
	captureWindowCycles         int
}

func runWindow(closeAfter time.Duration, videoConfig *core.VideoConfig, config windowConfiguration) error {
	engine, err := core.New()
	if err != nil {
		return err
	}
	defer engine.Close()
	application := newApplication()
	var previewError error
	var previewStatus map[string]any
	captureWindowCheck := config.captureWindow && closeAfter > 0
	if captureWindowCheck {
		previewError = errors.New("capture window check did not complete")
	}
	if videoConfig != nil {
		device := core.VideoDevice{Path: videoConfig.Path, Name: "Video smoke test"}
		application.devices = []core.VideoDevice{device}
		application.device = videoLabel(device)
		application.selection.setModes([]core.VideoMode{videoConfig.Mode})
		previewError = errors.New("UI preview check did not complete")
	}
	if config.pattern {
		application.bitmap = ui.NewBitmap(previewPattern(config.patternWidth, config.patternHeight))
	}
	mygo.App.WhenReady(func() {
		window := mygo.NewWindow(mygo.WindowOptions{Title: "MyGOFrame", Width: config.width, Height: config.height, UseContentSize: true, MinWidth: 320, MinHeight: 240, Content: ui.View(application.view)})
		application.window = window
		application.applyWindowIcon(window)
		if !config.pattern {
			application.worker = newWorker(engine, window, application)
		}
		var cycleTimers []*time.Timer
		window.OnClosed(func() {
			for _, timer := range cycleTimers {
				timer.Stop()
			}
			previewError = nil
			if videoConfig != nil {
				currentStatus, statusError := engine.Status()
				if statusError != nil {
					previewError = statusError
				} else {
					application.status = currentStatus
					previewError = validatePreview(application)
				}
				previewStatus = maps.Clone(application.status)
				if previewStatus == nil {
					previewStatus = make(map[string]any)
				}
				if application.displayedFrame != nil {
					previewStatus["displayedFrameSequence"] = application.displayedFrame.Sequence
				}
			}
			if captureWindowCheck {
				if previewStatus == nil {
					previewStatus = make(map[string]any)
				}
				presentation := application.presentation
				if presentation == nil {
					previewError = errors.Join(previewError, errors.New("capture window closed before the UI check completed"))
				} else {
					previewStatus["captureWindowDisplayedSequence"] = presentation.displayedSequence
					previewStatus["captureWindowRenderCount"] = presentation.renderCount
					previewStatus["captureWindowDisplayedBitmap"] = presentation.displayedBitmap
					if presentation.renderCount == 0 {
						previewError = errors.Join(previewError, errors.New("capture window check completed without rendering"))
					} else if ((config.pattern || videoConfig != nil) && !presentation.displayedBitmap) || (videoConfig != nil && presentation.displayedSequence == 0) {
						previewError = errors.Join(previewError, errors.New("capture window check completed without displaying its video"))
					}
				}
			}
			application.close()
			mygo.App.Quit()
		})
		window.OnEnterFullScreen(func() { application.fullscreen = true; window.Invalidate() })
		window.OnLeaveFullScreen(func() { application.fullscreen = false; window.Invalidate() })
		if config.captureWindow {
			application.openPresentation()
		}
		for index := 1; index <= config.captureWindowCycles; index++ {
			delay := closeAfter * time.Duration(index) / time.Duration(config.captureWindowCycles+1)
			cycleTimers = append(cycleTimers, time.AfterFunc(delay, func() {
				window.Update(func() {
					if !application.closing {
						application.closePresentation()
						application.openPresentation()
					}
				})
			}))
		}
		if videoConfig != nil {
			application.startVideo()
		} else if !config.pattern {
			application.refresh()
		}
		if closeAfter > 0 {
			time.AfterFunc(closeAfter, window.Close)
		}
	})
	defer application.close()
	if err := mygo.App.Run(); err != nil {
		return err
	}
	if previewError != nil {
		return previewError
	}
	if videoConfig != nil || captureWindowCheck {
		return writeJSON(previewStatus)
	}
	return nil
}
