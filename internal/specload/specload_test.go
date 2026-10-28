package specload

import (
	"errors"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const swagger2JSON = `{
  "swagger": "2.0",
  "info": {"title": "Old Service", "version": "1"},
  "host": "api.example.com",
  "basePath": "/v1",
  "paths": {
    "/widgets": {
      "post": {
        "operationId": "createWidget",
        "consumes": ["application/json"],
        "parameters": [
          {"in": "body", "name": "body", "required": true,
           "schema": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}}}
        ],
        "responses": {"200": {"description": "ok", "schema": {"type": "object", "properties": {"id": {"type": "string"}}}}}
      }
    }
  }
}`

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want Format
		err  bool
	}{
		{"openapi 3 string", `{"openapi":"3.0.3"}`, OpenAPI3, false},
		{"swagger 2 string", `{"swagger":"2.0"}`, Swagger2, false},
		{"swagger 2 bare number", `{"swagger":2.0}`, Swagger2, false},
		{"empty object", `{}`, "", true},
		{"unrelated json", `{"hello":"world"}`, "", true},
		{"not json", `not json`, "", true},
	}
	for _, tc := range cases {
		got, err := Detect([]byte(tc.doc))
		if tc.err {
			if !errors.Is(err, ErrNotSpec) {
				t.Errorf("%s: err = %v, want ErrNotSpec", tc.name, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: got (%q, %v), want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestToJSON_AcceptsYAMLAndJSON(t *testing.T) {
	y, err := ToJSON([]byte("swagger: 2.0\ninfo:\n  title: x\n"))
	if err != nil || !strings.HasPrefix(string(y), "{") {
		t.Fatalf("yaml -> json failed: %q, %v", y, err)
	}
	if f, err := Detect(y); err != nil || f != Swagger2 {
		t.Errorf("unquoted YAML version should still be detected as swagger 2; got %q, %v", f, err)
	}
	j, err := ToJSON([]byte("  {\"openapi\":\"3.0.0\"}\n"))
	if err != nil || string(j) != `{"openapi":"3.0.0"}` {
		t.Errorf("json passthrough = %q, %v", j, err)
	}
}

func TestNormalize_OpenAPI3IsReturnedUnchanged(t *testing.T) {
	in := []byte("openapi: 3.0.3\ninfo:\n  title: x\n  version: '1'\npaths: {}\n")
	out, f, err := Normalize(in)
	if err != nil || f != OpenAPI3 || string(out) != string(in) {
		t.Errorf("got (%q, %q, %v), want input back unchanged", out, f, err)
	}
}

func TestNormalize_ConvertsSwagger2(t *testing.T) {
	out, f, err := Normalize([]byte(swagger2JSON))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if f != Swagger2 {
		t.Errorf("format = %q, want swagger2", f)
	}
	doc, err := openapi3.NewLoader().LoadFromData(out)
	if err != nil {
		t.Fatalf("converted document is not loadable OpenAPI 3: %v", err)
	}
	op := doc.Paths.Find("/widgets").Post
	if op == nil {
		t.Fatal("POST /widgets missing after conversion")
	}
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		t.Fatal("body parameter should have become a request body")
	}
	if _, ok := op.RequestBody.Value.Content["application/json"]; !ok {
		t.Errorf("request body content types = %v, want application/json", op.RequestBody.Value.Content)
	}
}

func TestNormalize_RejectsNonSpecs(t *testing.T) {
	for _, in := range []string{"", "{}", `{"hello":"world"}`, "just text"} {
		if _, _, err := Normalize([]byte(in)); !errors.Is(err, ErrNotSpec) {
			t.Errorf("Normalize(%q) err = %v, want ErrNotSpec", in, err)
		}
	}
}
