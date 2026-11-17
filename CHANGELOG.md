# Changelog

## 0.3.9
- Spaghetti tab: model dropdown (obico default, printguard) via bind:value; defaults: model=obico, min_interval_seconds=30.
- History and stored frames capped at the last 10 entries.
- Tiling controls hidden; default is upstream PrintGuard short-side squares.

## 0.3.6
- PrintGuard preprocessing fixed to match upstream exactly: sliding short-side
  squares (max score), Pillow-style triangle downscale, luminance, ImageNet norm.

## 0.3.5
- Fixed feeder-start race (store created in configure after watch started);
  model preloaded at startup; failed downloads retried instead of sticky.

## 0.3.4
- Cached model with wrong sha256 is re-downloaded (model switch self-heal);
  byte-identical duplicate frames skipped.

## 0.3.3
- Fixed sync.Mutex self-deadlock in printingNow; model cache on its own mutex.

## 0.3.0
- Dual models: printguard (default then) + obico; pure-Go ONNX engine.
