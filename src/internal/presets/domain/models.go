package domain

import (
	audio "github.com/TomoriLab/MyGOFrame/src/internal/audio/domain"
	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/domain"
	recording "github.com/TomoriLab/MyGOFrame/src/internal/recording/domain"
)

type CapturePreset struct {
	Recording      *recording.RecordingConfiguration `json:"recording,omitempty"`
	MonitorEnabled *bool                             `json:"monitorEnabled,omitempty"`
	Name           string                            `json:"name"`
	DevicePath     string                            `json:"devicePath"`
	DeviceName     string                            `json:"deviceName"`
	Mode           capture.VideoMode                 `json:"mode"`
	ColorRange     string                            `json:"colorRange"`
	Backend        string                            `json:"backend"`
	AudioSource    string                            `json:"audioSource"`
	AudioOutput    string                            `json:"audioOutput"`
	Volume         float64                           `json:"volume"`
	Muted          bool                              `json:"muted"`
	CaptureWindow  bool                              `json:"captureWindow"`
}

type PresetRequest struct {
	Operation string         `json:"operation"`
	Name      string         `json:"name,omitempty"`
	Preset    *CapturePreset `json:"preset,omitempty"`
}

type PresetDocument struct {
	SchemaVersion int             `json:"schemaVersion"`
	Presets       []CapturePreset `json:"presets"`
	SelectedName  string          `json:"selectedName,omitempty"`
}

type PresetResolution struct {
	Preset       CapturePreset         `json:"preset"`
	Devices      []capture.VideoDevice `json:"devices"`
	AudioDevices audio.AudioDevices    `json:"audioDevices"`
	Modes        []capture.VideoMode   `json:"modes"`
	Mode         capture.VideoMode     `json:"mode"`
}
