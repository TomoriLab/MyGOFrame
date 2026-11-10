package presentation

import (
	"fmt"
	"sync"

	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"
)

// modeSelection projects Swift's numeric selection into native control labels.
type modeSelection struct {
	resolve    func(session.ModeRequest) (session.ModeSelection, error)
	modes      []session.VideoMode
	format     string
	resolution string
	frameRate  string
	resolved   session.ModeSelection
	failure    error
}

func (selection *modeSelection) formats() []string { return selection.resolved.Formats }
func (selection *modeSelection) resolutions() []string {
	result := make([]string, 0, len(selection.resolved.Resolutions))
	for _, mode := range selection.resolved.Resolutions {
		result = append(result, mode.Resolution())
	}
	return result
}
func (selection *modeSelection) frameRates() []string {
	result := make([]string, 0, len(selection.resolved.FrameRates))
	for _, mode := range selection.resolved.FrameRates {
		result = append(result, mode.FrameRate())
	}
	return result
}
func (selection *modeSelection) reconcile() {
	request := session.ModeRequest{Modes: selection.modes, Format: selection.format}
	for _, mode := range selection.modes {
		if mode.Resolution() == selection.resolution {
			request.Width, request.Height = mode.Width, mode.Height
		}
		if mode.FrameRate() == selection.frameRate {
			request.IntervalNumerator, request.IntervalDenominator = mode.IntervalNumerator, mode.IntervalDenominator
		}
	}
	selection.resolved, selection.failure = selection.resolve(request)
	selection.format, selection.resolution, selection.frameRate = "", "", ""
	if mode := selection.resolved.Selected; mode != nil {
		selection.format, selection.resolution, selection.frameRate = mode.Format, mode.Resolution(), mode.FrameRate()
	}
}
func (selection *modeSelection) setModes(modes []session.VideoMode) {
	selection.modes = modes
	selection.reconcile()
}
func (selection *modeSelection) selected() (session.VideoMode, bool) {
	if selection.failure != nil {
		return session.VideoMode{}, false
	}
	// Native selectors update the label before their change handler runs.
	for _, mode := range selection.resolved.FrameRates {
		if mode.FrameRate() == selection.frameRate {
			return mode, true
		}
	}
	return session.VideoMode{}, false
}

func videoLabel(device session.VideoDevice) string {
	return fmt.Sprintf("%s (%s)", device.Name, device.Path)
}
func audioLabel(device session.AudioDevice) string {
	return fmt.Sprintf("%s (%s)", device.Name, device.ID)
}

// latestSlot bounds retained frames even when the window stops drawing.
type latestSlot[Value interface{ Release() }] struct {
	mutex   sync.Mutex
	value   Value
	present bool
}

func (slot *latestSlot[Value]) replace(value Value) {
	slot.mutex.Lock()
	previous, present := slot.value, slot.present
	slot.value, slot.present = value, true
	slot.mutex.Unlock()
	if present {
		previous.Release()
	}
}

func (slot *latestSlot[Value]) take() (Value, bool) {
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	value, present := slot.value, slot.present
	var empty Value
	slot.value, slot.present = empty, false
	return value, present
}

func (slot *latestSlot[Value]) clear() {
	if value, present := slot.take(); present {
		value.Release()
	}
}
