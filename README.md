> **Warning: this project was 100% written by Mistral Vibe.** I have tried to
> direct it and test as best I can, but consider this potential AI slop and take
> your precautions. I did it because it was fun, but it should not be shared
> without this disclaimer. However, it works on my machine!

# failure-detector — AI spaghetti detection as a Prusa Connect Local extension

A [Prusa Connect Local](https://github.com/prusa3d/prusa-connect) module
(`runtime: service`) that watches a printer's camera during prints and detects
failed prints ("spaghetti"), in the style of
[Obico's The Spaghetti Detective](https://github.com/TheSpaghettiDetective/obico-server).

It subscribes to `com.prusa.connect.camera.snapshot.captured` events, reads each
camera still from the server's storage (`storageapi`, `read_storage`
permission), runs an on-device ONNX model over it, smooths the per-frame
failure probability with an EWMA (the same decision logic Obico's backend uses
in `lib/prediction.py`), publishes `<namespace>/failure.alert` events, and can
pause the print (`core.command.pause_print`, needs the `operate` grant on the
printer).

## What it does, per camera snapshot

1. `camera.snapshot.captured` arrives (the camera's own cadence, ~10 s for an
   RTSP camera). If `detect_only_printing` is on, frames are only scored while
   a job is running on the printer (`device.job.started` / `device.job.settled`).
2. The JPEG is read from storage (capped at `max_frame_bytes`), decoded and
   resized to the model input (416x416).
3. The ONNX model runs **in-process, pure Go** — no cgo, no external runtime,
   so the release binary satisfies the marketplace's `CGO_ENABLED=0` build
   rule. The engine is a small hand-rolled ONNX executor (`onnx.go`, `ops.go`,
   `tensor.go`) that implements exactly the op set the model uses (Conv,
   LeakyRelu, MaxPool, Reshape, Transpose, Concat, Slice, Shape, Gather, Mul,
   Add, Sub, Div, Exp, Sigmoid, Unsqueeze, Cast, Constant).
4. Post-processing mirrors Obico's `ml_api` (`detect.go`): box threshold
   (`threshold`, default 0.08), NMS (`nms`, default 0.45), normalized
   x1y1x2y2 boxes → pixel xywh.
5. The frame's max confidence feeds a per-printer EWMA
   (`smoothed = smoothing*new + (1-smoothing)*smoothed`). At
   `alert_threshold` an `alert` event is emitted; at `pause_threshold` the
   print is paused if `pause_on_failure` is on.

## HTTP routes (mounted at `/api/v1/modules/<namespace>/`)

- `GET /status` — per-printer state: frames scored, detections, max confidence,
  smoothed probability, alerting/paused flags.
- `GET /config` — the effective settings.
- `GET /healthz` — liveness.

## Configuration (module options, set in the instance's `config:` block)

| key | default | meaning |
|---|---|---|
| `model_url` | `""` | Where `model.onnx` is downloaded from at first start (into the module's data dir). Required. |
| `model_sha256` | `""` | Hex sha256 the downloaded model must match; refuses to load otherwise. |
| `threshold` | `0.08` | Min box confidence for a detection (Obico's `THRESH`). |
| `nms` | `0.45` | NMS IoU threshold. |
| `alert_threshold` | `0.5` | Smoothed probability at/above which `failure.alert` is published. |
| `pause_threshold` | `0.8` | Smoothed probability at/above which the print is paused. |
| `smoothing` | `0.4` | EWMA weight of the newest frame (0..1). |
| `pause_on_failure` | `false` | Send `core.command.pause_print` when the pause threshold is crossed. |
| `detect_only_printing` | `true` | Score frames only while a job runs. |
| `min_interval_seconds` | `2` | Min seconds between two detections of the same camera. |
| `max_frame_bytes` | `8388608` | Largest frame read and decoded. |

### Models (picked with the `use_printguard` switch on the extension's settings page; the `model` text option is the fallback, default `obico`)

- **printguard** (switch ON): ShuffleNetV2 encoder `encoder_float32.onnx` (~5 MB,
  sha256 `853f8653…b3f22`) + `prototypes.json` (sha256 `06467c39…264a`) from
  github.com/oliverbravery/PrintGuard (`models/`, GPL-2.0 project). Input
  1x3x224x224; the failure probability is the softmax over squared prototype
  distances. ~0.2 s/frame in the pure-Go engine.
- **obico**: `https://www.obico.io/static_pub/ml-models/model-weights-5a6b1be1fa.onnx`,
  sha256 `0a6ebd8e…b9aa`, ~200 MB single-class YOLO-style CNN, 416x416x3 input
  (AGPL-3.0), ~25 s/frame in the pure-Go engine.

Both are downloaded once into the module's data dir and sha256-verified;
`model_url`/`model_sha256`/`prototypes_url`/`prototypes_sha256` override the
built-ins with a third-party model.

**License:** this module is **AGPL-3.0-or-later** (see `LICENSE`). The reference
model preset below is Obico's AGPL-3.0 asset; `model_url`/`model_sha256` are
preset to it and can be overwritten in the instance's `config:` block with any
third-party model URL + sha256 — the module downloads whatever `model_url`
names and verifies the sha256 before loading. The Go code reimplements the
pipeline (detection + EWMA decision logic) and does not copy Obico code.

## Performance

The pure-Go conv is the hot path: one 416x416 inference takes on the order of
10-20 s on a modest x86-64 core (measured ~16 s for the full graph in a test
on this machine), so keep `min_interval_seconds` >= 2 and expect detection to
lag the camera cadence. This is the price of a pure-Go, CGO-free binary; on an
RK3588-class ARM board it will be slower. A GPU/NPU path is not possible in a
published module (CGO constraint) — see `../NOTES.md` for the option of a
locally sideloaded Python/onnxruntime module instead.

## Verification

`onnx_test.go` runs the full model against a reference produced by
onnxruntime (`gen_reference.py`):

```sh
FD_MODEL=/path/to/model.onnx FD_INPUT=/tmp/input.f32 FD_REF=/tmp/ref.json \
  go test -run TestONNXAgainstReference -v .
```

Current result: max abs diff vs onnxruntime = 2.0e-06 (boxes), 9.9e-08
(confs) — well within 1e-3.

## Install / use

See **GUIDE.md** for a step-by-step guide on a Linux server running Connect
Local as a systemd service.

## Printer-page UI (0.2.0+)

The module ships a browser half (`ui/`, a Svelte bundle built with Vite against
`@prusa/connect-ui`, all shared specifiers external). It contributes a
**device-tab** titled **Spaghetti**, so each printer's page has a Spaghetti
tab next to Files/Camera/Printer/History with: detection on/off, pause-on-failure
on/off, check-interval in seconds (all applied at run time via `POST /settings`,
persisted in the module's data dir), live status, and a pass/fail photo
history (frames saved under the data dir, served via `GET /frames/{name}`,
listed via `GET /history?printer=<id>`).
