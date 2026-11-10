package bootstrap

import (
	"os"

	"github.com/TomoriLab/MyGOFrame/src/internal/about/infrastructure/notices"
	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/application"
	desktopapp "github.com/TomoriLab/MyGOFrame/src/internal/desktop/application"
	"github.com/TomoriLab/MyGOFrame/src/internal/diagnostics/infrastructure/memory"
	sessionapp "github.com/TomoriLab/MyGOFrame/src/internal/session/application"
	"github.com/TomoriLab/MyGOFrame/src/internal/session/infrastructure/native"
	version "github.com/TomoriLab/MyGOFrame/src/internal/version/domain"
)

func services() desktopapp.Services {
	return desktopapp.Services{
		NewDiagnostics:   func(override *bool) desktopapp.Diagnostics { return memory.New(override) },
		BuildInformation: version.Current(),
		MeasureLatency:   os.Getenv("MYGOFRAME_MEASURE_LATENCY") == "1",
		NewEngine:        func() (sessionapp.Engine, error) { return native.New() },
		VideoDevices:     native.VideoDevices, VideoModes: native.VideoModes, AudioDevices: native.ListAudioDevices,
		ResolveModes: native.ResolveModes, Presets: native.Presets, RecordingDevices: native.RecordingDevices,
		NewGPUImage: func() capture.GPUImage { return native.NewGPUImage() }, MarkGPUReady: native.MarkGPUReady,
		GPUTimings: native.GPUTimings, Notices: notices.Load,
	}
}
