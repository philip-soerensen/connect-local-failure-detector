package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/cameraapi"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

// ffmpegGrab pulls one JPEG frame from an RTSP stream using the system's
// ffmpeg. It waits for a keyframe, so it can take a few seconds.
func ffmpegGrab(ctx context.Context, rtspURL string) ([]byte, error) {
	grabCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(grabCtx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-rtsp_transport", "tcp", "-i", rtspURL,
		"-frames:v", "1", "-q:v", "4", "-f", "mjpeg", "pipe:1",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if len(out) < 3 || out[0] != 0xFF || out[1] != 0xD8 {
		return nil, fmt.Errorf("ffmpeg produced no JPEG (%d bytes)", len(out))
	}
	return out, nil
}

// feedCameras runs one feeder goroutine per printer that has a camera source
// configured (per printer, because the camera is the printer's). Feeders are
// persisted in the module's data dir (feeders.json) and set from the
// Spaghetti tab via POST /feeders.
func (a *app) feedCameras(ctx context.Context, conn *wirerpc.Conn) {
	a.conn = conn
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		slog.WarnContext(ctx, "ffmpeg not found; RTSP camera feeders will not work (install ffmpeg or use an HTTP snapshot source)")
	}
	a.feederMu.Lock()
	a.feederCancels = map[string]context.CancelFunc{}
	a.feederMu.Unlock()
	// watch() starts this goroutine before the host sends plugin.configure,
	// so the store may not exist yet; wait for it instead of starting no
	// feeders at all.
	var store *store
	for {
		a.feederMu.Lock()
		store = a.store
		a.feederMu.Unlock()
		if store != nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	feeders := store.allFeeders()
	for printer, conf := range feeders {
		a.startFeeder(ctx, printer, conf)
	}
	slog.InfoContext(ctx, "camera feeders started", "printers", len(feeders))
	// Preload the model so the Spaghetti tab shows it as loaded and a bad
	// download surfaces at startup instead of on the first frame.
	go func() {
		if _, err := a.getDetector(ctx, a.settings.load()); err != nil {
			slog.WarnContext(ctx, "the model could not be preloaded", "error", err)
		}
	}()
	<-ctx.Done()
}

// startFeeder (re)starts the feeder for one printer, cancelling any previous.
func (a *app) startFeeder(ctx context.Context, printer string, conf feederConf) {
	a.feederMu.Lock()
	if cancel, ok := a.feederCancels[printer]; ok {
		cancel()
	}
	fctx, cancel := context.WithCancel(ctx)
	a.feederCancels[printer] = cancel
	conn := a.conn
	a.feederMu.Unlock()
	go a.feedOne(fctx, conn, printer, conf)
}

// setFeeder persists and applies the camera source for one printer.
func (a *app) setFeeder(ctx context.Context, printer string, conf feederConf) error {
	if err := a.store.setFeeder(printer, conf); err != nil {
		return err
	}
	if conf.Camera == "" && conf.RTSPURL == "" && conf.SnapshotURL == "" {
		a.feederMu.Lock()
		if cancel, ok := a.feederCancels[printer]; ok {
			cancel()
			delete(a.feederCancels, printer)
		}
		a.feederMu.Unlock()
		return nil
	}
	a.startFeeder(ctx, printer, conf)
	return nil
}

// feedOne grabs one JPEG per check interval from the printer's camera source
// and hands it to the named camera over the module wire (cameraapi.Accept),
// so the host's own snapshot recorder then stores and publishes it like any
// other camera still.
func (a *app) feedOne(ctx context.Context, conn *wirerpc.Conn, printer string, conf feederConf) {
	cams := cameraapi.NewClient(conn)
	for ctx.Err() == nil {
		cfg := a.settings.load()
		if conf.Camera == "" || (conf.SnapshotURL == "" && conf.RTSPURL == "") {
			return // feeder cleared
		}
		var jpeg []byte
		var err error
		switch {
		case conf.SnapshotURL != "":
			user := conf.User
			if user == "" {
				user = "maker"
			}
			jpeg, err = digestGet(ctx, conf.SnapshotURL, user, conf.Token)
			if err != nil {
				slog.WarnContext(ctx, "the camera snapshot could not be fetched", "printer", printer, "url", conf.SnapshotURL, "error", err)
			}
		case conf.RTSPURL != "":
			jpeg, err = ffmpegGrab(ctx, conf.RTSPURL)
			if err != nil {
				slog.WarnContext(ctx, "ffmpeg could not grab a frame", "printer", printer, "url", conf.RTSPURL, "error", err)
			}
		}
		if err == nil && len(jpeg) > 0 {
			if err := cams.Accept(ctx, "", conf.Camera, jpeg); err != nil {
				slog.WarnContext(ctx, "the camera frame could not be pushed", "printer", printer, "camera", conf.Camera, "error", err)
			} else {
				slog.InfoContext(ctx, "camera frame pushed", "printer", printer, "camera", conf.Camera, "bytes", len(jpeg))
			}
		}
		interval := cfg.MinIntervalSeconds
		if interval <= 0 {
			interval = 30
		}
		select {
		case <-time.After(time.Duration(interval) * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

// digestGet fetches url with HTTP Digest auth (MD5), handling both the
// qop="auth" form and the legacy no-qop form old printer firmware uses.
func digestGet(ctx context.Context, rawURL, user, pass string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return io.ReadAll(resp.Body)
	}
	challenge := parseChallenge(resp.Header.Get("WWW-Authenticate"))
	if challenge == nil {
		return nil, fmt.Errorf("camera endpoint answered 401 without a digest challenge")
	}
	resp.Body.Close()

	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req2.Header.Set("Authorization", digestHeader("GET", rawURL, user, pass, challenge))
	resp2, err := client.Do(req2)
	if err != nil {
		return nil, err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("camera endpoint answered %d after digest auth", resp2.StatusCode)
	}
	return io.ReadAll(resp2.Body)
}

type challenge struct {
	realm string
	nonce string
	qop   string
}

func parseChallenge(header string) *challenge {
	if !strings.HasPrefix(strings.ToLower(header), "digest ") {
		return nil
	}
	c := &challenge{}
	for _, part := range strings.Split(header[len("Digest "):], ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		v := strings.Trim(kv[1], `"`)
		switch strings.ToLower(kv[0]) {
		case "realm":
			c.realm = v
		case "nonce":
			c.nonce = v
		case "qop":
			if v == "auth" {
				c.qop = "auth"
			}
		}
	}
	if c.realm == "" || c.nonce == "" {
		return nil
	}
	return c
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func digestHeader(method, url, user, pass string, c *challenge) string {
	ha1 := md5hex(user + ":" + c.realm + ":" + pass)
	ha2 := md5hex(method + ":" + url)
	var resp string
	if c.qop == "auth" {
		resp = md5hex(ha1 + ":" + c.nonce + ":00000001:nonce:auth:" + ha2)
	} else {
		resp = md5hex(ha1 + ":" + c.nonce + ":" + ha2)
	}
	h := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`, user, c.realm, c.nonce, url, resp)
	if c.qop == "auth" {
		h += `, qop=auth, nc=00000001, cnonce="nonce"`
	}
	return h
}
