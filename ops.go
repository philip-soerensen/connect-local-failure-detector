package main

import (
	"fmt"
	"math"
)

// exec runs one node against the environment and returns its outputs.
func (m *onnxModel) exec(env map[string]*tensor, n onnxNode) ([]*tensor, error) {
	in := make([]*tensor, len(n.inputs))
	for i, name := range n.inputs {
		t, ok := env[name]
		if !ok {
			return nil, fmt.Errorf("onnx: %s: input %q not found", n.op, name)
		}
		in[i] = t
	}

	switch n.op {
	case "Constant":
		return execConstant(n)
	case "Conv":
		return execConv(n, in)
	case "LeakyRelu":
		alpha := float32(0.01)
		if a, ok := n.attrs["alpha"]; ok {
			alpha = a.f
		}
		return []*tensor{unaryF(in[0], func(x float32) float32 {
			if x < 0 {
				return alpha * x
			}
			return x
		})}, nil
	case "Relu":
		return []*tensor{unaryF(in[0], func(x float32) float32 {
			if x < 0 {
				return 0
			}
			return x
		})}, nil
	case "ReduceMean":
		return execReduceMean(n, in)
	case "Flatten":
		return execFlatten(n, in[0])
	case "MaxPool":
		return execMaxPool(n, in[0])
	case "Reshape":
		return execReshape(in[0], in[1])
	case "Transpose":
		return execTranspose(n, in[0])
	case "Concat":
		return execConcat(n, in)
	case "Slice":
		return execSlice(n, in)
	case "Shape":
		return []*tensor{intTensor([]int64{int64(len(in[0].dims))}, in[0].dims)}, nil
	case "Gather":
		return execGather(n, in[0], in[1])
	case "Mul":
		return bin(n, in, func(x, y float32) float32 { return x * y }, func(x, y int64) int64 { return x * y })
	case "Add":
		return bin(n, in, func(x, y float32) float32 { return x + y }, func(x, y int64) int64 { return x + y })
	case "Sub":
		return bin(n, in, func(x, y float32) float32 { return x - y }, func(x, y int64) int64 { return x - y })
	case "Div":
		return bin(n, in, func(x, y float32) float32 { return x / y }, func(x, y int64) int64 { return x / y })
	case "Exp":
		return []*tensor{unaryF(in[0], func(x float32) float32 { return float32(math.Exp(float64(x))) })}, nil
	case "Sigmoid":
		return []*tensor{unaryF(in[0], func(x float32) float32 { return 1 / (1 + float32(math.Exp(-float64(x)))) })}, nil
	case "Unsqueeze":
		return execUnsqueeze(n, in)
	case "Cast":
		return execCast(n, in[0])
	default:
		return nil, fmt.Errorf("onnx: unsupported op %q", n.op)
	}
}

func unaryF(t *tensor, f func(float32) float32) *tensor {
	out := make([]float32, len(t.data))
	for i, x := range t.data {
		out[i] = f(x)
	}
	return floatTensor(t.dims, out)
}

// bin applies a broadcast binary op, keeping int64 tensors int64.
func bin(n onnxNode, in []*tensor, opF func(x, y float32) float32, opI func(x, y int64) int64) ([]*tensor, error) {
	a, b := in[0], in[1]
	if a.isInt() && b.isInt() {
		outDims, err := broadcastShape(a.dims, b.dims)
		if err != nil {
			return nil, err
		}
		total := int64(1)
		for _, d := range outDims {
			total *= d
		}
		out := make([]int64, total)
		for i := int64(0); i < total; i++ {
			out[i] = opI(a.ints[broadcastIndex(i, outDims, a.dims)], b.ints[broadcastIndex(i, outDims, b.dims)])
		}
		return []*tensor{intTensor(outDims, out)}, nil
	}
	return binF(a, b, opF)
}

func binF(a, b *tensor, op func(x, y float32) float32) ([]*tensor, error) {
	af, bf := a.asFloat(), b.asFloat()
	t, err := binaryOp(af, bf, op)
	if err != nil {
		return nil, err
	}
	return []*tensor{t}, nil
}

func execConstant(n onnxNode) ([]*tensor, error) {
	if a, ok := n.attrs["value"]; ok && a.t != nil {
		return []*tensor{a.t}, nil
	}
	if a, ok := n.attrs["value_float"]; ok {
		return []*tensor{floatTensor([]int64{}, []float32{a.f})}, nil
	}
	if a, ok := n.attrs["value_int"]; ok {
		return []*tensor{intTensor([]int64{}, []int64{a.i})}, nil
	}
	return nil, fmt.Errorf("onnx: Constant without a value attribute")
}

// execConv runs a 2D convolution: X [N,C,H,W], W [M,C/g,kH,kW], optional B
// [M], via im2col + matmul.
func execConv(n onnxNode, in []*tensor) ([]*tensor, error) {
	x, w := in[0], in[1]
	if len(x.dims) != 4 || len(w.dims) != 4 {
		return nil, fmt.Errorf("onnx: Conv: want 4D input and weight, got %v and %v", x.dims, w.dims)
	}
	N, C, H, W := x.dims[0], x.dims[1], x.dims[2], x.dims[3]
	M, cg, kH, kW := w.dims[0], w.dims[1], w.dims[2], w.dims[3]

	pads := n.attrs["pads"].ints
	if len(pads) == 0 {
		pads = []int64{0, 0, 0, 0}
	}
	pt, pl, pb, pr := pads[0], pads[1], pads[2], pads[3]
	strides := n.attrs["strides"].ints
	if len(strides) == 0 {
		strides = []int64{1, 1}
	}
	sh, sw := strides[0], strides[1]
	dil := n.attrs["dilations"].ints
	if len(dil) == 0 {
		dil = []int64{1, 1}
	}
	dh, dw := dil[0], dil[1]
	group := n.attrs["group"].i
	if group == 0 {
		group = 1
	}

	effKH := dh*(kH-1) + 1
	effKW := dw*(kW-1) + 1
	OH := (H+pt+pb-effKH)/sh + 1
	OW := (W+pl+pr-effKW)/sw + 1

	var bias []float32
	if len(in) > 2 && in[2] != nil {
		bias = in[2].data
	}

	out := make([]float32, N*M*OH*OW)
	K := C / group * kH * kW // im2col row length
	im2col := make([]float32, int(K*OH*OW))

	cpw := C / group // input channels per group
	mpg := M / group // output channels per group

	for nIdx := int64(0); nIdx < N; nIdx++ {
		xImg := x.data[nIdx*C*H*W:]
		outImg := out[nIdx*M*OH*OW:]
		for g := int64(0); g < group; g++ {
			for oh := int64(0); oh < OH; oh++ {
				for ow := int64(0); ow < OW; ow++ {
					col := oh*OW + ow
					row := int64(0)
					for c := int64(0); c < cpw; c++ {
						for kh := int64(0); kh < kH; kh++ {
							ih := oh*sh - pt + kh*dh
							for kw := int64(0); kw < kW; kw++ {
								iw := ow*sw - pl + kw*dw
								if ih >= 0 && ih < H && iw >= 0 && iw < W {
									im2col[row*int64(OH*OW)+col] = xImg[(g*cpw+c)*H*W+ih*W+iw]
								}
								row++
							}
						}
					}
				}
			}
			wBase := w.data[g*mpg*cg*kH*kW:]
			for m := int64(0); m < mpg; m++ {
				wm := wBase[m*cg*kH*kW:]
				oo := outImg[g*mpg*OH*OW+m*OH*OW:]
				for k := int64(0); k < K; k++ {
					wv := wm[k]
					if wv == 0 {
						continue
					}
					base := k * int64(OH*OW)
					for col := int64(0); col < int64(OH*OW); col++ {
						oo[col] += wv * im2col[base+col]
					}
				}
				if bias != nil {
					bv := bias[g*mpg+m]
					for col := int64(0); col < int64(OH*OW); col++ {
						oo[col] += bv
					}
				}
			}
		}
	}

	return []*tensor{floatTensor([]int64{N, M, OH, OW}, out)}, nil
}

// execMaxPool runs 2D max pooling with -inf padding and optional ceil mode.
func execMaxPool(n onnxNode, x *tensor) ([]*tensor, error) {
	if len(x.dims) != 4 {
		return nil, fmt.Errorf("onnx: MaxPool: want 4D input, got %v", x.dims)
	}
	N, C, H, W := x.dims[0], x.dims[1], x.dims[2], x.dims[3]
	ks := n.attrs["kernel_shape"].ints
	if len(ks) == 0 {
		return nil, fmt.Errorf("onnx: MaxPool without kernel_shape")
	}
	kH, kW := ks[0], ks[1]
	pads := n.attrs["pads"].ints
	if len(pads) == 0 {
		pads = []int64{0, 0, 0, 0}
	}
	pt, pl := pads[0], pads[1]
	strides := n.attrs["strides"].ints
	if len(strides) == 0 {
		strides = []int64{1, 1}
	}
	sh, sw := strides[0], strides[1]
	ceilMode := n.attrs["ceil_mode"].i == 1

	pool := func(in int64) int64 {
		v := (in + pads[0] + pads[2] - kH) / sh + 1
		if ceilMode {
			v = int64(math.Ceil(float64(in+pads[0]+pads[2]-kH)/float64(sh))) + 1
			if v > 0 && (v-1)*sh >= in+pads[0]+pads[2] {
				v--
			}
		}
		return v
	}
	OH, OW := pool(H), pool(W)
	if OH < 1 {
		OH = 1
	}
	if OW < 1 {
		OW = 1
	}

	out := make([]float32, N*C*OH*OW)
	negInf := float32(math.Inf(-1))
	for i := range out {
		out[i] = negInf
	}
	for nIdx := int64(0); nIdx < N; nIdx++ {
		for c := int64(0); c < C; c++ {
			base := (nIdx*C + c) * H * W
			oBase := (nIdx*C + c) * OH * OW
			for oh := int64(0); oh < OH; oh++ {
				for ow := int64(0); ow < OW; ow++ {
					m := negInf
					for kh := int64(0); kh < kH; kh++ {
						ih := oh*sh - pt + kh
						for kw := int64(0); kw < kW; kw++ {
							iw := ow*sw - pl + kw
							if ih >= 0 && ih < H && iw >= 0 && iw < W {
								if v := x.data[base+ih*W+iw]; v > m {
									m = v
								}
							}
						}
					}
					out[oBase+oh*OW+ow] = m
				}
			}
		}
	}
	return []*tensor{floatTensor([]int64{N, C, OH, OW}, out)}, nil
}

func execReshape(x, shape *tensor) ([]*tensor, error) {
	dims := make([]int64, len(shape.ints))
	copy(dims, shape.ints)
	infer := -1
	known := int64(1)
	for i, d := range dims {
		if d == 0 {
			if i < len(x.dims) {
				dims[i] = x.dims[i]
			} else {
				return nil, fmt.Errorf("onnx: Reshape: 0 dim out of range")
			}
		}
		if dims[i] == -1 {
			infer = i
		} else {
			known *= dims[i]
		}
	}
	if infer >= 0 {
		dims[infer] = x.size() / known
	}
	if x.isInt() {
		return []*tensor{intTensor(dims, x.ints)}, nil
	}
	return []*tensor{floatTensor(dims, x.data)}, nil
}

func execTranspose(n onnxNode, x *tensor) ([]*tensor, error) {
	perm := n.attrs["perm"].ints
	if len(perm) == 0 {
		perm = make([]int64, len(x.dims))
		for i := range perm {
			perm[i] = int64(len(perm) - 1 - i)
		}
	}
	outDims := make([]int64, len(perm))
	for i, p := range perm {
		outDims[i] = x.dims[p]
	}
	total := x.size()
	outF := make([]float32, total)
	outI := make([]int64, total)
	inStride := make([]int64, len(x.dims))
	inStride[len(x.dims)-1] = 1
	for i := len(x.dims) - 2; i >= 0; i-- {
		inStride[i] = inStride[i+1] * x.dims[i+1]
	}
	for off := int64(0); off < total; off++ {
		rest := off
		inOff := int64(0)
		for i := len(outDims) - 1; i >= 0; i-- {
			idx := rest % outDims[i]
			rest /= outDims[i]
			inOff += idx * inStride[perm[i]]
		}
		if x.isInt() {
			outI[off] = x.ints[inOff]
		} else {
			outF[off] = x.data[inOff]
		}
	}
	if x.isInt() {
		return []*tensor{intTensor(outDims, outI)}, nil
	}
	return []*tensor{floatTensor(outDims, outF)}, nil
}

func execConcat(n onnxNode, in []*tensor) ([]*tensor, error) {
	axis := n.attrs["axis"].i
	if len(in) == 0 {
		return nil, fmt.Errorf("onnx: Concat with no inputs")
	}
	dims := append([]int64{}, in[0].dims...)
	for _, t := range in[1:] {
		dims[axis] += t.dims[axis]
	}
	total := int64(1)
	for _, d := range dims {
		total *= d
	}
	outer := int64(1)
	for i := int64(0); i < axis; i++ {
		outer *= in[0].dims[i]
	}
	inner := int64(1)
	for i := axis + 1; i < int64(len(dims)); i++ {
		inner *= dims[i]
	}
	outAxisDim := dims[axis]
	if in[0].isInt() {
		out := make([]int64, total)
		var base int64
		for _, t := range in {
			adim := t.dims[axis]
			for o := int64(0); o < outer; o++ {
				for i := int64(0); i < adim; i++ {
					dst := (o*outAxisDim + base + i) * inner
					src := (o*adim + i) * inner
					copy(out[dst:dst+inner], t.ints[src:src+inner])
				}
			}
			base += adim
		}
		return []*tensor{intTensor(dims, out)}, nil
	}
	out := make([]float32, total)
	var base int64
	for _, t := range in {
		adim := t.dims[axis]
		for o := int64(0); o < outer; o++ {
			for i := int64(0); i < adim; i++ {
				dst := (o*outAxisDim + base + i) * inner
				src := (o*adim + i) * inner
				copy(out[dst:dst+inner], t.data[src:src+inner])
			}
		}
		base += adim
	}
	return []*tensor{floatTensor(dims, out)}, nil
}

func execSlice(n onnxNode, in []*tensor) ([]*tensor, error) {
	x := in[0]
	var starts, ends, axes, steps []int64
	if len(in) >= 3 { // opset >= 10: attributes are inputs
		starts, ends = in[1].ints, in[2].ints
		if len(in) > 3 {
			axes = in[3].ints
		}
		if len(in) > 4 {
			steps = in[4].ints
		}
	} else {
		starts, ends = n.attrs["starts"].ints, n.attrs["ends"].ints
		axes = n.attrs["axes"].ints
		steps = n.attrs["steps"].ints
	}
	if len(axes) == 0 {
		axes = make([]int64, len(starts))
		for i := range axes {
			axes[i] = int64(i)
		}
	}
	if len(steps) == 0 {
		steps = make([]int64, len(starts))
		for i := range steps {
			steps[i] = 1
		}
	}
	for i := range axes {
		if axes[i] < 0 {
			axes[i] += int64(len(x.dims))
		}
	}
	for i := range starts {
		if starts[i] < 0 {
			starts[i] += x.dims[axes[i]]
		}
		if ends[i] < 0 {
			ends[i] += x.dims[axes[i]]
		}
		if starts[i] < 0 {
			starts[i] = 0
		}
		if ends[i] > x.dims[axes[i]] {
			ends[i] = x.dims[axes[i]]
		}
	}
	outDims := append([]int64{}, x.dims...)
	for i, ax := range axes {
		outDims[ax] = (ends[i] - starts[i] + steps[i] - 1) / steps[i]
		if outDims[ax] < 0 {
			outDims[ax] = 0
		}
	}
	total := int64(1)
	for _, d := range outDims {
		total *= d
	}

	inStride := make([]int64, len(x.dims))
	inStride[len(x.dims)-1] = 1
	for i := len(x.dims) - 2; i >= 0; i-- {
		inStride[i] = inStride[i+1] * x.dims[i+1]
	}

	getF := func(off int64) float32 { return x.data[off] }
	getI := func(off int64) int64 { return x.ints[off] }
	if x.isInt() {
		out := make([]int64, total)
		for off := int64(0); off < total; off++ {
			out[off] = getI(sliceSrcOff(off, outDims, x.dims, axes, starts, steps, inStride))
		}
		return []*tensor{intTensor(outDims, out)}, nil
	}
	out := make([]float32, total)
	for off := int64(0); off < total; off++ {
		out[off] = getF(sliceSrcOff(off, outDims, x.dims, axes, starts, steps, inStride))
	}
	return []*tensor{floatTensor(outDims, out)}, nil
}

func sliceSrcOff(off int64, outDims, inDims []int64, axes, starts, steps, inStride []int64) int64 {
	rest := off
	inOff := int64(0)
	for i := len(outDims) - 1; i >= 0; i-- {
		idx := rest % outDims[i]
		rest /= outDims[i]
		inIdx := idx
		for j, ax := range axes {
			if ax == int64(i) {
				inIdx = starts[j] + idx*steps[j]
			}
		}
		inOff += inIdx * inStride[i]
	}
	return inOff
}

func execGather(n onnxNode, data, indices *tensor) ([]*tensor, error) {
	axis := n.attrs["axis"].i
	if axis < 0 {
		axis += int64(len(data.dims))
	}
	outer := int64(1)
	for i := int64(0); i < axis; i++ {
		outer *= data.dims[i]
	}
	inner := int64(1)
	for i := axis + 1; i < int64(len(data.dims)); i++ {
		inner *= data.dims[i]
	}
	axisDim := data.dims[axis]
	outDims := append([]int64{}, data.dims...)
	outDims[axis] = int64(len(indices.ints))
	total := int64(1)
	for _, d := range outDims {
		total *= d
	}
	if data.isInt() {
		out := make([]int64, total)
		for oi, iv := range indices.ints {
			if iv < 0 {
				iv += axisDim
			}
			for o := int64(0); o < outer; o++ {
				src := (o*axisDim + iv) * inner
				dst := (o*int64(len(indices.ints)) + int64(oi)) * inner
				copy(out[dst:dst+inner], data.ints[src:src+inner])
			}
		}
		return []*tensor{intTensor(outDims, out)}, nil
	}
	out := make([]float32, total)
	for oi, iv := range indices.ints {
		if iv < 0 {
			iv += axisDim
		}
		for o := int64(0); o < outer; o++ {
			src := (o*axisDim + iv) * inner
			dst := (o*int64(len(indices.ints)) + int64(oi)) * inner
			copy(out[dst:dst+inner], data.data[src:src+inner])
		}
	}
	return []*tensor{floatTensor(outDims, out)}, nil
}

func execUnsqueeze(n onnxNode, in []*tensor) ([]*tensor, error) {
	x := in[0]
	var axes []int64
	if len(in) > 1 { // opset >= 13
		axes = in[1].ints
	} else {
		axes = n.attrs["axes"].ints
	}
	rank := int64(len(x.dims)) + int64(len(axes))
	for i := range axes {
		if axes[i] < 0 {
			axes[i] += rank
		}
	}
	dims := make([]int64, rank)
	src := 0
	for i := int64(0); i < rank; i++ {
		isAxis := false
		for _, a := range axes {
			if a == i {
				isAxis = true
				break
			}
		}
		if isAxis {
			dims[i] = 1
		} else {
			dims[i] = x.dims[src]
			src++
		}
	}
	if x.isInt() {
		return []*tensor{intTensor(dims, x.ints)}, nil
	}
	return []*tensor{floatTensor(dims, x.data)}, nil
}

func execCast(n onnxNode, x *tensor) ([]*tensor, error) {
	to := n.attrs["to"].i
	switch to {
	case 1: // FLOAT
		if x.isInt() {
			out := make([]float32, len(x.ints))
			for i, v := range x.ints {
				out[i] = float32(v)
			}
			return []*tensor{floatTensor(x.dims, out)}, nil
		}
		return []*tensor{x}, nil
	case 7, 6: // INT64, INT32
		if !x.isInt() {
			out := make([]int64, len(x.data))
			for i, v := range x.data {
				out[i] = int64(v)
			}
			return []*tensor{intTensor(x.dims, out)}, nil
		}
		return []*tensor{x}, nil
	default:
		return nil, fmt.Errorf("onnx: Cast to type %d unsupported", to)
	}
}

// checkInput rejects a tensor whose element count does not match the model's
// declared input shape. Without this, a stale or mismatched model file makes
// execConv index out of range and panic the whole module.
func (m *onnxModel) checkInput(name string, t *tensor) error {
	if len(m.inputs) == 0 || name != m.inputs[0] || len(m.inputDim) == 0 {
		return nil
	}
	want := int64(1)
	for _, d := range m.inputDim {
		if d <= 0 {
			return nil // dynamic dim: cannot check
		}
		want *= d
	}
	n := int64(len(t.data))
	if t.isInt() {
		n = int64(len(t.ints))
	}
	if n != want {
		return fmt.Errorf("onnx: input %q has %d elements, model expects %d (shape %v)", name, n, want, m.inputDim)
	}
	return nil
}

// run executes the whole graph and returns the named outputs.
func (m *onnxModel) run(inputs map[string]*tensor) (map[string]*tensor, error) {
	env := map[string]*tensor{}
	for k, v := range m.inits {
		env[k] = v
	}
	for k, v := range inputs {
		if err := m.checkInput(k, v); err != nil {
			return nil, err
		}
		env[k] = v
	}
	for _, n := range m.nodes {
		outs, err := m.exec(env, n)
		if err != nil {
			return nil, fmt.Errorf("node %s (%v): %w", n.op, n.inputs, err)
		}
		for i, name := range n.outputs {
			if i < len(outs) && outs[i] != nil {
				env[name] = outs[i]
			}
		}
	}
	res := map[string]*tensor{}
	for _, name := range m.outputs {
		t, ok := env[name]
		if !ok {
			return nil, fmt.Errorf("onnx: output %q was not produced", name)
		}
		res[name] = t
	}
	return res, nil
}

// execReduceMean averages over the attr axes (default: all), keeping dims
// when keepdims is set (default 1).
func execReduceMean(n onnxNode, in []*tensor) ([]*tensor, error) {
	x := in[0]
	var axes []int64
	if len(in) > 1 && in[1] != nil && in[1].size() > 0 { // opset >= 13: axes is an input
		axes = in[1].ints
	} else if a, ok := n.attrs["axes"]; ok {
		axes = a.ints
	}
	if len(axes) == 0 { // no axes given: reduce over all
		axes = make([]int64, len(x.dims))
		for i := range x.dims {
			axes[i] = int64(i)
		}
	}
	keep := true
	if k, ok := n.attrs["keepdims"]; ok {
		keep = k.i != 0
	}
	norm := make([]bool, len(x.dims))
	for _, a := range axes {
		if a < 0 {
			a += int64(len(x.dims))
		}
		norm[a] = true
	}
	var outDims []int64
	for i, d := range x.dims {
		switch {
		case norm[i] && keep:
			outDims = append(outDims, 1)
		case !norm[i]:
			outDims = append(outDims, d)
		}
	}
	if len(outDims) == 0 {
		outDims = []int64{1}
	}
	out := make([]float32, prod64(outDims))
	counts := make([]float32, prod64(outDims))
	idx := make([]int64, len(x.dims))
	for {
		// out index: non-reduced dims in order; reduced dims contribute nothing
		var oi, om int64
		om = 1
		for i := len(x.dims) - 1; i >= 0; i-- {
			if !norm[i] {
				oi += idx[i] * om
				om *= x.dims[i]
			}
		}
		// flat index into x
		var flat, fm int64
		fm = 1
		for i := len(x.dims) - 1; i >= 0; i-- {
			flat += idx[i] * fm
			fm *= x.dims[i]
		}
		out[oi] += x.data[flat]
		counts[oi]++
		carry := true
		for i := len(x.dims) - 1; i >= 0 && carry; i-- {
			idx[i]++
			if idx[i] >= x.dims[i] {
				idx[i] = 0
			} else {
				carry = false
			}
		}
		if carry {
			break
		}
	}
	for i := range out {
		if counts[i] > 0 {
			out[i] /= counts[i]
		}
	}
	return []*tensor{floatTensor(outDims, out)}, nil
}

func prod64(dims []int64) int64 {
	p := int64(1)
	for _, d := range dims {
		p *= d
	}
	return p
}

// execFlatten reshapes to [prod(dims[:axis]), prod(dims[axis:])], axis default 1.
func execFlatten(n onnxNode, x *tensor) ([]*tensor, error) {
	axis := int64(1)
	if a, ok := n.attrs["axis"]; ok {
		axis = a.i
	}
	if axis < 0 {
		axis += int64(len(x.dims))
	}
	d0, d1 := int64(1), int64(1)
	for i := int64(0); i < axis; i++ {
		d0 *= x.dims[i]
	}
	for i := axis; i < int64(len(x.dims)); i++ {
		d1 *= x.dims[i]
	}
	dims := []int64{d0, d1}
	if x.isInt() {
		return []*tensor{intTensor(dims, x.ints)}, nil
	}
	return []*tensor{floatTensor(dims, x.data)}, nil
}
