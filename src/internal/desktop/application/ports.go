package application

import (
	about "github.com/TomoriLab/MyGOFrame/src/internal/about/domain"
	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/application"
	diagnostics "github.com/TomoriLab/MyGOFrame/src/internal/diagnostics/domain"
	sessionapp "github.com/TomoriLab/MyGOFrame/src/internal/session/application"
	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"
	version "github.com/TomoriLab/MyGOFrame/src/internal/version/domain"
)

// Services defines replaceable external operations consumed by desktop presentation.
// The executable composition root supplies concrete adapters once at startup.
type Services struct {
	Diagnostics      Diagnostics
	NewDiagnostics   func(*bool) Diagnostics
	BuildInformation version.Information
	MeasureLatency   bool
	NewEngine        func() (sessionapp.Engine, error)
	VideoDevices     func() ([]session.VideoDevice, error)
	VideoModes       func(string) ([]session.VideoMode, error)
	AudioDevices     func() (session.AudioDevices, error)
	ResolveModes     func(session.ModeRequest) (session.ModeSelection, error)
	Presets          func(session.PresetRequest) (session.PresetDocument, error)
	RecordingDevices func() ([]session.RecordingDevice, error)
	NewGPUImage      func() capture.GPUImage
	MarkGPUReady     func(uint64, uint64, uint64) bool
	GPUTimings       func() (map[string]any, error)
	Notices          func() (*about.Catalog, error)
}

// Diagnostics bounds diagnostic ownership independently of the media pipeline.
type Diagnostics interface {
	Enabled() bool
	SetEnabled(bool) error
	Record(string, string)
	State() diagnostics.State
	Snapshot() diagnostics.Snapshot
	Clear()
}
