// Package workflow holds the steps that the command line and the web interface
// both run: working out which APIs a codebase uses, and what changed in them
// that touches it.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yug/mend/internal/affect"
	"github.com/yug/mend/internal/catalog"
	"github.com/yug/mend/internal/change"
	"github.com/yug/mend/internal/fetch"
	"github.com/yug/mend/internal/history"
	"github.com/yug/mend/internal/inventory"
	"github.com/yug/mend/internal/scan"
	"github.com/yug/mend/internal/snapshot"
	"github.com/yug/mend/internal/track"
)

// NewDeps builds the services a comparison needs, with the default cache and
// snapshot locations.
func NewDeps() (track.Deps, error) {
	cacheDir, err := fetch.DefaultDir()
	if err != nil {
		return track.Deps{}, err
	}
	snapDir, err := snapshot.DefaultDir()
	if err != nil {
		return track.Deps{}, err
	}
	return track.Deps{
		Fetch:     fetch.New(cacheDir, 24*time.Hour),
		History:   history.New(filepath.Join(filepath.Dir(cacheDir), "history")),
		Snapshots: &snapshot.Store{Dir: snapDir},
	}, nil
}

// Result is everything one provider contributes to a scan.
type Result struct {
	Provider  catalog.Provider
	Since     string // date of the earlier spec
	Total     int    // changes the provider made in the window
	Findings  []affect.Finding
	Notes     []string
	FilesUsed int
	// Compared is how many spec files were actually compared. Zero means the
	// result says nothing about whether the API changed.
	Compared int
	// Extra holds changes below the chosen severity. They never produce
	// findings, but additions to an endpoint often name the replacement for
	// something removed from it, so the fix agent is shown them.
	Extra []change.Change
}

// Detected is a provider the code appears to use, with how many files show it.
type Detected struct {
	P     catalog.Provider
	Files int
}

// Choose returns the providers to scan for: those named with
// -provider, or those the code appears to use.
func Choose(cat *catalog.Catalog, root, only string) ([]Detected, error) {
	if only != "" {
		var out []Detected
		for _, id := range strings.Split(only, ",") {
			p, ok := cat.Find(strings.TrimSpace(id))
			if !ok {
				return nil, fmt.Errorf("no provider %q in the catalog (try: mend catalog list)", id)
			}
			out = append(out, Detected{P: *p})
		}
		return out, nil
	}
	hints := map[string][]string{}
	for _, p := range cat.Providers {
		hints[p.ID] = p.Hints()
	}
	counts, err := scan.DetectUse(root, hints)
	if err != nil {
		return nil, err
	}
	var out []Detected
	for _, p := range cat.Providers {
		if n := counts[p.ID]; n > 0 {
			out = append(out, Detected{P: p, Files: n})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Files > out[j].Files })
	return out, nil
}

// ScanProvider compares one provider's API now with the past, finds the calls to
// the endpoints that changed, and returns what affects the code. progress, if
// set, is told each stage as it starts.
func ScanProvider(ctx context.Context, deps track.Deps, p catalog.Provider, root string, dur time.Duration, sev change.Severity, progress func(string)) (Result, error) {
	r := Result{Provider: p}
	say := func(s string) {
		if progress != nil {
			progress(s)
		}
	}
	say(fmt.Sprintf("Comparing %s now with %d days ago", p.Name, int(dur.Hours()/24)))
	res, err := track.Since(ctx, deps, p, track.Options{Since: dur, MinSeverity: change.Info})
	if res.From.SHA != "" {
		r.Since = res.From.Date.Format("2006-01-02")
	}
	if err != nil {
		return r, err
	}

	var changes []change.Change
	for _, s := range res.Specs {
		if s.Note != "" {
			r.Notes = append(r.Notes, s.Note)
		}
		if s.Err != nil && !errors.Is(s.Err, track.ErrNoHistory) && !errors.Is(s.Err, track.ErrFirstSnapshot) {
			r.Notes = append(r.Notes, fmt.Sprintf("%s: %v", s.Spec.Name, s.Err))
		}
		if errors.Is(s.Err, track.ErrNoHistory) {
			r.Notes = append(r.Notes, "this provider publishes no spec history; run `mend snapshot` regularly to build one")
		}
		if errors.Is(s.Err, track.ErrFirstSnapshot) {
			r.Notes = append(r.Notes, "first look at this provider's spec: saved as the baseline. Run again after it has had time to change.")
		}
		switch {
		case s.NewSpec:
			r.Notes = append(r.Notes, fmt.Sprintf("%s did not exist at the earlier date (it may be new, or may have moved), so nothing could be compared", s.Spec.Name))
		case s.Err == nil:
			r.Compared++
		}
		for _, c := range s.Changes {
			if c.AtLeast(sev) {
				changes = append(changes, c)
			} else {
				r.Extra = append(r.Extra, c)
			}
		}
	}
	r.Total = len(changes)
	if len(changes) == 0 {
		return r, nil
	}

	seen := map[string]bool{}
	var targets []scan.Target
	for _, c := range changes {
		ops := c.Operations
		if len(ops) == 0 && c.Operation() != "" {
			ops = []string{c.Operation()}
		}
		for _, op := range ops {
			if seen[op] {
				continue
			}
			seen[op] = true
			m, path, _ := strings.Cut(op, " ")
			targets = append(targets, scan.Target{Method: m, Path: path})
		}
	}
	say(fmt.Sprintf("Scanning your code for %d changed endpoint(s)", len(targets)))
	matches, err := scan.Scan(root, targets, scan.Options{Hints: p.Hints(), Universe: universe(ctx, deps, p)})
	if err != nil {
		return r, err
	}
	r.Findings = affect.Correlate(p.ID, changes, matches)
	return r, nil
}

// universe lists every endpoint of the provider's current specs. The scanner
// uses it to notice names shared by several endpoints. If a spec can't be read
// the scan still runs, just with less ability to tell look-alikes apart.
func universe(ctx context.Context, deps track.Deps, p catalog.Provider) []scan.Target {
	var out []scan.Target
	for _, s := range p.Specs {
		body, err := deps.Fetch.Get(ctx, s.SourceURL())
		if err != nil {
			continue
		}
		inv, err := inventory.List(body)
		if err != nil {
			continue
		}
		for _, o := range inv.Operations {
			out = append(out, scan.Target{Method: o.Method, Path: o.Path})
		}
	}
	return out
}

// ParseSince reads "90d", "12w", "6m", "1y", or any Go duration like "2160h".
// A month is 30 days and a year is 365.
func ParseSince(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errors.New("empty duration")
	}
	units := map[byte]time.Duration{'d': 24 * time.Hour, 'w': 7 * 24 * time.Hour, 'm': 30 * 24 * time.Hour, 'y': 365 * 24 * time.Hour}
	if mult, ok := units[s[len(s)-1]]; ok {
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("bad duration %q (want something like 90d, 12w, 6m or 1y)", s)
		}
		return time.Duration(n) * mult, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("bad duration %q (want something like 90d, 12w, 6m or 1y)", s)
	}
	return d, nil
}
