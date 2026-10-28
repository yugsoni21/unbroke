package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/yug/mend/internal/change"
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
