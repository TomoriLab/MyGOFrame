// Package memory retains bounded session diagnostics and persists only the enable preference.
package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	diagnostics "github.com/TomoriLab/MyGOFrame/src/internal/diagnostics/domain"
)

const Capacity = 256
const maximumDetailBytes = 4096

type preference struct {
	SchemaVersion int   `json:"schemaVersion"`
	DebugEnabled  *bool `json:"debugEnabled"`
}

type Recorder struct {
	enabled            atomic.Bool
	dropped            atomic.Uint64
	mutex              sync.Mutex
	events             [Capacity]diagnostics.Event
	start, count       int
	configurationMutex sync.Mutex
	configurationPath  string
	errorMessage       atomic.Pointer[string]
	initialError       error
}

func New(override *bool) *Recorder {
	directory, err := os.UserConfigDir()
	if err == nil && !filepath.IsAbs(directory) {
		err = errors.New("The configuration directory must be absolute.")
	}
	recorder := newRecorder(filepath.Join(directory, "mygoframe", "app-settings.json"), err)
	if override != nil {
		recorder.enabled.Store(*override)
	}
	return recorder
}

func newRecorder(path string, initialError error) *Recorder {
	recorder := &Recorder{configurationPath: path, initialError: initialError}
	recorder.setError(initialError)
	if initialError != nil {
		return recorder
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return recorder
	}
	if err == nil {
		var content []byte
		content, err = io.ReadAll(io.LimitReader(file, 8193))
		err = errors.Join(err, file.Close())
		if err == nil {
			var settings preference
			if len(content) > 8192 {
				err = errors.New("The app settings file exceeds its size limit.")
			} else {
				decoder := json.NewDecoder(bytes.NewReader(content))
				decoder.DisallowUnknownFields()
				err = decoder.Decode(&settings)
				if err == nil && decoder.Decode(&struct{}{}) != io.EOF {
					err = errors.New("The app settings file contains trailing data.")
				}
			}
			if err == nil && (settings.SchemaVersion != 1 || settings.DebugEnabled == nil) {
				err = errors.New("The app settings schema is not supported.")
			}
			if err == nil {
				recorder.enabled.Store(*settings.DebugEnabled)
			}
		}
	}
	recorder.initialError = err
	recorder.setError(err)
	return recorder
}

func (recorder *Recorder) Enabled() bool { return recorder.enabled.Load() }

func (recorder *Recorder) SetEnabled(enabled bool) error {
	recorder.configurationMutex.Lock()
	defer recorder.configurationMutex.Unlock()
	if recorder.initialError != nil {
		return recorder.initialError
	}
	if err := recorder.save(enabled); err != nil {
		recorder.setError(err)
		return err
	}
	recorder.setError(nil)
	if !enabled {
		recorder.Record("debug.disabled", "")
	}
	recorder.enabled.Store(enabled)
	if enabled {
		recorder.Record("debug.enabled", "")
	}
	return nil
}

func (recorder *Recorder) save(enabled bool) error {
	directory := filepath.Dir(recorder.configurationPath)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(preference{SchemaVersion: 1, DebugEnabled: &enabled}, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".app-settings-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeError := file.Write(append(data, '\n'))
	if writeError == nil {
		writeError = file.Sync()
	}
	if err := errors.Join(writeError, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), recorder.configurationPath)
}

// Record never waits for another diagnostic reader or performs filesystem operations.
func (recorder *Recorder) Record(name, detail string) {
	if !recorder.enabled.Load() {
		return
	}
	if !recorder.mutex.TryLock() {
		recorder.dropped.Add(1)
		return
	}
	defer recorder.mutex.Unlock()
	if !recorder.enabled.Load() {
		return
	}
	position := (recorder.start + recorder.count) % Capacity
	if recorder.count == Capacity {
		position = recorder.start
		recorder.start = (recorder.start + 1) % Capacity
		recorder.dropped.Add(1)
	} else {
		recorder.count++
	}
	recorder.events[position] = diagnostics.Event{Timestamp: time.Now().UTC(), Name: bounded(name, 128), Detail: bounded(detail, maximumDetailBytes), Truncated: len(name) > 128 || len(detail) > maximumDetailBytes}
}

func bounded(value string, maximum int) string {
	if len(value) > maximum {
		end := maximum
		for end > 0 && !utf8.RuneStart(value[end]) {
			end--
		}
		value = value[:end]
	}
	// Copying also prevents a short substring from pinning an arbitrarily large caller allocation.
	return strings.Clone(value)
}

func (recorder *Recorder) State() diagnostics.State {
	recorder.mutex.Lock()
	count := recorder.count
	recorder.mutex.Unlock()
	errorMessage := recorder.errorMessage.Load()
	state := diagnostics.State{Enabled: recorder.Enabled(), EventCount: count, Dropped: recorder.dropped.Load()}
	if errorMessage != nil {
		state.Error = *errorMessage
	}
	return state
}

func (recorder *Recorder) Snapshot() diagnostics.Snapshot {
	state := recorder.State()
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	events := make([]diagnostics.Event, recorder.count)
	for index := range events {
		events[index] = recorder.events[(recorder.start+index)%Capacity]
	}
	state.EventCount = len(events)
	return diagnostics.Snapshot{State: state, Events: events}
}

func (recorder *Recorder) Clear() {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	recorder.events = [Capacity]diagnostics.Event{}
	recorder.start, recorder.count = 0, 0
	recorder.dropped.Store(0)
}

func (recorder *Recorder) setError(err error) {
	if err == nil {
		recorder.errorMessage.Store(nil)
		return
	}
	message := fmt.Sprintf("App settings: %v", err)
	recorder.errorMessage.Store(&message)
}
