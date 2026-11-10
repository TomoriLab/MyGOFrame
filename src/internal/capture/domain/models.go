package domain

import (
	"encoding/binary"
	"fmt"
)

type VideoDevice struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type VideoMode struct {
	FourCC              uint32 `json:"fourcc"`
	Format              string `json:"format"`
	Width               uint32 `json:"width"`
	Height              uint32 `json:"height"`
	IntervalNumerator   uint32 `json:"intervalNumerator"`
	IntervalDenominator uint32 `json:"intervalDenominator"`
}

func (mode VideoMode) Resolution() string { return fmt.Sprintf("%d × %d", mode.Width, mode.Height) }

func (mode VideoMode) FrameRate() string {
	if mode.IntervalNumerator == 0 {
		return "Unknown"
	}
	return fmt.Sprintf("%.3f fps (%d/%d)", float64(mode.IntervalDenominator)/float64(mode.IntervalNumerator), mode.IntervalDenominator, mode.IntervalNumerator)
}

type VideoConfig struct {
	Path           string
	Mode           VideoMode
	ColorRange     int32
	Backend        string
	FallbackReason string
}

func FourCC(value string) (uint32, error) {
	if len(value) != 4 {
		return 0, fmt.Errorf("pixel format must contain four ASCII characters")
	}
	for _, character := range []byte(value) {
		if character < 32 || character > 126 {
			return 0, fmt.Errorf("pixel format must contain four ASCII characters")
		}
	}
	return binary.LittleEndian.Uint32([]byte(value)), nil
}

type ModeRequest struct {
	Modes               []VideoMode `json:"modes"`
	Format              string      `json:"format"`
	Width               uint32      `json:"width"`
	Height              uint32      `json:"height"`
	IntervalNumerator   uint32      `json:"intervalNumerator"`
	IntervalDenominator uint32      `json:"intervalDenominator"`
}

type ModeSelection struct {
	Formats     []string    `json:"formats"`
	Resolutions []VideoMode `json:"resolutions"`
	FrameRates  []VideoMode `json:"frameRates"`
	Selected    *VideoMode  `json:"selected"`
}
