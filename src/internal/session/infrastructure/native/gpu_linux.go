//go:build linux && cgo

package native

/*
#cgo CFLAGS: -I${SRCDIR}/../../../../abi
#cgo LDFLAGS: -L${SRCDIR}/../../../../../build -lmygoframe_media -Wl,-rpath,$ORIGIN
#include "rust_core.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"unsafe"

	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/application"
)

type gpuOperations struct {
	create  func() (unsafe.Pointer, error)
	render  func(unsafe.Pointer, unsafe.Pointer) (uint32, GPURenderMetrics, error)
	destroy func(unsafe.Pointer)
	abandon func(unsafe.Pointer)
}

type gpuContext struct {
	handle       unsafe.Pointer
	lastSequence uint64
}

type gpuImageState struct {
	mutex      sync.Mutex
	frame      capture.Frame
	contexts   map[uint64]gpuContext
	operations gpuOperations
	closed     bool
	failed     bool
	failure    error
	metrics    []GPURenderMetrics
}

// GPUImage shares its context ownership and lock across value copies.
// MyGo calls Texture, Release, and Abandon according to its render-context rules.
type GPUImage struct {
	*gpuImageState
}

func takeGPUError(value *C.char) error {
	if value == nil {
		return nil
	}
	defer C.mgfr_string_free(value)
	return errors.New(C.GoString(value))
}

// NewGPUImage creates a presenter whose native objects are allocated on render contexts.
func NewGPUImage() *GPUImage {
	return &GPUImage{gpuImageState: &gpuImageState{
		contexts: make(map[uint64]gpuContext),
		operations: gpuOperations{
			create: func() (unsafe.Pointer, error) {
				if os.Getenv("MYGOFRAME_FORCE_GPU_FAILURE") != "" {
					return nil, errors.New("GPU initialization was rejected by the verification override")
				}
				var failure *C.char
				handle := C.mgfr_gpu_create(&failure)
				if err := takeGPUError(failure); err != nil {
					if handle != nil {
						C.mgfr_gpu_destroy(handle)
					}
					return nil, err
				}
				if handle == nil {
					return nil, errors.New("the GPU renderer did not create a context owner")
				}
				return handle, nil
			},
			render: func(handle, frame unsafe.Pointer) (uint32, GPURenderMetrics, error) {
				var texture C.uint32_t
				var metrics C.mgfr_gpu_metrics
				var failure *C.char
				result := C.mgfr_gpu_render(handle, frame, &texture, &metrics, &failure)
				if err := takeGPUError(failure); err != nil {
					return 0, GPURenderMetrics{}, err
				}
				if result != 1 || texture == 0 {
					return 0, GPURenderMetrics{}, errors.New("GPU frame conversion did not produce a texture")
				}
				return uint32(texture), GPURenderMetrics{
					Sequence: uint64(metrics.sequence), CaptureTimestampNS: uint64(metrics.capture_timestamp_ns),
					SubmitTimestampNS: uint64(metrics.submit_timestamp_ns), SubmissionCPUTimeNS: uint64(metrics.gpu_submit_cpu_ns),
					Width: uint32(metrics.width), Height: uint32(metrics.height),
				}, nil
			},
			destroy: func(handle unsafe.Pointer) { C.mgfr_gpu_destroy(handle) },
			abandon: func(handle unsafe.Pointer) { C.mgfr_gpu_abandon(handle) },
		},
	}}
}

// SetFrame borrows the displayed frame until it is replaced or the presenter is closed.
// The application replaces this borrow before releasing its previous frame owner.
func (presenter *GPUImage) SetFrame(frame capture.Frame) {
	if presenter == nil || presenter.gpuImageState == nil {
		return
	}

	presenter.mutex.Lock()
	defer presenter.mutex.Unlock()
	if !presenter.closed {
		presenter.frame = frame
	}
}

func (presenter *GPUImage) fail(err error) {
	presenter.failed = true
	if presenter.failure == nil {
		presenter.failure = err
	}
}

// Texture prepares a renderer-local RGBA texture while the caller's context is current.
func (presenter *GPUImage) Texture(contextID uint64) uint32 {
	if presenter == nil || presenter.gpuImageState == nil {
		return 0
	}

	presenter.mutex.Lock()
	defer presenter.mutex.Unlock()
	frame, valid := presenter.frame.(*Frame)
	if presenter.closed || presenter.failed || !valid || frame == nil || !frame.IsDMA() || frame.state == nil {
		return 0
	}
	frame.state.mutex.RLock()
	defer frame.state.mutex.RUnlock()
	if frame.state.dmaToken == nil {
		return 0
	}
	context, exists := presenter.contexts[contextID]
	if !exists {
		handle, err := presenter.operations.create()
		if err != nil {
			presenter.fail(err)
			return 0
		}
		context.handle = handle
		presenter.contexts[contextID] = context
	}
	texture, metrics, err := presenter.operations.render(context.handle, frame.state.dmaToken)
	if err != nil {
		presenter.fail(err)
		return 0
	}
	if metrics.Sequence != context.lastSequence {
		metrics.ContextID = contextID
		if len(presenter.metrics) < 7200 {
			presenter.metrics = append(presenter.metrics, metrics)
		}
		context.lastSequence = metrics.Sequence
		presenter.contexts[contextID] = context
	}
	return texture
}

// Release destroys native resources only when MyGo has made the owning context current.
func (presenter *GPUImage) Release(contextID uint64) {
	if presenter == nil || presenter.gpuImageState == nil {
		return
	}

	presenter.mutex.Lock()
	defer presenter.mutex.Unlock()
	if context, exists := presenter.contexts[contextID]; exists {
		delete(presenter.contexts, contextID)
		presenter.operations.destroy(context.handle)
	}
}

// Abandon releases ownership after context loss without issuing invalid GL calls.
func (presenter *GPUImage) Abandon(contextID uint64) {
	if presenter == nil || presenter.gpuImageState == nil {
		return
	}

	presenter.mutex.Lock()
	defer presenter.mutex.Unlock()
	if context, exists := presenter.contexts[contextID]; exists {
		delete(presenter.contexts, contextID)
		presenter.operations.abandon(context.handle)
	}
}

// Close stops new frame access; renderer callbacks retire context-owned objects later.
func (presenter *GPUImage) Close() {
	if presenter == nil || presenter.gpuImageState == nil {
		return
	}

	presenter.mutex.Lock()
	defer presenter.mutex.Unlock()
	presenter.closed = true
	presenter.frame = nil
}

func (presenter *GPUImage) TakeError() error {
	if presenter == nil || presenter.gpuImageState == nil {
		return nil
	}

	presenter.mutex.Lock()
	defer presenter.mutex.Unlock()
	failure := presenter.failure
	presenter.failure = nil
	return failure
}

func (presenter *GPUImage) Metrics() []GPURenderMetrics {
	if presenter == nil || presenter.gpuImageState == nil {
		return nil
	}

	presenter.mutex.Lock()
	defer presenter.mutex.Unlock()
	return append([]GPURenderMetrics(nil), presenter.metrics...)
}

// Prepared reports whether a renderer has completed at least one texture preparation.
func (presenter *GPUImage) Prepared() bool {
	if presenter == nil || presenter.gpuImageState == nil {
		return false
	}

	presenter.mutex.Lock()
	defer presenter.mutex.Unlock()
	return len(presenter.metrics) != 0
}

// MarkGPUReady inserts the same completion marker after CPU uploads and DMA conversion.
func MarkGPUReady(captureTimestampNS, sequence, contextID uint64) bool {
	return C.mgfr_gpu_timing_mark(C.uint64_t(captureTimestampNS), C.uint64_t(sequence), C.uint64_t(contextID)) == 1
}

func GPUTimings() (map[string]any, error) {
	value := C.mgfr_gpu_timings_json()
	if value == nil {
		return nil, errors.New("GPU timing collector returned no result")
	}
	defer C.mgfr_string_free(value)
	var result map[string]any
	if err := json.Unmarshal([]byte(C.GoString(value)), &result); err != nil {
		return nil, err
	}
	return result, nil
}
