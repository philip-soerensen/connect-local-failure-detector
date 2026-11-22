# GUIDE — install and use failure-detector on a Linux server running Prusa Connect Local as a systemd service

Assumed layout (the one `docs/src/content/docs/install-from-a-release.md` in
`prusa-connect/` sets up):

```
/var/lib/prusa-connect/bin/prusa-connect   # the server binary (user prusa-connect)
/etc/prusa-connect/config.yaml             # main config
/etc/prusa-connect/config.d/               # machine-owned overlay (module declarations land here)
/var/lib/prusa-connect                     # state, incl. core.modules and module-data
systemctl unit: prusa-connect.service
```

API base: `http://<server>:8080/api/v1` (bearer credential; mint one in the web UI
under **Settings → API credentials → New credential** in the left-hand menu, or
`POST /api/v1/auth/credentials`).

---

## 1. Build the release (on any machine with Go >= 1.24)

```sh
cd connect-local-extensions/failure-detector
# the go.mod replaces the SDK with the local prusa-connect checkout:
#   replace github.com/prusa3d/prusa-connect/sdk => ../prusa-connect/sdk

export PATH=$PATH:$HOME/.local/go/bin
mkdir -p release
for p in linux_amd64 linux_arm64; do
  GOOS=${p%_*} GOARCH=${p#*_} CGO_ENABLED=0 \
    go build -trimpath -buildvcs=false -o release/failure-detector_${p} .
done
cp module.yaml openapi.json release/
cd release && sha256sum module.yaml openapi.json failure-detector_* > checksums.txt
```

A release is one directory: the manifest at its root, one binary per platform
named `<name>_<GOOS>_<GOARCH>`, and a `checksums.txt` listing every file
(`prusa-connect module add` verifies it and refuses unlisted files).

## 2. Copy the release to the server and install it

```sh
# on the server:
sudo install -d -m 0755 /opt/failure-detector-release
sudo cp -r release/. /opt/failure-detector-release/
# the prusa-connect service user must be able to read it:
sudo chown -R root:prusa-connect /opt/failure-detector-release
sudo chmod -R a+rX /opt/failure-detector-release
```

### Option A — from the Extensions page

The Extensions page lists marketplace extensions plus **one card per entry of
`core.modules_local`**. Important: the server reads only `config.yaml` itself
plus `config.d/setup.yaml` (the wizard's file) and `config.d/resources.json` —
**an arbitrary `*.yaml` dropped into `config.d/` is NOT read**. So edit
`/etc/prusa-connect/config.yaml` directly, under its existing `core:` block:

```yaml
core:
  modules_local:
    github.com/frenchvibes/failure-detector: /opt/failure-detector-release
```

(verified against `internal/app/config/load.go` in prusa-connect: `Load` reads
the operator file and `SetupFile(path)` = `config.d/setup.yaml` only).

Restart the service so the config is read (`sudo systemctl restart
prusa-connect`), reload the Extensions page: a "Failure Detector" card appears
next to the marketplace ones, showing the version and the permissions it asks
for. Click install and approve — that writes the declaration and the grants,
and switches the instance on. The release directory is re-read on each
listing, so dropping a rebuilt release (higher `version` in `module.yaml`) into
the same directory turns the card into an Update.

### Option B — from the command line

`prusa-connect module add` does the same without the page:

Settings file (the model is downloaded once into the module's data directory
under `/var/lib/prusa-connect/module-data/failure-detector/` and verified
against the sha256 before every load):

```sh
sudo tee /etc/prusa-connect/failure-detector-settings.json <<'EOF'
{
  "model_url": "https://www.obico.io/static_pub/ml-models/model-weights-5a6b1be1fa.onnx",
  "model_sha256": "0a6ebd8e30dbf6a450c50f9c0a5406f04ba7eb1c99fd5996e888c78bb383b9aa",
  "alert_threshold": 0.5,
  "pause_threshold": 0.8,
  "pause_on_failure": false
}
EOF
```

Install (works while the server is stopped or running; the declaration is
written to the machine-owned overlay `config.d/resources.json`):

```sh
sudo /var/lib/prusa-connect/bin/prusa-connect \
  module add github.com/frenchvibes/failure-detector@0.2.0 \
  -config /etc/prusa-connect/config.yaml \
  -from /opt/failure-detector-release \
  -settings /etc/prusa-connect/failure-detector-settings.json \
  -approve
```

`-approve` writes the requested `permissions`/`events` and the org-level grant
(`viewer` for `observe`, which also carries `read_storage`), and switches the
instance on. The namespace is derived from the module path:
`com.github.aux.frenchvibes.failure-detector`.

```sh
sudo systemctl restart prusa-connect
sudo journalctl -u prusa-connect -f        # watch the module download the model and start
```

The module's routes are mounted at
`/api/v1/modules/com.github.aux.frenchvibes.failure-detector/`.

## 3. Attach the Buddy3D camera to the printer

The Buddy3D camera is a first-class camera resource bound to a printer with
the `printer` config key (the printer's grants are copied onto the camera).
Add it once you have a bearer token (`$TOKEN`) and the printer's device id
(`GET /api/v1/devices`). The API is on `https://<server>:8080` (TLS, self-signed
→ `curl -k`):

```sh
curl -k -X PUT "https://<server>:8080/api/v1/cameras/buddy-cam" \
  -H "Authorization: Bearer $TOKEN" \
  -H "If-None-Match: *" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "generic",
    "config": {
      "printer": "<printer-device-id>",
      "stream_url": "rtsp://<buddy3d-ip>/live",
      "snapshot_every": "30s",
      "snapshot_storage": "local"
    }
  }'
```

Important, learned on a real server: **`stream_url` alone produces no stills.**
On a `generic` camera, `stream_url` (rtsp://) feeds only the live view, and the
`rtsp` camera type answers `Snapshot()` with "no frame" — neither publishes
`com.prusa.connect.camera.snapshot.captured`. Stills come only from
`snapshot_url` (an HTTP image URL the server polls) or from frames pushed to
`PUT /api/v1/cameras/{camera}/snapshot` (JPEG, <= 8 MiB). The periodic
`snapshot_every` recorder then stores the latest still into
`snapshot_storage` every interval and publishes the snapshot event — that
event is exactly what failure-detector consumes.

The Buddy3D camera exposes RTSP only (port 554; no HTTP port), so there is no
`snapshot_url` to poll. Instead a tiny pusher grabs one frame from RTSP with
ffmpeg and PUTs it:

```sh
# ~/.local/bin/camera-pusher.sh — see camera-pusher.sh in this repo.
# Token comes from ~/.config/camera-pusher.env (CONNECT_TOKEN=..., chmod 600).
# Install as a systemd *user* timer (no sudo needed):
mkdir -p ~/.config/systemd/user
cp camera-pusher.service camera-pusher.timer ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now camera-pusher.timer   # pushes a frame every 30 s
sudo loginctl enable-linger $USER                    # so it survives reboot w/o login
```

To change the camera later, quote the version from `GET /api/v1/camera-settings`
in `If-Match` (e.g. `-H 'If-Match: "5"'`) and send the full config — a partial
body would drop keys like `snapshot_every`.

## 4. (Optional) let it pause the printer

`pause_on_failure: true` needs the `operate` permission *on the printer*. The
`-approve` grant only covers org-level `observe`. Write a grant for the module
principal `sink:failure-detector` on the printer (web UI: printer → Share /
grants, or `POST /api/v1/grants`) giving it a role that includes `operate`,
then set `"pause_on_failure": true` in the instance's `config:` block
(`GET/PATCH /api/v1/resources` shows and edits it; unknown keys stop the
instance, so only touch declared keys).

## 5. Watch it work

```sh
# module status (per-printer frames, detections, smoothed probability, alerting)
curl -H "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:8080/api/v1/modules/com.github.aux.frenchvibes.failure-detector/status"

# effective config
curl -H "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:8080/api/v1/modules/com.github.aux.frenchvibes.failure-detector/config"

# alert events on the stream (event type com.github.aux.frenchvibes.failure-detector/failure.alert)
curl -N -H "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:8080/api/v1/events?types=com.github.aux.frenchvibes.failure-detector/failure.alert"

# module logs
sudo journalctl -u prusa-connect -f | grep -i failure
```

An alert payload looks like:

```json
{
  "printer": "<device-id>",
  "camera": "buddy3d",
  "smoothed": 0.63,
  "max_confidence": 0.91,
  "detections": [{"class": "failure", "confidence": 0.91, "bbox": [x, y, w, h]}],
  "snapshot": "<storage path of the JPEG>",
  "taken": "2026-11-17T12:00:00Z"
}
```

## 6. Tuning

- False alarms: raise `alert_threshold` / `pause_threshold`, lower
  `smoothing` (more frames averaged), or raise `threshold`.
- Missed failures: lower `alert_threshold`, raise `smoothing`.
- Load: `min_interval_seconds` defaults to **30** since 0.2.1 (the pure-Go
  inference takes ~10-30 s per 416x416 frame on a modest CPU core, so a shorter
  interval just builds a backlog of queued snapshot events and burns CPU).
  The camera recorder (`snapshot_every`) and the pusher timer should match it.
- Only during prints: keep `detect_only_printing: true` (default). Frames are
  skipped unless the printer has a running job; since 0.2.1 the module also
  asks the host for the current job the first time it sees a printer, so a
  module restart mid-print still detects. The Spaghetti tab flips
  `enabled` / `pause_on_failure` / `min_interval_seconds` at run time via
  `POST .../settings` (persisted in the module's data dir).

## 7. Update / remove

```sh
# rebuild the release, bump `version` in module.yaml, re-copy, then:
sudo /var/lib/prusa-connect/bin/prusa-connect \
  module add github.com/frenchvibes/failure-detector@0.2.0 \
  -config /etc/prusa-connect/config.yaml -from /opt/failure-detector-release -approve
sudo /var/lib/prusa-connect/bin/prusa-connect \
  module remove failure-detector -config /etc/prusa-connect/config.yaml
```

## Troubleshooting notes (learned on a real 0.1.0-beta.1 server)

- The Extensions page reads local releases only from `core.modules_local` in
  `config.yaml` itself (or `config.d/setup.yaml`) — a random `*.yaml` dropped
  into `config.d/` is **not** read.
- **0.1.0 bug, fixed in 0.1.1:** the module decoded `plugin.configure` params
  into `wirerpc.Settings` with `DisallowUnknownFields`, so the host's settings
  object (all manifest option keys) was refused and the instance hung in
  `starting`. 0.1.1 uses `wirerpc.ConfigureGranted[config]`. If an instance is
  stuck in `starting`, update to >= 0.1.1 and restart.
- "Permission operate — the organization cannot grant it": expected. Page/CLI
  approval only grants org-level `viewer` (observe + read_storage). `operate`
  is per-printer and must be granted by hand (only needed for
  `pause_on_failure`):
  `POST /api/v1/grants/device/{printer-id}` `{"relation":"operator","subject":"sink:failure-detector"}`.
- Set `model_url`/`model_sha256` in the card's settings pane; without them the
  module runs but logs `model_url is not set` on each snapshot.
- The module's routes mount at
  `/api/v1/modules/com.github.aux.frenchvibes.failure-detector/` on whatever
  port/TLS the server serves (check `journalctl -u prusa-connect` for
  `serving api=...`; e.g. `https://<server>:8080`, self-signed cert → `curl -k`).

## 0.2.0 — the "Spaghetti" tab on the printer page

0.2.0 adds the module's browser half: a **device-tab** contributed as
`ui.contributes.device_tab: [{id: spaghetti, title: Spaghetti}]`, so every
printer's page gets a **Spaghetti** tab next to Files / Camera / Printer /
History at `/printers/<id>/com.github.aux.frenchvibes.failure-detector.spaghetti`.

The tab offers, per printer:

- **Detection enabled** toggle and **Pause print on failure** toggle and a
  **check every N s** field — these POST to the module's `/settings` route,
  which persists them in the module's data directory (`runtime.json`) and
  applies them without a restart.
- Live per-printer state from `/status` (frames, detections, smoothed
  probability, alerting).
- A **history grid of pass/fail photos**: every scored frame's JPEG is saved
  to the data dir (`frames/`, capped at 500) and listed via `/history`, with
  thumbnails served from `/frames/{name}`; entries carry the fail verdict
  (alerting or detections > 0), confidence and time.

Build the UI half (Node >= 22): `cd ui && npm install && npm run build` →
`ui/dist/` (`module.yaml` + `ui/index.js`, CSS inlined into the single entry,
all `@prusa/connect-ui`/`svelte` imports left external for the app's import
map). The release directory then is `module.yaml`, `openapi.json`,
`failure-detector_<GOOS>_<GOARCH>`, `ui/index.js`, `checksums.txt`.

Update on the server: copy the new release over `/opt/failure-detector-release`
(sudo), then the Extensions page shows **Update 0.2.0** on the Failure Detector
card (the `modules_local` dir is re-read on each listing) — approve it. Or
while stopped: `module add github.com/frenchvibes/failure-detector@0.2.0 -config
/etc/prusa-connect/config.yaml -from /opt/failure-detector-release -approve`.

## 0.2.1 — lower load: 30 s default interval, printing-state from the host

0.2.1 (staged at `/tmp/fd-release-0.2.1` on philip-server, checksums verified):

- `min_interval_seconds` default is now **30** (manifest + code), because the
  pure-Go ONNX inference takes tens of seconds per frame and a 10 s camera
  cadence only queued events and kept a CPU core busy. The camera recorder
  (`snapshot_every`) and the pusher timer were set to 30 s to match.
- Frames are processed **only while the printer is printing**
  (`detect_only_printing: true`, the default), and the module now asks the
  host (`deviceapi.Jobs`) for the printer's current job the first time it sees
  a printer — so a module restart mid-print no longer disables detection for
  the rest of the print.
- On the running 0.2.0 the interval was already set to 30 at run time:
  `curl -k -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
   -d '{"min_interval_seconds":30}' https://<server>:8080/api/v1/modules/com.github.aux.frenchvibes.failure-detector/settings`
  (persisted in the module's data dir, survives restarts).

Install 0.2.1 the same way as 0.2.0 (copy over `/opt/failure-detector-release`,
then Extensions page → **Update 0.2.1**, or `module add ...@0.2.1` while stopped).

## Verified end-to-end on philip-server (2026-11-17 ~16:44-17:00)

- 0.2.0 serving (`plugin_version=0.2.0`, openapi paths=6, UI bundle mounted).
- Camera `buddy-cam` (generic, `stream_url: rtsp://<camera-ip>/live`,
  `snapshot_every: 30s`, `snapshot_storage: local`) — pusher timer pushes a
  fresh ffmpeg frame every 30 s (HTTP 204).
- During the spaghetti_trap print: model downloaded (sha256-verified) and
  loaded (446 nodes), frames scored (~25 s/frame pure Go), history entries
  with saved JPEGs, `smoothed` ~0.26 below `alert_threshold` 0.5 → no false
  alert. After the job was cancelled the module idles (0% CPU): frames are
  skipped unless printing.

## 0.2.3 — thresholds settable from the Spaghetti tab, pause default 0.4

`alert_threshold` and `pause_threshold` are now run-time settings: the Spaghetti
tab has "Alert at smoothed" / "Pause at smoothed" fields, and the API accepts
them in `POST .../settings` (validated to (0, 1], persisted in the module's data
dir). The manifest default for `pause_threshold` is now **0.4** (was 0.8) —
0.8 rarely trips because the EWMA of a real failure climbs slowly. Update as
usual: copy `/tmp/fd-release-0.2.3` over `/opt/failure-detector-release` (with
`chown root:prusa-connect` + `chmod -R u+rwX,go+rX`), then Extensions page →
Update 0.2.3. Pausing still needs the per-printer `operator` grant on
`sink:failure-detector` (see section 4).

## 0.2.5 — no detection while paused, alert default 0.2, built-in camera feeder

- With `detect_only_printing` (default true) the module now reads the printer's
  `/state` on every snapshot and only runs detection while it is `PRINTING` —
  a paused print (`PAUSED`) is skipped until resumed.
- `alert_threshold` default is now **0.2** (pause_threshold stays **0.4**);
  both remain settable at run time from the Spaghetti tab.
- **Built-in camera feeder** for cameras that expose an HTTP JPEG endpoint
  (an IP cam with a snapshot URL, or a camera attached to the printer, whose
  firmware serves `http://<printer-ip>/api/camera/snapshot` behind PrusaLink
  Digest auth — user `maker`, password = the PrusaLink token). Set in the
  instance config block:
  ```yaml
  camera: buddy-cam
  camera_snapshot_url: http://<host-with-jpeg>/snapshot.jpg   # or the printer endpoint
  camera_user: maker            # default
  camera_token: <PrusaLink token, only for the printer endpoint>
  ```
  The module fetches one JPEG every `min_interval_seconds` (pure-Go Digest
  auth) and pushes it to the camera over the module wire; the host's
  `snapshot_every` recorder publishes the snapshot events as before.
- **A standalone WiFi Buddy3D camera is different**: a full port scan of the
  camera device shows exactly one open port, 554, with a single H.264 RTSP
  stream (`rtsp://<cam>/live`) — no HTTP, no MJPEG. RTSP H.264 cannot be turned
  into JPEG without an H.264 decoder, and no viable pure-Go decoder exists
  (marketplace builds must be CGO_ENABLED=0). So for the standalone Buddy3D,
  keep the external `camera-pusher` ffmpeg timer from section 3 — that remains
  the working path. (A cgo build linking OpenH264 would remove even ffmpeg,
  but is not marketplace-eligible; it would only work as a local sideload.)

Install 0.2.5: copy `/tmp/fd-release-0.2.5` over `/opt/failure-detector-release`
(chown root:prusa-connect, chmod -R u+rwX,go+rX), then Extensions page →
Update 0.2.5.

## 0.2.6 — built-in RTSP feeder (uses the system's ffmpeg)

For an RTSP-only camera like the standalone Buddy3D, set:

```yaml
camera: buddy-cam
camera_rtsp_url: rtsp://<camera-ip>/live
```

The module then runs `ffmpeg -rtsp_transport tcp -i <url> -frames:v 1 -q:v 4
-f mjpeg pipe:1` every `min_interval_seconds` and pushes the JPEG to the camera
over the module wire — no systemd timer needed (`systemctl --user disable --now
camera-pusher.timer`). Requires **ffmpeg installed on the server**; the module
logs a warning at start if it is missing. `camera_snapshot_url` (HTTP JPEG)
still takes precedence when both are set. Egress to the RTSP port is declared in
the manifest. Install 0.2.6 like the previous versions (`/tmp/fd-release-0.2.6`).

## 0.3.0 — two built-in models, selectable on the config page; PrintGuard is the default

New option **`model`** on the extension card's settings pane:

- `printguard` (default): ShuffleNetV2 encoder (~5 MB ONNX) + prototype classifier
  from oliverbravery/PrintGuard (GPL-2.0 project; weights fetched at runtime from
  GitHub raw). One frame scores in ~0.2 s on a modest CPU core — much lighter than
  Obico. The failure probability is the softmax over squared prototype distances.
- `obico`: the previous ~200 MB AGPL-3.0 detector with box NMS.

`model_url` / `model_sha256` (and `prototypes_url` / `prototypes_sha256` for
printguard) are now empty by default and resolve to the selected built-in model;
set them to point at a third-party model. **If your card still has the Obico URL
saved in `model_url` from 0.2.4, clear that field** (or set `model: printguard`)
so the lighter default applies. The Spaghetti tab's status line shows which model
is loaded. Install: copy `/tmp/fd-release-0.3.0` over `/opt/failure-detector-release`
(chown root:prusa-connect, chmod -R u+rwX,go+rX), Extensions page → Update 0.3.0.

## 0.3.1 — the tab tells you when it can't pause

`/status` now reports `can_operate` per printer (the module asks the host via
`accessapi.Check` with action `operate` on the device). When **Pause print on
failure** is on but the module lacks `operate` on that printer, the Spaghetti
tab shows a red banner with the exact grant command to run. Install 0.3.1 like
0.3.0 (`/tmp/fd-release-0.3.1` → `/opt/failure-detector-release` → Update).

## 0.3.2 — camera source is a per-printer setting (Spaghetti tab), not a module option

The camera belongs to the printer, so the feeder is configured per printer: open the
printer's **Spaghetti** tab → "Camera source (this printer)" → set **Camera**
(`buddy-cam`) and **RTSP URL** (`rtsp://<camera-ip>/live`) → Save. Or via API:

```sh
curl -k -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"printer":"<device-id>","camera":"buddy-cam","rtsp_url":"rtsp://<camera-ip>/live"}' \
  https://<server>:8080/api/v1/modules/com.github.aux.frenchvibes.failure-detector/feeders
```

It is persisted per printer (`feeders.json` in the module data dir) and the feeder
starts immediately — no external pusher, no module-level camera options (those were
removed from the manifest in 0.3.2). Install 0.3.2 from `/tmp/fd-release-0.3.2`.
