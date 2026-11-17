package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// This file is a minimal, dependency-free reader for ONNX ModelProto files.
// It parses only the fields the executor needs: the graph's nodes,
// initializers, inputs and outputs. Protobuf wire format is walked by hand
// so the module stays pure Go (marketplace releases build with CGO_ENABLED=0
// and no third-party modules).

// onnxModel is a parsed model graph.
type onnxModel struct {
	nodes    []onnxNode
	inits    map[string]*tensor
	inputs   []string
	outputs  []string
	inputDim []int64
}

type onnxNode struct {
	op      string
	inputs  []string
	outputs []string
	attrs   map[string]onnxAttr
}

type onnxAttr struct {
	f      float32
	i      int64
	s      []byte
	t      *tensor
	floats []float32
	ints   []int64
	typ    int64
}

// pbReader walks protobuf wire fields.
type pbReader struct {
	b   []byte
	pos int
}

func (r *pbReader) done() bool { return r.pos >= len(r.b) }

// field reads the next tag. It returns the field number and wire type, and
// advances past the tag. ok is false at end of buffer.
func (r *pbReader) field() (num int, wt int, ok bool) {
	if r.done() {
		return 0, 0, false
	}
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 {
		return 0, 0, false
	}
	r.pos += n
	return int(v >> 3), int(v & 7), true
}

func (r *pbReader) varint() uint64 {
	v, n := binary.Uvarint(r.b[r.pos:])
	if n > 0 {
		r.pos += n
	}
	return v
}

func (r *pbReader) fixed32() uint32 {
	v := binary.LittleEndian.Uint32(r.b[r.pos:])
	r.pos += 4
	return v
}

func (r *pbReader) fixed64() uint64 {
	v := binary.LittleEndian.Uint64(r.b[r.pos:])
	r.pos += 8
	return v
}

// bytes reads a length-delimited field.
func (r *pbReader) bytes() []byte {
	l := int(r.varint())
	out := r.b[r.pos : r.pos+l]
	r.pos += l
	return out
}

// skip advances past a field of the given wire type.
func (r *pbReader) skip(wt int) {
	switch wt {
	case 0:
		r.varint()
	case 1:
		r.pos += 8
	case 2:
		l := int(r.varint())
		r.pos += l
	case 5:
		r.pos += 4
	}
}

// loadONNX reads and parses a .onnx file.
func loadONNX(path string) (*onnxModel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := &onnxModel{inits: map[string]*tensor{}}
	r := &pbReader{b: raw}
	for num, wt, ok := r.field(); ok; num, wt, ok = r.field() {
		if num == 7 && wt == 2 { // graph
			if err := m.parseGraph(r.bytes()); err != nil {
				return nil, err
			}
			continue
		}
		r.skip(wt)
	}
	if len(m.nodes) == 0 {
		return nil, fmt.Errorf("onnx: %s: no nodes", path)
	}
	return m, nil
}

func (m *onnxModel) inputShape() string {
	return fmt.Sprintf("%v", m.inputDim)
}

func (m *onnxModel) parseGraph(b []byte) error {
	r := &pbReader{b: b}
	for num, wt, ok := r.field(); ok; num, wt, ok = r.field() {
		switch {
		case num == 1 && wt == 2: // node
			n, err := parseNode(r.bytes())
			if err != nil {
				return err
			}
			m.nodes = append(m.nodes, n)
		case num == 5 && wt == 2: // initializer
			t, err := parseTensor(r.bytes())
			if err != nil {
				return err
			}
			m.inits[t.name] = t.t
		case num == 11 && wt == 2: // input
			name, dims, err := parseValueInfo(r.bytes())
			if err != nil {
				return err
			}
			m.inputs = append(m.inputs, name)
			if len(m.inputDim) == 0 {
				m.inputDim = dims
			}
		case num == 12 && wt == 2: // output
			name, _, err := parseValueInfo(r.bytes())
			if err != nil {
				return err
			}
			m.outputs = append(m.outputs, name)
		default:
			r.skip(wt)
		}
	}
	return nil
}

func parseNode(b []byte) (onnxNode, error) {
	var n onnxNode
	n.attrs = map[string]onnxAttr{}
	r := &pbReader{b: b}
	for num, wt, ok := r.field(); ok; num, wt, ok = r.field() {
		switch {
		case num == 1 && wt == 2:
			n.inputs = append(n.inputs, string(r.bytes()))
		case num == 2 && wt == 2:
			n.outputs = append(n.outputs, string(r.bytes()))
		case num == 4 && wt == 2:
			n.op = string(r.bytes())
		case num == 5 && wt == 2:
			a, err := parseAttr(r.bytes())
			if err != nil {
				return n, err
			}
			n.attrs[a.name] = a.attr
		default:
			r.skip(wt)
		}
	}
	if n.op == "" {
		return n, fmt.Errorf("onnx: a node has no op_type")
	}
	return n, nil
}

type namedAttr struct {
	name string
	attr onnxAttr
}

func parseAttr(b []byte) (namedAttr, error) {
	var na namedAttr
	var a onnxAttr
	r := &pbReader{b: b}
	for num, wt, ok := r.field(); ok; num, wt, ok = r.field() {
		switch {
		case num == 1 && wt == 2:
			na.name = string(r.bytes())
		case num == 2 && wt == 5: // f
			a.f = math.Float32frombits(r.fixed32())
		case num == 3 && wt == 0: // i
			a.i = int64(r.varint())
		case num == 4 && wt == 2: // s
			a.s = r.bytes()
		case num == 5 && wt == 2: // t
			t, err := parseTensor(r.bytes())
			if err != nil {
				return na, err
			}
			a.t = t.t
		case num == 7 && wt == 2: // floats (packed)
			p := r.bytes()
			for i := 0; i+4 <= len(p); i += 4 {
				a.floats = append(a.floats, math.Float32frombits(binary.LittleEndian.Uint32(p[i:])))
			}
		case num == 7 && wt == 5: // floats (unpacked)
			a.floats = append(a.floats, math.Float32frombits(r.fixed32()))
		case num == 8 && wt == 2: // ints (packed)
			p := r.bytes()
			pr := &pbReader{b: p}
			for !pr.done() {
				a.ints = append(a.ints, int64(pr.varint()))
			}
		case num == 8 && wt == 0:
			a.ints = append(a.ints, int64(r.varint()))
		case num == 20 && wt == 0: // type
			a.typ = int64(r.varint())
		default:
			r.skip(wt)
		}
	}
	na.attr = a
	return na, nil
}

// namedTensor pairs a tensor with its name field.
type namedTensor struct {
	name string
	t    *tensor
}

func parseTensor(b []byte) (namedTensor, error) {
	var nt namedTensor
	var dims []int64
	var dataType int64
	var floats []float32
	var ints []int64
	var raw []byte
	r := &pbReader{b: b}
	for num, wt, ok := r.field(); ok; num, wt, ok = r.field() {
		switch {
		case num == 1 && wt == 0:
			dims = append(dims, int64(r.varint()))
		case num == 1 && wt == 2: // packed dims
			p := r.bytes()
			pr := &pbReader{b: p}
			for !pr.done() {
				dims = append(dims, int64(pr.varint()))
			}
		case num == 2 && wt == 0:
			dataType = int64(r.varint())
		case num == 4 && wt == 2: // float_data packed
			p := r.bytes()
			for i := 0; i+4 <= len(p); i += 4 {
				floats = append(floats, math.Float32frombits(binary.LittleEndian.Uint32(p[i:])))
			}
		case num == 4 && wt == 5:
			floats = append(floats, math.Float32frombits(r.fixed32()))
		case num == 7 && wt == 2: // int64_data packed
			p := r.bytes()
			pr := &pbReader{b: p}
			for !pr.done() {
				ints = append(ints, int64(pr.varint()))
			}
		case num == 7 && wt == 0:
			ints = append(ints, int64(r.varint()))
		case num == 8 && wt == 2:
			nt.name = string(r.bytes())
		case num == 9 && wt == 2:
			raw = r.bytes()
		default:
			r.skip(wt)
		}
	}

	t := &tensor{dims: dims}
	switch dataType {
	case 1: // FLOAT
		if len(floats) == 0 && len(raw) > 0 {
			floats = make([]float32, len(raw)/4)
			for i := range floats {
				floats[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
			}
		}
		t.data = floats
	case 7: // INT64
		if len(ints) == 0 && len(raw) > 0 {
			ints = make([]int64, len(raw)/8)
			for i := range ints {
				ints[i] = int64(binary.LittleEndian.Uint64(raw[i*8:]))
			}
		}
		t.ints = ints
	case 6: // INT32
		n := int64(1)
		for _, d := range dims {
			n *= d
		}
		ints = make([]int64, n)
		if len(raw) > 0 {
			for i := range ints {
				ints[i] = int64(int32(binary.LittleEndian.Uint32(raw[i*4:])))
			}
		}
		t.ints = ints
	default:
		// Unknown type: keep raw as float bits only for float; otherwise empty.
		if dataType == 1 {
			t.data = floats
		}
	}
	nt.t = t
	return nt, nil
}

// parseValueInfo reads a ValueInfoProto: its name and, for tensor types, the
// static dims.
func parseValueInfo(b []byte) (string, []int64, error) {
	var name string
	var dims []int64
	r := &pbReader{b: b}
	for num, wt, ok := r.field(); ok; num, wt, ok = r.field() {
		switch {
		case num == 1 && wt == 2:
			name = string(r.bytes())
		case num == 2 && wt == 2: // type
			dims = append(dims, parseTensorShape(r.bytes())...)
		default:
			r.skip(wt)
		}
	}
	return name, dims, nil
}

// parseTensorShape walks TypeProto -> tensor_type -> shape -> dim list.
func parseTensorShape(typeBytes []byte) []int64 {
	var dims []int64
	tr := &pbReader{b: typeBytes}
	for tnum, twt, tok := tr.field(); tok; tnum, twt, tok = tr.field() {
		if tnum == 1 && twt == 2 { // tensor_type
			tt := &pbReader{b: tr.bytes()}
			for un, uw, uok := tt.field(); uok; un, uw, uok = tt.field() {
				if un == 2 && uw == 2 { // shape
					dims = append(dims, parseShape(tt.bytes())...)
				} else {
					tt.skip(uw)
				}
			}
		} else {
			tr.skip(twt)
		}
	}
	return dims
}

// parseShape walks TensorShapeProto -> repeated dim -> dim_value.
func parseShape(shapeBytes []byte) []int64 {
	var dims []int64
	sh := &pbReader{b: shapeBytes}
	for sn, sw, sok := sh.field(); sok; sn, sw, sok = sh.field() {
		if sn == 1 && sw == 2 { // dim
			dr := &pbReader{b: sh.bytes()}
			for dn, dw, dok := dr.field(); dok; dn, dw, dok = dr.field() {
				if dn == 1 && dw == 0 {
					dims = append(dims, int64(dr.varint()))
				} else {
					dr.skip(dw)
				}
			}
		} else {
			sh.skip(sw)
		}
	}
	return dims
}
