// Package specload turns whatever a provider publishes into something the
// rest of Mend can read: JSON text, with Swagger 2.0 converted to OpenAPI 3.
//
// Providers publish YAML or JSON, and a surprising number still publish
// Swagger 2.0 (Slack, Kubernetes, Docker, Netlify). Every entry point into
// Mend goes through here so the rest of the code only ever sees OpenAPI 3.
package specload

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/oasdiff/yaml"
)

// Format is the specification family a document belongs to.
type Format string

const (
	OpenAPI3 Format = "openapi3"
	Swagger2 Format = "swagger2"
)

// ErrNotSpec means the document parsed but is not an OpenAPI or Swagger
// description. An empty or truncated download lands here, which keeps it from
// being read as "every endpoint was removed".
var ErrNotSpec = errors.New("not an OpenAPI or Swagger document")

// ToJSON returns the document as JSON, converting from YAML when needed.
func ToJSON(data []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return trimmed, nil
	}
	out, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotSpec, err)
	}
	return out, nil
}

// Detect reports the format of a JSON document from its version marker.
func Detect(jsonDoc []byte) (Format, error) {
	// The marker can be a string ("3.0.1") or, in sloppy YAML, a bare number
	// (2.0), so read it as any.
	var head struct {
		OpenAPI any `json:"openapi"`
		Swagger any `json:"swagger"`
	}
	if err := json.Unmarshal(jsonDoc, &head); err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotSpec, err)
	}
	switch {
	case head.OpenAPI != nil && fmt.Sprint(head.OpenAPI) != "":
		return OpenAPI3, nil
	case head.Swagger != nil && fmt.Sprint(head.Swagger) != "":
		return Swagger2, nil
	}
	return "", fmt.Errorf("%w: no openapi or swagger version", ErrNotSpec)
}

// Normalize returns the document as OpenAPI 3. OpenAPI 3 input is returned
// unchanged; Swagger 2 input is converted and returned as JSON.
func Normalize(data []byte) ([]byte, Format, error) {
	j, err := ToJSON(data)
	if err != nil {
		return nil, "", err
	}
	f, err := Detect(j)
	if err != nil {
		return nil, "", err
	}
	if f == OpenAPI3 {
		return data, f, nil
	}

	var v2 openapi2.T
	if err := json.Unmarshal(j, &v2); err != nil {
		return nil, f, fmt.Errorf("read swagger 2 document: %w", err)
	}
	v3, err := openapi2conv.ToV3(&v2)
	if err != nil {
		return nil, f, fmt.Errorf("convert swagger 2 to openapi 3: %w", err)
	}
	out, err := json.Marshal(v3)
	if err != nil {
		return nil, f, fmt.Errorf("write converted document: %w", err)
	}
	return out, f, nil
}

// Canonical returns a normalised JSON form of a document in which key order
// and the order of lists of plain values (scopes, tags, enums, required names)
// no longer matter. Two downloads of the same spec that a provider's server
// happened to serialise in a different order have the same canonical form.
func Canonical(data []byte) ([]byte, error) {
	j, err := ToJSON(data)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(j, &v); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotSpec, err)
	}
	return json.Marshal(canon(v))
}

func canon(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = canon(e)
		}
		return x
	case []any:
		scalars := true
		for i, e := range x {
			x[i] = canon(e)
			switch e.(type) {
			case map[string]any, []any:
				scalars = false
			}
		}
		if scalars {
			sort.Slice(x, func(i, j int) bool {
				a, _ := json.Marshal(x[i])
				b, _ := json.Marshal(x[j])
				return string(a) < string(b)
			})
		}
		return x
	}
	return v
}
