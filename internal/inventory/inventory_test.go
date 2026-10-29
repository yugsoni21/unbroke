package inventory

import (
	"os"
	"strings"
	"testing"

	"github.com/yug/unbroke/internal/specload"
)

func TestList_OpenAPI3Fixture(t *testing.T) {
	data, err := os.ReadFile("../specdiff/testdata/acme_before.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, err := List(data)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if r.Title != "Acme Payments" || r.Version != "2026-06-01" || r.Format != specload.OpenAPI3 {
		t.Errorf("header = %q %q %q", r.Title, r.Version, r.Format)
	}
	want := []string{
		"POST /v1/charges", "GET /v1/customers/{id}", "GET /v1/invoices",
		"GET /v1/me", "POST /v1/refunds", "GET /v1/sources",
	}
	if len(r.Operations) != len(want) {
		t.Fatalf("got %d operations, want %d: %v", len(r.Operations), len(want), r.Operations)
	}
	for i, w := range want {
		if got := r.Operations[i].String(); got != w {
			t.Errorf("operation %d = %q, want %q", i, got, w)
		}
	}
	if r.Operations[0].ID != "createCharge" {
		t.Errorf("operation id = %q, want createCharge", r.Operations[0].ID)
	}
}

func TestList_CountsDeprecated(t *testing.T) {
	data, _ := os.ReadFile("../specdiff/testdata/acme_after.yaml")
	r, err := List(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Deprecated(); got != 1 {
		t.Errorf("Deprecated() = %d, want 1 (GET /v1/sources)", got)
	}
}

func TestList_SortsByPathThenMethod(t *testing.T) {
	doc := `{"openapi":"3.0.0","info":{"title":"t","version":"1"},"paths":{
	  "/b":{"post":{},"get":{}},
	  "/a":{"delete":{},"get":{},"parameters":[]},
	  "x-internal":{"get":{}}}}`
	r, err := List([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range r.Operations {
		got = append(got, o.String())
	}
	want := []string{"GET /a", "DELETE /a", "GET /b", "POST /b"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
}

func TestList_Swagger2AndYAMLNumberVersion(t *testing.T) {
	doc := "swagger: 2.0\ninfo:\n  title: Old\n  version: 1.5\npaths:\n  /w:\n    get:\n      operationId: listW\n"
	r, err := List([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != specload.Swagger2 || r.Version != "1.5" || len(r.Operations) != 1 {
		t.Errorf("got format=%q version=%q ops=%d", r.Format, r.Version, len(r.Operations))
	}
}

func TestList_RejectsNonSpec(t *testing.T) {
	if _, err := List([]byte("{}")); err == nil {
		t.Error("expected an error for an empty document")
	}
}

func TestList_PathItemReferences(t *testing.T) {
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
	  "paths":{"/a":{"$ref":"#/components/pathItems/A"},"/b":{"$ref":"other.yaml#/B"},"/c":{"get":{}}},
	  "components":{"pathItems":{"A":{"get":{"operationId":"getA"},"post":{}}}}}`
	r, err := List([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range r.Operations {
		got = append(got, o.String())
	}
	want := []string{"GET /a", "POST /a", "GET /c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("operations = %v, want %v", got, want)
	}
	if r.Unresolved != 1 {
		t.Errorf("Unresolved = %d, want 1 (the reference into another file)", r.Unresolved)
	}
}
