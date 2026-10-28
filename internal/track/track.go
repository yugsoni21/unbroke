// Package track answers "what changed in this provider's API since then?" by
// comparing each of its specs as they are now with how they looked at an
// earlier date.
package track

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yug/unbroke/internal/catalog"
	"github.com/yug/unbroke/internal/change"
	"github.com/yug/unbroke/internal/fetch"
	"github.com/yug/unbroke/internal/history"
	"github.com/yug/unbroke/internal/inventory"
	"github.com/yug/unbroke/internal/snapshot"
	"github.com/yug/unbroke/internal/specdiff"
)

// ErrNoHistory means the spec is hosted by its provider and has no public
// version history, and Unbroke has no snapshots to compare either.
var ErrNoHistory = errors.New("no public history for this spec (Unbroke builds one by snapshotting; run `unbroke snapshot`)")

// ErrFirstSnapshot means this look at the spec was the first, so it is now the
// baseline. There is nothing older to compare with until the spec is seen again.
var ErrFirstSnapshot = errors.New("saved the first snapshot of this spec; changes will show up on later runs")

// Deps are the outside services track needs. The URL functions default to the
// real ones and exist so tests can point at a local server.
type Deps struct {
	Fetch   *fetch.Fetcher
	History *history.Client
	// Snapshots, if set, gives history to specs the provider hosts itself.
	Snapshots *snapshot.Store

	CurrentURL func(catalog.Spec) string
	OldURL     func(repo, sha, path string) string
}

func (d Deps) currentURL(s catalog.Spec) string {
	if d.CurrentURL != nil {
		return d.CurrentURL(s)
	}
	return s.SourceURL()
}

func (d Deps) oldURL(repo, sha, path string) string {
	if d.OldURL != nil {
		return d.OldURL(repo, sha, path)
	}
	return history.RawURL(repo, sha, path)
}

// SpecChanges is the result for one spec file.
type SpecChanges struct {
	Spec    catalog.Spec
	Changes []change.Change
	// NewSpec is true when the file did not exist at the earlier date. It may
	// also have been moved or renamed since.
	NewSpec bool
	// Note explains a reduced result, for example that only endpoint-level
	// changes could be compared because one version of the spec is malformed.
	Note string
	// Err is set when this spec could not be compared.
	Err error
}

// Result is the outcome for one provider.
type Result struct {
	Provider catalog.Provider
	// From is the commit used as "then". Zero when no spec had history.
	From  history.Commit
	Until time.Time
	Specs []SpecChanges
}

// Total counts changes across all specs.
func (r Result) Total() int {
	n := 0
	for _, s := range r.Specs {
		n += len(s.Changes)
	}
	return n
}

// Options narrow a run.
type Options struct {
	// Since is how far back "then" is.
	Since time.Duration
	// Now is "now"; zero means the current time.
	Now time.Time
	// MinSeverity filters the returned changes.
	MinSeverity change.Severity
	// Spec, if set, limits the run to the spec with this name.
	Spec string
}

// Since compares every spec of the provider with how it looked Options.Since ago.
func Since(ctx context.Context, d Deps, p catalog.Provider, o Options) (Result, error) {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	then := now.Add(-o.Since)
	res := Result{Provider: p, Until: then}

	commits := map[string]history.Commit{}
	for _, s := range p.Specs {
		if o.Spec != "" && !strings.EqualFold(s.Name, o.Spec) {
			continue
		}
		sc := SpecChanges{Spec: s}
		if !s.HasHistory() {
			var at time.Time
			sc.Changes, sc.Note, at, sc.Err = compareSnapshots(ctx, d, p, s, then, now, o.MinSeverity)
			if res.From.SHA == "" && !at.IsZero() {
				res.From = history.Commit{SHA: "snapshot", Date: at}
			}
			res.Specs = append(res.Specs, sc)
			continue
		}

		cm, ok := commits[s.Repo]
		if !ok {
			var err error
			cm, err = d.History.CommitAt(ctx, s.Repo, then)
			if err != nil {
				var rl *history.RateLimitError
				if errors.As(err, &rl) {
					return res, err // every remaining lookup would fail the same way
				}
				sc.Err = err
				res.Specs = append(res.Specs, sc)
				continue
			}
			commits[s.Repo] = cm
		}
		if res.From.SHA == "" {
			res.From = cm
		}

		sc.Changes, sc.Note, sc.NewSpec, sc.Err = compareSpec(ctx, d, p, s, cm, o.MinSeverity)
		res.Specs = append(res.Specs, sc)
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
	}
	if len(res.Specs) == 0 {
		return res, fmt.Errorf("provider %s has no spec named %q", p.ID, o.Spec)
	}
	return res, nil
}

// compareSnapshots handles a spec the provider hosts itself. It saves a
// snapshot of the spec as it is now, then compares with the snapshot from
// around the start of the window.
func compareSnapshots(ctx context.Context, d Deps, p catalog.Provider, s catalog.Spec, then, now time.Time, min change.Severity) ([]change.Change, string, time.Time, error) {
	if d.Snapshots == nil {
		return nil, "", time.Time{}, ErrNoHistory
	}
	cur, err := d.Fetch.Get(ctx, d.currentURL(s))
	if err != nil {
		return nil, "", time.Time{}, err
	}
	// A download that is not a real spec must not become history.
	if inv, err := inventory.List(cur); err != nil || len(inv.Operations) == 0 {
		return nil, "", time.Time{}, fmt.Errorf("the spec as downloaded is not usable, so it was not saved as a snapshot")
	}
	saved, err := d.Snapshots.Save(p.ID, s.Name, cur, now)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	snaps, err := d.Snapshots.List(p.ID, s.Name)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	if len(snaps) == 1 && saved {
		return nil, "", time.Time{}, ErrFirstSnapshot
	}
	older, at, exact, err := d.Snapshots.AtOrBefore(p.ID, s.Name, then)
	if err != nil || older == nil {
		return nil, "", time.Time{}, ErrNoHistory
	}
	note := ""
	if !exact {
		note = fmt.Sprintf("snapshot history for this spec only goes back to %s, so the comparison covers less than the period asked for", at.Format("2006-01-02"))
	}
	name := p.Name
	if len(p.Specs) > 1 {
		name = p.Name + " / " + s.Name
	}
	cs, cnote, err := specdiff.CompareResilient(older, cur, specdiff.Options{API: name, MinSeverity: min})
	if cnote != "" {
		note = strings.TrimSpace(note + " " + cnote)
	}
	return cs, note, at, err
}

func compareSpec(ctx context.Context, d Deps, p catalog.Provider, s catalog.Spec, cm history.Commit, min change.Severity) ([]change.Change, string, bool, error) {
	newer, err := d.Fetch.Get(ctx, d.currentURL(s))
	if err != nil {
		return nil, "", false, err
	}
	older, err := d.Fetch.Get(ctx, d.oldURL(s.Repo, cm.SHA, s.Path))
	if err != nil {
		if fetch.NotFound(err) {
			return nil, "", true, nil
		}
		return nil, "", false, err
	}
	name := p.Name
	if len(p.Specs) > 1 {
		name = p.Name + " / " + s.Name
	}
	cs, note, err := specdiff.CompareResilient(older, newer, specdiff.Options{API: name, MinSeverity: min})
	return cs, note, false, err
}
