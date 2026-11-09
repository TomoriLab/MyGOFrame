package core

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// SessionSnapshot is a value copy of Swift's versioned session state.
type SessionSnapshot struct {
	RequestedVideo *RequestedVideo `json:"requestedVideo,omitempty"`
	Muted          bool            `json:"muted"`
	EffectiveGain  float64         `json:"effectiveGain"`
	SchemaVersion  uint32          `json:"schemaVersion"`
	Generation     uint64          `json:"generation"`
	CommandID      uint64          `json:"commandID"`
	VideoPhase     string          `json:"videoPhase"`
	AudioPhase     string          `json:"audioPhase"`
	VideoRunning   bool            `json:"videoRunning"`
	Error          string          `json:"error"`
	Frames         uint64          `json:"frames"`
	DroppedFrames  uint64          `json:"droppedFrames"`
	Video          NegotiatedVideo `json:"video"`
	VideoBackend   string          `json:"videoBackend"`
	FallbackReason string          `json:"fallbackReason"`
	Audio          AudioSnapshot   `json:"audio"`
	Volume         float64         `json:"volume"`
}

type NegotiatedVideo struct {
	Path                string `json:"path"`
	Backend             string `json:"backend"`
	Format              string `json:"format"`
	FourCC              uint32 `json:"fourcc"`
	Width               uint32 `json:"width"`
	Height              uint32 `json:"height"`
	IntervalNumerator   uint32 `json:"intervalNumerator"`
	IntervalDenominator uint32 `json:"intervalDenominator"`
	ColorRange          int32  `json:"colorRange"`
	BytesPerLine        uint32 `json:"bytesPerLine"`
	Quantization        uint32 `json:"quantization"`
	Encoding            uint32 `json:"encoding"`
	Colorspace          uint32 `json:"colorspace"`
	TransferFunction    uint32 `json:"transferFunction"`
	CaptureBuffers      uint32 `json:"captureBuffers"`
}

type AudioSnapshot struct {
	Running                bool   `json:"running"`
	Source                 string `json:"source"`
	Output                 string `json:"output"`
	CaptureState           int32  `json:"captureState"`
	PlaybackState          int32  `json:"playbackState"`
	QueuedFrames           uint64 `json:"queuedFrames"`
	DroppedFrames          uint64 `json:"droppedFrames"`
	UnderrunFrames         uint64 `json:"underrunFrames"`
	CapturedFrames         uint64 `json:"capturedFrames"`
	PlayedFrames           uint64 `json:"playedFrames"`
	RequestedLatencyFrames uint32 `json:"requestedLatencyFrames"`
	SampleRate             uint32 `json:"sampleRate"`
	Error                  string `json:"error"`
}

func (snapshot SessionSnapshot) validate() error {
	if snapshot.SchemaVersion != 1 {
		return fmt.Errorf("unsupported session snapshot version: %d", snapshot.SchemaVersion)
	}
	return nil
}

// JSONMap supports the diagnostic CLI's extensible JSON output.
func (snapshot SessionSnapshot) JSONMap() map[string]any {
	data, _ := json.Marshal(snapshot)
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	_ = decoder.Decode(&result)
	return result
}

type RequestedVideo struct {
	Path                string `json:"path"`
	FourCC              uint32 `json:"fourcc"`
	Width               uint32 `json:"width"`
	Height              uint32 `json:"height"`
	IntervalNumerator   uint32 `json:"intervalNumerator"`
	IntervalDenominator uint32 `json:"intervalDenominator"`
	ColorRange          int32  `json:"colorRange"`
	Backend             int32  `json:"backend"`
	FallbackReason      string `json:"fallbackReason"`
}
