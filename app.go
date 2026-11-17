package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/busapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/event"
	"github.com/prusa3d/prusa-connect/sdk/storageapi"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

// app is the module's state: settings, the detector, per-printer prediction
// state, and the clients the host's wire gives.
type app struct {
	settings settingsStore
	dataDir  string
	base     atomic.Value // config, as the host delivered it
	store    *store

	mu     sync.Mutex
	states map[string]*printerState
	access *accessapi.Client

	// detMu guards the model/detector cache separately from mu: loading the
	// model downloads it over the network and must not block /status.
	detMu       sync.Mutex
	model       *onnxModel
	modelErr    string
	modelBroken bool // the model file itself failed to parse; only a new download can fix it
	modelOn     bool
	detector    *detector

	conn          *wirerpc.Conn
	feederMu      sync.Mutex
	feederCancels map[string]context.CancelFunc

	// inflight bounds concurrent detections; a camera snapshot cadence is
	// slow, so two slots are plenty.
	inflight chan struct{}
}

func newApp() *app {
	return &app{
		states:   map[string]*printerState{},
		inflight: make(chan struct{}, 2),
	}
}

// configure answers plugin.configure. The host sends the manifest's defaults
// with the operator's config block merged over them; zero numeric fields fall
// back to the defaults here so an empty block still yields a working config.
func (a *app) configure(ctx context.Context, held config, dataDir string) error {
	cfg := defaultConfig()
	if held.Model != "" {
		cfg.Model = held.Model
	}
	if held.ModelURL != "" {
		cfg.ModelURL = held.ModelURL
	}
	if held.ModelSHA256 != "" {
		cfg.ModelSHA256 = held.ModelSHA256
	}
	if held.PrototypesURL != "" {
		cfg.PrototypesURL = held.PrototypesURL
	}
	if held.PrototypesSHA256 != "" {
		cfg.PrototypesSHA256 = held.PrototypesSHA256
	}
	cfg.resolve()
	if held.Threshold != 0 {
		cfg.Threshold = held.Threshold
	}
	if held.NMS != 0 {
		cfg.NMS = held.NMS
	}
	if held.AlertThreshold != 0 {
		cfg.AlertThreshold = held.AlertThreshold
	}
	if held.PauseThreshold != 0 {
		cfg.PauseThreshold = held.PauseThreshold
	}
	if held.Smoothing != 0 {
		cfg.Smoothing = held.Smoothing
	}
	if held.MinIntervalSeconds != 0 {
		cfg.MinIntervalSeconds = held.MinIntervalSeconds
	}
	if held.MaxFrameBytes != 0 {
		cfg.MaxFrameBytes = held.MaxFrameBytes
	}
	if held.TileSizePx != 0 {
		cfg.TileSizePx = held.TileSizePx
	}
	if held.TileOverlap != 0 {
		cfg.TileOverlap = held.TileOverlap
	}
	cfg.PauseOnFailure = held.PauseOnFailure
	cfg.DetectOnlyPrinting = held.DetectOnlyPrinting
	cfg.Enabled = held.Enabled
	if cfg.Threshold <= 0 || cfg.Threshold > 1 {
		return fmt.Errorf("failure-detector: threshold %v is outside (0, 1]", cfg.Threshold)
	}
	if cfg.Smoothing <= 0 || cfg.Smoothing > 1 {
		return fmt.Errorf("failure-detector: smoothing %v is outside (0, 1]", cfg.Smoothing)
	}
	if cfg.TileSizePx < 0 || cfg.TileSizePx > 8192 {
		return fmt.Errorf("failure-detector: tile_size_px %v is outside [0, 8192]", cfg.TileSizePx)
	}
	if cfg.TileOverlap < 0 || cfg.TileOverlap >= 0.95 {
		return fmt.Errorf("failure-detector: tile_overlap %v is outside [0, 0.95)", cfg.TileOverlap)
	}
	a.base.Store(cfg)
	a.dataDir = dataDir
	a.feederMu.Lock()
	a.store = newStore(dataDir)
	a.feederMu.Unlock()
	a.settings.held.Store(a.store.getPatch().effective(cfg))
	slog.InfoContext(ctx, "configured", "data_dir", dataDir, "threshold", cfg.Threshold,
		"alert_threshold", cfg.AlertThreshold, "pause_on_failure", cfg.PauseOnFailure)
	return nil
}

// setOverride merges a run-time patch from the UI and re-derives the effective
// config.
func (a *app) setOverride(p overridePatch) (overridePatch, error) {
	if a.store == nil {
		return overridePatch{}, fmt.Errorf("failure-detector: no data directory granted")
	}
	merged, err := a.store.setPatch(p)
	if err != nil {
		return merged, err
	}
	if v := a.base.Load(); v != nil {
		old := a.settings.load()
		cfg := merged.effective(v.(config))
		a.settings.held.Store(cfg)
		// A model or tiling change invalidates the cached detector.
		if old.Model != cfg.Model || old.TileSizePx != cfg.TileSizePx || old.TileOverlap != cfg.TileOverlap {
			a.detMu.Lock()
			a.detector = nil
			a.modelErr = ""
			a.modelBroken = false
			a.detMu.Unlock()
		}
	}
	return merged, nil
}

// watch subscribes to the event stream and dispatches events. It retries a
// refused or broken subscription after a second, like the guide's example.
func (a *app) watch(ctx context.Context, conn *wirerpc.Conn) {
	bus := busapi.NewClient(conn)
	files := storageapi.NewClient(conn)
	devices := deviceapi.NewClient(conn)

	go a.feedCameras(ctx, conn)

	params := busapi.SubscribeParams{Types: []string{
		event.TypeCameraSnapshotCaptured,
		event.TypeDeviceJobStarted,
		event.TypeDeviceJobSettled,
	}}

	for ctx.Err() == nil {
		err := busapi.Subscribe(ctx, conn, params, func(window busapi.Window) error {
			for _, raw := range window.Events {
				a.handleEvent(ctx, bus, files, devices, raw)
			}
			return nil
		})
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "subscribe ended", "error", err)
		}

		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
	}
}

// snapshotEvent is the data member of com.prusa.connect.camera.snapshot.captured.
type snapshotEvent struct {
	Camera  string    `json:"camera"`
	Printer string    `json:"printer,omitempty"`
	Storage string    `json:"storage"`
	Path    string    `json:"path"`
	Bytes   int64     `json:"bytes"`
	Taken   time.Time `json:"taken"`
}

func (a *app) handleEvent(ctx context.Context, bus *busapi.Client, files *storageapi.Client, devices *deviceapi.Client, raw json.RawMessage) {
	var env event.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.WarnContext(ctx, "an event could not be parsed", "error", err)
		return
	}

	switch env.Type {
	case event.TypeCameraSnapshotCaptured:
		var snap snapshotEvent
		if err := json.Unmarshal(env.Data, &snap); err != nil {
			slog.WarnContext(ctx, "a snapshot event could not be parsed", "error", err)
			return
		}
		printer := snap.Printer
		if printer == "" {
			printer = strings.TrimPrefix(env.Subject, "camera:")
		}
		go a.onSnapshot(ctx, bus, files, devices, printer, snap)

	case event.TypeDeviceJobStarted, event.TypeDeviceJobSettled:
		device := strings.TrimPrefix(env.Subject, "device:")
		a.setPrinting(device, env.Type == event.TypeDeviceJobStarted)
	}
}

// onSnapshot fetches the frame, runs detection and updates the printer state.
func (a *app) onSnapshot(ctx context.Context, bus *busapi.Client, files *storageapi.Client, devices *deviceapi.Client, printer string, snap snapshotEvent) {
	cfg := a.settings.load()

	if !cfg.Enabled {
		return
	}
	if cfg.DetectOnlyPrinting && !a.printingNow(ctx, devices, printer) {
		return
	}
	if snap.Bytes <= 0 || snap.Bytes > cfg.MaxFrameBytes {
		return
	}

	select {
	case a.inflight <- struct{}{}:
		defer func() { <-a.inflight }()
	case <-ctx.Done():
		return
	}

	det, err := a.getDetector(ctx, cfg)
	if err != nil {
		slog.WarnContext(ctx, "no detector available", "error", err)
		return
	}

	f, err := files.Open(ctx, storageapi.Target{Storage: snap.Storage, Path: snap.Path})
	if err != nil {
		slog.WarnContext(ctx, "a snapshot could not be opened", "storage", snap.Storage, "path", snap.Path, "error", err)
		return
	}
	buf := make([]byte, f.Size())
	if _, err := f.ReadAt(buf, 0); err != nil && err != io.EOF {
		slog.WarnContext(ctx, "a snapshot could not be read", "path", snap.Path, "error", err)
		return
	}

	// Skip a frame byte-identical to the previous one: some cameras
	// re-publish the same stored still, and re-scoring it would skew the
	// smoothed confidence and flood the history.
	sum := sha256.Sum256(buf)
	h := hex.EncodeToString(sum[:])
	a.mu.Lock()
	st := a.states[printer]
	if st == nil {
		st = &printerState{}
		a.states[printer] = st
	}
	if st.lastHash == h {
		st.dups++
		a.mu.Unlock()
		slog.InfoContext(ctx, "the same camera frame arrived again; skipping", "printer", printer, "path", snap.Path)
		return
	}
	st.lastHash = h
	a.mu.Unlock()

	if !a.throttle(printer, cfg) {
		return
	}

	img, _, err := image.Decode(bytesReader(buf))
	if err != nil {
		slog.WarnContext(ctx, "a snapshot is not a decodable image", "path", snap.Path, "error", err)
		return
	}

	dets, maxConf, err := det.detect(img, cfg.Threshold, cfg.NMS)
	if err != nil {
		slog.WarnContext(ctx, "detection failed", "error", err)
		return
	}

	alerting, paused := a.update(printer, maxConf, len(dets))

	if a.store != nil {
		a.store.record(historyEntry{
			Taken:        snap.Taken,
			Printer:      printer,
			Camera:       snap.Camera,
			MaxConf:      maxConf,
			Smoothed:     a.smoothedOf(printer),
			Detections:   len(dets),
			Alerting:     alerting,
			SnapshotPath: snap.Path,
		}, buf)
	}

	if alerting {
		payload := map[string]any{
			"printer":        printer,
			"camera":         snap.Camera,
			"smoothed":       a.smoothedOf(printer),
			"max_confidence": maxConf,
			"detections":     dets,
			"snapshot":       snap.Path,
			"taken":          snap.Taken.UTC(),
		}
		if err := bus.Emit(ctx, "alert", "printer/"+printer, payload); err != nil {
			slog.WarnContext(ctx, "an alert could not be published", "error", err)
		}
		slog.InfoContext(ctx, "failure detected", "printer", printer,
			"smoothed", a.smoothedOf(printer), "max_confidence", maxConf, "detections", len(dets))
	}

	if paused && cfg.PauseOnFailure {
		res, err := devices.Command(ctx, deviceapi.CommandParams{ID: printer, Verb: "core.command.pause_print"})
		if err != nil {
			slog.WarnContext(ctx, "the pause command failed", "printer", printer, "error", err)
		} else {
			slog.InfoContext(ctx, "print paused by failure-detector", "printer", printer, "ok", res.OK, "reason", res.MachineReason)
		}
		a.setPaused(printer, true)
	}
}

// getDetector loads the model once and caches it.
func (a *app) getDetector(ctx context.Context, cfg config) (*detector, error) {
	a.detMu.Lock()
	defer a.detMu.Unlock()
	if a.detector != nil {
		return a.detector, nil
	}
	if a.modelBroken {
		return nil, fmt.Errorf("%s", a.modelErr)
	}
	path, err := a.ensureModel(ctx, cfg)
	if err != nil {
		// A failed download is transient: report it in /status but retry on
		// the next frame instead of disabling detection until a restart.
		a.modelErr = err.Error()
		return nil, err
	}
	m, err := loadONNX(path)
	if err != nil {
		a.modelBroken = true
		a.modelErr = err.Error()
		return nil, err
	}
	a.model = m
	a.modelOn = true
	det := &detector{model: m, pg: cfg.Model == "printguard", tileSize: cfg.TileSizePx, overlap: cfg.TileOverlap}
	if det.pg {
		protoPath, err := a.ensurePrototypes(ctx, cfg)
		if err != nil {
			a.modelErr = err.Error()
			return nil, err
		}
		protos, err := loadPrototypes(protoPath)
		if err != nil {
			a.modelBroken = true
			a.modelErr = err.Error()
			return nil, err
		}
		det.prototypes = protos
	}
	a.detector = det
	a.modelErr = ""
	slog.InfoContext(ctx, "model loaded", "model", cfg.Model, "path", path, "nodes", len(m.nodes), "inputs", m.inputShape())
	return a.detector, nil
}

// ensurePrototypes downloads the PrintGuard prototype file next to the model.
func (a *app) ensurePrototypes(ctx context.Context, cfg config) (string, error) {
	dir := a.dataDir
	if dir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(cache, "failure-detector")
	}
	path := filepath.Join(dir, "prototypes.json")
	if _, err := os.Stat(path); err == nil {
		if cfg.PrototypesSHA256 != "" {
			if err := verifySHA256(path, cfg.PrototypesSHA256); err != nil {
				slog.WarnContext(ctx, "the cached prototypes do not match the configured sha256; downloading them again", "path", path, "error", err)
				if rmErr := os.Remove(path); rmErr != nil {
					return "", rmErr
				}
			} else {
				return path, nil
			}
		} else {
			return path, nil
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := downloadVerified(ctx, cfg.PrototypesURL, path, cfg.PrototypesSHA256); err != nil {
		return "", err
	}
	return path, nil
}

// ensureModel returns the local path of the model, downloading and verifying
// it into the module's data directory when needed.
func (a *app) ensureModel(ctx context.Context, cfg config) (string, error) {
	if cfg.ModelURL == "" {
		return "", fmt.Errorf("model_url is not set; configure it in the instance's config block (see GUIDE.md)")
	}
	dir := a.dataDir
	if dir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(cache, "failure-detector")
	}
	path := filepath.Join(dir, "model.onnx")
	if _, err := os.Stat(path); err == nil {
		if err := verifySHA256(path, cfg.ModelSHA256); err != nil {
			// The cached file belongs to another model (e.g. the user
			// switched from obico to printguard): drop it and re-download.
			slog.WarnContext(ctx, "the cached model does not match the configured sha256; downloading it again", "path", path, "error", err)
			if rmErr := os.Remove(path); rmErr != nil {
				return "", rmErr
			}
		} else {
			return path, nil
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := downloadVerified(ctx, cfg.ModelURL, path, cfg.ModelSHA256); err != nil {
		return "", err
	}
	return path, nil
}

// downloadVerified fetches url into path (via a .part sibling) and checks the
// sha256 when one is configured.
func downloadVerified(ctx context.Context, url, path, sha string) error {
	slog.InfoContext(ctx, "downloading", "url", url, "path", path)
	req, err := newGet(ctx, url)
	if err != nil {
		return err
	}
	defer req.Close()
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, req); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if sha != "" {
		if err := verifySHA256(tmp, sha); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return os.Rename(tmp, path)
}

// printerState is the per-printer prediction state.
type printerState struct {
	printing  bool
	known     bool // printing was confirmed by a job event or a jobs query
	frames    int64
	detections int64
	maxConf   float64
	smoothed  float64
	alerting  bool
	paused    bool
	dups      int64  // frames skipped because they were byte-identical to the previous one
	lastHash  string // sha256 of the last processed frame, for dedup
	lastFrame time.Time
	lastCam   time.Time
}

func (a *app) state(printer string) *printerState {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.states[printer]
	if !ok {
		s = &printerState{}
		a.states[printer] = s
	}
	return s
}

func (a *app) setPrinting(printer string, printing bool) {
	s := a.state(printer)
	a.mu.Lock()
	s.printing = printing
	s.known = true
	if !printing {
		s.paused = false
	}
	a.mu.Unlock()
}

func (a *app) isPrinting(printer string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.states[printer]
	return ok && s.known && s.printing
}

// printingNow reports whether the printer is currently printing and not
// paused. The state is read from the host's device view on every call, so a
// pause (state PAUSED) stops detection immediately; if that read fails, the
// job-event view is the fallback, which is seeded lazily because the module
// may have started mid-print, when no job.started event will arrive.
func (a *app) printingNow(ctx context.Context, devices *deviceapi.Client, printer string) bool {
	dev, err := devices.Get(ctx, deviceapi.GetParams{ID: printer})
	if err == nil {
		if raw, ok := dev.State["/state"]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				printing := s == "PRINTING"
				a.mu.Lock()
				st := a.states[printer]
				if st == nil {
					st = &printerState{}
					a.states[printer] = st
				}
				st.printing = printing
				st.known = true
				a.mu.Unlock()
				return printing
			}
		}
	}
	if err != nil {
		slog.WarnContext(ctx, "the printer's state could not be read", "printer", printer, "error", err)
	}
	a.mu.Lock()
	s, ok := a.states[printer]
	known := ok && s.known
	printing := ok && s.printing
	a.mu.Unlock()
	if known {
		return printing
	}
	jobs, jerr := devices.Jobs(ctx, deviceapi.JobsParams{ID: printer})
	if jerr != nil {
		slog.WarnContext(ctx, "the printer's jobs could not be read", "printer", printer, "error", jerr)
		return false
	}
	printing = jobs.Current != nil && jobs.Current.Ended == nil
	a.setPrinting(printer, printing)
	return printing
}

// throttle reports whether a frame of this camera may be processed now.
func (a *app) throttle(printer string, cfg config) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.states[printer]
	if !ok {
		s = &printerState{}
		a.states[printer] = s
	}
	if time.Since(s.lastFrame) < time.Duration(cfg.MinIntervalSeconds)*time.Second {
		return false
	}
	s.lastFrame = time.Now()
	return true
}

// update folds one frame's max confidence into the EWMA and reports whether
// the alert and pause thresholds are crossed.
func (a *app) update(printer string, maxConf float64, nDets int) (alerting, paused bool) {
	cfg := a.settings.load()
	s := a.state(printer)
	a.mu.Lock()
	defer a.mu.Unlock()
	s.frames++
	s.detections += int64(nDets)
	if maxConf > s.maxConf {
		s.maxConf = maxConf
	}
	if s.frames == 1 {
		s.smoothed = maxConf
	} else {
		s.smoothed = cfg.Smoothing*maxConf + (1-cfg.Smoothing)*s.smoothed
	}
	s.alerting = s.smoothed >= cfg.AlertThreshold
	paused = cfg.PauseOnFailure && s.smoothed >= cfg.PauseThreshold && !s.paused
	if paused {
		s.paused = true
	}
	return s.alerting, paused
}

func (a *app) setPaused(printer string, v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.states[printer]; ok {
		s.paused = v
	}
}

func (a *app) smoothedOf(printer string) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.states[printer]; ok {
		return s.smoothed
	}
	return 0
}
