package native

import (
	media "github.com/TomoriLab/MyGOFrame/src/internal/capture/application"
	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/domain"
	recording "github.com/TomoriLab/MyGOFrame/src/internal/recording/domain"
	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"
)

type VideoDevice = session.VideoDevice
type VideoMode = session.VideoMode
type VideoConfig = session.VideoConfig
type ModeRequest = session.ModeRequest
type ModeSelection = session.ModeSelection
type AudioDevice = session.AudioDevice
type AudioDevices = session.AudioDevices
type AudioControls = session.AudioControls
type AudioSnapshot = session.AudioSnapshot
type RecordingConfiguration = session.RecordingConfiguration
type RecordingDevice = session.RecordingDevice
type RecordingSnapshot = session.RecordingSnapshot
type CapturePreset = session.CapturePreset
type PresetRequest = session.PresetRequest
type PresetDocument = session.PresetDocument
type PresetResolution = session.PresetResolution
type SessionSnapshot = session.SessionSnapshot
type DeviceCatalog = session.DeviceCatalog
type GPURenderMetrics = media.GPURenderMetrics

var FourCC = capture.FourCC
var DefaultRecordingConfiguration = recording.DefaultRecordingConfiguration
