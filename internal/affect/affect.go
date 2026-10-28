// Package affect decides which API changes matter to a particular codebase,
// by joining the changes a provider made to the places the code calls them.
package affect

import (
	"regexp"
	"sort"
	"strings"

	"github.com/yug/unbroke/internal/change"
	"github.com/yug/unbroke/internal/scan"
)

// Relevance says how a change relates to the code that calls the endpoint.
type Relevance string

const (
	// RemovedEndpoint: the code calls an endpoint that no longer exists.
	RemovedEndpoint Relevance = "calls-removed-endpoint"
	// ChangedField: the code calls the endpoint and the field that changed
	// appears next to the call.
	ChangedField Relevance = "uses-changed-field"
	// CallsEndpoint: the code calls the endpoint, but nothing near the call
	// mentions the field that changed, so it may well be unaffected.
	CallsEndpoint Relevance = "calls-endpoint"
)

// Hit is one call site, with what Unbroke could tell about the changed field.
type Hit struct {
	scan.Match
	// Field is the changed field's name when it was found near the call.
	Field string `json:"field,omitempty"`
}

// Finding is a change that touches code in the repository.
type Finding struct {
	Provider  string        `json:"provider"`
	Change    change.Change `json:"change"`
	Relevance Relevance     `json:"relevance"`
	Hits      []Hit         `json:"hits"`
}

// Priority orders findings: higher means look at it first.
func (f Finding) Priority() int {
	p := 0
	switch f.Relevance {
	case RemovedEndpoint:
		p = 30
	case ChangedField:
		p = 20
	default:
		p = 10
	}
	switch f.Change.Severity {
	case change.Breaking:
		p += 2
	case change.Warning:
		p++
	}
	return p
}

var removedRules = map[string]bool{
	"api-path-removed-without-deprecation": true,
	"api-path-removed-with-deprecation":    true,
	"api-removed-without-deprecation":      true,
	"api-removed-with-deprecation":         true,
	"api-removed-before-sunset":            true,
	"api-path-removed-before-sunset":       true,
	"endpoint-removed":                     true,
}

// Correlate returns the changes that touch at least one call in matches.
// Changes the code does not call are dropped; that is the point.
func Correlate(provider string, changes []change.Change, matches []scan.Match) []Finding {
	byOp := map[string][]scan.Match{}
	for _, m := range matches {
		byOp[m.Operation] = append(byOp[m.Operation], m)
	}

	var out []Finding
	for _, c := range changes {
		ops := c.Operations
		if len(ops) == 0 && c.Operation() != "" {
			ops = []string{c.Operation()}
		}
		var ms []scan.Match
		for _, op := range ops {
			ms = append(ms, byOp[op]...)
		}
		if len(ms) == 0 {
			continue
		}

		f := Finding{Provider: provider, Change: c, Relevance: CallsEndpoint}
		names := fieldNames(c)
		used := false
		for _, m := range ms {
			h := Hit{Match: m}
			if name := findField(m.Window, names); name != "" {
				h.Field, used = name, true
			}
			f.Hits = append(f.Hits, h)
		}
		switch {
		case removedRules[c.RuleID]:
			f.Relevance = RemovedEndpoint
		case used:
			f.Relevance = ChangedField
		}
		sort.SliceStable(f.Hits, func(i, j int) bool {
			if (f.Hits[i].Field != "") != (f.Hits[j].Field != "") {
				return f.Hits[i].Field != ""
			}
			return f.Hits[i].Confidence == scan.High && f.Hits[j].Confidence != scan.High
		})
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority() > out[j].Priority() })
	return out
}

var (
	backticked = regexp.MustCompile("`([^`]+)`")
	generic    = map[string]bool{
		"items": true, "type": true, "id": true, "name": true, "value": true, "data": true,
		"object": true, "status": true, "200": true, "201": true, "204": true, "400": true,
		"default": true, "body": true, "query": true, "path": true, "header": true, "string": true,
		"any": true, "list": true, "anyof": true, "oneof": true, "allof": true,
	}
)

// fieldNames pulls the names a change is about out of its sentence: the
// property or parameter, and any enum value, as they would be written in code.
func fieldNames(c change.Change) []string {
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		n = strings.Trim(n, " ./")
		if len(n) < 3 || generic[strings.ToLower(n)] || seen[n] || strings.ContainsAny(n, " #[]()") {
			return
		}
		seen[n] = true
		names = append(names, n)
	}
	for _, m := range backticked.FindAllStringSubmatch(c.Summary, -1) {
		tok := m[1]
		if strings.Contains(tok, "application/") || strings.HasPrefix(tok, "subschema") {
			continue
		}
		segs := strings.Split(tok, "/")
		// The last specific segment is the field; its parent helps when the
		// last one is generic ("billing_cycle_anchor/type").
		for i := len(segs) - 1; i >= 0 && i >= len(segs)-2; i-- {
			add(segs[i])
		}
	}
	return names
}

// findField reports the first name that appears as a whole word in the code
// window, written in snake_case, camelCase or as it is in the API.
func findField(window string, names []string) string {
	lower := strings.ToLower(window)
	for _, n := range names {
		for _, form := range spellings(n) {
			if hasWord(lower, form) {
				return n
			}
		}
	}
	return ""
}

func spellings(name string) []string {
	snake := strings.ToLower(name)
	forms := []string{snake}
	if strings.Contains(snake, "_") {
		forms = append(forms, strings.ReplaceAll(snake, "_", ""))
	}
	return forms
}

// hasWord reports whether w occurs in s not inside a longer identifier.
func hasWord(s, w string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(w)
		okStart := start == 0 || !isIdent(s[start-1])
		okEnd := end == len(s) || !isIdent(s[end])
		if okStart && okEnd {
			return true
		}
		i = start + 1
	}
}

func isIdent(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
