// Package inventory lists the endpoints a specification describes.
//
// It reads only the parts of a document it needs (the title, the version, and
// each path's operations) and never resolves references, so it handles a
// 45 MB spec in a couple of seconds where a full parse would take far longer.
package inventory

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/yug/unbroke/internal/specload"
)

// Operation is one endpoint: an HTTP method on a path.
type Operation struct {
	Method     string   `json:"method"`
	Path       string   `json:"path"`
	ID         string   `json:"operation_id,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Deprecated bool     `json:"deprecated,omitempty"`
}

// String renders the operation as "METHOD /path".
func (o Operation) String() string { return o.Method + " " + o.Path }

// Result is the endpoint list of one specification.
type Result struct {
	Title      string          `json:"title"`
	Version    string          `json:"version"`
	Format     specload.Format `json:"format"`
	Operations []Operation     `json:"operations"`
	// Unresolved counts paths whose definition is a reference Unbroke could not
	// follow (one into another file). Their operations are missing from the
	// list, so a caller comparing lists should not trust it to be complete.
	Unresolved int `json:"unresolved,omitempty"`
}

// Deprecated counts operations marked deprecated.
func (r *Result) Deprecated() int {
	n := 0
	for _, o := range r.Operations {
		if o.Deprecated {
			n++
		}
	}
	return n
}

var methods = []string{"get", "put", "post", "delete", "patch", "options", "head", "trace"}

// List reads a specification (YAML or JSON, OpenAPI 3 or Swagger 2) and
// returns its operations sorted by path, then method.
func List(data []byte) (*Result, error) {
	j, err := specload.ToJSON(data)
	if err != nil {
		return nil, err
	}
	format, err := specload.Detect(j)
	if err != nil {
		return nil, err
	}

	var doc struct {
		Info struct {
			Title   string `json:"title"`
			Version any    `json:"version"`
		} `json:"info"`
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(j, &doc); err != nil {
		return nil, fmt.Errorf("read paths: %w", err)
	}

	res := &Result{Title: doc.Info.Title, Format: format}
	if doc.Info.Version != nil {
		res.Version = fmt.Sprint(doc.Info.Version)
	}

	var whole any // the parsed document, read only if a path item is a $ref
	for path, item := range doc.Paths {
		if strings.HasPrefix(path, "x-") {
			continue
		}
		// A path item can be a reference to a definition elsewhere.
		if raw, ok := item["$ref"]; ok {
			var ref string
			_ = json.Unmarshal(raw, &ref)
			if whole == nil {
				if err := json.Unmarshal(j, &whole); err != nil {
					whole = struct{}{}
				}
			}
			resolved, ok := resolveLocal(whole, ref)
			if !ok {
				res.Unresolved++
				continue
			}
			item = resolved
		}
		for _, m := range methods {
			raw, ok := item[m]
			if !ok {
				continue
			}
			var op struct {
				ID         string   `json:"operationId"`
				Summary    string   `json:"summary"`
				Tags       []string `json:"tags"`
				Deprecated bool     `json:"deprecated"`
			}
			// A malformed operation still counts as an endpoint; it just
			// has no metadata.
			_ = json.Unmarshal(raw, &op)
			res.Operations = append(res.Operations, Operation{
				Method:     strings.ToUpper(m),
				Path:       path,
				ID:         op.ID,
				Summary:    op.Summary,
				Tags:       op.Tags,
				Deprecated: op.Deprecated,
			})
		}
	}

	order := map[string]int{}
	for i, m := range methods {
		order[strings.ToUpper(m)] = i
	}
	sort.Slice(res.Operations, func(i, j int) bool {
		a, b := res.Operations[i], res.Operations[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return order[a.Method] < order[b.Method]
	})
	return res, nil
}

// resolveLocal follows a reference within the same (parsed) document
// ("#/a/b") and returns the path item it points at. The document is parsed
// once by the caller; parsing it per reference made a large spec whose paths
// are all references take minutes.
func resolveLocal(doc any, ref string) (map[string]json.RawMessage, bool) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	cur := doc
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return nil, false
	}
	var item map[string]json.RawMessage
	if err := json.Unmarshal(b, &item); err != nil {
		return nil, false
	}
	return item, true
}
