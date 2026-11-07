package main

import (
	"image"
	"image/color"
	"image/draw"
)

// Colored corners and a continuous white border expose cropping during native display checks.
func previewPattern(width, height int) *image.RGBA {
	result := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(result, result.Bounds(), image.NewUniform(color.RGBA{80, 80, 80, 255}), image.Point{}, draw.Src)
	markerWidth, markerHeight := width/8, height/8
	for _, marker := range []struct {
		area  image.Rectangle
		color color.RGBA
	}{
		{image.Rect(0, 0, markerWidth, markerHeight), color.RGBA{255, 0, 0, 255}},
		{image.Rect(width-markerWidth, 0, width, markerHeight), color.RGBA{0, 255, 0, 255}},
		{image.Rect(0, height-markerHeight, markerWidth, height), color.RGBA{0, 0, 255, 255}},
		{image.Rect(width-markerWidth, height-markerHeight, width, height), color.RGBA{255, 255, 0, 255}},
	} {
		draw.Draw(result, marker.area, image.NewUniform(marker.color), image.Point{}, draw.Src)
	}
	border := max(1, min(width, height)/40)
	for _, area := range []image.Rectangle{
		image.Rect(0, 0, width, border), image.Rect(0, height-border, width, height),
		image.Rect(0, 0, border, height), image.Rect(width-border, 0, width, height),
	} {
		draw.Draw(result, area, image.NewUniform(color.RGBA{255, 255, 255, 255}), image.Point{}, draw.Src)
	}
	return result
}
