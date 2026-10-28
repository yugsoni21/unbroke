package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/yug/unbroke/internal/catalog"
	"github.com/yug/unbroke/internal/change"
	"github.com/yug/unbroke/internal/history"
	"github.com/yug/unbroke/internal/track"
	"github.com/yug/unbroke/internal/workflow"
)

// parseSince reads "90d", "12w", "6m", "1y", or any Go duration.
func parseSince(s string) (time.Duration, error) { return workflow.ParseSince(s) }

func runChanges(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("changes", flag.ContinueOnError)
	since := fs.String("since", "6m", "how far back to compare: 90d, 12w, 6m, 1y")
	spec := fs.String("spec", "", "only this spec file (for providers with several)")
	min := fs.String("min", "warning", "lowest severity to show: info, warning, breaking")
	asJSON := fs.Bool("json", false, "print JSON")
	summary := fs.Bool("summary", false, "print one line of counts per spec")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: unbroke changes <provider> [-since 6m] [-spec name] [-min warning] [-json]")
	}
	dur, err := parseSince(*since)
	if err != nil {
		return err
	}
	sev, err := parseSeverity(*min)
	if err != nil {
		return err
	}
	c, err := catalog.Load()
	if err != nil {
		return err
	}
	p, ok := c.Find(fs.Arg(0))
	if !ok {
		return fmt.Errorf("no provider %q in the catalog (try: unbroke catalog list)", fs.Arg(0))
	}

	deps, err := newTrackDeps()
	if err != nil {
		return err
	}
	res, err := track.Since(context.Background(), deps, *p, track.Options{Since: dur, MinSeverity: sev, Spec: *spec})
	if err != nil {
		var rl *history.RateLimitError
		if errors.As(err, &rl) {
			return err
		}
		if res.Total() == 0 && len(res.Specs) == 0 {
			return err
		}
		fmt.Fprintf(os.Stderr, "unbroke: %v\n", err)
	}

	switch {
	case *asJSON:
		return writeChangesJSON(out, res)
	case *summary:
		writeChangesSummary(out, res)
	default:
		writeChangesReport(out, res, dur)
	}
	return nil
}

func newTrackDeps() (track.Deps, error) { return workflow.NewDeps() }

func writeChangesReport(w io.Writer, res track.Result, dur time.Duration) {
	p := res.Provider
	if res.From.SHA == "" {
		fmt.Fprintf(w, "%s: no public history available for these specs yet\n", p.Name)
		for _, s := range res.Specs {
			if s.Err != nil {
				fmt.Fprintf(w, "  %s: %v\n", s.Spec.Name, s.Err)
			}
		}
		return
	}
	if res.From.SHA == "snapshot" {
		fmt.Fprintf(w, "%s: changes since %s (from an Unbroke snapshot) compared with today\n", p.Name, res.From.Date.Format("2006-01-02"))
	} else {
		fmt.Fprintf(w, "%s: changes since %s (spec repository as of commit %s) compared with today\n",
			p.Name, res.From.Date.Format("2006-01-02"), short(res.From.SHA))
	}

	changed, failed, fresh := 0, 0, 0
	for _, s := range res.Specs {
		switch {
		case s.Err != nil:
			failed++
			fmt.Fprintf(w, "\n%s: could not compare: %v\n", s.Spec.Name, s.Err)
		case s.NewSpec:
			fresh++
		case len(s.Changes) > 0 || s.Note != "":
			if len(s.Changes) > 0 {
				changed++
			}
			fmt.Fprintln(w)
			if len(p.Specs) > 1 {
				fmt.Fprintf(w, "== %s ==\n", s.Spec.Name)
			}
			if s.Note != "" {
				fmt.Fprintf(w, "note: %s\n", s.Note)
			}
			if len(s.Changes) > 0 {
				printReport(w, p.Name, s.Changes)
			}
		}
	}
	fmt.Fprintf(w, "\n%d change(s) in %d of %d spec file(s)", res.Total(), changed, len(res.Specs))
	if fresh > 0 {
		fmt.Fprintf(w, "; %d spec file(s) did not exist then (new, or moved)", fresh)
	}
	if failed > 0 {
		fmt.Fprintf(w, "; %d could not be compared", failed)
	}
	fmt.Fprintln(w)
}

func writeChangesSummary(w io.Writer, res track.Result) {
	for _, s := range res.Specs {
		name := res.Provider.Name
		if len(res.Provider.Specs) > 1 {
			name += " / " + s.Spec.Name
		}
		switch {
		case s.Err != nil:
			fmt.Fprintf(w, "%-34s error: %v\n", name, s.Err)
		case s.NewSpec:
			fmt.Fprintf(w, "%-34s did not exist then (new, or moved)\n", name)
		default:
			counts := map[change.Severity]int{}
			for _, c := range s.Changes {
				counts[c.Severity]++
			}
			fmt.Fprintf(w, "%-34s %4d breaking %4d warning %4d info\n", name, counts[change.Breaking], counts[change.Warning], counts[change.Info])
		}
	}
}

func writeChangesJSON(w io.Writer, res track.Result) error {
	type specOut struct {
		Name    string          `json:"spec"`
		NewSpec bool            `json:"new_spec,omitempty"`
		Note    string          `json:"note,omitempty"`
		Error   string          `json:"error,omitempty"`
		Changes []change.Change `json:"changes"`
	}
	doc := struct {
		Provider string    `json:"provider"`
		From     string    `json:"from_commit,omitempty"`
		FromDate string    `json:"from_date,omitempty"`
		Specs    []specOut `json:"specs"`
	}{Provider: res.Provider.ID}
	if res.From.SHA != "" {
		doc.From, doc.FromDate = res.From.SHA, res.From.Date.Format("2006-01-02")
	}
	for _, s := range res.Specs {
		o := specOut{Name: s.Spec.Name, NewSpec: s.NewSpec, Note: s.Note, Changes: s.Changes}
		if o.Changes == nil {
			o.Changes = []change.Change{}
		}
		if s.Err != nil {
			o.Error = s.Err.Error()
		}
		doc.Specs = append(doc.Specs, o)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
