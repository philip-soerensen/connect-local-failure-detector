package main

import (
	"image"
)

// preprocess converts an image to the model's input tensor: RGB, resized to
// the model's input size with bilinear interpolation, NCHW float32 in [0, 1].
func preprocess(img image.Image, w, h int) []float32 {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	out := make([]float32, 3*w*h)

	// Per-channel planes, filled row by row.
	for y := 0; y < h; y++ {
		sy := float64(y) * float64(sh) / float64(h)
		y0 := int(sy)
		if y0 >= sh-1 {
			y0 = sh - 2
		}
		fy := sy - float64(y0)
		if y0 < 0 {
			y0, fy = 0, 0
		}
		for x := 0; x < w; x++ {
			sx := float64(x) * float64(sw) / float64(w)
			x0 := int(sx)
			if x0 >= sw-1 {
				x0 = sw - 2
			}
			fx := sx - float64(x0)
			if x0 < 0 {
				x0, fx = 0, 0
			}

			// Bilinear sample of the four neighbours.
			var r, g, bl float64
				for _, c := range [][2]int{{y0, x0}, {y0, x0 + 1}, {y0 + 1, x0}, {y0 + 1, x0 + 1}} {
					weight := (1 - fy) * (1 - fx)
					if c[0] != y0 {
						weight = fy * (1 - fx)
					}
					if c[1] != x0 {
						if c[0] == y0 {
							weight = (1 - fy) * fx
						} else {
							weight = fy * fx
						}
					}
					pr, pg, pb, _ := img.At(b.Min.X+c[1], b.Min.Y+c[0]).RGBA()
					r += weight * float64(pr>>8)
					g += weight * float64(pg>>8)
					bl += weight * float64(pb>>8)
				}

				idx := y*w + x
				out[idx] = float32(r / 255.0)
				out[w*h+idx] = float32(g / 255.0)
				out[2*w*h+idx] = float32(bl / 255.0)
			}
		}
	return out
}
