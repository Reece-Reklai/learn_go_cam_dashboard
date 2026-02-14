package ui

import (
	"image"
	"image/color"
)

// =============================================================================
// Night Mode Filter
// =============================================================================
// Red-tinted, brightness-enhanced rendering for night driving.
// Matches Python's LUT-based grayscale-to-red conversion (1.6x brightness gain).
// Algorithm:
//   1. Convert pixel to grayscale luminance
//   2. Apply 1.6x brightness boost (clamped to 255)
//   3. Map result to red channel only (R = boosted, G = 0, B = 0)
// =============================================================================

// nightModeLUT is a pre-computed lookup table: grayscale value -> boosted value.
// Equivalent to Python's: np.clip(np.arange(256) * 1.6, 0, 255).astype(np.uint8)
var nightModeLUT [256]uint8

func init() {
	for i := 0; i < 256; i++ {
		v := float64(i) * 1.6
		if v > 255 {
			v = 255
		}
		nightModeLUT[i] = uint8(v)
	}
}

// applyNightMode converts an image to a red-tinted night vision image.
// It converts to grayscale, applies 1.6x brightness gain, and maps
// the result to the red channel only.
//
// If dst is non-nil and has sufficient capacity, it will be reused to avoid
// allocation. The caller can maintain a per-slot buffer for this purpose.
//
// This is optimized for the common case of *image.RGBA and *image.NRGBA
// but falls back to the generic color.Model interface for other types.
func applyNightMode(src image.Image) *image.RGBA {
	return applyNightModeReuse(src, nil)
}

// applyNightModeReuse is like applyNightMode but reuses dst if possible.
func applyNightModeReuse(src image.Image, dst *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	neededLen := w * h * 4

	// Reuse dst buffer if it has enough capacity
	if dst != nil && cap(dst.Pix) >= neededLen {
		dst.Pix = dst.Pix[:neededLen]
		dst.Stride = w * 4
		dst.Rect = image.Rect(0, 0, w, h)
	} else {
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
	}

	// Fast path for RGBA source
	if rgba, ok := src.(*image.RGBA); ok {
		applyNightModeRGBA(rgba, dst)
		return dst
	}

	// Fast path for NRGBA source
	if nrgba, ok := src.(*image.NRGBA); ok {
		applyNightModeNRGBA(nrgba, dst)
		return dst
	}

	// Generic fallback
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := src.At(x+bounds.Min.X, y+bounds.Min.Y).RGBA()
			// Convert to 8-bit
			r8 := uint8(r >> 8)
			g8 := uint8(g >> 8)
			b8 := uint8(b >> 8)
			// ITU-R BT.601 luminance
			gray := uint8((299*uint32(r8) + 587*uint32(g8) + 114*uint32(b8)) / 1000)
			boosted := nightModeLUT[gray]
			off := (y*dst.Stride + x*4)
			dst.Pix[off+0] = boosted // R
			dst.Pix[off+1] = 0       // G
			dst.Pix[off+2] = 0       // B
			dst.Pix[off+3] = 255     // A
		}
	}

	return dst
}

// applyNightModeRGBA is the fast path for *image.RGBA sources.
func applyNightModeRGBA(src *image.RGBA, dst *image.RGBA) {
	bounds := src.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	for y := 0; y < h; y++ {
		srcOff := (y+bounds.Min.Y-src.Rect.Min.Y)*src.Stride + (bounds.Min.X-src.Rect.Min.X)*4
		dstOff := y * dst.Stride

		for x := 0; x < w; x++ {
			r := src.Pix[srcOff+0]
			g := src.Pix[srcOff+1]
			b := src.Pix[srcOff+2]

			gray := uint8((299*uint32(r) + 587*uint32(g) + 114*uint32(b)) / 1000)
			boosted := nightModeLUT[gray]

			dst.Pix[dstOff+0] = boosted
			dst.Pix[dstOff+1] = 0
			dst.Pix[dstOff+2] = 0
			dst.Pix[dstOff+3] = 255

			srcOff += 4
			dstOff += 4
		}
	}
}

// applyNightModeNRGBA is the fast path for *image.NRGBA sources.
func applyNightModeNRGBA(src *image.NRGBA, dst *image.RGBA) {
	bounds := src.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	for y := 0; y < h; y++ {
		srcOff := (y+bounds.Min.Y-src.Rect.Min.Y)*src.Stride + (bounds.Min.X-src.Rect.Min.X)*4
		dstOff := y * dst.Stride

		for x := 0; x < w; x++ {
			r := src.Pix[srcOff+0]
			g := src.Pix[srcOff+1]
			b := src.Pix[srcOff+2]

			gray := uint8((299*uint32(r) + 587*uint32(g) + 114*uint32(b)) / 1000)
			boosted := nightModeLUT[gray]

			dst.Pix[dstOff+0] = boosted
			dst.Pix[dstOff+1] = 0
			dst.Pix[dstOff+2] = 0
			dst.Pix[dstOff+3] = 255

			srcOff += 4
			dstOff += 4
		}
	}
}

// nightModeColor returns the night-mode equivalent of a single color.
// Useful for UI elements that should respect night mode.
func nightModeColor(c color.Color) color.RGBA {
	r, g, b, _ := c.RGBA()
	r8 := uint8(r >> 8)
	g8 := uint8(g >> 8)
	b8 := uint8(b >> 8)
	gray := uint8((299*uint32(r8) + 587*uint32(g8) + 114*uint32(b8)) / 1000)
	boosted := nightModeLUT[gray]
	return color.RGBA{R: boosted, G: 0, B: 0, A: 255}
}
