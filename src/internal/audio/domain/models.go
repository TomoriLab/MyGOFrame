package domain

type AudioDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type AudioDevices struct {
	Sources []AudioDevice `json:"sources"`
	Outputs []AudioDevice `json:"outputs"`
}

type AudioControls struct {
	Volume float64
	Muted  bool
}

type AudioSnapshot struct {
	InputPeak              float64 `json:"inputPeak"`
	Running                bool    `json:"running"`
	Source                 string  `json:"source"`
	Output                 string  `json:"output"`
	CaptureState           int32   `json:"captureState"`
	PlaybackState          int32   `json:"playbackState"`
	QueuedFrames           uint64  `json:"queuedFrames"`
	DroppedFrames          uint64  `json:"droppedFrames"`
	UnderrunFrames         uint64  `json:"underrunFrames"`
	CapturedFrames         uint64  `json:"capturedFrames"`
	PlayedFrames           uint64  `json:"playedFrames"`
	RequestedLatencyFrames uint32  `json:"requestedLatencyFrames"`
	SampleRate             uint32  `json:"sampleRate"`
	Error                  string  `json:"error"`
}
