package presentation

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/application"
	sessionapp "github.com/TomoriLab/MyGOFrame/src/internal/session/application"
	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"

	"github.com/egoist/mygo"
)

type command struct {
	run func(*captureWorker) (func(*applicationState), error)
}

type captureWorker struct {
	engine         sessionapp.Engine
	window         *mygo.Window
	presentation   atomic.Pointer[mygo.Window]
	application    *applicationState
	commands       chan command
	stop           chan struct{}
	done           chan struct{}
	closing        atomic.Bool
	closeOnce      sync.Once
	controls       atomic.Pointer[session.AudioControls]
	controlChanged chan struct{}
	frames         latestSlot[capture.Frame]
	afterSequence  uint64
	frameEvents    *os.File
	frameReady     chan struct{}
	eventErrors    chan error
	readerDone     chan struct{}
}

func newWorker(engine sessionapp.Engine, window *mygo.Window, application *applicationState) (*captureWorker, error) {
	events, err := engine.FrameEvents()
	if err != nil {
		return nil, err
	}
	worker := &captureWorker{engine: engine, window: window, application: application, commands: make(chan command, 16), stop: make(chan struct{}), done: make(chan struct{}), controlChanged: make(chan struct{}, 1), frameEvents: events, frameReady: make(chan struct{}, 1), eventErrors: make(chan error, 1), readerDone: make(chan struct{})}
	worker.setAudioControls(session.AudioControls{Volume: application.volume / 100, Muted: application.muted})
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
			apply, err := operation.run(worker)
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
				_ = worker.engine.SetAudioControls(*controls)
			}
		case err := <-worker.eventErrors:
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
}

func (application *applicationState) submit(notice string, run func(*captureWorker) (func(*applicationState), error)) {
	if application.worker == nil || application.busy {
		return
	}
	application.busy = true
	application.errorMessage = ""
	application.notice = notice
	select {
	case application.worker.commands <- command{run: run}:
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

func statusSummary(status session.SessionSnapshot) string {
	if status.SchemaVersion == 0 {
		return "Ready"
	}
	summary := fmt.Sprintf("Captured %d frames · skipped %d frames", status.Frames, status.DroppedFrames)
	if status.VideoBackend == "dmabuf" {
		return "DMA-BUF · " + summary
	}
	if status.VideoBackend == "cpu" {
		return "CPU · " + summary
	}
	return summary
}
