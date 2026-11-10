package application

import (
	"os"

	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/application"
	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"
)

// Engine is the shared session-control port used by the GUI and diagnostic CLI.
type Engine interface {
	Close()
	StartVideo(session.VideoConfig) error
	StopVideo()
	StartAudio(string, string) error
	StopAudio()
	SetVolume(float64)
	Status() (map[string]any, error)
	Snapshot() (session.SessionSnapshot, error)
	AcquireFrame(uint64) (capture.Frame, error)
	FrameEvents() (*os.File, error)
	RefreshDevices(string, string, string) (session.DeviceCatalog, error)
	SetAudioControls(session.AudioControls) error
	ReportGPUFailure(uint64, string) error
	ApplyPreset(session.CapturePreset) (session.PresetResolution, error)
	ActivatePreset(session.CapturePreset, bool) (session.PresetResolution, error)
	StartRecording(session.RecordingConfiguration) error
	StopRecording() error
	SetMonitoring(bool) error
}
