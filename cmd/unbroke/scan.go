package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yug/unbroke/internal/affect"
	"github.com/yug/unbroke/internal/catalog"
	"github.com/yug/unbroke/internal/change"
	"github.com/yug/unbroke/internal/history"
	"github.com/yug/unbroke/internal/workflow"
)

func runScan(args []string, out io.Writer) error {
	fs := newFlags("scan", "unbroke scan [flags] [repo-path]")
	since := fs.String("since", "6m", "how far back to compare: 90d, 12w, 6m, 1y")
	only := fs.String("provider", "", "comma-separated provider ids (default: detect from the code)")
	min := fs.String("min", "warning", "lowest severity of API change to consider: info, warning, breaking")
	asJSON := fs.Bool("json", false, "print JSON")
	all := fs.Bool("all", false, "also list calls to changed endpoints where the changed field is not seen nearby")
	failOn := fs.String("fail-on", "never", "for CI: exit 3 when a change at this level affects the code, 4 when an API could not be checked: never, warning, breaking")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	root := "."
	if fs.NArg() > 1 {
		return usagef("usage: unbroke scan [flags] [repo-path] (one path; got %d)", fs.NArg())
	}
	if fs.NArg() == 1 {
		root = fs.Arg(0)
	}
	if err := requireDir(root); err != nil {
		return err
	}
	dur, err := parseSince(*since)
	if err != nil {
		return usageError{msg: err.Error()}
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
		return usagef("unknown -fail-on %q (want never, warning or breaking)", *failOn)
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
		if *asJSON {
			return writeScanJSON(out, root, nil)
		}
		fmt.Fprintln(out, "No catalogued API usage found in this repository.")
		fmt.Fprintf(out, "Unbroke watches %d providers (see: unbroke catalog list). Use -provider to choose one explicitly.\n", len(cat.Providers))
		return nil
	}

	deps, err := newTrackDeps()
	if err != nil {
		return err
	}
	var results []workflow.Result
	for _, pc := range providers {
		r, err := workflow.ScanProvider(context.Background(), deps, pc.P, root, dur, sev, nil)
		if err != nil {
			var rl *history.RateLimitError
			if errors.As(err, &rl) {
				return err
			}
			r.Notes = append(r.Notes, err.Error())
		}
		r.FilesUsed = pc.Files
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
		return gateExit(results, failSev, os.Stderr)
	}
	return nil
}

// gateExit decides the exit status of a CI gate. A gate that silently passes
// because nothing could be checked is worse than no gate, so "could not check"
// has its own status:
//
//	0  every API used was checked and nothing affecting the code was found
//	3  a change at or above the threshold affects the code
//	4  at least one API could not be checked (and nothing failed the gate)
func gateExit(results []workflow.Result, sev change.Severity, warn io.Writer) error {
	for _, r := range results {
		for _, f := range r.Findings {
			if f.Relevance != affect.CallsEndpoint && f.Change.AtLeast(sev) {
				return exitError{code: 3}
			}
		}
	}
	var unchecked []string
	for _, r := range results {
		if r.Compared == 0 {
			unchecked = append(unchecked, r.Provider.Name)
		}
	}
	if len(unchecked) > 0 {
		fmt.Fprintf(warn, "unbroke: could not check %s, so this run proves nothing about them (exit status 4)\n", strings.Join(unchecked, ", "))
		return exitError{code: 4}
	}
	return nil
}

type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

func writeScanJSON(w io.Writer, root string, results []workflow.Result) error {
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
	}{Repo: root, Providers: []prov{}}
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

func writeScanReport(w io.Writer, root string, results []workflow.Result, all bool) {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	fmt.Fprintf(w, "unbroke scan: %s\n\n", abs)

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
	unchecked := 0
	for _, r := range results {
		if r.Compared == 0 {
			unchecked++
		}
	}
	if unchecked == len(results) {
		// "0 changes" would read as good news when nothing was looked at.
		fmt.Fprintln(w, "None of them could be checked, so this scan says nothing about whether they changed (see the notes below).")
	} else {
		fmt.Fprintf(w, "Those APIs made %d change(s) in the period; %d affect this code", totalChanges, affecting)
		if calling > 0 {
			fmt.Fprintf(w, ", and %d more touch endpoints it calls", calling)
		}
		fmt.Fprintln(w, ".")
		if unchecked > 0 {
			fmt.Fprintf(w, "%d of the %d APIs could not be checked (see the notes below).\n", unchecked, len(results))
		}
	}

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
		case shown == 0 && hidden == 0 && r.Compared == 0:
			fmt.Fprintln(w, "Nothing could be compared for this API, so this says nothing about whether it changed (see the note above).")
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
