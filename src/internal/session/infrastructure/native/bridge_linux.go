//go:build linux && cgo

package native

/*
#cgo CFLAGS: -I${SRCDIR}/../../../../abi
#cgo LDFLAGS: -L${SRCDIR}/../../../../../build -lmygoframe_core -Wl,-rpath,$ORIGIN
#include "mygoframe.h"
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"sync"
	"unsafe"

	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/application"
)

type engineState struct {
	mutex  sync.Mutex
	handle C.mgf_engine_handle
}

// Engine shares its native handle and control lock across value copies.
type Engine struct {
	state *engineState
}

func (engine *Engine) currentState() *engineState {
	if engine == nil {
		return nil
	}
	return engine.state
}

func New() (*Engine, error) {
	if version := C.mgf_api_version(); version != 2 {
		return nil, fmt.Errorf("unsupported session ABI: %d", version)
	}
	handle := C.mgf_engine_create()
	if handle == 0 {
		return nil, errors.New("could not create capture engine")
	}
	return &Engine{state: &engineState{handle: handle}}, nil
}

func (engine *Engine) Close() {
	state := engine.currentState()
	if state == nil {
		return
	}
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if state.handle != 0 {
		C.mgf_engine_destroy(state.handle)
		state.handle = 0
	}
}

func takeError(value *C.char) error {
	if value == nil {
		return nil
	}
	defer C.mgf_string_free(value)
	return errors.New(C.GoString(value))
}

func takeJSON(value *C.char, result any, preserveStatusError ...bool) error {
	if value == nil {
		return errors.New("native engine returned no JSON")
	}
	defer C.mgf_string_free(value)
	data := []byte(C.GoString(value))
	var failure struct {
		Error string `json:"error"`
	}
	if len(preserveStatusError) == 0 && json.Unmarshal(data, &failure) == nil && failure.Error != "" {
		return errors.New(failure.Error)
	}
	if err := json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("decode native response: %w", err)
	}
	return nil
}

func VideoDevices() ([]VideoDevice, error) {
	result := []VideoDevice{}
	err := takeJSON(C.mgf_video_devices_json(), &result)
	return result, err
}

func VideoModes(path string) ([]VideoMode, error) {
	value := C.CString(path)
	defer C.free(unsafe.Pointer(value))
	result := []VideoMode{}
	err := takeJSON(C.mgf_video_modes_json(value), &result)
	return result, err
}

func ListAudioDevices() (AudioDevices, error) {
	result := AudioDevices{Sources: []AudioDevice{}, Outputs: []AudioDevice{}}
	err := takeJSON(C.mgf_audio_devices_json(), &result)
	return result, err
}

func (engine *Engine) StartVideo(config VideoConfig) error {
	state := engine.currentState()
	if state == nil {
		return errors.New("capture engine is closed")
	}
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if state.handle == 0 {
		return errors.New("capture engine is closed")
	}
	path := C.CString(config.Path)
	defer C.free(unsafe.Pointer(path))
	mode := config.Mode
	backend := C.int32_t(0)
	switch config.Backend {
	case "", "auto":
	case "cpu":
		backend = 1
	case "dma", "dmabuf":
		backend = 2
	default:
		return fmt.Errorf("unsupported video backend: %s", config.Backend)
	}
	fallback := C.CString(config.FallbackReason)
	defer C.free(unsafe.Pointer(fallback))
	return takeError(C.mgf_start_video_backend(state.handle, path, C.uint32_t(mode.FourCC), C.uint32_t(mode.Width), C.uint32_t(mode.Height), C.uint32_t(mode.IntervalNumerator), C.uint32_t(mode.IntervalDenominator), C.int32_t(config.ColorRange), backend, fallback))
}

func (engine *Engine) StopVideo() {
	state := engine.currentState()
	if state == nil {
		return
	}
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if state.handle != 0 {
		C.mgf_stop_video(state.handle)
	}
}

func (engine *Engine) StartAudio(source, output string) error {
	state := engine.currentState()
	if state == nil {
		return errors.New("capture engine is closed")
	}
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if state.handle == 0 {
		return errors.New("capture engine is closed")
	}
	sourceValue, outputValue := C.CString(source), C.CString(output)
	defer C.free(unsafe.Pointer(sourceValue))
	defer C.free(unsafe.Pointer(outputValue))
	return takeError(C.mgf_start_audio(state.handle, sourceValue, outputValue))
}

func (engine *Engine) StopAudio() {
	state := engine.currentState()
	if state == nil {
		return
	}
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if state.handle != 0 {
		C.mgf_stop_audio(state.handle)
	}
}

func (engine *Engine) SetVolume(volume float64) {
	state := engine.currentState()
	if state == nil {
		return
	}
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if math.IsNaN(volume) || math.IsInf(volume, 0) {
		volume = 0
	}
	volume = max(0, min(1, volume))
	if state.handle != 0 {
		C.mgf_set_volume(state.handle, C.float(volume))
	}
}

func (engine *Engine) Status() (map[string]any, error) {
	state := engine.currentState()
	if state == nil {
		return nil, errors.New("capture engine is closed")
	}
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if state.handle == 0 {
		return nil, errors.New("capture engine is closed")
	}
	var result map[string]any
	err := takeJSON(C.mgf_status_json(state.handle), &result, true)
	return result, err
}

// Snapshot preserves integer counters without a JSON float64 round trip.
func (engine *Engine) Snapshot() (SessionSnapshot, error) {
	state := engine.currentState()
	if state == nil {
		return SessionSnapshot{}, errors.New("capture engine is closed")
	}
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if state.handle == 0 {
		return SessionSnapshot{}, errors.New("capture engine is closed")
	}
	var result SessionSnapshot
	if err := takeJSON(C.mgf_status_json(state.handle), &result, true); err != nil {
		return result, err
	}
	return result, result.Validate()
}

func (engine *Engine) AcquireFrame(afterSequence uint64) (capture.Frame, error) {
	state := engine.currentState()
	if state == nil {
		return nil, nil
	}
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if state.handle == 0 {
		return nil, nil
	}
	var dmaView C.mgfr_dma_frame_view
	dmaAvailable := C.mgf_acquire_dma_frame(state.handle, C.uint64_t(afterSequence), &dmaView)
	if dmaAvailable < 0 {
		return nil, errors.New("native DMA-BUF frame acquisition failed")
	}
	if dmaAvailable == 1 {
		token := dmaView.token
		width, height, stride := uint64(dmaView.width), uint64(dmaView.height), uint64(dmaView.stride)
		uvOffset, byteLength := uint64(dmaView.uv_offset), uint64(dmaView.byte_length)
		if dmaView.abi_version != 1 || uint64(dmaView.struct_size) < uint64(C.sizeof_mgfr_dma_frame_view) ||
			token == nil || dmaView.fd < 0 || dmaView.modifier != 0 ||
			!validDMALayout(uint32(dmaView.fourcc), width, height, stride, uvOffset, byteLength, uint32(dmaView.encoding)) ||
			(dmaView.color_range != 1 && dmaView.color_range != 2) ||
			(dmaView.transfer_function != 1 && dmaView.transfer_function != 2) {
			if token != nil {
				C.mgf_release_dma_frame(token)
			}
			return nil, errors.New("native engine returned an invalid DMA-BUF frame")
		}
		return newDMAFrame(token, int(width), int(height), uint64(dmaView.sequence), uint64(dmaView.timestamp_ns), func() { C.mgf_release_dma_frame(token) }), nil
	}
	var view C.mgf_frame_view
	available := C.mgf_acquire_frame(state.handle, C.uint64_t(afterSequence), &view)
	if available < 0 {
		return nil, errors.New("native frame acquisition failed")
	}
	if available == 0 {
		return nil, nil
	}
	token := view.token
	width, height, stride := uint64(view.width), uint64(view.height), uint64(view.stride)
	if token == nil || view.pixels == nil || width == 0 || height == 0 || stride != width*4 || stride*height > uint64(math.MaxInt) || uint64(view.byte_length) != stride*height {
		if token != nil {
			C.mgf_release_frame(token)
		}
		return nil, errors.New("native engine returned an invalid RGBA frame")
	}
	pixels := unsafe.Slice((*byte)(unsafe.Pointer(view.pixels)), int(stride*height))
	value := &image.RGBA{Pix: pixels, Stride: int(stride), Rect: image.Rect(0, 0, int(width), int(height))}
	return newFrame(value, uint64(view.sequence), uint64(view.timestamp_ns), func() { C.mgf_release_frame(token) }), nil
}
