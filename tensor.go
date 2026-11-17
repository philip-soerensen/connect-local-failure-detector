package main

import "fmt"

// tensor is a dense float32 or int64 tensor in NCHW-style row-major order.
type tensor struct {
	dims []int64
	data []float32 // set for float tensors
	ints []int64   // set for int64 tensors
}

func floatTensor(dims []int64, data []float32) *tensor {
	return &tensor{dims: dims, data: data}
}

func intTensor(dims []int64, data []int64) *tensor {
	return &tensor{dims: dims, ints: data}
}

func (t *tensor) size() int64 {
	var n int64 = 1
	for _, d := range t.dims {
		n *= d
	}
	return n
}

func (t *tensor) isInt() bool { return t.ints != nil }

// asFloat returns the tensor as float data, converting int64 when needed.
func (t *tensor) asFloat() *tensor {
	if !t.isInt() {
		return t
	}
	out := make([]float32, len(t.ints))
	for i, v := range t.ints {
		out[i] = float32(v)
	}
	return floatTensor(t.dims, out)
}

func (t *tensor) String() string { return fmt.Sprintf("tensor%v", t.dims) }

// broadcastShape computes the numpy-style broadcast of two shapes.
func broadcastShape(a, b []int64) ([]int64, error) {
	n := max(len(a), len(b))
	out := make([]int64, n)
	for i := 0; i < n; i++ {
		var da, db int64 = 1, 1
		if i >= n-len(a) {
			da = a[i-(n-len(a))]
		}
		if i >= n-len(b) {
			db = b[i-(n-len(b))]
		}
		switch {
		case da == db:
			out[i] = da
		case da == 1:
			out[i] = db
		case db == 1:
			out[i] = da
		default:
			return nil, fmt.Errorf("shapes %v and %v do not broadcast", a, b)
		}
	}
	return out, nil
}

// broadcastIndex maps an output offset to an input offset under broadcasting.
func broadcastIndex(outOff int64, outDims, inDims []int64) int64 {
	// Compute multi-index of outOff in outDims, then scale into inDims.
	idx := make([]int64, len(outDims))
	rest := outOff
	for i := len(outDims) - 1; i >= 0; i-- {
		idx[i] = rest % outDims[i]
		rest /= outDims[i]
	}
	var inOff int64
	for i := 0; i < len(outDims); i++ {
		inI := i - (len(outDims) - len(inDims))
		if inI < 0 {
			continue
		}
		d := inDims[inI]
		if d == 1 {
			continue // broadcast: index 0
		}
		inOff = inOff*inDims[inI] + idx[i]
	}
	return inOff
}

// binaryOp applies op elementwise with broadcasting. Both operands must be
// float tensors.
func binaryOp(a, b *tensor, op func(x, y float32) float32) (*tensor, error) {
	outDims, err := broadcastShape(a.dims, b.dims)
	if err != nil {
		return nil, err
	}
	n := int64(1)
	for _, d := range outDims {
		n *= d
	}
	out := make([]float32, n)
	for i := int64(0); i < n; i++ {
		out[i] = op(a.data[broadcastIndex(i, outDims, a.dims)], b.data[broadcastIndex(i, outDims, b.dims)])
	}
	return floatTensor(outDims, out), nil
}
