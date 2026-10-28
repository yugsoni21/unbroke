package catalog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var errNoOperations = errors.New("parsed, but the document lists no endpoints")

// Summary totals one provider's results.
type Summary struct {
	Provider   Provider
	Specs      int
	OK         int
	Failed     int
	Skipped    int
	Operations int
	Deprecated int
	Bytes      int
}

// Summarize groups results by provider, in catalog order.
func Summarize(c *Catalog, results []Result) []Summary {
	byID := map[string]*Summary{}
	var order []string
	for _, p := range c.Providers {
		byID[p.ID] = &Summary{Provider: p, Specs: len(p.Specs)}
		order = append(order, p.ID)
	}
	for _, r := range results {
		s, ok := byID[r.ProviderID]
		if !ok {
			continue
		}
		switch {
		case r.Skipped != "":
			s.Skipped++
		case r.Err != nil:
			s.Failed++
		default:
			s.OK++
			s.Operations += len(r.Operations)
			s.Deprecated += r.Deprecated
			s.Bytes += r.Bytes
		}
	}
	var out []Summary
	for _, id := range order {
		if byID[id].OK+byID[id].Failed+byID[id].Skipped > 0 {
			out = append(out, *byID[id])
		}
	}
	return out
}

// Totals adds up a set of summaries.
func Totals(ss []Summary) (providers, specs, operations int) {
	for _, s := range ss {
		if s.OK > 0 {
			providers++
		}
		specs += s.OK
		operations += s.Operations
	}
	return
}

// WriteInventories writes one text file per provider listing every endpoint,
// and returns the paths written.
func WriteInventories(dir string, results []Result) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	byProvider := map[string][]Result{}
	var ids []string
	for _, r := range results {
		if !r.OK() {
			continue
		}
		if _, seen := byProvider[r.ProviderID]; !seen {
			ids = append(ids, r.ProviderID)
		}
		byProvider[r.ProviderID] = append(byProvider[r.ProviderID], r)
	}
	var written []string
	for _, id := range ids {
		rs := byProvider[id]
		var b strings.Builder
		total := 0
		for _, r := range rs {
			total += len(r.Operations)
		}
		fmt.Fprintf(&b, "# %s: %d endpoints across %d spec file(s)\n", rs[0].Provider, total, len(rs))
		for _, r := range rs {
			fmt.Fprintf(&b, "\n## %s (%s %s, %d endpoints)\n", r.Spec.Name, r.Title, r.Version, len(r.Operations))
			for _, o := range r.Operations {
				line := o.String()
				if o.ID != "" {
					line += "  " + o.ID
				}
				if o.Deprecated {
					line += "  [deprecated]"
				}
				b.WriteString(line + "\n")
			}
		}
		path := filepath.Join(dir, id+".txt")
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			return written, err
		}
		written = append(written, path)
	}
	return written, nil
}

// WriteMarkdown writes the catalog as a readable table.
func WriteMarkdown(w io.Writer, c *Catalog, results []Result) {
	sums := Summarize(c, results)
	np, ns, no := Totals(sums)

	fmt.Fprintf(w, "# API catalog\n\n")
	fmt.Fprintf(w, "%d providers, %d specification files, %s endpoints. Every row below was downloaded and parsed.\n\n", np, ns, comma(no))
	fmt.Fprintf(w, "History: **git** means past versions of the spec are public, so any two versions can be compared. **snapshot** means the provider hosts the spec itself and Unbroke builds the history by saving copies.\n\n")

	byCat := map[string][]Summary{}
	for _, s := range sums {
		byCat[s.Provider.Category] = append(byCat[s.Provider.Category], s)
	}
	cats := make([]string, 0, len(byCat))
	for k := range byCat {
		cats = append(cats, k)
	}
	sort.Strings(cats)

	for _, cat := range cats {
		fmt.Fprintf(w, "## %s\n\n| Provider | Company | Spec files | Endpoints | Deprecated | Size | History |\n|---|---|---:|---:|---:|---:|---|\n", cat)
		rows := byCat[cat]
		sort.Slice(rows, func(i, j int) bool { return rows[i].Operations > rows[j].Operations })
		for _, s := range rows {
			hist := "snapshot"
			if s.Provider.Specs[0].HasHistory() {
				hist = "git"
			}
			note := ""
			if s.Failed > 0 {
				note = fmt.Sprintf(" (%d failed)", s.Failed)
			}
			if s.Skipped > 0 {
				note += fmt.Sprintf(" (%d skipped)", s.Skipped)
			}
			fmt.Fprintf(w, "| %s | %s | %d%s | %s | %d | %.1f MB | %s |\n",
				s.Provider.Name, s.Provider.Company, s.OK, note, comma(s.Operations), s.Deprecated, float64(s.Bytes)/1e6, hist)
		}
		fmt.Fprintln(w)
	}

	if len(c.Unlocated) > 0 {
		fmt.Fprintf(w, "## Not located\n\nWell-known companies whose public OpenAPI spec was not found. That is not proof none exists.\n\n")
		for _, u := range c.Unlocated {
			fmt.Fprintf(w, "- **%s**: %s\n", u.Name, u.Note)
		}
		fmt.Fprintln(w)
	}
}

func comma(n int) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
