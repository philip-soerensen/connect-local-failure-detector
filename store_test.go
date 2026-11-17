package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreHistoryAndFrames(t *testing.T) {
	dir := t.TempDir()
	s := newStore(dir)
	s.record(historyEntry{Taken: time.Unix(1000, 0), Printer: "p1", Camera: "cam", MaxConf: 0.9, Smoothed: 0.9, Detections: 2, Alerting: true, SnapshotPath: "snap/1.jpg"}, []byte("jpeg1"))
	s.record(historyEntry{Taken: time.Unix(2000, 0), Printer: "p2", Camera: "cam", MaxConf: 0.01, Smoothed: 0.01, Detections: 0, SnapshotPath: "snap/2.jpg"}, []byte("jpeg2"))

	all := s.listHistory("")
	if len(all) != 2 || all[0].Printer != "p2" { // newest first
		t.Fatalf("history order wrong: %+v", all)
	}
	one := s.listHistory("p1")
	if len(one) != 1 || one[0].Alerting != true {
		t.Fatalf("printer filter wrong: %+v", one)
	}
	frame := one[0].Frame
	if frame == "" {
		t.Fatal("no frame recorded")
	}
	name := filepath.Base(frame)
	p, err := s.framePath(name)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "jpeg1" {
		t.Fatalf("frame bytes wrong: %v %q", err, b)
	}
	if _, err := s.framePath("../evil.jpg"); err == nil {
		t.Fatal("path escape not refused")
	}

	// patch merge + effective
	b := true
	iv := 7
	if _, err := s.setPatch(overridePatch{Enabled: &b, MinIntervalSeconds: &iv}); err != nil {
		t.Fatal(err)
	}
	cfg := s.getPatch().effective(defaultConfig())
	if !cfg.Enabled || cfg.MinIntervalSeconds != 7 || cfg.PauseOnFailure != false {
		t.Fatalf("effective wrong: %+v", cfg)
	}
	// reload from disk
	s2 := newStore(dir)
	if s2.getPatch().Enabled == nil || *s2.getPatch().Enabled != true {
		t.Fatal("patch not persisted")
	}
	if len(s2.listHistory("")) != 2 {
		t.Fatal("history not persisted")
	}
}

// TestPruneKeepsNewestAfterSeqReset guards the bug where a restarted seq made
// new frame names sort before old ones and pruning deleted the newest frames.
func TestPruneKeepsNewestAfterSeqReset(t *testing.T) {
	dir := t.TempDir()
	s := newStore(dir)
	// Simulate a pre-restart store with 10 frames, seq 5..14.
	for i := 5; i <= 14; i++ {
		name := fmt.Sprintf("%06d_%d_%s.jpg", i, int64(1000+i), "p")
		if err := os.MkdirAll(filepath.Join(dir, framesDir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, framesDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// After a "restart", seq recovered from history would be 14; simulate a
	// buggy reset by recording with seq forced low, nano newer than all:
	s.seq = 0
	s.record(historyEntry{Taken: time.Unix(0, 2000), Printer: "p"}, []byte("new"))
	// 11 files, cap 10: the oldest by NANO (the seq-5 file, nano 1005) must go.
	entries, _ := os.ReadDir(filepath.Join(dir, framesDir))
	if len(entries) != maxFrames {
		t.Fatalf("want %d frames, have %d", maxFrames, len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, framesDir, "000005_1005_p.jpg")); !os.IsNotExist(err) {
		t.Fatal("oldest-by-time frame was not pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, framesDir, "000001_2000_p.jpg")); err != nil {
		t.Fatal("newest frame (restarted seq) was pruned - the bug")
	}
	// seq recovery on load
	s2 := newStore(dir)
	if s2.seq < 14 {
		t.Fatalf("seq not recovered from history/frames: %d", s2.seq)
	}
}

// TestSetPatchModel guards the dropdown snap-back: model must be persisted in
// the patch (not just applied in memory), or a re-configure reverts it.
func TestSetPatchModel(t *testing.T) {
	dir := t.TempDir()
	s := newStore(dir)
	m := "printguard"
	merged, err := s.setPatch(overridePatch{Model: &m})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Model == nil || *merged.Model != "printguard" {
		t.Fatalf("model not merged: %+v", merged)
	}
	if s2 := newStore(dir); s2.getPatch().Model == nil || *s2.getPatch().Model != "printguard" {
		t.Fatal("model not persisted across reload")
	}
	bad := "evil"
	if _, err := s.setPatch(overridePatch{Model: &bad}); err == nil {
		t.Fatal("invalid model accepted")
	}
}

// TestNoteBaseModel: a changed global model clears the tab override; an
// unchanged one (startup) keeps it.
func TestNoteBaseModel(t *testing.T) {
	dir := t.TempDir()
	s := newStore(dir)
	m := "obico"
	if _, err := s.setPatch(overridePatch{Model: &m}); err != nil {
		t.Fatal(err)
	}
	s.noteBaseModel("printguard") // first run records base, keeps tab override
	if s.getPatch().Model == nil {
		t.Fatal("tab override cleared on first record")
	}
	s.noteBaseModel("printguard") // restart, unchanged: keep override
	if s.getPatch().Model == nil {
		t.Fatal("tab override cleared on unchanged restart")
	}
	s.noteBaseModel("obico") // global page changed to obico: clear override
	if s.getPatch().Model != nil {
		t.Fatal("tab override not cleared after global change")
	}
	s2 := newStore(dir)
	if s2.getPatch().Model != nil {
		t.Fatal("cleared override not persisted")
	}
}
