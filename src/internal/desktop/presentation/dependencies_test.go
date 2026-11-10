package presentation

import (
	"github.com/TomoriLab/MyGOFrame/src/internal/about/infrastructure/notices"
	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/application"
	desktopapp "github.com/TomoriLab/MyGOFrame/src/internal/desktop/application"
	sessionapp "github.com/TomoriLab/MyGOFrame/src/internal/session/application"
	"github.com/TomoriLab/MyGOFrame/src/internal/session/infrastructure/native"
)

func testServices() desktopapp.Services {
	return desktopapp.Services{
		NewEngine:    func() (sessionapp.Engine, error) { return native.New() },
		VideoDevices: native.VideoDevices, VideoModes: native.VideoModes, AudioDevices: native.ListAudioDevices,
		ResolveModes: native.ResolveModes, Presets: native.Presets, RecordingDevices: native.RecordingDevices,
		NewGPUImage: func() capture.GPUImage { return native.NewGPUImage() }, MarkGPUReady: native.MarkGPUReady,
		GPUTimings: native.GPUTimings, Notices: notices.Load,
	}
}
func newTestApplication() *applicationState { return newApplication(testServices()) }
