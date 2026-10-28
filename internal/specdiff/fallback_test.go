package specdiff

import (
	"errors"
	"strings"
	"testing"

	"github.com/yug/mend/internal/change"
)

// brokenBase is a real OpenAPI document that a strict loader rejects: one
// response points at a schema that does not exist. Providers have published
// specs like this.
const brokenBase = `{
 "openapi":"3.0.3","info":{"title":"Old","version":"1"},
 "paths":{
  "/a":{"get":{"operationId":"getA","responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#/components/schemas/Missing"}}}}}}},
  "/b":{"get":{"operationId":"getB","responses":{"200":{"description":"ok"}}}},
  "/d":{"get":{"operationId":"getD","responses":{"200":{"description":"ok"}}}}
 }}`

const goodRevision = `{
 "openapi":"3.0.3","info":{"title":"New","version":"2"},
 "paths":{
  "/a":{"get":{"operationId":"getA","responses":{"200":{"description":"ok"}}}},
  "/c":{"get":{"operationId":"getC","responses":{"200":{"description":"ok"}}}},
  "/d":{"get":{"operationId":"getD","deprecated":true,"responses":{"200":{"description":"ok"}}}}
 }}`

func TestCompareResilient_FallsBackWhenASpecWillNotLoad(t *testing.T) {
	if _, err := Compare([]byte(brokenBase), []byte(goodRevision), Options{}); !errors.Is(err, ErrLoad) {
		t.Fatalf("precondition: the strict comparison should fail with ErrLoad, got %v", err)
	}
	cs, note, err := CompareResilient([]byte(brokenBase), []byte(goodRevision), Options{API: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "endpoint-level") {
		t.Errorf("note = %q, want it to say only endpoint-level changes are shown", note)
	}
	if strings.Contains(note, "could not be loaded for a full comparison") {
		t.Errorf("note repeats the generic prefix instead of giving the cause: %q", note)
	}
	if !strings.Contains(note, "Missing") {
		t.Errorf("note = %q, want it to name the schema the broken reference points at", note)
	}
	got := map[string]change.Severity{}
	for _, c := range cs {
		got[c.RuleID+" "+c.Operation()] = c.Severity
	}
	if got["endpoint-removed GET /b"] != change.Breaking {
		t.Errorf("removed endpoint not reported as breaking: %v", got)
	}
	if got["endpoint-newly-deprecated GET /d"] != change.Warning {
		t.Errorf("newly deprecated endpoint not reported as a warning: %v", got)
	}
	for k := range got {
		if strings.HasPrefix(k, "endpoint-added") {
			t.Errorf("an added endpoint is informational and should be hidden by default: %v", got)
		}
	}
}

func TestCompareResilient_InfoThresholdShowsAddedEndpoints(t *testing.T) {
	cs, _, err := CompareResilient([]byte(brokenBase), []byte(goodRevision), Options{MinSeverity: change.Info})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cs {
		if c.RuleID == "endpoint-added" && c.Operation() == "GET /c" {
			found = true
		}
	}
	if !found {
		t.Errorf("GET /c was added but not reported: %+v", cs)
	}
}

func TestCompareResilient_NeverTurnsAnEmptyRevisionIntoMassRemoval(t *testing.T) {
	empty := `{"openapi":"3.0.3","info":{"title":"x","version":"1"},"paths":{}}`
	_, _, err := CompareResilient([]byte(brokenBase), []byte(empty), Options{})
	if err == nil || !strings.Contains(err.Error(), "no endpoints") {
		t.Errorf("err = %v, want a refusal because the revision lists no endpoints", err)
	}
}

func TestCompareResilient_StillRejectsDocumentsThatAreNotSpecs(t *testing.T) {
	for _, bad := range []string{"{}", "", "not a spec", `{"hello":"world"}`} {
		if _, _, err := CompareResilient([]byte(brokenBase), []byte(bad), Options{}); err == nil {
			t.Errorf("revision %q should be an error, not a comparison", bad)
		}
	}
}

func TestCompareResilient_HealthySpecsUseTheFullComparison(t *testing.T) {
	cs, note, err := CompareResilient(fixture(t, "acme_before.yaml"), fixture(t, "acme_after.yaml"), Options{API: "acme"})
	if err != nil || note != "" {
		t.Fatalf("note = %q, err = %v; healthy specs should not degrade", note, err)
	}
	full, _ := Compare(fixture(t, "acme_before.yaml"), fixture(t, "acme_after.yaml"), Options{API: "acme"})
	if len(cs) != len(full) {
		t.Errorf("got %d changes, the full comparison gives %d", len(cs), len(full))
	}
}

func TestGroup_ParameterChangesAcrossOperationsAreOneRecord(t *testing.T) {
	spec := func(enum string) string {
		op := `{"parameters":[{"name":"fields","in":"query","schema":{"type":"string","enum":` + enum + `}}],"responses":{"200":{"description":"ok"}}}`
		return `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{"/a":{"get":` + op + `},"/b":{"get":` + op + `}}}`
	}
	cs, err := Compare([]byte(spec(`["x","y","z"]`)), []byte(spec(`["x","y"]`)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var found []change.Change
	for _, c := range cs {
		if c.RuleID == "request-parameter-enum-value-removed" {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the same enum value was removed from the same parameter on two endpoints; got %d records, want 1: %+v", len(found), found)
	}
	if len(found[0].Operations) != 2 {
		t.Errorf("Operations = %v, want both endpoints", found[0].Operations)
	}
}

func TestNamesWhatChanged(t *testing.T) {
	cases := map[string]bool{
		"api path removed without deprecation":                   false,
		"removed the request body":                               false,
		"the security scope `org:read` was added":                true,
		"removed the enum value `x` of the request property `y`": true,
	}
	for s, want := range cases {
		if got := namesWhatChanged(s); got != want {
			t.Errorf("namesWhatChanged(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestStripMediaType(t *testing.T) {
	in := "the response property `a/b` became optional for the status `200` (media type: application/cbor)"
	want := "the response property `a/b` became optional for the status `200`"
	if got := stripMediaType(in); got != want {
		t.Errorf("stripMediaType = %q, want %q", got, want)
	}
	if got := stripMediaType("nothing to strip"); got != "nothing to strip" {
		t.Errorf("stripMediaType changed text without a media type note: %q", got)
	}
}

func TestGroup_SameChangeInSeveralMediaTypesIsOneRecord(t *testing.T) {
	mk := func(media string) change.Change {
		return change.Change{
			RuleID: "response-property-became-optional", Method: "GET", Path: "/pods",
			Summary: "the response property `spec` became optional for the status `200` (media type: " + media + ")",
		}
	}
	out := group([]change.Change{mk("application/json"), mk("application/yaml"), mk("application/cbor")})
	if len(out) != 1 {
		t.Fatalf("got %d records, want 1 (one change, three media types): %+v", len(out), out)
	}
	if strings.Contains(out[0].Summary, "media type") {
		t.Errorf("summary still mentions the media type: %q", out[0].Summary)
	}
}

func TestGroup_EndpointRemovalsStayPerEndpoint(t *testing.T) {
	mk := func(path string) change.Change {
		return change.Change{RuleID: "api-path-removed-without-deprecation", Method: "GET", Path: path, Summary: "api path removed without deprecation"}
	}
	out := group([]change.Change{mk("/a"), mk("/b"), mk("/c")})
	if len(out) != 3 {
		t.Errorf("got %d records, want 3: each removed endpoint should be listed on its own", len(out))
	}
}

func TestGroup_SameScopeAddedToManyEndpointsIsOneRecord(t *testing.T) {
	mk := func(path string) change.Change {
		return change.Change{RuleID: "api-security-scope-added", Method: "GET", Path: path,
			Summary: "the security scope `org:read` was added to the endpoint's security scheme `auth_token`"}
	}
	out := group([]change.Change{mk("/a"), mk("/b"), mk("/c")})
	if len(out) != 1 || len(out[0].Operations) != 3 {
		t.Errorf("got %+v, want one record naming three operations", out)
	}
}

func TestSeverity_LooseningAndWideningAreNotBreaking(t *testing.T) {
	for rule, want := range map[string]change.Severity{
		"response-property-became-optional": change.Warning,
		"response-property-became-nullable": change.Warning,
		"response-property-one-of-added":    change.Info,
		"response-property-any-of-added":    change.Info,
	} {
		if got := severityFor(rule, 3, nil); got != want {
			t.Errorf("severityFor(%q) = %s, want %s", rule, got, want)
		}
	}
}
