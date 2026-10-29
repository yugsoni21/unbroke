package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yug/unbroke/internal/affect"
	"github.com/yug/unbroke/internal/change"
	"github.com/yug/unbroke/internal/workflow"
)

func TestPreview(t *testing.T) {
	if got := preview([]string{"a", "b"}, 4); got != "a, b" {
		t.Errorf("short list = %q", got)
	}
	if got := preview([]string{"a", "b", "c", "d", "e", "f"}, 4); got != "a, b, c, d, and 2 more" {
		t.Errorf("long list = %q", got)
	}
}

func TestPrintReport_GroupsByOperationAndNamesSharedScope(t *testing.T) {
	sunset := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	cs := []change.Change{
		{Severity: change.Breaking, RuleID: "r1", Summary: "removed `id`", Method: "POST", Path: "/x", Operations: []string{"POST /x"}, FromVersion: "1", ToVersion: "2"},
		{Severity: change.Breaking, RuleID: "r1", Summary: "removed `name`", Method: "POST", Path: "/x", Operations: []string{"POST /x"}},
		{Severity: change.Breaking, RuleID: "r2", Summary: "enum narrowed", Method: "GET", Path: "/a", Operations: []string{"GET /a", "GET /b", "GET /c", "GET /d", "GET /e", "GET /f"}},
		{Severity: change.Warning, RuleID: "r3", Summary: "deprecated", Method: "GET", Path: "/old", Operations: []string{"GET /old"}, Sunset: &sunset},
	}
	var b bytes.Buffer
	printReport(&b, "Acme", cs)
	out := b.String()

	if strings.Count(out, "  POST /x\n") != 1 {
		t.Errorf("POST /x should appear once with both changes under it:\n%s", out)
	}
	for _, want := range []string{
		"4 changes: 3 breaking, 1 warning",
		"affects 6 operations: GET /a, GET /b, GET /c, GET /d, and 2 more",
		"sunset 2026-12-31",
		"WARNING (1)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

func TestPrintReport_Empty(t *testing.T) {
	var b bytes.Buffer
	printReport(&b, "Acme", nil)
	if !strings.Contains(b.String(), "no changes") {
		t.Errorf("got %q", b.String())
	}
}

func TestScanReport_SaysWhenNothingWasCompared(t *testing.T) {
	r := workflow.Result{Notes: []string{"REST API did not exist at the earlier date"}, Compared: 0}
	r.Provider.Name = "OpenAI"
	r.Since = "2025-06-17"
	var b bytes.Buffer
	writeScanReport(&b, ".", []workflow.Result{r}, false)
	out := b.String()
	if strings.Contains(out, "No changes found") {
		t.Errorf("a comparison that never happened must not be reported as 'no changes':\n%s", out)
	}
	if !strings.Contains(out, "Nothing could be compared") {
		t.Errorf("report should say nothing was compared:\n%s", out)
	}

	r.Compared = 1
	b.Reset()
	writeScanReport(&b, ".", []workflow.Result{r}, false)
	if !strings.Contains(b.String(), "No changes found") {
		t.Errorf("a real comparison with no changes should still say so:\n%s", b.String())
	}
}

func TestGateExit(t *testing.T) {
	affecting := affect.Finding{Relevance: affect.ChangedField, Change: change.Change{Severity: change.Breaking}}
	checked := workflow.Result{Compared: 1}
	checked.Provider.Name = "Stripe"
	unchecked := workflow.Result{Compared: 0}
	unchecked.Provider.Name = "OpenAI"
	withFinding := checked
	withFinding.Findings = []affect.Finding{affecting}

	code := func(err error) int {
		if err == nil {
			return 0
		}
		var ee exitError
		if errors.As(err, &ee) {
			return ee.code
		}
		return -1
	}
	var warn bytes.Buffer
	cases := []struct {
		name string
		rs   []workflow.Result
		want int
	}{
		{"all checked, nothing found", []workflow.Result{checked}, 0},
		{"a finding fails the gate", []workflow.Result{withFinding, unchecked}, 3},
		{"nothing found but an API was not checked", []workflow.Result{checked, unchecked}, 4},
	}
	for _, c := range cases {
		if got := code(gateExit(c.rs, change.Warning, &warn)); got != c.want {
			t.Errorf("%s: exit %d, want %d", c.name, got, c.want)
		}
	}
	if !strings.Contains(warn.String(), "could not check OpenAI") {
		t.Errorf("warning = %q", warn.String())
	}
}
