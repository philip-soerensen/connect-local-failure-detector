package main

import (
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
