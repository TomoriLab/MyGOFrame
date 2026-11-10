package branding

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"math"

	"golang.org/x/image/draw"
)

const CornerRadiusRatio = 0.2

func RoundedPNG(source []byte, size int) ([]byte, error) {
	if size <= 0 {
		return nil, fmt.Errorf("icon size must be positive")
	}
	artwork, err := png.Decode(bytes.NewReader(source))
	if err != nil {
		return nil, fmt.Errorf("decode icon artwork: %w", err)
	}
	icon := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(icon, icon.Bounds(), artwork, artwork.Bounds(), draw.Src, nil)
	radius := float64(size) * CornerRadiusRatio
	for y := range size {
		for x := range size {
			distanceX := math.Max(0, math.Max(radius-float64(x)-0.5, float64(x)+0.5-(float64(size)-radius)))
			distanceY := math.Max(0, math.Max(radius-float64(y)-0.5, float64(y)+0.5-(float64(size)-radius)))
			coverage := math.Max(0, math.Min(1, radius+0.5-math.Hypot(distanceX, distanceY)))
			if coverage == 1 {
				continue
			}
			// RGBA stores premultiplied colors, so the smooth mask must scale every channel.
			offset := icon.PixOffset(x, y)
			for channel := range 4 {
				icon.Pix[offset+channel] = uint8(math.Round(float64(icon.Pix[offset+channel]) * coverage))
			}
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, icon); err != nil {
		return nil, fmt.Errorf("encode rounded icon: %w", err)
	}
	return encoded.Bytes(), nil
}
