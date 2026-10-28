// Package snapshot keeps dated copies of specifications that providers host
// themselves and publish no history for. Mend builds that history by looking
// repeatedly: each look is saved if the spec changed, and later comparisons
// run between saved copies.
package snapshot

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yug/mend/internal/specload"
)

// Store is a directory of snapshots.
type Store struct{ Dir string }

// DefaultDir is where snapshots live. They are state, not cache: deleting them
// loses history that cannot be downloaded again.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mend", "snapshots"), nil
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(slugRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func (s *Store) dir(provider, spec string) string {
	return filepath.Join(s.Dir, slug(provider), slug(spec))
}

// Snap is one saved copy.
type Snap struct {
	At   time.Time
	Path string
}

// List returns the saved copies of a spec, oldest first.
func (s *Store) List(provider, spec string) ([]Snap, error) {
	entries, err := os.ReadDir(s.dir(provider, spec))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Snap
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".spec")
		secs, err := strconv.ParseInt(name, 10, 64)
		if err != nil || name == e.Name() {
			continue
		}
		out = append(out, Snap{At: time.Unix(secs, 0).UTC(), Path: filepath.Join(s.dir(provider, spec), e.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// Save stores body as the spec's snapshot at time at, unless it is identical to
// the latest one. It reports whether anything was written.
func (s *Store) Save(provider, spec string, body []byte, at time.Time) (bool, error) {
	if len(body) == 0 {
		return false, errors.New("refusing to snapshot an empty document")
	}
	snaps, err := s.List(provider, spec)
	if err != nil {
		return false, err
	}
	if n := len(snaps); n > 0 {
		last, err := os.ReadFile(snaps[n-1].Path)
		if err == nil && sameSpec(last, body) {
			return false, nil
		}
	}
	dir := s.dir(provider, spec)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	path := filepath.Join(dir, strconv.FormatInt(at.Unix(), 10)+".spec")
	return true, os.WriteFile(path, body, 0o644)
}

// sameSpec reports whether two downloads are the same specification. Servers
// sometimes serialise lists in a different order each time (X does, for its
// OAuth scopes), which must not count as a new version.
func sameSpec(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	ca, errA := specload.Canonical(a)
	cb, errB := specload.Canonical(b)
	return errA == nil && errB == nil && bytes.Equal(ca, cb)
}

// AtOrBefore returns the newest snapshot taken at or before t. If none is that
// old it returns the oldest one available and ok=false, so a caller can still
// compare and say that history is shorter than asked for.
func (s *Store) AtOrBefore(provider, spec string, t time.Time) (body []byte, at time.Time, exact bool, err error) {
	snaps, err := s.List(provider, spec)
	if err != nil || len(snaps) == 0 {
		return nil, time.Time{}, false, err
	}
	pick := snaps[0]
	exact = false
	for _, sn := range snaps {
		if !sn.At.After(t) {
			pick, exact = sn, true
		}
	}
	body, err = os.ReadFile(pick.Path)
	return body, pick.At, exact, err
}
