//go:build linux && cgo

package native

/*
#include "mygoframe.h"
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"
	"unsafe"
)

func Presets(request PresetRequest) (PresetDocument, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return PresetDocument{}, err
	}
	input := C.CString(string(data))
	defer C.free(unsafe.Pointer(input))
	var result PresetDocument
	err = takeJSON(C.mgf_presets_json(input), &result)
	return result, err
}

func (engine *Engine) ApplyPreset(preset CapturePreset) (PresetResolution, error) {
	data, err := json.Marshal(preset)
	if err != nil {
		return PresetResolution{}, err
	}
	var result PresetResolution
	err = engine.withHandle(func(handle C.mgf_engine_handle) error {
		input := C.CString(string(data))
		defer C.free(unsafe.Pointer(input))
		return takeJSON(C.mgf_apply_preset_json(handle, input), &result)
	})
	return result, err
}

func (engine *Engine) ActivatePreset(preset CapturePreset, startVideo bool) (PresetResolution, error) {
	data, err := json.Marshal(struct {
		Preset     CapturePreset `json:"preset"`
		StartVideo bool          `json:"startVideo"`
	}{preset, startVideo})
	if err != nil {
		return PresetResolution{}, err
	}
	var result PresetResolution
	err = engine.withHandle(func(handle C.mgf_engine_handle) error {
		input := C.CString(string(data))
		defer C.free(unsafe.Pointer(input))
		return takeJSON(C.mgf_activate_preset_json(handle, input), &result)
	})
	return result, err
}
