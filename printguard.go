package main

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
)

// PrintGuard model support (github.com/oliverbravery/PrintGuard, GPL-2.0).
// The release ships an ONNX encoder (ShuffleNetV2, input 1x3x224x224, output
// a 1024-d embedding) plus a prototypes.json with one embedding per class
// ("success", "failure"). The failure probability is the softmax over
// squared prototype distances, written as
//
//	score = 0.5 * (1 + tanh((dSuccess^2 - dFailure^2) / 2))
//
// which is what the reference implementation computes (printguard/engine/
// vision.py, defect_score). A frame is scored square by square along its
// long side (vision.squares + scheduler.infer_squares: the frame is as risky
// as its worst square), each square preprocessed per vision.preprocess: scale
// the edge to 256 px bilinear, convert to luminance, center-crop 224x224,
// normalise each channel of the grey value with the ImageNet mean/std.

const (
	pgInputSize  = 224
	pgResizeEdge = 256
	pgLongRatio  = 4
	pgOverlap    = 0.2
)

var (
	pgMean = [3]float32{0.485, 0.456, 0.406}
	pgStd  = [3]float32{0.229, 0.224, 0.225}
)

// loadPrototypes reads prototypes.json: {"prototypes": {"success": [...], ...}}.
func loadPrototypes(path string) (map[string][]float32, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Prototypes map[string][]float64 `json:"prototypes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("prototypes: %w", err)
	}
	out := map[string][]float32{}
	for cls, vec := range doc.Prototypes {
		v := make([]float32, len(vec))
		for i, x := range vec {
			v[i] = float32(x)
		}
		out[cls] = v
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("prototypes: no classes in %s", path)
	}
	return out, nil
}

// pgSquareStarts mirrors vision.square_starts, generalised to a configurable
// tile side and overlap: the offsets of the tiles scored along the frame's
// long side. tile <= 0 or tile > short falls back to the short side, which is
// upstream's behaviour.
func pgSquareStarts(long, tile int, overlap float64) []int {
	if overlap <= 0 {
		overlap = pgOverlap
	}
	span := long
	if tile*pgLongRatio < span {
		span = tile * pgLongRatio
	}
	ratio := float64(span) / float64(tile)
	if ratio <= 1+overlap {
		return []int{(long - tile) / 2}
	}
	count := int(math.Ceil((ratio - overlap) / (1 - overlap)))
	margin := (long - span) / 2
	step := float64(span-tile) / float64(count-1)
	starts := make([]int, count)
	for i := range starts {
		starts[i] = margin + int(math.Round(float64(i)*step))
	}
	return starts
}

// pgLuminance is Pillow's RGB->L conversion (ITU-R 601-2).
func pgLuminance(r8, g8, b8 uint32) float64 {
	return float64(299*r8+587*g8+114*b8) / 1000.0
}

// preprocessPGSquare builds the encoder input for one square of the frame,
// mirroring vision.preprocess: the square (side x side at offset offX/offY)
// is downscaled to a 256 px edge with Pillow's convention — triangle filter
// whose support widens with the downscale factor, pixel-centre mapping —
// then converted to luminance, center-cropped to 224x224 and normalised per
// channel with the ImageNet mean/std. The wide kernel matters: a plain
// 4-neighbour bilinear downscale shifts the defect score by an order of
// magnitude on fine spaghetti detail.
func preprocessPGSquare(img image.Image, offX, offY, side int) []float32 {
	const crop = (pgResizeEdge - pgInputSize) / 2 // 16
	out := make([]float32, 3*pgInputSize*pgInputSize)
	b := img.Bounds()
	k := float64(side) / float64(pgResizeEdge)
	if k < 1 {
		k = 1
	}
	for y := 0; y < pgInputSize; y++ {
		cy := (float64(crop+y)+0.5)*float64(side)/float64(pgResizeEdge) - 0.5
		yMin := int(math.Floor(cy - k))
		yMax := int(math.Ceil(cy + k))
		if yMin < 0 {
			yMin = 0
		}
		if yMax > side-1 {
			yMax = side - 1
		}
		for x := 0; x < pgInputSize; x++ {
			cx := (float64(crop+x)+0.5)*float64(side)/float64(pgResizeEdge) - 0.5
			xMin := int(math.Floor(cx - k))
			xMax := int(math.Ceil(cx + k))
			if xMin < 0 {
				xMin = 0
			}
			if xMax > side-1 {
				xMax = side - 1
			}
			var rsum, gsum, bsum, wsum float64
			for py := yMin; py <= yMax; py++ {
				wy := 1 - math.Abs(float64(py)-cy)/k
				if wy <= 0 {
					continue
				}
				for px := xMin; px <= xMax; px++ {
					wx := 1 - math.Abs(float64(px)-cx)/k
					if wx <= 0 {
						continue
					}
					w := wy * wx
					pr, pg, pb, _ := img.At(b.Min.X+offX+px, b.Min.Y+offY+py).RGBA()
					rsum += w * float64(pr>>8)
					gsum += w * float64(pg>>8)
					bsum += w * float64(pb>>8)
					wsum += w
				}
			}
			clamp := func(v float64) uint32 {
				r := math.Round(v / wsum)
				if r < 0 {
					return 0
				}
				if r > 255 {
					return 255
				}
				return uint32(r)
			}
			g := pgLuminance(clamp(rsum), clamp(gsum), clamp(bsum)) / 255.0
			dst := y*pgInputSize + x
			out[dst] = float32((g - float64(pgMean[0])) / float64(pgStd[0]))
			out[pgInputSize*pgInputSize+dst] = float32((g - float64(pgMean[1])) / float64(pgStd[1]))
			out[2*pgInputSize*pgInputSize+dst] = float32((g - float64(pgMean[2])) / float64(pgStd[2]))
		}
	}
	return out
}

// pgScore maps an embedding to the failure probability in [0, 1].
func pgScore(emb []float32, protos map[string][]float32) (float64, error) {
	success, okS := protos["success"]
	failure, okF := protos["failure"]
	if !okS || !okF {
		return 0, fmt.Errorf("prototypes missing success/failure classes")
	}
	if len(emb) != len(success) || len(emb) != len(failure) {
		return 0, fmt.Errorf("embedding dim %d, prototype dim %d", len(emb), len(success))
	}
	var ds, df float64
	for i := range emb {
		d := float64(emb[i] - success[i])
		ds += d * d
		d = float64(emb[i] - failure[i])
		df += d * d
	}
	// euclidean distances squared are ds/df; score = softmax(-d^2) form:
	score := 0.5 * (1.0 + math.Tanh((ds-df)/2))
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return 0.5, nil
	}
	return score, nil
}
