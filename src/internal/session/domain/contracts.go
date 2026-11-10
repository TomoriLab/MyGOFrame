package domain

import (
	audio "github.com/TomoriLab/MyGOFrame/src/internal/audio/domain"
	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/domain"
	presets "github.com/TomoriLab/MyGOFrame/src/internal/presets/domain"
	recording "github.com/TomoriLab/MyGOFrame/src/internal/recording/domain"
)

type VideoDevice = capture.VideoDevice
type VideoMode = capture.VideoMode
type VideoConfig = capture.VideoConfig
type ModeRequest = capture.ModeRequest
type ModeSelection = capture.ModeSelection
type AudioDevice = audio.AudioDevice
type AudioDevices = audio.AudioDevices
type AudioControls = audio.AudioControls
type AudioSnapshot = audio.AudioSnapshot
type RecordingConfiguration = recording.RecordingConfiguration
type RecordingDevice = recording.RecordingDevice
type RecordingSnapshot = recording.RecordingSnapshot
type CapturePreset = presets.CapturePreset
type PresetRequest = presets.PresetRequest
type PresetDocument = presets.PresetDocument
type PresetResolution = presets.PresetResolution
