package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

// storePatch is the persisted run-time override, or an empty patch.
func (a *app) storePatch() overridePatch {
	if a.store == nil {
		return overridePatch{}
	}
	return a.store.getPatch()
}

// routes is the module's HTTP surface, mounted by the host at
// /api/v1/modules/<namespace>/.
func (a *app) routes(conn *wirerpc.Conn) http.Handler {
	if conn != nil {
		a.access = accessapi.NewClient(conn)
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		cfg := a.settings.load()
		a.detMu.Lock()
		model := "not loaded"
		if a.detector != nil {
			model = cfg.Model + " loaded"
		} else if a.modelErr != "" {
			model = a.modelErr
		}
		a.detMu.Unlock()
		a.mu.Lock()
		printers := map[string]any{}
		for id, s := range a.states {
			var lastFrame string
			if !s.lastFrame.IsZero() {
				lastFrame = s.lastFrame.UTC().Format(time.RFC3339)
			}
			printers[id] = map[string]any{
				"printing":         s.printing,
				"frames":           s.frames,
				"detections":       s.detections,
				"max_confidence":   s.maxConf,
				"smoothed":         s.smoothed,
				"alerting":         s.alerting,
				"paused_by_module": s.paused,
				"duplicate_frames": s.dups,
				"last_frame_at":    lastFrame,
			}
		}
		// ?printer=<id> asks about one printer even before any frame arrived.
		if id := r.URL.Query().Get("printer"); id != "" {
			if _, ok := printers[id]; !ok {
				printers[id] = map[string]any{}
			}
		}
		a.mu.Unlock()

		// Ask the host whether this module may operate each printer (pause on
		// failure needs it); the Spaghetti tab shows an error when it may not.
		for id, entry := range printers {
			m := entry.(map[string]any)
			allowed := false
			if a.access != nil {
				decision, err := a.access.Check(r.Context(), accessapi.CheckParams{
					Action:     "operate",
					ObjectType: "device",
					ObjectID:   id,
				})
				if err == nil {
					allowed = decision.Allowed
				}
			}
			m["can_operate"] = allowed
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":          model,
			"detect_enabled": cfg.Enabled,
			"printers":       printers,
			"asked_by":       routesapi.PrincipalOf(r),
		})
	})

	// GET /settings is what the Spaghetti tab reads: the effective
	// run-time-relevant settings plus the persisted overrides.
	mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		cfg := a.settings.load()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled":              cfg.Enabled,
			"pause_on_failure":     cfg.PauseOnFailure,
			"min_interval_seconds": cfg.MinIntervalSeconds,
			"detect_only_printing": cfg.DetectOnlyPrinting,
			"alert_threshold":      cfg.AlertThreshold,
			"pause_threshold":      cfg.PauseThreshold,
			"threshold":            cfg.Threshold,
			"smoothing":            cfg.Smoothing,
			"model":                cfg.Model,
			"tile_size_px":         cfg.TileSizePx,
			"tile_overlap":         cfg.TileOverlap,
			"overrides":            a.storePatch(),
			"asked_by":             routesapi.PrincipalOf(r),
		})
	})

	// POST /settings flips run-time settings from the tab and persists them in
	// the module's data directory.
	mux.HandleFunc("POST /settings", func(w http.ResponseWriter, r *http.Request) {
		var p overridePatch
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
			return
		}
		merged, err := a.setOverride(p)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		cfg := a.settings.load()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":                   true,
			"enabled":              cfg.Enabled,
			"pause_on_failure":     cfg.PauseOnFailure,
			"min_interval_seconds": cfg.MinIntervalSeconds,
			"alert_threshold":      cfg.AlertThreshold,
			"pause_threshold":      cfg.PauseThreshold,
			"model":                cfg.Model,
			"tile_size_px":         cfg.TileSizePx,
			"tile_overlap":         cfg.TileOverlap,
			"overrides":            merged,
		})
	})

	// GET /feeders?printer=<id> is the per-printer camera source the built-in
	// feeder uses (the camera belongs to the printer, not the module).
	mux.HandleFunc("GET /feeders", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		out := map[string]any{}
		if id := r.URL.Query().Get("printer"); id != "" {
			if conf, ok := a.store.feeder(id); ok {
				out[id] = conf
			}
		} else if a.store != nil {
			for id, conf := range a.store.allFeeders() {
				out[id] = conf
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"feeders": out, "asked_by": routesapi.PrincipalOf(r)})
	})

	// POST /feeders sets the camera source for one printer and starts (or
	// stops) its feeder. Empty camera+urls clears it.
	mux.HandleFunc("POST /feeders", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Printer     string `json:"printer"`
			feederConf
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
			return
		}
		if body.Printer == "" || body.Camera == "" || (body.RTSPURL == "" && body.SnapshotURL == "") {
			http.Error(w, `{"error":"need printer, camera and rtsp_url or snapshot_url (all empty clears)"}`, http.StatusBadRequest)
			return
		}
		if err := a.setFeeder(r.Context(), body.Printer, body.feederConf); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "printer": body.Printer, "feeder": body.feederConf})
	})

	// GET /history?printer=<id> lists scored frames, newest first.
	mux.HandleFunc("GET /history", func(w http.ResponseWriter, r *http.Request) {
		printer := r.URL.Query().Get("printer")
		var entries []historyEntry
		if a.store != nil {
			entries = a.store.listHistory(printer)
		}
		if entries == nil {
			entries = []historyEntry{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": entries})
	})

	// GET /frames/{name} serves one saved frame JPEG.
	mux.HandleFunc("GET /frames/{name}", func(w http.ResponseWriter, r *http.Request) {
		if a.store == nil {
			http.Error(w, "no data directory", http.StatusServiceUnavailable)
			return
		}
		p, err := a.store.framePath(r.PathValue("name"))
		if err != nil {
			http.Error(w, "bad frame name", http.StatusBadRequest)
			return
		}
		http.ServeFile(w, r, p)
	})

	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		cfg := a.settings.load()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config":   cfg,
			"asked_by": routesapi.PrincipalOf(r),
		})
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	return mux
}
