package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/yug/mend/internal/catalog"
	"github.com/yug/mend/internal/snapshot"
)

// runSnapshot saves a dated copy of every spec that its provider hosts itself.
// Run it on a schedule (cron, a CI job) to build the history those providers
// don't publish.
func runSnapshot(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	refresh := fs.Bool("refresh", true, "download fresh copies instead of using the cache")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	cat, err := catalog.Load()
	if err != nil {
		return err
	}
	var providers []catalog.Provider
	if fs.NArg() == 0 {
		for _, p := range cat.Providers {
			if !p.Specs[0].HasHistory() {
				providers = append(providers, p)
			}
		}
	} else {
		for _, id := range fs.Args() {
			p, ok := cat.Find(id)
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
	dir, err := snapshot.DefaultDir()
	if err != nil {
		return err
	}
	store := &snapshot.Store{Dir: dir}

	saved, same, failed := 0, 0, 0
	for _, p := range providers {
		for _, s := range p.Specs {
			if s.HasHistory() {
				fmt.Fprintf(out, "%-14s %-10s already has public history; no snapshot needed\n", p.ID, s.Name)
				continue
			}
			body, err := f.Get(context.Background(), s.SourceURL())
			if err == nil {
				var wrote bool
				if wrote, err = store.Save(p.ID, s.Name, body, time.Now()); err == nil {
					if wrote {
						saved++
						fmt.Fprintf(out, "%-14s %-10s saved (%.1f MB)\n", p.ID, s.Name, float64(len(body))/1e6)
					} else {
						same++
						fmt.Fprintf(out, "%-14s %-10s unchanged since the last snapshot\n", p.ID, s.Name)
					}
					continue
				}
			}
			failed++
			fmt.Fprintf(out, "%-14s %-10s failed: %v\n", p.ID, s.Name, err)
		}
	}
	fmt.Fprintf(out, "\n%d saved, %d unchanged, %d failed. Snapshots are in %s\n", saved, same, failed, dir)
	if failed > 0 {
		return fmt.Errorf("%d snapshot(s) failed", failed)
	}
	_ = strings.TrimSpace
	return nil
}
