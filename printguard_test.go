package main

import (
	"image"
	_ "image/jpeg"
	"math"
	"os"
	"strconv"
	"testing"
)

// TestPrintGuardFrameScore runs the full PrintGuard frame pipeline (squares +
// luminance preprocessing + encoder + prototypes) on a real JPEG and compares
// the worst-square score with the reference implementation's output.
//
// FD_PG_FRAME=/path/frame.jpg FD_PG_MODEL=/path/encoder.onnx
// FD_PG_PROTOS=/path/prototypes.json FD_PG_EXPECT=0.4574 go test -run TestPrintGuardFrameScore
func TestPrintGuardFrameScore(t *testing.T) {
	frame := os.Getenv("FD_PG_FRAME")
	modelPath := os.Getenv("FD_PG_MODEL")
	protosPath := os.Getenv("FD_PG_PROTOS")
	expect := os.Getenv("FD_PG_EXPECT")
	if frame == "" || modelPath == "" || protosPath == "" || expect == "" {
		t.Skip("set FD_PG_FRAME, FD_PG_MODEL, FD_PG_PROTOS, FD_PG_EXPECT")
	}
	want, err := strconv.ParseFloat(expect, 64)
	if err != nil {
		t.Fatalf("bad FD_PG_EXPECT: %v", err)
	}

	raw, err := os.ReadFile(frame)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytesReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	m, err := loadONNX(modelPath)
	if err != nil {
		t.Fatal(err)
	}
	protos, err := loadPrototypes(protosPath)
	if err != nil {
		t.Fatal(err)
	}
	det := &detector{model: m, pg: true, prototypes: protos}
	if v := os.Getenv("FD_PG_TILE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("bad FD_PG_TILE: %v", err)
		}
		det.tileSize = n
	}
	if v := os.Getenv("FD_PG_OVERLAP"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("bad FD_PG_OVERLAP: %v", err)
		}
		det.overlap = f
	}
	_, score, err := det.detect(img, 0.0, 0.45)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(score-want) > 0.02 {
		t.Fatalf("score = %v, want %v (±0.02)", score, want)
	}
	t.Logf("score = %v (reference %v)", score, want)
}
