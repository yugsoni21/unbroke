package specdiff

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yug/unbroke/internal/change"
	"github.com/yug/unbroke/internal/specload"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func compareAcme(t *testing.T, min change.Severity) []change.Change {
	t.Helper()
	cs, err := Compare(fixture(t, "acme_before.yaml"), fixture(t, "acme_after.yaml"),
		Options{API: "acme", MinSeverity: min})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	return cs
}

type key struct{ rule, method, path string }

func keys(cs []change.Change) map[key]change.Change {
	m := map[key]change.Change{}
	for _, c := range cs {
		m[key{c.RuleID, c.Method, c.Path}] = c
	}
	return m
}

func TestCompare_FindsEveryPlantedChange(t *testing.T) {
	got := keys(compareAcme(t, ""))
	want := []struct {
		k   key
		sev change.Severity
	}{
		{key{"api-path-removed-without-deprecation", "POST", "/v1/charges"}, change.Breaking},
		{key{"new-required-request-property", "POST", "/v1/refunds"}, change.Breaking},
		{key{"request-property-removed", "POST", "/v1/refunds"}, change.Warning},
		{key{"response-required-property-removed", "GET", "/v1/customers/{id}"}, change.Breaking},
		{key{"request-parameter-enum-value-removed", "GET", "/v1/invoices"}, change.Breaking},
		{key{"endpoint-deprecated-with-sunset", "GET", "/v1/sources"}, change.Warning},
	}
	for _, w := range want {
		c, ok := got[w.k]
		if !ok {
			t.Errorf("missing change %+v; got %d changes", w.k, len(got))
			continue
		}
		if c.Severity != w.sev {
			t.Errorf("%+v severity = %s, want %s", w.k, c.Severity, w.sev)
		}
	}
}

func TestCompare_DefaultDropsInformationalChanges(t *testing.T) {
	for _, c := range compareAcme(t, "") {
		if c.Severity == change.Info {
			t.Errorf("default threshold returned informational change %s: %s", c.RuleID, c.Summary)
		}
	}
	got := keys(compareAcme(t, ""))
	if _, ok := got[key{"response-optional-property-added", "POST", "/v1/refunds"}]; ok {
		t.Error("adding an optional response field should not be reported by default")
	}
}

func TestCompare_InfoThresholdKeepsHarmlessChanges(t *testing.T) {
	got := keys(compareAcme(t, change.Info))
	if _, ok := got[key{"response-optional-property-added", "POST", "/v1/refunds"}]; !ok {
		t.Error("with MinSeverity=info the new optional field should be reported")
	}
}

func TestCompare_DeprecationCarriesSunsetDate(t *testing.T) {
	c, ok := keys(compareAcme(t, ""))[key{"endpoint-deprecated-with-sunset", "GET", "/v1/sources"}]
	if !ok {
		t.Fatal("deprecation change not found")
	}
	if c.Sunset == nil {
		t.Fatal("Sunset not populated")
	}
	want := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	if !c.Sunset.Equal(want) {
		t.Errorf("Sunset = %v, want %v", c.Sunset, want)
	}
}

func TestCompare_ChangesWithoutADateHaveNoSunset(t *testing.T) {
	for _, c := range compareAcme(t, "") {
		if c.RuleID != "endpoint-deprecated-with-sunset" && c.Sunset != nil {
			t.Errorf("%s unexpectedly has Sunset %v", c.RuleID, c.Sunset)
		}
	}
}

func TestCompare_StampsProviderAndVersions(t *testing.T) {
	for _, c := range compareAcme(t, "") {
		if c.API != "acme" {
			t.Errorf("API = %q, want acme", c.API)
		}
		if c.FromVersion != "2026-06-01" || c.ToVersion != "2026-09-15" {
			t.Errorf("versions = %q -> %q", c.FromVersion, c.ToVersion)
		}
	}
}

func TestCompare_MostSevereFirst(t *testing.T) {
	cs := compareAcme(t, change.Info)
	for i := 1; i < len(cs); i++ {
		if cs[i-1].Severity.Rank() < cs[i].Severity.Rank() {
			t.Fatalf("change %d (%s) is less severe than change %d (%s)",
				i-1, cs[i-1].Severity, i, cs[i].Severity)
		}
	}
}

func TestCompare_IdenticalSpecsHaveNoChanges(t *testing.T) {
	b := fixture(t, "acme_before.yaml")
	cs, err := Compare(b, b, Options{API: "acme", MinSeverity: change.Info})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(cs) != 0 {
		t.Errorf("identical specs produced %d changes: %+v", len(cs), cs)
	}
}

func TestCompare_RejectsInvalidSpec(t *testing.T) {
	if _, err := Compare([]byte("not: [a valid, openapi"), fixture(t, "acme_after.yaml"), Options{}); err == nil {
		t.Error("expected an error for an unparseable base spec")
	}
	if _, err := Compare(fixture(t, "acme_before.yaml"), []byte("{}"), Options{}); err == nil {
		t.Error("expected an error for a revision that is not an OpenAPI document")
	}
}

func TestChange_Operation(t *testing.T) {
	cases := []struct {
		c    change.Change
		want string
	}{
		{change.Change{Method: "POST", Path: "/v1/refunds"}, "POST /v1/refunds"},
		{change.Change{Path: "/v1/refunds"}, "/v1/refunds"},
		{change.Change{}, ""},
	}
	for _, tc := range cases {
		if got := tc.c.Operation(); got != tc.want {
			t.Errorf("Operation() = %q, want %q", got, tc.want)
		}
	}
}

func TestCompare_SharedSchemaChangeIsOneRecordListingEveryOperation(t *testing.T) {
	var found []change.Change
	for _, c := range compareAcme(t, "") {
		if c.RuleID == "response-required-property-removed" {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("Customer.email was removed from a schema used by two endpoints; got %d records, want 1: %+v", len(found), found)
	}
	got := found[0].Operations
	want := []string{"GET /v1/customers/{id}", "GET /v1/me"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Operations = %v, want %v", got, want)
	}
	if found[0].Method != "GET" || found[0].Path != "/v1/customers/{id}" {
		t.Errorf("primary location = %s %s, want the alphabetically first operation", found[0].Method, found[0].Path)
	}
}

func TestCompare_OperationLevelChangesKeepTheirOwnOperation(t *testing.T) {
	c, ok := keys(compareAcme(t, ""))[key{"api-path-removed-without-deprecation", "POST", "/v1/charges"}]
	if !ok {
		t.Fatal("removed endpoint not found")
	}
	if len(c.Operations) != 1 || c.Operations[0] != "POST /v1/charges" {
		t.Errorf("Operations = %v, want [POST /v1/charges]", c.Operations)
	}
}

func TestCompare_ResponseAdditionsAreInformationalNotBreaking(t *testing.T) {
	for _, c := range compareAcme(t, "") {
		if c.RuleID == "response-property-enum-value-added" {
			t.Errorf("a new response enum value was reported at the default threshold: %s", c.Summary)
		}
	}
	c, ok := keys(compareAcme(t, change.Info))[key{"response-property-enum-value-added", "POST", "/v1/refunds"}]
	if !ok {
		t.Fatal("new response enum value should still be available at the info threshold")
	}
	if c.Severity != change.Info {
		t.Errorf("severity = %s, want info", c.Severity)
	}
}

func TestRevisionTypeIsAny(t *testing.T) {
	cases := []struct {
		name string
		args []any
		want bool
	}{
		{"property widened to any", []any{"a/b", "type", "string", "any", "200"}, true},
		{"body widened to any", []any{"type", "object", "any", "200"}, true},
		{"real type change", []any{"a/b", "type", "integer", "string", "200"}, false},
		{"already any", []any{"a/b", "type", "any", "any", "200"}, false},
		{"too few args", []any{"any"}, false},
		{"no args", nil, false},
	}
	for _, tc := range cases {
		if got := revisionTypeIsAny(tc.args); got != tc.want {
			t.Errorf("%s: revisionTypeIsAny(%v) = %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

func TestCompare_RemovalsTheRuleEngineSkipsAreStillReported(t *testing.T) {
	// The rule engine drops removals it considers properly announced (deprecated
	// with a sunset date that has passed) and removals of alpha operations. A
	// caller still breaks when the endpoint disappears.
	cs, err := Compare(fixture(t, "removals_before.yaml"), fixture(t, "removals_after.yaml"), Options{MinSeverity: change.Info})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]change.Change{}
	for _, c := range cs {
		got[c.Operation()] = c
	}
	for _, op := range []string{"GET /old", "GET /alpha"} {
		c, ok := got[op]
		if !ok || c.Severity != change.Breaking {
			t.Errorf("%s was removed but reported as %+v", op, c)
		}
	}
	if !strings.Contains(got["GET /old"].Summary, "deprecated") {
		t.Errorf("summary should say the endpoint had been deprecated: %q", got["GET /old"].Summary)
	}
	for op := range got {
		if strings.HasPrefix(op, "GET /b/") {
			t.Errorf("renaming a path parameter is not a removal: %s", op)
		}
	}
	if len(cs) != 2 {
		t.Errorf("got %d changes, want exactly the two removals: %+v", len(cs), cs)
	}
}

func TestCompare_ARevisionWithNoEndpointsIsRefused(t *testing.T) {
	stub := []byte("openapi: 3.0.3\ninfo: {title: t, version: \"3\"}\n")
	_, err := Compare(fixture(t, "acme_before.yaml"), stub, Options{})
	if !errors.Is(err, specload.ErrNotSpec) {
		t.Fatalf("err = %v; a revision with no endpoints must not read as 'every endpoint removed'", err)
	}
	if _, _, err := CompareResilient(fixture(t, "acme_before.yaml"), stub, Options{}); err == nil {
		t.Error("the resilient path must refuse it too")
	}
}

func TestCompare_OneChangeInSeveralMediaTypesIsOneRecord(t *testing.T) {
	mk := func(enum string, v string) []byte {
		body := "{type: string" + enum + "}"
		return []byte("openapi: 3.0.3\ninfo: {title: t, version: \"" + v + "\"}\npaths:\n  /a:\n    post:\n      requestBody:\n        content:\n" +
			"          application/json: {schema: " + body + "}\n          application/xml: {schema: " + body + "}\n          text/plain: {schema: " + body + "}\n" +
			"      responses: {\"200\": {description: ok}}\n")
	}
	cs, err := Compare(mk("", "1"), mk(", enum: [x]", "2"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 {
		t.Errorf("got %d records for one change served in three media types: %+v", len(cs), cs)
	}
}

func TestSeverity_PublisherPolicyFindingsAreDeprecationsForACaller(t *testing.T) {
	for _, rule := range []string{"api-sunset-date-too-small", "api-deprecated-sunset-missing", "request-parameter-sunset-date-too-small", "api-sunset-date-changed-too-small"} {
		if got := severityFor(rule, 3, nil); got != change.Warning {
			t.Errorf("%s = %s, want warning: the endpoint still exists", rule, got)
		}
	}
	if got := severityFor("api-removed-before-sunset", 1, nil); got != change.Breaking {
		t.Errorf("a removal is breaking, got %s", got)
	}
}

func TestGroup_OperationIDBelongsToThePrimaryOperation(t *testing.T) {
	out := group([]change.Change{
		{RuleID: "r", Summary: "x `f`", Method: "POST", Path: "/z", OperationID: "postZ"},
		{RuleID: "r", Summary: "x `f`", Method: "GET", Path: "/a", OperationID: "getA"},
	})
	if len(out) != 1 || out[0].Path != "/a" || out[0].OperationID != "getA" {
		t.Errorf("got %+v; Method/Path and OperationID must describe the same operation", out)
	}
}

func TestNormOp(t *testing.T) {
	if normOp("get", "/b/{id}/c/{x}") != normOp("GET", "/b/{bId}/c/{y}") {
		t.Error("parameter names must not matter")
	}
	if normOp("GET", "/b/{id}") == normOp("POST", "/b/{id}") {
		t.Error("the method must matter")
	}
}

func TestCompare_LooserResponseTypesAreWarningsNotBreaks(t *testing.T) {
	spec := func(prop string) []byte {
		return []byte(`openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /things:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
` + prop)
	}
	before := spec("                  created_at: {type: string, format: date-time}\n                  sent_at: {type: string, format: date-time}\n                  count: {type: integer}\n")
	after := spec("                  created_at: {type: string}\n                  sent_at: {type: [string, \"null\"], format: date-time}\n                  count: {type: string}\n")
	// Type arrays are OpenAPI 3.1.
	before = []byte(strings.Replace(string(before), "3.0.3", "3.1.0", 1))
	after = []byte(strings.Replace(string(after), "3.0.3", "3.1.0", 1))
	cs, err := Compare(before, after, Options{MinSeverity: change.Info})
	if err != nil {
		t.Fatal(err)
	}
	sev := map[string]change.Severity{}
	for _, c := range cs {
		for _, f := range []string{"created_at", "sent_at", "count"} {
			if strings.Contains(c.Summary, "`"+f+"`") {
				sev[f] = c.Severity
			}
		}
	}
	if sev["created_at"] != change.Warning {
		t.Errorf("format dropped from a response: %q, want warning (%v)", sev["created_at"], cs)
	}
	if sev["sent_at"] != change.Warning {
		t.Errorf("response type widened to include null: %q, want warning (%v)", sev["sent_at"], cs)
	}
	if sev["count"] != change.Breaking {
		t.Errorf("integer to string in a response: %q, want breaking", sev["count"])
	}
}
