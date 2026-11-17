package main

import (
	"sync/atomic"
)

// config is the effective settings: the manifest defaults with the operator's
// config block written over them, delivered on plugin.configure.
type config struct {
	Enabled            bool    `json:"enabled"`
	Model              string  `json:"model"` // "printguard" (default) or "obico"
	ModelURL           string  `json:"model_url"`
	ModelSHA256        string  `json:"model_sha256"`
	PrototypesURL      string  `json:"prototypes_url"`
	PrototypesSHA256   string  `json:"prototypes_sha256"`
	Threshold          float64 `json:"threshold"`
	NMS                float64 `json:"nms"`
	AlertThreshold     float64 `json:"alert_threshold"`
	PauseThreshold     float64 `json:"pause_threshold"`
	Smoothing          float64 `json:"smoothing"`
	PauseOnFailure     bool    `json:"pause_on_failure"`
	DetectOnlyPrinting bool    `json:"detect_only_printing"`
	MinIntervalSeconds int     `json:"min_interval_seconds"`
	MaxFrameBytes      int64   `json:"max_frame_bytes"`
	// TileSizePx tiles the frame into squares of this many source pixels
	// before the 256 px downscale (printguard only). 0 = the frame's short
	// side, which is what upstream PrintGuard does. Smaller tiles raise the
	// effective resolution (more encoder runs per frame) but leave the
	// model's training distribution.
	TileSizePx int `json:"tile_size_px"`
	// TileOverlap is the minimum overlap fraction between adjacent tiles.
	TileOverlap float64 `json:"tile_overlap"`
}

// builtinModels are the reference models the module can download when
// model_url/prototypes_url are not set. "printguard" is the default: a ~5 MB
// ShuffleNetV2 encoder + prototype classifier (GPL-2.0 project, weights fetched
// at runtime), far lighter than Obico's ~200 MB detector (AGPL-3.0).
var builtinModels = map[string]struct {
	url, sha         string
	prototypesURL    string
	prototypesSHA256 string
}{
	"printguard": {
		url:              "https://raw.githubusercontent.com/oliverbravery/PrintGuard/3310b30c87dd3d4b8bd0695cb05c0064c47b68d7/models/encoder_float32.onnx",
		sha:              "853f8653483ba062c6c2f5779869a2898f4f8fe6d4e0974f5f131354462b3f22",
		prototypesURL:    "https://raw.githubusercontent.com/oliverbravery/PrintGuard/3310b30c87dd3d4b8bd0695cb05c0064c47b68d7/models/prototypes.json",
		prototypesSHA256: "06467c395a7547b45dab75bd9ff4d3a4d4291391bd116353b5815e7f6829264a",
	},
	"obico": {
		url: "https://www.obico.io/static_pub/ml-models/model-weights-5a6b1be1fa.onnx",
		sha: "0a6ebd8e30dbf6a450c50f9c0a5406f04ba7eb1c99fd5996e888c78bb383b9aa",
	},
}

// resolve fills unset URL/sha fields from the selected builtin model.
func (c *config) resolve() {
	if c.Model == "" {
		c.Model = "obico"
	}
	b, ok := builtinModels[c.Model]
	if !ok {
		c.Model = "obico"
		b = builtinModels["obico"]
	}
	if c.ModelURL == "" {
		c.ModelURL = b.url
	}
	if c.ModelSHA256 == "" {
		c.ModelSHA256 = b.sha
	}
	if c.Model == "printguard" {
		if c.PrototypesURL == "" {
			c.PrototypesURL = b.prototypesURL
		}
		if c.PrototypesSHA256 == "" {
			c.PrototypesSHA256 = b.prototypesSHA256
		}
	}
}

func defaultConfig() config {
	return config{
		Enabled:            true,
		Model:              "obico",
		Threshold:          0.08,
		NMS:                0.45,
		AlertThreshold:     0.2,
		PauseThreshold:     0.4,
		Smoothing:          0.4,
		PauseOnFailure:     false,
		DetectOnlyPrinting: true,
		MinIntervalSeconds: 30,
		MaxFrameBytes:      8 << 20,
		TileSizePx:         0,
		TileOverlap:        0.2,
	}
}

// settingsStore holds the config atomically so routes and the event loop read
// the same value.
type settingsStore struct {
	held atomic.Value // config
}

func (s *settingsStore) load() config {
	if v := s.held.Load(); v != nil {
		return v.(config)
	}
	return defaultConfig()
}


