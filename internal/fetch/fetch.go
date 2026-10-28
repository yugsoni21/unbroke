// Package fetch downloads specifications with a disk cache.
//
// Specs are large (Stripe's is 8 MB, Cloudflare's 28 MB) and providers host
// them for free, so Mend caches every download and sends a descriptive
// User-Agent. A rerun inside the cache window costs no network traffic.
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DefaultMaxBytes caps a single download. The largest real spec seen so far
// is 45 MB.
const DefaultMaxBytes = 120 << 20

// Fetcher downloads URLs and remembers them on disk.
type Fetcher struct {
	// Dir is the cache directory. Empty disables caching.
	Dir string
	// TTL is how long a cached copy stays fresh. Zero means forever.
	TTL      time.Duration
	MaxBytes int64
	Client   *http.Client
}

// New returns a Fetcher caching under dir (created on first write).
func New(dir string, ttl time.Duration) *Fetcher {
	return &Fetcher{
		Dir:      dir,
		TTL:      ttl,
		MaxBytes: DefaultMaxBytes,
		Client:   &http.Client{Timeout: 3 * time.Minute},
	}
}

// DefaultDir is the per-user cache location for downloaded specs.
func DefaultDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mend", "specs"), nil
}

// Get returns the body of url, from the cache when fresh.
func (f *Fetcher) Get(ctx context.Context, url string) ([]byte, error) {
	path := f.cachePath(url)
	if path != "" {
		if info, err := os.Stat(path); err == nil && (f.TTL == 0 || time.Since(info.ModTime()) < f.TTL) {
			if b, err := os.ReadFile(path); err == nil {
				return b, nil
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mend/0.1 (api change monitor; github.com/yug/mend)")
	req.Header.Set("Accept", "application/json, application/yaml, text/yaml, */*")

	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}

	limit := f.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("download %s: larger than the %d MB limit", url, limit>>20)
	}
	if len(body) == 0 {
		return nil, errors.New("download " + url + ": empty response")
	}

	if path != "" {
		_ = writeAtomic(path, body)
	}
	return body, nil
}

func (f *Fetcher) cachePath(url string) string {
	if f.Dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(f.Dir, hex.EncodeToString(sum[:12])+".spec")
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dl-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
