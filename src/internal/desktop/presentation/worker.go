package presentation

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/application"
	desktopapp "github.com/TomoriLab/MyGOFrame/src/internal/desktop/application"
	sessionapp "github.com/TomoriLab/MyGOFrame/src/internal/session/application"
	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"

	"github.com/TomoriLab/MyGOFrame/src/internal/desktop/presentation/localization"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type command struct {
	name string
	run  func(*captureWorker) (func(*applicationState), error)
}

type captureWorker struct {
	diagnostics       desktopapp.Diagnostics
	engine            sessionapp.Engine
	window            *mygo.Window
	presentation      atomic.Pointer[mygo.Window]
	application       *applicationState
	commands          chan command
	stop              chan struct{}
	done              chan struct{}
	closing           atomic.Bool
	closeOnce         sync.Once
	controls          atomic.Pointer[session.AudioControls]
	controlChanged    chan struct{}
	frames            latestSlot[capture.Frame]
	afterSequence     uint64
	frameEvents       *os.File
	frameReady        chan struct{}
	eventErrors       chan error
	readerDone        chan struct{}
	deviceWatcherDone chan struct{}
}

func newWorker(engine sessionapp.Engine, window *mygo.Window, application *applicationState) (*captureWorker, error) {
	events, err := engine.FrameEvents()
	if err != nil {
		return nil, err
	}
	worker := &captureWorker{diagnostics: application.services.Diagnostics, engine: engine, window: window, application: application, commands: make(chan command, 16), stop: make(chan struct{}), done: make(chan struct{}), controlChanged: make(chan struct{}, 1), frameEvents: events, frameReady: make(chan struct{}, 1), eventErrors: make(chan error, 1), readerDone: make(chan struct{})}
	worker.setAudioControls(session.AudioControls{Volume: application.volume / 100, Muted: application.muted})
	worker.deviceWatcherDone = make(chan struct{})
	go worker.watchDevices()
	go worker.readFrameEvents()
	go worker.run()
	return worker, nil
}

func (worker *captureWorker) post(apply func(*applicationState)) {
	if worker.closing.Load() {
		return
	}
	worker.window.Update(func() {
		if !worker.closing.Load() {
			apply(worker.application)
			worker.application.invalidatePresentation()
		}
	})
}

func (worker *captureWorker) run() {
	defer close(worker.done)
	defer worker.engine.Close()
	defer worker.frames.clear()
	defer func() { _ = worker.frameEvents.Close(); <-worker.readerDone }()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	lastFrameError := ""
	for {
		select {
		case <-worker.stop:
			return
		case operation := <-worker.commands:
			worker.debugEvent("command.started", operation.name)
			apply, err := operation.run(worker)
			if err != nil {
				worker.debugEvent("command.failed", operation.name+" "+err.Error())
			} else {
				worker.debugEvent("command.completed", operation.name)
			}
			snapshot, snapshotError := worker.engine.Snapshot()
			worker.post(func(application *applicationState) {
				application.busy = false
				application.notice = ""
				if apply != nil {
					apply(application)
				}
				if snapshotError == nil {
					application.applySnapshot(snapshot)
				}
				if err != nil {
					application.errorMessage = err.Error()
				}
			})
		case <-worker.controlChanged:
			if controls := worker.controls.Load(); controls != nil {
				if err := worker.engine.SetAudioControls(*controls); err != nil {
					worker.debugEvent("audio.controls-failed", err.Error())
				}
			}
		case err := <-worker.eventErrors:
			worker.debugEvent("frame.notification-failed", err.Error())
			worker.post(func(application *applicationState) {
				application.errorMessage = err.Error()
				application.running = false
				application.clearPreview()
			})
			return
		case <-worker.frameReady:
			frame, err := worker.engine.AcquireFrame(worker.afterSequence)
			if err != nil && err.Error() != lastFrameError {
				lastFrameError = err.Error()
				worker.debugEvent("frame.acquire-failed", lastFrameError)
				worker.post(func(application *applicationState) { application.errorMessage = err.Error() })
			}
			if frame != nil {
				worker.afterSequence = frame.SequenceNumber()
				worker.frames.replace(frame)
				worker.window.Invalidate()
				if window := worker.presentation.Load(); window != nil {
					window.Invalidate()
				}
			}
		case <-ticker.C:
			status, err := worker.engine.Snapshot()
			if err == nil && !status.VideoRunning {
				worker.frames.clear()
			}
			worker.post(func(application *applicationState) {
				if err != nil {
					application.errorMessage = err.Error()
					return
				}
				application.applySnapshot(status)
			})
		}
	}
}

func (worker *captureWorker) readFrameEvents() {
	defer close(worker.readerDone)
	var counter [8]byte
	for {
		if _, err := io.ReadFull(worker.frameEvents, counter[:]); err != nil {
			if !worker.closing.Load() {
				select {
				case worker.eventErrors <- fmt.Errorf("read frame notification: %w", err):
				default:
				}
			}
			return
		}
		select {
		case worker.frameReady <- struct{}{}:
		default:
		}
	}
}

func (worker *captureWorker) close() {
	worker.closeOnce.Do(func() { worker.closing.Store(true); close(worker.stop) })
	<-worker.done
	if worker.deviceWatcherDone != nil {
		<-worker.deviceWatcherDone
	}
}

func (application *applicationState) submit(notice string, run func(*captureWorker) (func(*applicationState), error)) {
	if application.worker == nil || application.busy {
		return
	}
	application.busy = true
	application.errorMessage = ""
	application.notice = notice
	application.debugEvent("command.queued", notice)
	select {
	case application.worker.commands <- command{name: notice, run: run}:
	default:
		application.busy = false
		application.errorMessage = "Another operation is still pending."
	}
}

func (worker *captureWorker) setAudioControls(controls session.AudioControls) {
	worker.controls.Store(&controls)
	select {
	case worker.controlChanged <- struct{}{}:
	default:
	}
}

func (application *applicationState) applySnapshot(status session.SessionSnapshot) {
	if status.CommandID < application.status.CommandID {
		return
	}
	previous := application.status
	if status.Error != "" && status.Error != previous.Error {
		application.debugEvent("session.error", status.Error)
	}
	if status.Audio.Error != "" && status.Audio.Error != previous.Audio.Error {
		application.debugEvent("audio.error", status.Audio.Error)
	}
	if status.Recording.Error != "" && status.Recording.Error != previous.Recording.Error {
		application.debugEvent("recording.error", status.Recording.Error)
	}
	if status.VideoPhase != previous.VideoPhase {
		application.debugEvent("video.phase", status.VideoPhase+" "+status.VideoBackend)
	}
	if status.AudioPhase != previous.AudioPhase {
		application.debugEvent("audio.phase", status.AudioPhase)
	}
	if status.Recording.Phase != previous.Recording.Phase {
		application.debugEvent("recording.phase", status.Recording.Phase)
	}
	application.status = status
	application.monitorAudio = status.MonitorEnabled
	if status.Error != "" {
		application.errorMessage = status.Error
	}
	if status.Audio.Error != "" {
		application.errorMessage = status.Audio.Error
	}
	if !status.VideoRunning && application.running {
		application.clearPreview()
	}
	application.running = status.VideoRunning
}

func statusSummary(status session.SessionSnapshot, contexts ...*ui.Context) string {
	if status.SchemaVersion == 0 {
		return localization.Text(messageLocale(contexts), "Ready")
	}
	summary := localization.Format(messageLocale(contexts), "Captured {frames, plural, one {# frame} other {# frames}} · skipped {dropped, plural, one {# frame} other {# frames}}", map[string]any{"frames": status.Frames, "dropped": status.DroppedFrames})
	if status.VideoBackend == "dmabuf" {
		return "DMA-BUF · " + summary
	}
	if status.VideoBackend == "cpu" {
		return "CPU · " + summary
	}
	return summary
}

func (worker *captureWorker) debugEvent(name, detail string) {
	if worker.diagnostics != nil {
		worker.diagnostics.Record(name, detail)
	}
}

// Device enumeration runs separately so PipeWire discovery cannot delay frame delivery.
func (worker *captureWorker) watchDevices() {
	defer close(worker.deviceWatcherDone)
	if worker.application.services.VideoDevices == nil || worker.application.services.AudioDevices == nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-worker.stop:
			return
		case <-ticker.C:
			devices, videoError := worker.application.services.VideoDevices()
			audio, audioError := worker.application.services.AudioDevices()
			worker.post(func(application *applicationState) {
				if application.busy || application.presetBusy || application.closing {
					return
				}
				videoChanged := videoError == nil && !reflect.DeepEqual(devices, application.devices)
				audioChanged := audioError == nil && !reflect.DeepEqual(audio, application.audioDevices)
				if videoChanged || audioChanged {
					application.refresh()
				}
			})
		}
	}
}
