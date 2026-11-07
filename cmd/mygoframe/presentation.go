package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const captureWindowTitle = "MyGOFrame Capture"

type presentationWindow struct {
	window            *mygo.Window
	application       *applicationState
	fullscreen        bool
	displayedBitmap   bool
	displayedSequence uint64
	renderCount       uint64
}

func (application *applicationState) openPresentation() {
	if application.closing {
		return
	}
	if presentation := application.presentation; presentation != nil {
		presentation.window.Show()
		presentation.window.Focus()
		return
	}
	presentation := &presentationWindow{application: application}
	application.presentation = presentation
	// A separate top-level window remains visible when the controls are minimized.
	window := mygo.NewWindow(mygo.WindowOptions{
		Title: captureWindowTitle, Width: 960, Height: 540, UseContentSize: true,
		MinWidth: 320, MinHeight: 180, BackgroundColor: "#000000", Hidden: true,
		Content: ui.View(presentation.view),
	})
	presentation.window = window
	application.applyWindowIcon(window)
	if application.worker != nil {
		application.worker.presentation.Store(window)
	}
	window.OnClosed(func() {
		if application.worker != nil {
			application.worker.presentation.CompareAndSwap(window, nil)
		}
		if application.presentation == presentation {
			application.presentation = nil
		}
		if !application.closing && application.window != nil {
			application.window.Invalidate()
		}
	})
	window.OnEnterFullScreen(func() { presentation.fullscreen = true; window.Invalidate() })
	window.OnLeaveFullScreen(func() { presentation.fullscreen = false; window.Invalidate() })
	window.Show()
}

func (application *applicationState) closePresentation() {
	presentation := application.presentation
	if presentation == nil {
		return
	}
	application.presentation = nil
	if application.worker != nil {
		application.worker.presentation.CompareAndSwap(presentation.window, nil)
	}
	if presentation.window != nil {
		presentation.window.Close()
	}
}

func (application *applicationState) invalidatePresentation() {
	if presentation := application.presentation; presentation != nil && presentation.window != nil {
		presentation.window.Invalidate()
	}
}

func (presentation *presentationWindow) setFullscreen(value bool) {
	presentation.fullscreen = value
	if presentation.window != nil {
		presentation.window.SetFullScreen(value)
	}
}

func (presentation *presentationWindow) view(context *ui.Context) {
	application := presentation.application
	application.takeFrame()
	if context.Shortcut(0, ui.KeyF11) {
		presentation.setFullscreen(!presentation.fullscreen)
	}
	if context.Shortcut(0, ui.KeyEscape) && presentation.fullscreen {
		presentation.setFullscreen(false)
	}
	// Both native views use the same main-thread bitmap and its single pinned frame lease.
	ui.Box(context).Fill().MinWidth(0).MinHeight(0).Background(ui.RGB(0, 0, 0)).Clip().Label("Capture window viewport").Draw(func(painter *ui.Painter, bounds ui.Rect) {
		presentation.renderCount++
		presentation.displayedBitmap = false
		presentation.displayedSequence = 0
		if bitmap := application.bitmap; bitmap != nil {
			width, height := bitmap.Size()
			painter.ImageRegion(bitmap, ui.Rect{}, fitVideoRectangle(bounds, width, height), ui.ImageOptions{})
			presentation.displayedBitmap = true
			if frame := application.displayedFrame; frame != nil {
				presentation.displayedSequence = frame.Sequence
			}
		}
	})
}
