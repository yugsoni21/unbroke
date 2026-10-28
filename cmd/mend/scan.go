package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yug/mend/internal/affect"
	"github.com/yug/mend/internal/catalog"
	"github.com/yug/mend/internal/change"
	"github.com/yug/mend/internal/history"
	"github.com/yug/mend/internal/inventory"
	"github.com/yug/mend/internal/scan"
	"github.com/yug/mend/internal/track"
)

// scanResult is everything one provider contributes to a scan.
type scanResult struct {
	Provider  catalog.Provider
	Since     string // date of the earlier spec
	Total     int    // changes the provider made in the window
	Findings  []affect.Finding
	Notes     []string
	FilesUsed int
	// Extra holds changes below the chosen severity. They never produce
	// findings, but additions to an endpoint often name the replacement for
	// something removed from it, so the fix agent is shown them.
	Extra []change.Change
}

func runScan(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	since := fs.String("since", "6m", "how far back to compare: 90d, 12w, 6m, 1y")
	only := fs.String("provider", "", "comma-separated provider ids (default: detect from the code)")
	min := fs.String("min", "warning", "lowest severity of API change to consider: info, warning, breaking")
	asJSON := fs.Bool("json", false, "print JSON")
	all := fs.Bool("all", false, "also list calls to changed endpoints where the changed field is not seen nearby")
	failOn := fs.String("fail-on", "never", "exit with status 3 when an affecting change is found: never, warning, breaking")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	root := "."
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: mend scan [flags] [repo-path]")
	}
	if fs.NArg() == 1 {
		root = fs.Arg(0)
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	dur, err := parseSince(*since)
	if err != nil {
		return err
	}
	sev, err := parseSeverity(*min)
	if err != nil {
		return err
	}
	var failSev change.Severity
	switch *failOn {
	case "never":
	case "warning":
		failSev = change.Warning
	case "breaking":
		failSev = change.Breaking
	default:
		return fmt.Errorf("unknown -fail-on %q (want never, warning or breaking)", *failOn)
	}

	cat, err := catalog.Load()
	if err != nil {
		return err
	}

	providers, err := chooseProviders(cat, root, *only)
	if err != nil {
		return err
	}
	if len(providers) == 0 {
		fmt.Fprintln(out, "No catalogued API usage found in this repository.")
		fmt.Fprintf(out, "Mend watches %d providers (see: mend catalog list). Use -provider to choose one explicitly.\n", len(cat.Providers))
		return nil
	}

	deps, err := newTrackDeps()
	if err != nil {
		return err
	}
	var results []scanResult
	for _, pc := range providers {
		r, err := scanProvider(context.Background(), deps, pc.p, root, dur, sev)
		if err != nil {
			var rl *history.RateLimitError
			if errors.As(err, &rl) {
				return err
			}
			r.Notes = append(r.Notes, err.Error())
		}
		r.FilesUsed = pc.files
		results = append(results, r)
	}

	if *asJSON {
		if err := writeScanJSON(out, root, results); err != nil {
			return err
		}
	} else {
		writeScanReport(out, root, results, *all)
	}

	if failSev != "" {
		for _, r := range results {
			for _, f := range r.Findings {
				if f.Relevance != affect.CallsEndpoint && f.Change.AtLeast(failSev) {
					return exitError{code: 3}
				}
			}
		}
	}
	return nil
}

type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

type detected struct {
	p     catalog.Provider
	files int
}

// chooseProviders returns the providers to scan for: those named with
// -provider, or those the code appears to use.
func chooseProviders(cat *catalog.Catalog, root, only string) ([]detected, error) {
	if only != "" {
		var out []detected
		for _, id := range strings.Split(only, ",") {
			p, ok := cat.Find(strings.TrimSpace(id))
			if !ok {
				return nil, fmt.Errorf("no provider %q in the catalog (try: mend catalog list)", id)
			}
			out = append(out, detected{p: *p})
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
	var out []detected
	for _, p := range cat.Providers {
		if n := counts[p.ID]; n > 0 {
			out = append(out, detected{p: p, files: n})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].files > out[j].files })
	return out, nil
}

func scanProvider(ctx context.Context, deps track.Deps, p catalog.Provider, root string, dur time.Duration, sev change.Severity) (scanResult, error) {
	r := scanResult{Provider: p}
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

func writeScanJSON(w io.Writer, root string, results []scanResult) error {
	type prov struct {
		Provider string           `json:"provider"`
		Since    string           `json:"compared_since,omitempty"`
		Changes  int              `json:"api_changes"`
		Findings []affect.Finding `json:"findings"`
		Notes    []string         `json:"notes,omitempty"`
	}
	doc := struct {
		Repo      string `json:"repo"`
		Providers []prov `json:"providers"`
	}{Repo: root}
	for _, r := range results {
		f := r.Findings
		if f == nil {
			f = []affect.Finding{}
		}
		doc.Providers = append(doc.Providers, prov{r.Provider.ID, r.Since, r.Total, f, r.Notes})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func writeScanReport(w io.Writer, root string, results []scanResult, all bool) {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	fmt.Fprintf(w, "mend scan: %s\n\n", abs)

	var names []string
	for _, r := range results {
		if r.FilesUsed > 0 {
			names = append(names, fmt.Sprintf("%s (%d files)", r.Provider.Name, r.FilesUsed))
		} else {
			names = append(names, r.Provider.Name)
		}
	}
	fmt.Fprintf(w, "APIs used: %s\n", strings.Join(names, ", "))

	totalChanges, affecting, calling := 0, 0, 0
	for _, r := range results {
		totalChanges += r.Total
		for _, f := range r.Findings {
			if f.Relevance == affect.CallsEndpoint {
				calling++
			} else {
				affecting++
			}
		}
	}
	fmt.Fprintf(w, "Those APIs made %d change(s) in the period; %d affect this code", totalChanges, affecting)
	if calling > 0 {
		fmt.Fprintf(w, ", and %d more touch endpoints it calls", calling)
	}
	fmt.Fprintln(w, ".")

	for _, r := range results {
		fmt.Fprintf(w, "\n== %s ==", strings.ToUpper(r.Provider.Name))
		if r.Since != "" {
			fmt.Fprintf(w, "  (compared with %s)", r.Since)
		}
		fmt.Fprintln(w)
		for _, n := range r.Notes {
			fmt.Fprintf(w, "note: %s\n", n)
		}
		shown := 0
		for _, f := range r.Findings {
			if f.Relevance == affect.CallsEndpoint && !all {
				continue
			}
			shown++
			printFinding(w, f)
		}
		hidden := 0
		for _, f := range r.Findings {
			if f.Relevance == affect.CallsEndpoint {
				hidden++
			}
		}
		switch {
		case shown == 0 && hidden == 0 && r.Total > 0:
			fmt.Fprintf(w, "None of its %d change(s) touch code in this repository.\n", r.Total)
		case shown == 0 && hidden == 0:
			fmt.Fprintln(w, "No changes found in the period.")
		}
		if hidden > 0 && !all {
			fmt.Fprintf(w, "\n%d more change(s) touch endpoints this code calls, but the changed field was not seen near the call. They may not affect you; -all lists them.\n", hidden)
		}
	}
}

func printFinding(w io.Writer, f affect.Finding) {
	label := strings.ToUpper(string(f.Change.Severity))
	fmt.Fprintf(w, "\n[%s] %s\n", label, f.Change.Summary)
	ops := f.Change.Operations
	if len(ops) == 0 {
		ops = []string{f.Change.Operation()}
	}
	fmt.Fprintf(w, "  %s\n", preview(ops, 3))
	switch f.Relevance {
	case affect.RemovedEndpoint:
		fmt.Fprintln(w, "  this code calls an endpoint that has been removed:")
	case affect.ChangedField:
		fmt.Fprintln(w, "  this code uses the field that changed:")
	default:
		fmt.Fprintln(w, "  this code calls the endpoint:")
	}
	max := 4
	for i, h := range f.Hits {
		if i == max {
			fmt.Fprintf(w, "    ... and %d more call site(s)\n", len(f.Hits)-max)
			break
		}
		field := ""
		if h.Field != "" {
			field = fmt.Sprintf(", near `%s`", h.Field)
		}
		which := ""
		if len(ops) > 1 {
			which = ", " + h.Operation
		}
		fmt.Fprintf(w, "    %s:%d   %s   (%s, %s%s%s)\n", h.File, h.Line, h.Text, h.Kind, h.Confidence, field, which)
	}
	if f.Change.Sunset != nil {
		fmt.Fprintf(w, "  the provider says this stops working on %s\n", f.Change.Sunset.Format("2006-01-02"))
	}
}
