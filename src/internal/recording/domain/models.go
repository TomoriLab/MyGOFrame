package domain

type RecordingConfiguration struct {
	GPUID       string  `json:"gpuID"`
	Codec       string  `json:"codec"`
	BitrateMbps float64 `json:"bitrateMbps"`
	RateControl string  `json:"rateControl"`
	Path        string  `json:"path"`
	RecordAudio bool    `json:"recordAudio"`
}

func DefaultRecordingConfiguration() RecordingConfiguration {
	return RecordingConfiguration{GPUID: "auto", Codec: "h264", BitrateMbps: 40, RateControl: "vbr", RecordAudio: true}
}

type RecordingDevice struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Backend string   `json:"backend"`
	PCI     string   `json:"pci"`
	Codecs  []string `json:"codecs"`
	Error   string   `json:"error"`
}

type RecordingSnapshot struct {
	Phase               string `json:"phase"`
	Frames              uint64 `json:"frames"`
	DroppedFrames       uint64 `json:"droppedFrames"`
	Bytes               uint64 `json:"bytes"`
	DurationNanoseconds uint64 `json:"durationNanoseconds"`
	Error               string `json:"error"`
	Path                string `json:"path,omitempty"`
	GPU                 string `json:"gpu,omitempty"`
	Codec               string `json:"codec,omitempty"`
	Decoder             string `json:"decoder,omitempty"`
	Transfer            string `json:"transfer,omitempty"`
}

func (snapshot RecordingSnapshot) Active() bool {
	return snapshot.Phase == "starting" || snapshot.Phase == "recording" || snapshot.Phase == "stopping"
}
