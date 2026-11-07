package main

import (
	"fmt"
	"slices"
	"sync"

	"github.com/TomoriLab/MyGOFrame/internal/core"
)

type modeSelection struct {
	modes      []core.VideoMode
	format     string
	resolution string
	frameRate  string
}

func unique(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func retainChoice(value string, options []string) string {
	if slices.Contains(options, value) {
		return value
	}
	if len(options) > 0 {
		return options[0]
	}
	return ""
}

func (selection *modeSelection) formats() []string {
	var values []string
	for _, mode := range selection.modes {
		values = append(values, mode.Format)
	}
	return unique(values)
}

func (selection *modeSelection) resolutions() []string {
	var values []string
	for _, mode := range selection.modes {
		if mode.Format == selection.format {
			values = append(values, mode.Resolution())
		}
	}
	return unique(values)
}

func (selection *modeSelection) frameRates() []string {
	var values []string
	for _, mode := range selection.modes {
		if mode.Format == selection.format && mode.Resolution() == selection.resolution {
			values = append(values, mode.FrameRate())
		}
	}
	return unique(values)
}

func (selection *modeSelection) reconcile() {
	selection.format = retainChoice(selection.format, selection.formats())
	selection.resolution = retainChoice(selection.resolution, selection.resolutions())
	selection.frameRate = retainChoice(selection.frameRate, selection.frameRates())
}

func (selection *modeSelection) setModes(modes []core.VideoMode) {
	selection.modes = modes
	selection.reconcile()
}

func (selection *modeSelection) selected() (core.VideoMode, bool) {
	for _, mode := range selection.modes {
		if mode.Format == selection.format && mode.Resolution() == selection.resolution && mode.FrameRate() == selection.frameRate {
			return mode, true
		}
	}
	return core.VideoMode{}, false
}

func videoLabel(device core.VideoDevice) string {
	return fmt.Sprintf("%s (%s)", device.Name, device.Path)
}
func audioLabel(device core.AudioDevice) string {
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
