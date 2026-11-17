package main

import (
	"fmt"
	"image"
	"sort"
)

// Detection is one positive detection: the class name, the confidence, and
// the bounding box as centre-x, centre-y, width, height in pixels of the
// original image — the same shape Obico's /p/ endpoint returns.
type Detection struct {
	Class string  `json:"class"`
	Conf  float64 `json:"conf"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
}

// detector runs the ONNX model on preprocessed frames. pg selects the
// PrintGuard classifier path (embedding + prototypes) instead of the Obico
// boxes/confs path.
type detector struct {
	model      *onnxModel
	pg         bool
	prototypes map[string][]float32
	tileSize   int     // printguard tile side in source px; 0 = frame short side (upstream)
	overlap    float64 // minimum tile overlap fraction
}

// detect preprocesses img to the model input, runs the graph and
// post-processes the boxes/confs outputs with a confidence threshold and NMS.
// It returns the surviving detections and the highest raw confidence seen
// (before thresholding), which feeds the smoothed failure probability.
func (d *detector) detect(img image.Image, thresh, nms float64) ([]Detection, float64, error) {
	m := d.model
	if len(m.inputDim) != 4 || m.inputDim[0] != 1 || m.inputDim[1] != 3 {
		return nil, 0, fmt.Errorf("detect: unexpected model input shape %v", m.inputDim)
	}
	mh, mw := m.inputDim[2], m.inputDim[3]

	if d.pg {
		// Score the frame square by square along its long side and keep the
		// worst square's score (scheduler.infer_squares: max over squares).
		b := img.Bounds()
		iw, ih := b.Dx(), b.Dy()
		short := iw
		if ih < short {
			short = ih
		}
		long := iw
		horizontal := true
		if ih > iw {
			long, horizontal = ih, false
		}
		tile := d.tileSize
		if tile <= 0 || tile > short {
			tile = short // upstream default: squares as wide as the frame is tall
		}
		overlap := d.overlap
		if overlap <= 0 {
			overlap = pgOverlap
		}
		maxScore := 0.0
		shortOff := (short - tile) / 2 // center smaller tiles on the short axis; 0 when tile == short
		for _, s := range pgSquareStarts(long, tile, overlap) {
			offX, offY := shortOff, shortOff
			if horizontal {
				offX = s
			} else {
				offY = s
			}
			input := floatTensor([]int64{1, 3, mh, mw}, preprocessPGSquare(img, offX, offY, tile))
			outs, err := m.run(map[string]*tensor{m.inputs[0]: input})
			if err != nil {
				return nil, 0, err
			}
			embT := outs[m.outputs[0]]
			if embT == nil {
				return nil, 0, fmt.Errorf("detect: no output %q (have %v)", m.outputs[0], m.outputs)
			}
			score, err := pgScore(embT.data, d.prototypes)
			if err != nil {
				return nil, 0, err
			}
			if score > maxScore {
				maxScore = score
			}
		}
		var dets []Detection
		if maxScore >= thresh {
			dets = []Detection{{Class: "failure", Conf: maxScore}}
		}
		return dets, maxScore, nil
	}

	input := floatTensor([]int64{1, 3, mh, mw}, preprocess(img, int(mw), int(mh)))
	outs, err := m.run(map[string]*tensor{m.inputs[0]: input})
	if err != nil {
		return nil, 0, err
	}

	boxes, okB := outs["boxes"]
	confs, okC := outs["confs"]
	if !okB || !okC {
		// fall back to positional outputs
		if len(m.outputs) >= 2 {
			boxes, confs = outs[m.outputs[0]], outs[m.outputs[1]]
		} else {
			return nil, 0, fmt.Errorf("detect: model has no boxes/confs outputs (%v)", m.outputs)
		}
	}

	// boxes [1, N, 1, 4] normalized x1,y1,x2,y2; confs [1, N, C]
	n := int(boxes.size() / 4)
	nCls := 1
	if len(confs.dims) >= 3 {
		nCls = int(confs.dims[2])
	}

	bw, bh := img.Bounds().Dx(), img.Bounds().Dy()

	maxConf := 0.0
	type cand struct {
		box  []float32
		conf float64
		cls  int
	}
	var all []cand
	for i := 0; i < n; i++ {
		conf := float64(confs.data[i*nCls])
		if conf > maxConf {
			maxConf = conf
		}
		if conf <= thresh {
			continue
		}
		b := boxes.data[i*4 : i*4+4]
		all = append(all, cand{box: b, conf: conf, cls: 0})
	}

	// NMS per class (the reference model has a single class).
	sort.SliceStable(all, func(i, j int) bool { return all[i].conf > all[j].conf })
	var keep []cand
	for len(all) > 0 {
		best := all[0]
		keep = append(keep, best)
		rest := all[1:]
		var filtered []cand
		for _, c := range rest {
			if c.cls != best.cls || iou(c.box, best.box) <= nms {
				filtered = append(filtered, c)
			}
		}
		all = filtered
	}

	dets := make([]Detection, 0, len(keep))
	for _, c := range keep {
		b := c.box
		dets = append(dets, Detection{
			Class: "failure",
			Conf:  c.conf,
			X:     0.5 * float64(bw) * float64(b[0]+b[2]),
			Y:     0.5 * float64(bh) * float64(b[1]+b[3]),
			W:     float64(bw) * float64(b[2]-b[0]),
			H:     float64(bh) * float64(b[3]-b[1]),
		})
	}
	return dets, maxConf, nil
}

// iou computes intersection-over-union of two normalized x1y1x2y2 boxes.
func iou(a, b []float32) float64 {
	x1 := maxF(float64(a[0]), float64(b[0]))
	y1 := maxF(float64(a[1]), float64(b[1]))
	x2 := minF(float64(a[2]), float64(b[2]))
	y2 := minF(float64(a[3]), float64(b[3]))
	w := maxF(0, x2-x1)
	h := maxF(0, y2-y1)
	inter := w * h
	areaA := maxF(0, float64(a[2]-a[0])) * maxF(0, float64(a[3]-a[1]))
	areaB := maxF(0, float64(b[2]-b[0])) * maxF(0, float64(b[3]-b[1]))
	if areaA+areaB-inter <= 0 {
		return 0
	}
	return inter / (areaA + areaB - inter)
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
