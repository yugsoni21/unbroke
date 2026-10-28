package catalog

import (
	"context"
	"sync"
	"time"

	"github.com/yug/mend/internal/fetch"
	"github.com/yug/mend/internal/inventory"
	"github.com/yug/mend/internal/specload"
)

// Result is the outcome of downloading and reading one spec.
type Result struct {
	ProviderID string
	Provider   string
	Spec       Spec

	Bytes      int
	Title      string
	Version    string
	Format     specload.Format
	Operations []inventory.Operation
	Deprecated int

	// Skipped is set when the spec was deliberately not fetched.
	Skipped string
	// Err is set when the download or parse failed.
	Err      error
	Duration time.Duration
}

// OK reports whether the spec was fetched and parsed.
func (r Result) OK() bool { return r.Err == nil && r.Skipped == "" }

// VerifyOptions controls a verification run.
type VerifyOptions struct {
	Fetcher *fetch.Fetcher
	// Workers is the number of concurrent downloads.
	Workers int
	// IncludeLarge also fetches specs marked Large.
	IncludeLarge bool
	// OnResult, if set, is called as each spec finishes.
	OnResult func(Result)
}

// Verify downloads and parses every spec of the given providers. Results
// come back in catalog order regardless of completion order.
func Verify(ctx context.Context, providers []Provider, opts VerifyOptions) []Result {
	type job struct {
		idx int
		p   Provider
		s   Spec
	}
	var jobs []job
	for _, p := range providers {
		for _, s := range p.Specs {
			jobs = append(jobs, job{len(jobs), p, s})
		}
	}
	results := make([]Result, len(jobs))

	workers := opts.Workers
	if workers <= 0 {
		workers = 6
	}
	ch := make(chan job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				r := verifyOne(ctx, j.p, j.s, opts)
				results[j.idx] = r
				if opts.OnResult != nil {
					mu.Lock()
					opts.OnResult(r)
					mu.Unlock()
				}
			}
		}()
	}
	for _, j := range jobs {
		ch <- j
	}
	close(ch)
	wg.Wait()
	return results
}

func verifyOne(ctx context.Context, p Provider, s Spec, opts VerifyOptions) Result {
	r := Result{ProviderID: p.ID, Provider: p.Name, Spec: s}
	if s.Large && !opts.IncludeLarge {
		r.Skipped = "large spec; use -large to include"
		return r
	}
	start := time.Now()
	defer func() { r.Duration = time.Since(start) }()

	body, err := opts.Fetcher.Get(ctx, s.SourceURL())
	if err != nil {
		r.Err = err
		return r
	}
	r.Bytes = len(body)

	inv, err := inventory.List(body)
	if err != nil {
		r.Err = err
		return r
	}
	r.Title, r.Version, r.Format = inv.Title, inv.Version, inv.Format
	r.Operations = inv.Operations
	r.Deprecated = inv.Deprecated()
	if len(inv.Operations) == 0 {
		r.Err = errNoOperations
	}
	return r
}
