// Package vectorize turns raster images (PNG, JPEG) and SVG into editable vector geometry
// without any model: thresholding, noise removal, contour tracing with holes, Bezier fitting and
// centerline extraction. It has no kernel dependency; tools turn its result into path and sketch
// nodes.
package vectorize

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // registered decoders
	_ "image/png"
	"math"
)

// Ink is a grayscale field where larger means "more likely foreground" (0..255).
type Ink struct {
	W, H int
	Pix  []uint8
	// Alpha reports that the signal came from transparency (a logo on a transparent background).
	Alpha bool
}

// Decode reads a PNG or JPEG.
func Decode(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("cannot read the image (PNG and JPEG are supported): %w", err)
	}
	b := img.Bounds()
	if b.Dx() < 2 || b.Dy() < 2 {
		return nil, fmt.Errorf("image is too small (%dx%d)", b.Dx(), b.Dy())
	}
	return img, nil
}

// Source selects what the foreground signal is made of.
const (
	SourceAuto      = "auto"
	SourceLuminance = "luminance"
	SourceAlpha     = "alpha"
)

// Extract builds the ink field: dark pixels on a light background by default; with a real
// alpha channel (a transparent background) the opaque pixels. invert swaps dark and light.
func Extract(ctx context.Context, img image.Image, source string, invert bool) (*Ink, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	ink := &Ink{W: w, H: h, Pix: make([]uint8, w*h)}
	// does the image carry transparency?
	transparent := false
	if source != SourceLuminance {
		stride := 1 + (w*h)/200000
		n := 0
		for y := b.Min.Y; y < b.Max.Y && !transparent; y++ {
			for x := b.Min.X; x < b.Max.X; x += stride {
				if _, _, _, a := img.At(x, y).RGBA(); a < 0xf000 {
					if n++; n > 8 {
						transparent = true
						break
					}
				}
			}
		}
	}
	useAlpha := source == SourceAlpha || (source != SourceLuminance && transparent)
	ink.Alpha = useAlpha
	for y := 0; y < h; y++ {
		if y&63 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for x := 0; x < w; x++ {
			c := img.At(b.Min.X+x, b.Min.Y+y)
			var v float64
			if useAlpha {
				_, _, _, a := c.RGBA()
				v = float64(a >> 8)
			} else {
				// composite over white, then Rec. 601 luma: dark = foreground
				r, g, bl, a := c.RGBA()
				al := float64(a) / 0xffff
				lum := 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
				lum = lum*al + 255*(1-al)
				v = 255 - lum
			}
			if invert {
				v = 255 - v
			}
			ink.Pix[y*w+x] = uint8(math.Round(math.Min(255, math.Max(0, v))))
		}
	}
	return ink, nil
}

// FromGray wraps raw gray values (0 = background .. 255 = foreground), for tests and callers
// that already have a field.
func FromGray(w, h int, pix []uint8) *Ink { return &Ink{W: w, H: h, Pix: pix} }

// GrayImage renders an Ink back to an image (debugging and tests).
func (k *Ink) GrayImage() *image.Gray {
	g := image.NewGray(image.Rect(0, 0, k.W, k.H))
	for i, v := range k.Pix {
		g.Pix[i] = 255 - v
	}
	return g
}

var _ color.Color
