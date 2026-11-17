package main

import (
	"context"
	"testing"
)

// TestFFmpegGrabRefusesNonRTSP guards the URL check: only rtsp:// and rtsps://
// reach ffmpeg, so file:/http:/concat: tricks are refused before exec.
func TestFFmpegGrabRefusesNonRTSP(t *testing.T) {
	for _, u := range []string{
		"file:///etc/passwd",
		"http://192.168.1.100/live",
		"concat:file:///etc/passwd",
		"/etc/passwd",
		"rtsp://", // no host
	} {
		if _, err := ffmpegGrab(context.Background(), u); err == nil {
			t.Fatalf("accepted %q", u)
		}
	}
}

// TestDigestGetHostAllowlist: snapshot fetches go only to declared camera hosts.
func TestDigestGetHostAllowlist(t *testing.T) {
	allowed := map[string]bool{"192.168.1.100": true}
	if _, err := digestGet(context.Background(), "http://evil.example/snap", "maker", "x", allowed, 1<<20); err == nil {
		t.Fatal("undeclared host accepted")
	}
	if _, err := digestGet(context.Background(), "file:///etc/passwd", "maker", "x", allowed, 1<<20); err == nil {
		t.Fatal("file: url accepted")
	}
}
