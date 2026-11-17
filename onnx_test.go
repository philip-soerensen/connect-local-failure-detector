package main

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"testing"
)

// TestONNXAgainstReference runs the pure-Go ONNX executor on a fixed input and
// compares against reference outputs produced by onnxruntime. Set the
// environment variables:
//
//	FD_MODEL=/path/to/model.onnx FD_INPUT=/path/to/input.f32 FD_REF=/path/to/ref.json
//
// gen_reference.py (in this directory) produces input.f32 and ref.json.
// The test skips when the variables are unset.
func TestONNXAgainstReference(t *testing.T) {
	modelPath := os.Getenv("FD_MODEL")
	inputPath := os.Getenv("FD_INPUT")
	refPath := os.Getenv("FD_REF")
	if modelPath == "" || inputPath == "" || refPath == "" {
		t.Skip("FD_MODEL/FD_INPUT/FD_REF not set; see gen_reference.py")
	}

	m, err := loadONNX(modelPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	in := make([]float32, len(raw)/4)
	for i := range in {
		in[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	h, w := m.inputDim[2], m.inputDim[3]
	if int64(len(in)) != 3*h*w {
		t.Fatalf("input has %d floats, want %d", len(in), 3*h*w)
	}

	outs, err := m.run(map[string]*tensor{m.inputs[0]: floatTensor([]int64{1, 3, h, w}, in)})
	if err != nil {
		t.Fatal(err)
	}

	refRaw, err := os.ReadFile(refPath)
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		Boxes []float32 `json:"boxes"`
		Confs []float32 `json:"confs"`
	}
	if err := json.Unmarshal(refRaw, &ref); err != nil {
		t.Fatal(err)
	}

	boxes, confs := outs["boxes"], outs["confs"]
	if boxes == nil || confs == nil {
		boxes, confs = outs[m.outputs[0]], outs[m.outputs[1]]
	}
	if len(boxes.data) != len(ref.Boxes) || len(confs.data) != len(ref.Confs) {
		t.Fatalf("output sizes differ: boxes %d vs %d, confs %d vs %d",
			len(boxes.data), len(ref.Boxes), len(confs.data), len(ref.Confs))
	}
	maxDiffB, maxDiffC := 0.0, 0.0
	for i := range ref.Boxes {
		d := math.Abs(float64(boxes.data[i] - ref.Boxes[i]))
		if d > maxDiffB {
			maxDiffB = d
		}
	}
	for i := range ref.Confs {
		d := math.Abs(float64(confs.data[i] - ref.Confs[i]))
		if d > maxDiffC {
			maxDiffC = d
		}
	}
	t.Logf("max abs diff: boxes=%g confs=%g", maxDiffB, maxDiffC)
	if maxDiffB > 1e-3 || maxDiffC > 1e-3 {
		t.Errorf("outputs diverge from onnxruntime reference: boxes %g, confs %g", maxDiffB, maxDiffC)
	}
}

// TestPrintGuardEncoder runs the PrintGuard encoder ONNX against an
// onnxruntime-produced reference embedding. Set:
//
//	FD_PG_MODEL=/tmp/pg-encoder.onnx FD_PG_INPUT=/tmp/pg-input.f32 FD_PG_REF=/tmp/pg-ref.json
func TestPrintGuardEncoder(t *testing.T) {
	modelPath := os.Getenv("FD_PG_MODEL")
	inputPath := os.Getenv("FD_PG_INPUT")
	refPath := os.Getenv("FD_PG_REF")
	if modelPath == "" || inputPath == "" || refPath == "" {
		t.Skip("FD_PG_MODEL/FD_PG_INPUT/FD_PG_REF not set")
	}
	m, err := loadONNX(modelPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	in := make([]float32, len(raw)/4)
	for i := range in {
		in[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	h, w := m.inputDim[2], m.inputDim[3]
	if int64(len(in)) != 3*h*w {
		t.Fatalf("input has %d floats, want %d", len(in), 3*h*w)
	}
	outs, err := m.run(map[string]*tensor{m.inputs[0]: floatTensor([]int64{1, 3, h, w}, in)})
	if err != nil {
		t.Fatal(err)
	}
	got := outs[m.outputs[0]]
	if got == nil {
		t.Fatalf("no output %q", m.outputs[0])
	}
	refRaw, _ := os.ReadFile(refPath)
	var ref struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.Unmarshal(refRaw, &ref); err != nil {
		t.Fatal(err)
	}
	if len(got.data) != len(ref.Embedding) {
		t.Fatalf("embedding sizes differ: %d vs %d", len(got.data), len(ref.Embedding))
	}
	maxDiff := 0.0
	for i := range ref.Embedding {
		d := math.Abs(float64(got.data[i] - ref.Embedding[i]))
		if d > maxDiff {
			maxDiff = d
		}
	}
	t.Logf("embedding max abs diff: %g", maxDiff)
	if maxDiff > 1e-2 {
		t.Errorf("embedding diverges from onnxruntime reference: %g", maxDiff)
	}
}
