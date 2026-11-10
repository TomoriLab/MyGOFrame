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

func RecordingDevices() ([]RecordingDevice, error) {
	var result []RecordingDevice
	err := takeJSON(C.mgf_recording_devices_json(), &result)
	return result, err
}
func (engine *Engine) StartRecording(config RecordingConfiguration) error {
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return engine.withHandle(func(handle C.mgf_engine_handle) error {
		input := C.CString(string(data))
		defer C.free(unsafe.Pointer(input))
		return takeError(C.mgf_start_recording_json(handle, input))
	})
}
func (engine *Engine) StopRecording() error {
	return engine.withHandle(func(handle C.mgf_engine_handle) error { C.mgf_stop_recording(handle); return nil })
}
func (engine *Engine) SetMonitoring(enabled bool) error {
	return engine.withHandle(func(handle C.mgf_engine_handle) error {
		selected := C.int32_t(0)
		if enabled {
			selected = 1
		}
		return takeError(C.mgf_set_monitoring(handle, selected))
	})
}
