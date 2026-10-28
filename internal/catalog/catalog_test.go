package catalog

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/yug/unbroke/internal/inventory"
)

func TestEmbeddedCatalogIsValid(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("embedded catalog does not load: %v", err)
	}
	if len(c.Providers) < 30 {
		t.Errorf("only %d providers; the catalog should cover the major API publishers", len(c.Providers))
	}
	if c.SpecCount() < len(c.Providers) {
		t.Errorf("%d specs for %d providers", c.SpecCount(), len(c.Providers))
	}
	for _, want := range []string{"stripe", "github", "twilio", "openai", "slack", "cloudflare"} {
		if _, ok := c.Find(want); !ok {
			t.Errorf("catalog is missing %s", want)
		}
	}
	if len(c.Unlocated) == 0 {
		t.Error("the list of companies without a located spec should not be empty")
	}
}

func TestParse_RejectsBadEntries(t *testing.T) {
	cases := map[string]string{
		"duplicate id":         "providers:\n- {id: a, name: A, specs: [{name: x, url: 'http://x'}]}\n- {id: a, name: B, specs: [{name: y, url: 'http://y'}]}\n",
		"no specs":             "providers:\n- {id: a, name: A}\n",
		"both repo and url":    "providers:\n- {id: a, name: A, specs: [{name: x, repo: o/r, path: p, url: 'http://x'}]}\n",
		"neither repo nor url": "providers:\n- {id: a, name: A, specs: [{name: x}]}\n",
		"repo without path":    "providers:\n- {id: a, name: A, specs: [{name: x, repo: o/r}]}\n",
		"missing name":         "providers:\n- {id: a, specs: [{name: x, url: 'http://x'}]}\n",
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSpec_SourceURLAndHistory(t *testing.T) {
	g := Spec{Repo: "stripe/openapi", Path: "openapi/spec3.json"}
	if got, want := g.SourceURL(), "https://raw.githubusercontent.com/stripe/openapi/HEAD/openapi/spec3.json"; got != want {
		t.Errorf("SourceURL = %q, want %q", got, want)
	}
	if !g.HasHistory() {
		t.Error("a GitHub-hosted spec has history")
	}
	h := Spec{URL: "https://example.com/openapi.json"}
	if h.SourceURL() != "https://example.com/openapi.json" || h.HasHistory() {
		t.Error("a self-hosted spec uses its URL and has no public history")
	}
}

func TestSummarizeAndTotals(t *testing.T) {
	c, _ := Parse([]byte(`providers:
- {id: a, name: A, category: X, specs: [{name: one, url: 'http://1'}, {name: two, url: 'http://2'}]}
- {id: b, name: B, category: X, specs: [{name: one, url: 'http://3'}]}
`))
	ops := []inventory.Operation{{Method: "GET", Path: "/a", Deprecated: true}, {Method: "GET", Path: "/b"}}
	results := []Result{
		{ProviderID: "a", Provider: "A", Operations: ops, Deprecated: 1, Bytes: 100},
		{ProviderID: "a", Provider: "A", Err: errors.New("boom")},
		{ProviderID: "b", Provider: "B", Skipped: "large"},
	}
	sums := Summarize(c, results)
	if len(sums) != 2 {
		t.Fatalf("got %d summaries", len(sums))
	}
	a := sums[0]
	if a.OK != 1 || a.Failed != 1 || a.Operations != 2 || a.Deprecated != 1 {
		t.Errorf("A = %+v", a)
	}
	if sums[1].Skipped != 1 {
		t.Errorf("B = %+v", sums[1])
	}
	p, s, o := Totals(sums)
	if p != 1 || s != 1 || o != 2 {
		t.Errorf("Totals = %d providers, %d specs, %d endpoints", p, s, o)
	}
}

func TestWriteMarkdown_ReportsFailuresAndGaps(t *testing.T) {
	c, _ := Parse([]byte(`providers:
- {id: a, name: Alpha, company: Acme, category: Pay, specs: [{name: one, repo: o/r, path: p}, {name: two, repo: o/r, path: q}]}
unlocated:
- {name: Ghost, note: nothing found}
`))
	results := []Result{
		{ProviderID: "a", Provider: "Alpha", Operations: []inventory.Operation{{Method: "GET", Path: "/x"}}, Bytes: 2_000_000},
		{ProviderID: "a", Provider: "Alpha", Err: errors.New("boom")},
	}
	var b bytes.Buffer
	WriteMarkdown(&b, c, results)
	out := b.String()
	for _, want := range []string{"Alpha", "(1 failed)", "git", "Ghost", "nothing found"} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown is missing %q:\n%s", want, out)
		}
	}
}

func TestComma(t *testing.T) {
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 39110: "39,110", 1234567: "1,234,567"} {
		if got := comma(in); got != want {
			t.Errorf("comma(%d) = %q, want %q", in, got, want)
		}
	}
}
