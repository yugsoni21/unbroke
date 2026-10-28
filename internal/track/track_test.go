package track

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yug/mend/internal/catalog"
	"github.com/yug/mend/internal/change"
	"github.com/yug/mend/internal/fetch"
	"github.com/yug/mend/internal/history"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../specdiff/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type fake struct {
	t       *testing.T
	srv     *httptest.Server
	lookups int32
	rateLtd bool
	missing map[string]bool // raw paths that 404
	before  []byte
	after   []byte
	deps    Deps
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, missing: map[string]bool{}, before: read(t, "acme_before.yaml"), after: read(t, "acme_after.yaml")}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/repos/"):
			atomic.AddInt32(&f.lookups, 1)
			if f.rateLtd {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Write([]byte(`[{"sha":"old1","commit":{"committer":{"date":"2026-03-17T10:00:00Z"}}}]`))
		case strings.HasPrefix(r.URL.Path, "/raw/old1/"):
			if f.missing[strings.TrimPrefix(r.URL.Path, "/raw/old1/")] {
				http.NotFound(w, r)
				return
			}
			w.Write(f.before)
		case strings.HasPrefix(r.URL.Path, "/raw/HEAD/"):
			w.Write(f.after)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	f.deps = Deps{
		Fetch:      fetch.New("", 0),
		History:    &history.Client{HTTP: f.srv.Client(), BaseURL: f.srv.URL + "/api"},
		CurrentURL: func(s catalog.Spec) string { return f.srv.URL + "/raw/HEAD/" + s.Path },
		OldURL:     func(repo, sha, path string) string { return f.srv.URL + "/raw/" + sha + "/" + path },
	}
	return f
}

func provider(specs ...catalog.Spec) catalog.Provider {
	return catalog.Provider{ID: "acme", Name: "Acme", Specs: specs}
}

func gh(name, path string) catalog.Spec {
	return catalog.Spec{Name: name, Repo: "acme/specs", Path: path}
}

var now = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func TestSince_FindsRealChanges(t *testing.T) {
	f := newFake(t)
	res, err := Since(context.Background(), f.deps, provider(gh("REST", "acme.yaml")), Options{Since: 180 * 24 * time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if res.From.SHA != "old1" {
		t.Errorf("From = %+v", res.From)
	}
	if len(res.Specs) != 1 || res.Specs[0].Err != nil {
		t.Fatalf("specs = %+v", res.Specs)
	}
	if res.Total() == 0 {
		t.Fatal("expected the fixture's planted changes to be found")
	}
	hasBreaking := false
	for _, c := range res.Specs[0].Changes {
		if c.Severity == change.Breaking {
			hasBreaking = true
		}
		if c.API != "Acme" {
			t.Errorf("API name = %q, want Acme (single-spec providers use the provider name)", c.API)
		}
	}
	if !hasBreaking {
		t.Error("no breaking change found")
	}
}

func TestSince_OneHistoryLookupPerRepo(t *testing.T) {
	f := newFake(t)
	_, err := Since(context.Background(), f.deps, provider(gh("A", "a.yaml"), gh("B", "b.yaml"), gh("C", "c.yaml")), Options{Since: time.Hour * 24, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if f.lookups != 1 {
		t.Errorf("history was looked up %d times for three specs in one repo, want 1", f.lookups)
	}
}

func TestSince_MultiSpecProvidersNameTheSpec(t *testing.T) {
	f := newFake(t)
	res, _ := Since(context.Background(), f.deps, provider(gh("Payments", "a.yaml"), gh("Refunds", "b.yaml")), Options{Since: time.Hour, Now: now})
	if got := res.Specs[1].Changes[0].API; got != "Acme / Refunds" {
		t.Errorf("API = %q, want %q", got, "Acme / Refunds")
	}
}

func TestSince_SpecThatDidNotExistYetIsNewNotAnError(t *testing.T) {
	f := newFake(t)
	f.missing["fresh.yaml"] = true
	res, err := Since(context.Background(), f.deps, provider(gh("Fresh", "fresh.yaml")), Options{Since: time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Specs[0]
	if !s.NewSpec || s.Err != nil || len(s.Changes) != 0 {
		t.Errorf("got %+v, want NewSpec with no error", s)
	}
}

func TestSince_SelfHostedSpecHasNoHistory(t *testing.T) {
	f := newFake(t)
	res, err := Since(context.Background(), f.deps, provider(catalog.Spec{Name: "REST", URL: "https://example.com/o.json"}), Options{Since: time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(res.Specs[0].Err, ErrNoHistory) {
		t.Errorf("Err = %v, want ErrNoHistory", res.Specs[0].Err)
	}
	if f.lookups != 0 {
		t.Error("no GitHub lookup should be made for a self-hosted spec")
	}
}

func TestSince_RateLimitStopsTheRun(t *testing.T) {
	f := newFake(t)
	f.rateLtd = true
	_, err := Since(context.Background(), f.deps, provider(gh("A", "a.yaml"), gh("B", "b.yaml")), Options{Since: time.Hour, Now: now})
	var rl *history.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want a RateLimitError", err)
	}
}

func TestSince_SpecFilter(t *testing.T) {
	f := newFake(t)
	res, err := Since(context.Background(), f.deps, provider(gh("A", "a.yaml"), gh("B", "b.yaml")), Options{Since: time.Hour, Now: now, Spec: "b"})
	if err != nil || len(res.Specs) != 1 || res.Specs[0].Spec.Name != "B" {
		t.Errorf("got %+v, %v", res.Specs, err)
	}
	if _, err := Since(context.Background(), f.deps, provider(gh("A", "a.yaml")), Options{Since: time.Hour, Now: now, Spec: "nope"}); err == nil {
		t.Error("an unknown spec name should be an error")
	}
}
