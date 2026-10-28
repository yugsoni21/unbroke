package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/yug/mend/internal/catalog"
	"github.com/yug/mend/internal/fetch"
)

func newFetcher(refresh bool) (*fetch.Fetcher, error) {
	dir, err := fetch.DefaultDir()
	if err != nil {
		return nil, err
	}
	ttl := 24 * time.Hour
	if refresh {
		ttl = time.Nanosecond
	}
	return fetch.New(dir, ttl), nil
}

func runCatalog(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mend catalog <list|verify> [flags]")
	}
	switch args[0] {
	case "list":
		return catalogList(args[1:], out)
	case "verify":
		return catalogVerify(args[1:], out)
	}
	return fmt.Errorf("unknown catalog command %q (want list or verify)", args[0])
}

func catalogList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("catalog list", flag.ContinueOnError)
	category := fs.String("category", "", "only show this category")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := catalog.Load()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPROVIDER\tCOMPANY\tCATEGORY\tSPECS\tHISTORY")
	shown := 0
	for _, p := range c.Providers {
		if *category != "" && !strings.EqualFold(p.Category, *category) {
			continue
		}
		hist := "snapshot"
		if p.Specs[0].HasHistory() {
			hist = "git"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", p.ID, p.Name, p.Company, p.Category, len(p.Specs), hist)
		shown++
	}
	tw.Flush()
	fmt.Fprintf(out, "\n%d providers, %d spec files. Categories: %s\n", shown, c.SpecCount(), strings.Join(c.Categories(), ", "))
	if len(c.Unlocated) > 0 {
		names := make([]string, len(c.Unlocated))
		for i, u := range c.Unlocated {
			names[i] = u.Name
		}
		fmt.Fprintf(out, "No public OpenAPI spec located for: %s\n", strings.Join(names, ", "))
	}
	return nil
}

func catalogVerify(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("catalog verify", flag.ContinueOnError)
	only := fs.String("provider", "", "comma-separated provider ids (default: all)")
	outDir := fs.String("out", "", "write catalog.md and per-provider endpoint lists into this directory")
	large := fs.Bool("large", false, "also fetch very large specs")
	workers := fs.Int("j", 6, "concurrent downloads")
	refresh := fs.Bool("refresh", false, "ignore the download cache")
	if err := fs.Parse(args); err != nil {
		return err
	}

	c, err := catalog.Load()
	if err != nil {
		return err
	}
	providers := c.Providers
	if *only != "" {
		providers = nil
		for _, id := range strings.Split(*only, ",") {
			p, ok := c.Find(strings.TrimSpace(id))
			if !ok {
				return fmt.Errorf("no provider %q in the catalog (try: mend catalog list)", id)
			}
			providers = append(providers, *p)
		}
	}

	f, err := newFetcher(*refresh)
	if err != nil {
		return err
	}

	total := 0
	for _, p := range providers {
		total += len(p.Specs)
	}
	fmt.Fprintf(os.Stderr, "verifying %d spec files from %d providers...\n", total, len(providers))
	done := 0
	results := catalog.Verify(context.Background(), providers, catalog.VerifyOptions{
		Fetcher: f, Workers: *workers, IncludeLarge: *large,
		OnResult: func(r catalog.Result) {
			done++
			if r.Err != nil {
				fmt.Fprintf(os.Stderr, "\n  FAIL %s / %s: %v\n", r.ProviderID, r.Spec.Name, r.Err)
			} else if done%10 == 0 {
				fmt.Fprintf(os.Stderr, "  %d/%d\n", done, total)
			}
		},
	})

	sums := catalog.Summarize(c, results)
	sort.SliceStable(sums, func(i, j int) bool { return sums[i].Operations > sums[j].Operations })
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tSPECS\tENDPOINTS\tDEPRECATED\tSIZE\tNOTE")
	for _, s := range sums {
		note := ""
		if s.Failed > 0 {
			note = fmt.Sprintf("%d failed", s.Failed)
		}
		if s.Skipped > 0 {
			note = strings.TrimSpace(note + fmt.Sprintf(" %d skipped", s.Skipped))
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%.1f MB\t%s\n", s.Provider.Name, s.OK, s.Operations, s.Deprecated, float64(s.Bytes)/1e6, note)
	}
	tw.Flush()
	np, ns, no := catalog.Totals(sums)
	fmt.Fprintf(out, "\n%d providers, %d spec files, %d endpoints\n", np, ns, no)

	failed := 0
	for _, r := range results {
		if r.Err != nil {
			failed++
		}
	}

	if *outDir != "" {
		files, err := catalog.WriteInventories(filepath.Join(*outDir, "endpoints"), results)
		if err != nil {
			return err
		}
		md, err := os.Create(filepath.Join(*outDir, "catalog.md"))
		if err != nil {
			return err
		}
		catalog.WriteMarkdown(md, c, results)
		if err := md.Close(); err != nil {
			return err
		}
		fmt.Fprintf(out, "wrote %s and %d endpoint lists under %s\n", filepath.Join(*outDir, "catalog.md"), len(files), filepath.Join(*outDir, "endpoints"))
	}
	if failed > 0 {
		return fmt.Errorf("%d spec file(s) failed", failed)
	}
	return nil
}

func runEndpoints(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("endpoints", flag.ContinueOnError)
	specName := fs.String("spec", "", "only this spec file (for providers with several)")
	grep := fs.String("grep", "", "only endpoints whose method, path or id contain this text")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: mend endpoints <provider> [-spec name] [-grep text]")
	}
	c, err := catalog.Load()
	if err != nil {
		return err
	}
	p, ok := c.Find(fs.Arg(0))
	if !ok {
		return fmt.Errorf("no provider %q in the catalog (try: mend catalog list)", fs.Arg(0))
	}
	if *specName != "" {
		var keep []catalog.Spec
		for _, s := range p.Specs {
			if strings.EqualFold(s.Name, *specName) {
				keep = append(keep, s)
			}
		}
		if len(keep) == 0 {
			return fmt.Errorf("provider %s has no spec named %q", p.ID, *specName)
		}
		cp := *p
		cp.Specs = keep
		p = &cp
	}
	f, err := newFetcher(false)
	if err != nil {
		return err
	}
	results := catalog.Verify(context.Background(), []catalog.Provider{*p}, catalog.VerifyOptions{Fetcher: f, IncludeLarge: true})
	needle := strings.ToLower(*grep)
	shown := 0
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(out, "# %s: %v\n", r.Spec.Name, r.Err)
			continue
		}
		if len(results) > 1 {
			fmt.Fprintf(out, "# %s\n", r.Spec.Name)
		}
		for _, o := range r.Operations {
			line := o.String()
			if o.ID != "" {
				line += "  " + o.ID
			}
			if o.Deprecated {
				line += "  [deprecated]"
			}
			if needle != "" && !strings.Contains(strings.ToLower(line), needle) {
				continue
			}
			fmt.Fprintln(out, line)
			shown++
		}
	}
	fmt.Fprintf(os.Stderr, "%d endpoints\n", shown)
	return nil
}

// reorder moves flags ahead of positional arguments so users can write
// `mend endpoints stripe -grep refund` as naturally as `-grep refund stripe`.
// It asks the flag set which flags are boolean, since those take no value.
func reorder(fs *flag.FlagSet, args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			rest = append(rest, a)
			continue
		}
		flags = append(flags, a)
		if strings.Contains(a, "=") {
			continue
		}
		f := fs.Lookup(strings.TrimLeft(a, "-"))
		if f == nil || isBool(f) {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, rest...)
}

func isBool(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}
