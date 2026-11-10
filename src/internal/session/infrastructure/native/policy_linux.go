//go:build linux && cgo

package native

/*
#include "mygoframe.h"
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"
	"errors"
	"os"
	"unsafe"
)

func ResolveModes(request ModeRequest) (ModeSelection, error) {
	if request.Modes == nil {
		request.Modes = []VideoMode{}
	}
	data, err := json.Marshal(request)
	if err != nil {
		return ModeSelection{}, err
	}
	input := C.CString(string(data))
	defer C.free(unsafe.Pointer(input))
	var selection ModeSelection
	err = takeJSON(C.mgf_resolve_modes_json(input), &selection)
	return selection, err
}

func (engine *Engine) withHandle(operation func(C.mgf_engine_handle) error) error {
	state := engine.currentState()
	if state == nil {
		return errors.New("capture engine is closed")
	}
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if state.handle == 0 {
		return errors.New("capture engine is closed")
	}
	return operation(state.handle)
}

func (engine *Engine) RefreshDevices(path, source, output string) (DeviceCatalog, error) {
	var result DeviceCatalog
	err := engine.withHandle(func(handle C.mgf_engine_handle) error {
		pathValue, sourceValue, outputValue := C.CString(path), C.CString(source), C.CString(output)
		defer C.free(unsafe.Pointer(pathValue))
		defer C.free(unsafe.Pointer(sourceValue))
		defer C.free(unsafe.Pointer(outputValue))
		if err := takeJSON(C.mgf_refresh_devices_json(handle, pathValue, sourceValue, outputValue), &result, true); err != nil {
			return err
		}
		if result.Error != "" {
			return errors.New(result.Error)
		}
		return nil
	})
	return result, err
}

func (engine *Engine) SetAudioControls(controls AudioControls) error {
	return engine.withHandle(func(handle C.mgf_engine_handle) error {
		muted := C.int32_t(0)
		if controls.Muted {
			muted = 1
		}
		C.mgf_set_audio_controls(handle, C.float(controls.Volume), muted)
		return nil
	})
}

func (engine *Engine) ReportGPUFailure(generation uint64, reason string) error {
	return engine.withHandle(func(handle C.mgf_engine_handle) error {
		value := C.CString(reason)
		defer C.free(unsafe.Pointer(value))
		return takeError(C.mgf_report_gpu_failure(handle, C.uint64_t(generation), value))
	})
}

// FrameEvents returns an owned, pollable descriptor that survives session restarts.
func (engine *Engine) FrameEvents() (*os.File, error) {
	var file *os.File
	err := engine.withHandle(func(handle C.mgf_engine_handle) error {
		descriptor := C.mgf_frame_events_fd(handle)
		if descriptor < 0 {
			return errors.New("unable to duplicate the frame notification descriptor")
		}
		file = os.NewFile(uintptr(descriptor), "mygoframe-frame-events")
		return nil
	})
	return file, err
}
