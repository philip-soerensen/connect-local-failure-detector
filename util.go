package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
)

// bytesReader adapts a byte slice to io.Reader without copying.
func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// newGet opens a GET request whose body the caller must close.
func newGet(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("failure-detector: download %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

// verifySHA256 checks a file against a hex digest. An empty want refuses the
// file: the digest is the only thing that fixes which bytes arrive.
func verifySHA256(path, want string) error {
	if want == "" {
		return fmt.Errorf("failure-detector: model_sha256 is not set; refusing to load %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("failure-detector: %s: sha256 %s does not match the configured %s", path, got, want)
	}
	return nil
}
