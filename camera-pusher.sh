#!/bin/sh
# camera-pusher.sh — grab one frame from the Buddy3D camera's RTSP stream and
# push it to the Connect Local camera's snapshot endpoint.
#
# Why this exists: the generic camera driver only produces *stills* from
# `snapshot_url` (HTTP) or from pushed frames. `stream_url: rtsp://...` feeds
# the live view only. The Buddy3D camera exposes RTSP alone (no HTTP port), so
# a frame has to be pulled from RTSP and pushed here.
#
# The token comes from CONNECT_TOKEN in the environment, or from
# ~/.config/camera-pusher.env (KEY=VALUE lines, chmod 600), or as a last
# resort from connecttoken='...' in ~/.bashrc.
# Override with CONNECT_TOKEN / CONNECT_URL / RTSP_URL / CAMERA env vars.

set -eu

RTSP_URL="${RTSP_URL:-rtsp://192.168.1.100/live}"
CAMERA="${CAMERA:-buddy-cam}"
CONNECT_URL="${CONNECT_URL:-https://127.0.0.1:8090}"
TOKEN="${CONNECT_TOKEN:-}"
if [ -z "$TOKEN" ] && [ -f "$HOME/.config/camera-pusher.env" ]; then
    TOKEN="$(grep -oP '^CONNECT_TOKEN=\K.*' "$HOME/.config/camera-pusher.env" | head -1)"
fi
if [ -z "$TOKEN" ]; then
    TOKEN="$(grep -oP "connecttoken='\K[^']+" "$HOME/.bashrc" 2>/dev/null | head -1)"
fi

if [ -z "$TOKEN" ]; then
    echo "camera-pusher: no connect token found (set CONNECT_TOKEN or connecttoken in ~/.bashrc)" >&2
    exit 1
fi

TMP="$(mktemp /tmp/camera-pusher.XXXXXX.jpg)"
trap 'rm -f "$TMP"' EXIT

# One JPEG frame from the RTSP stream; wait for a keyframe.
timeout 30 ffmpeg -hide_banner -loglevel error -rtsp_transport tcp \
    -i "$RTSP_URL" -frames:v 1 -q:v 4 -f image2 "$TMP" -y

curl -sk -X PUT \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: image/jpeg" \
    --data-binary @"$TMP" \
    -o /dev/null -w "camera-pusher: pushed $(stat -c%s "$TMP") bytes to $CAMERA (HTTP %{http_code})\n" \
    "$CONNECT_URL/api/v1/cameras/$CAMERA/snapshot"
