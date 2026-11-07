package main

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TomoriLab/MyGOFrame/internal/core"
	"github.com/egoist/mygo"
)

type command struct {
	run func(*captureWorker) (func(*applicationState), error)
}

type captureWorker struct {
	engine        *core.Engine
	window        *mygo.Window
	presentation  atomic.Pointer[mygo.Window]
	application   *applicationState
	commands      chan command
	stop          chan struct{}
	done          chan struct{}
	closing       atomic.Bool
	closeOnce     sync.Once
	volume        atomic.Uint64
	frames        latestSlot[*core.Frame]
	afterSequence uint64
}

func newWorker(engine *core.Engine, window *mygo.Window, application *applicationState) *captureWorker {
	worker := &captureWorker{engine: engine, window: window, application: application, commands: make(chan command, 16), stop: make(chan struct{}), done: make(chan struct{})}
	worker.volume.Store(math.Float64bits(application.volume / 100))
	go worker.run()
	return worker
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
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	lastStatus := time.Time{}
	lastVolume := math.Inf(-1)
	lastFrameError := ""
	for {
		select {
		case <-worker.stop:
			return
		case operation := <-worker.commands:
			apply, err := operation.run(worker)
			worker.post(func(application *applicationState) {
				application.busy = false
				application.notice = ""
				if apply != nil {
					apply(application)
				}
				if err != nil {
					application.errorMessage = err.Error()
				}
			})
		case now := <-ticker.C:
			volume := math.Float64frombits(worker.volume.Load())
			if volume != lastVolume {
				worker.engine.SetVolume(volume)
				lastVolume = volume
			}
			frame, err := worker.engine.AcquireFrame(worker.afterSequence)
			if err != nil && err.Error() != lastFrameError {
				lastFrameError = err.Error()
				worker.post(func(application *applicationState) { application.errorMessage = err.Error() })
			}
			if frame != nil {
				worker.afterSequence = frame.Sequence
				worker.frames.replace(frame)
				worker.window.Invalidate()
				if window := worker.presentation.Load(); window != nil {
					window.Invalidate()
				}
			}
			if now.Sub(lastStatus) >= 200*time.Millisecond {
				lastStatus = now
				status, err := worker.engine.Status()
				if running, ok := status["videoRunning"].(bool); ok && !running {
					worker.frames.clear()
				}
				worker.post(func(application *applicationState) {
					if err != nil {
						application.errorMessage = err.Error()
						return
					}
					application.status = status
					if message, _ := status["error"].(string); message != "" {
						application.errorMessage = message
					}
					if audio, ok := status["audio"].(map[string]any); ok {
						if message, _ := audio["error"].(string); message != "" {
							application.errorMessage = message
						}
					}
					if running, ok := status["videoRunning"].(bool); ok {
						if !running && application.running {
							application.clearPreview()
						}
						application.running = running
					}
				})
			}
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

func statusSummary(status map[string]any) string {
	if len(status) == 0 {
		return "Ready"
	}
	frames, _ := status["frames"].(float64)
	dropped, _ := status["droppedFrames"].(float64)
	return fmt.Sprintf("Captured %.0f frames · skipped %.0f frames", frames, dropped)
}
