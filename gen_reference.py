#!/usr/bin/env python3
"""Generate a reference input/output pair for onnx_test.go.

Usage (needs onnxruntime + numpy):

    python3 gen_reference.py /path/to/model.onnx input.f32 ref.json

It writes a fixed-seed random input tensor (1x3xHxW float32, the model's
input shape) to input.f32 and the model's boxes/confs outputs for it to
ref.json, produced by onnxruntime. The Go test then runs the same input
through the pure-Go executor and compares.
"""
import json
import sys

import numpy as np
import onnxruntime as ort


def main() -> None:
    model_path, input_path, ref_path = sys.argv[1:4]
    sess = ort.InferenceSession(model_path, providers=["CPUExecutionProvider"])
    inp = sess.get_inputs()[0]
    _, _, h, w = inp.shape
    rng = np.random.default_rng(42)
    x = rng.random((1, 3, h, w), dtype=np.float32)
    x.astype("<f4").tofile(input_path)
    outs = sess.run(None, {inp.name: x})
    names = [o.name for o in sess.get_outputs()]
    boxes = next(o for n, o in zip(names, outs) if n == "boxes" or o.ndim == 4)
    confs = next(o for n, o in zip(names, outs) if n == "confs" or o.ndim == 3)
    with open(ref_path, "w") as f:
        json.dump(
            {"boxes": boxes.reshape(-1).tolist(), "confs": confs.reshape(-1).tolist()},
            f,
        )
    print(f"wrote {input_path} and {ref_path}: boxes {boxes.shape}, confs {confs.shape}")


if __name__ == "__main__":
    main()
