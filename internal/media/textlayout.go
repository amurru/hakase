package media

// Placeholder for shaping layout - isolated optional implementation.
// v1 ships simpleLayout only; this file exists to satisfy spec structure
// and provides the interface boundary for future go-text/typesetting integration.

import (
	"image"
	"image/color"
)

// shapingLayout is the optional system-font + shaping implementation.
// Currently degrades to simpleLayout (fallback) - full shaping may land in v1.1.
type shapingLayout struct {
	fallback textLayout
}

func newShapingLayout(size float64) (textLayout, error) {
	fb, err := newSimpleLayout(size)
	if err != nil {
		return nil, err
	}
	return &shapingLayout{fallback: fb}, nil
}

func (s *shapingLayout) DrawString(img *image.RGBA, text string, x, y int, col color.Color) error {
	return s.fallback.DrawString(img, text, x, y, col)
}
