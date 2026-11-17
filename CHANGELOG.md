# Changelog

## 0.3.18
- Check interval is now two-level: global min_interval_seconds is explicitly the
  DEFAULT (labelled as such); each printer's Spaghetti tab sets its own
  interval via the new /printer-settings route, which supersedes the default.


## 0.3.17
- Fix the settings-write permission check: `operate` is device-only in the
  host authz vocabulary; the module resource now checks `operate_resource` on
  resource_kind (was 403 for everyone, so the tab dropdown reset).


## 0.3.16
- ffmpeg requirement now also in the camera egress `why`, so it appears in the
  install/update dialog's "reaches outside" section, not just the listing.


## 0.3.15
- Review fixes: POST /settings and POST /feeders now require the caller to
  operate the module / the printer (accessapi + routesapi.PrincipalOf).
  GET /feeders never returns the camera token, only token_set.
  ffmpeg RTSP grabs accept only rtsp(s):// and run with -protocol_whitelist.
  HTTP snapshot fetches are limited to hosts declared in the new
  camera_snapshot_hosts option (also an egress entry), body-capped and timed
  out. Model downloads have a 5-minute timeout. UI camera list goes through
  host.client. ffmpeg requirement noted in the listing summary.


## 0.3.14
- Docs and UI placeholders no longer mention any specific port/address (web UI
  documented as :8080, camera as rtsp://<camera-ip>/live).


## 0.3.13
- Spaghetti tab: camera picker is a dropdown of the cameras registered on the
  instance for the printer (fetched from the host API; the module wire cannot
  list cameras by design). RTSP URL remains as an optional override.
- Model dropdown label "Model (global)".

## 0.3.12
- Model is one global option: the Spaghetti-tab choice now persists and
  overwrites the global config; changing it on the extension settings page
  clears the tab override (last writer wins, survives restarts).

## 0.3.11
- Fixed dropdown snap-back: run-time overrides (model etc.) are now persisted
  by setPatch with validation.

## 0.3.10
- Fixed history images 404ing: frame pruning sorted by the wrong field after a
  seq reset and deleted the newest frames; seq is now recovered on load,
  pruning sorts by embedded timestamp, and dead entries are dropped.

## 0.3.9
- Model dropdown (obico default) via bind:value; defaults obico / 30 s /
  alert 0.2 / pause 0.4; history and frames capped at the last 10 entries.

## 0.3.6
- PrintGuard preprocessing now matches upstream exactly: sliding short-side
  squares (max score), Pillow-style triangle downscale, luminance, ImageNet
  norm. Validated against onnxruntime reference on a live spaghetti frame
  (0.449 vs 0.457).

## 0.3.5
- Fixed feeder-start race (store created after watch started); model preloaded
  at startup; failed downloads retried instead of sticky.

## 0.3.4
- Cached model with wrong sha256 is re-downloaded (model switch self-heal);
  byte-identical duplicate frames skipped.

## 0.3.3
- Fixed sync.Mutex self-deadlock in printingNow; model cache on its own mutex.

## 0.3.0
- Dual models: printguard + obico; pure-Go ONNX engine; per-printer camera
  feeder; Spaghetti device tab.
