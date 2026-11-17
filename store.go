package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// overridePatch is the part of the settings the Spaghetti tab on a printer's
// page may flip at run time, without a restart of the instance. It is kept in
// the module's data directory and laid over the host-delivered config.
type overridePatch struct {
	Enabled            *bool    `json:"enabled,omitempty"`
	PauseOnFailure     *bool    `json:"pause_on_failure,omitempty"`
	MinIntervalSeconds *int     `json:"min_interval_seconds,omitempty"`
	AlertThreshold     *float64 `json:"alert_threshold,omitempty"`
	PauseThreshold     *float64 `json:"pause_threshold,omitempty"`
	Model              *string  `json:"model,omitempty"`
	TileSizePx         *int     `json:"tile_size_px,omitempty"`
	TileOverlap        *float64 `json:"tile_overlap,omitempty"`
}

// effective overlays the patch on the host-delivered config.
func (p overridePatch) effective(cfg config) config {
	if p.Enabled != nil {
		cfg.Enabled = *p.Enabled
	}
	if p.PauseOnFailure != nil {
		cfg.PauseOnFailure = *p.PauseOnFailure
	}
	if p.MinIntervalSeconds != nil {
		cfg.MinIntervalSeconds = *p.MinIntervalSeconds
	}
	if p.AlertThreshold != nil {
		cfg.AlertThreshold = *p.AlertThreshold
	}
	if p.PauseThreshold != nil {
		cfg.PauseThreshold = *p.PauseThreshold
	}
	if p.Model != nil {
		cfg.Model = *p.Model
	}
	if p.TileSizePx != nil {
		cfg.TileSizePx = *p.TileSizePx
	}
	if p.TileOverlap != nil {
		cfg.TileOverlap = *p.TileOverlap
	}
	return cfg
}

const (
	historyFile   = "history.jsonl"
	feedersFile   = "feeders.json"
	framesDir     = "frames"
	maxHistoryLen = 10
	maxFrames     = 10
)

// feederConf is the per-printer camera source the built-in feeder uses:
// push JPEG frames grabbed from rtsp_url (via system ffmpeg) or fetched from
// snapshot_url (HTTP) to the named camera. Per printer, because the camera is
// the printer's, not the module's.
type feederConf struct {
	Camera      string `json:"camera"`
	RTSPURL     string `json:"rtsp_url,omitempty"`
	SnapshotURL string `json:"snapshot_url,omitempty"`
	User        string `json:"user,omitempty"`
	Token       string `json:"token,omitempty"`
}

// publicFeeder is what GET /feeders may answer with: the camera source
// without the secret. The token is never sent back, only whether one is set.
type publicFeeder struct {
	Camera      string `json:"camera"`
	RTSPURL     string `json:"rtsp_url,omitempty"`
	SnapshotURL string `json:"snapshot_url,omitempty"`
	User        string `json:"user,omitempty"`
	TokenSet    bool   `json:"token_set,omitempty"`
}

func (c feederConf) public() publicFeeder {
	return publicFeeder{
		Camera:      c.Camera,
		RTSPURL:     c.RTSPURL,
		SnapshotURL: c.SnapshotURL,
		User:        c.User,
		TokenSet:    c.Token != "",
	}
}

var frameNameOK = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// historyEntry is one scored camera frame.
type historyEntry struct {
	Taken        time.Time `json:"taken"`
	Printer      string    `json:"printer"`
	Camera       string    `json:"camera"`
	MaxConf      float64   `json:"max_confidence"`
	Smoothed     float64   `json:"smoothed"`
	Detections   int       `json:"detections"`
	Alerting     bool      `json:"alerting"`
	Frame        string    `json:"frame,omitempty"`
	SnapshotPath string    `json:"snapshot"`
}

// store keeps the run-time overrides, the per-printer feeder config and the
// frame history in the module's data directory.
type store struct {
	dir string

	mu      sync.Mutex
	patch   overridePatch
	feeders map[string]feederConf // printer id -> camera source
	history []historyEntry
	seq     int64 // makes frame file names unique even for repeated snapshots
}

func newStore(dataDir string) *store {
	s := &store{dir: dataDir}
	if dataDir != "" {
		s.load()
	}
	return s
}

func (s *store) load() {
	if s.dir == "" {
		return
	}
	if raw, err := os.ReadFile(filepath.Join(s.dir, "runtime.json")); err == nil {
		_ = json.Unmarshal(raw, &s.patch)
	}
	if raw, err := os.ReadFile(filepath.Join(s.dir, historyFile)); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e historyEntry
			if json.Unmarshal([]byte(line), &e) == nil {
				s.history = append(s.history, e)
				// Recover the frame sequence counter so names never restart
				// after a restart (a restarted seq would sort before older
				// frames and get the newest files pruned first).
				if n := frameSeq(e.Frame); n > s.seq {
					s.seq = n
				}
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(s.dir, feedersFile)); err == nil {
		_ = json.Unmarshal(raw, &s.feeders)
	}
	if s.feeders == nil {
		s.feeders = map[string]feederConf{}
	}
	// Drop history entries whose frame file is gone (pruned), so the tab
	// does not render dead image links.
	kept := s.history[:0]
	for _, e := range s.history {
		if e.Frame != "" {
			if _, err := os.Stat(filepath.Join(s.dir, e.Frame)); err != nil {
				continue
			}
		}
		kept = append(kept, e)
	}
	s.history = kept
	// Recover the frame sequence counter from the files on disk as well, so
	// names never restart even if the history was pruned.
	if entries, err := os.ReadDir(filepath.Join(s.dir, framesDir)); err == nil {
		for _, e := range entries {
			if n := frameSeq(framesDir + "/" + e.Name()); n > s.seq {
				s.seq = n
			}
		}
	}
}

// allFeeders returns a copy of every configured printer feeder.
func (s *store) allFeeders() map[string]feederConf {
	if s == nil {
		return map[string]feederConf{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]feederConf, len(s.feeders))
	for k, v := range s.feeders {
		out[k] = v
	}
	return out
}

// feeder returns the camera source configured for one printer.
func (s *store) feeder(printer string) (feederConf, bool) {
	if s == nil {
		return feederConf{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.feeders[printer]
	return c, ok
}

// setFeeder stores the camera source for one printer and persists it.
func (s *store) setFeeder(printer string, c feederConf) error {
	if s == nil || s.dir == "" {
		return fmt.Errorf("failure-detector: no data directory granted")
	}
	s.mu.Lock()
	if c.Camera == "" && c.RTSPURL == "" && c.SnapshotURL == "" {
		delete(s.feeders, printer)
	} else {
		s.feeders[printer] = c
	}
	raw, err := json.Marshal(s.feeders)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, feedersFile), raw, 0o600)
}

func (s *store) savePatch() error {
	if s.dir == "" {
		return nil
	}
	raw, err := json.Marshal(s.patch)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "runtime.json"), raw, 0o600)
}

// setPatch merges a patch from the UI and persists it. It returns the merged
// patch.
func (s *store) setPatch(p overridePatch) (overridePatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Enabled != nil {
		s.patch.Enabled = p.Enabled
	}
	if p.PauseOnFailure != nil {
		s.patch.PauseOnFailure = p.PauseOnFailure
	}
	if p.MinIntervalSeconds != nil {
		if *p.MinIntervalSeconds < 0 {
			return s.patch, fmt.Errorf("min_interval_seconds must be >= 0")
		}
		s.patch.MinIntervalSeconds = p.MinIntervalSeconds
	}
	if p.AlertThreshold != nil {
		if *p.AlertThreshold <= 0 || *p.AlertThreshold > 1 {
			return s.patch, fmt.Errorf("alert_threshold must be in (0, 1]")
		}
		s.patch.AlertThreshold = p.AlertThreshold
	}
	if p.PauseThreshold != nil {
		if *p.PauseThreshold <= 0 || *p.PauseThreshold > 1 {
			return s.patch, fmt.Errorf("pause_threshold must be in (0, 1]")
		}
		s.patch.PauseThreshold = p.PauseThreshold
	}
	if p.Model != nil {
		switch *p.Model {
		case "printguard", "obico":
			s.patch.Model = p.Model
		default:
			return s.patch, fmt.Errorf("model must be printguard or obico")
		}
	}
	if p.TileSizePx != nil {
		if *p.TileSizePx < 0 || *p.TileSizePx > 8192 {
			return s.patch, fmt.Errorf("tile_size_px must be in [0, 8192]")
		}
		s.patch.TileSizePx = p.TileSizePx
	}
	if p.TileOverlap != nil {
		if *p.TileOverlap < 0 || *p.TileOverlap >= 0.95 {
			return s.patch, fmt.Errorf("tile_overlap must be in [0, 0.95)")
		}
		s.patch.TileOverlap = p.TileOverlap
	}
	if err := s.savePatch(); err != nil {
		return s.patch, err
	}
	return s.patch, nil
}

func (s *store) getPatch() overridePatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.patch
}

// baseModelFile records the model the host delivered in its config block, so a
// change made on the global settings page can be told apart from a restart.
const baseModelFile = "basemodel.txt"

// noteBaseModel records the host-delivered model. If it differs from the last
// recorded one, the operator changed the global option, so the run-time
// override from the Spaghetti tab is cleared and the global value wins —
// model is one option, settable from either page, last writer wins.
func (s *store) noteBaseModel(model string) {
	if s.dir == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, _ := os.ReadFile(filepath.Join(s.dir, baseModelFile))
	if len(prev) > 0 && string(prev) != model {
		s.patch.Model = nil
		_ = s.savePatch()
	}
	_ = os.WriteFile(filepath.Join(s.dir, baseModelFile), []byte(model), 0o600)
}

// record appends one scored frame to the history and saves its JPEG. The
// frame file is named so the names sort by time.
func (s *store) record(e historyEntry, jpeg []byte) {
	if s.dir == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(jpeg) > 0 {
		s.seq++
		name := fmt.Sprintf("%06d_%d_%s.jpg", s.seq, e.Taken.UnixNano(), sanitize(e.Printer))
		_ = os.MkdirAll(filepath.Join(s.dir, framesDir), 0o700)
		if os.WriteFile(filepath.Join(s.dir, framesDir, name), jpeg, 0o600) == nil {
			e.Frame = framesDir + "/" + name
		}
		s.pruneFramesLocked()
	}

	s.history = append(s.history, e)
	if len(s.history) > maxHistoryLen {
		s.history = s.history[len(s.history)-maxHistoryLen:]
	}
	// Rewrite the JSONL whole; the file is small and this keeps it parseable.
	var b strings.Builder
	for _, h := range s.history {
		raw, err := json.Marshal(h)
		if err != nil {
			continue
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	_ = os.WriteFile(filepath.Join(s.dir, historyFile), []byte(b.String()), 0o600)
}

// frameSeq extracts the sequence number from a frame name
// ("frames/000123_1794..._printer.jpg" -> 123), or 0.
func frameSeq(frame string) int64 {
	base := strings.TrimPrefix(frame, framesDir+"/")
	if i := strings.IndexByte(base, '_'); i > 0 {
		if n, err := strconv.ParseInt(base[:i], 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// frameNano extracts the unix-nano timestamp from a frame name, or 0. Frame
// names are "<seq>_<nano>_<printer>.jpg"; the seq restarts after a module
// restart, so time order must come from the nano field, not the name.
func frameNano(name string) int64 {
	parts := strings.SplitN(name, "_", 3)
	if len(parts) == 3 {
		if n, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// pruneFramesLocked deletes the oldest frame files past the cap, oldest by
// the timestamp embedded in the name.
func (s *store) pruneFramesLocked() {
	dir := filepath.Join(s.dir, framesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jpg") {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool {
		ni, nj := frameNano(names[i]), frameNano(names[j])
		if ni != nj {
			return ni < nj
		}
		return names[i] < names[j]
	})
	for len(names) > maxFrames {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// listHistory returns the newest entries first, optionally narrowed to one
// printer.
func (s *store) listHistory(printer string) []historyEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []historyEntry
	for i := len(s.history) - 1; i >= 0; i-- {
		e := s.history[i]
		if printer == "" || e.Printer == printer {
			out = append(out, e)
		}
	}
	return out
}

// framePath resolves a frame name from the URL to a file inside the frames
// directory, refusing anything that escapes it.
func (s *store) framePath(name string) (string, error) {
	if s.dir == "" || !frameNameOK.MatchString(name) || strings.Contains(name, "..") {
		return "", fmt.Errorf("bad frame name")
	}
	p := filepath.Join(s.dir, framesDir, name)
	if filepath.Dir(p) != filepath.Join(s.dir, framesDir) {
		return "", fmt.Errorf("bad frame name")
	}
	return p, nil
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
}
