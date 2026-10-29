// Package specdiff compares two versions of an OpenAPI description and
// returns the differences as Unbroke change records.
//
// The comparison and the breaking-change rules come from oasdiff, which has
// several hundred checks for things like removed endpoints, new required
// parameters, narrowed enums, and removed response fields. This package
// only loads specs, runs those checks, and translates the results.
package specdiff

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oasdiff/oasdiff/checker"
	"github.com/oasdiff/oasdiff/diff"
	"github.com/oasdiff/oasdiff/load"

	"github.com/yug/unbroke/internal/change"
	"github.com/yug/unbroke/internal/inventory"
	"github.com/yug/unbroke/internal/specload"
)

// ErrLoad marks a spec that is a real OpenAPI document but could not be loaded
// for a full comparison, usually because the provider published it with a
// broken reference or a field of the wrong type. CompareResilient falls back
// to comparing endpoint lists when it sees this.
var ErrLoad = errors.New("spec could not be loaded for a full comparison")

// Options controls a comparison.
type Options struct {
	// API is the provider name stamped on every returned change.
	API string
	// MinSeverity drops changes below this severity. The zero value keeps
	// warnings and breaking changes and drops informational ones.
	MinSeverity change.Severity
}

// Compare diffs two raw OpenAPI documents (YAML or JSON) and returns the
// changes between them, most severe first.
func Compare(base, revision []byte, opts Options) ([]change.Change, error) {
	min := opts.MinSeverity
	if min == "" {
		min = change.Warning
	}

	base, _, err := specload.Normalize(base)
	if err != nil {
		return nil, readError("base", err)
	}
	revision, _, err = specload.Normalize(revision)
	if err != nil {
		return nil, readError("revision", err)
	}

	loader := openapi3.NewLoader()
	baseInfo, err := load.NewSpecInfoFromData(loader, base, "base")
	if err != nil {
		return nil, fmt.Errorf("%w: base spec: %v", ErrLoad, err)
	}
	// A loader caches by location, so give the second spec its own.
	revInfo, err := load.NewSpecInfoFromData(openapi3.NewLoader(), revision, "revision")
	if err != nil {
		return nil, fmt.Errorf("%w: revision spec: %v", ErrLoad, err)
	}

	// An empty or truncated download parses without error but has no
	// endpoints, which would read as "the entire API was removed". Refuse
	// anything that is not actually an OpenAPI document.
	if err := requireSpec(baseInfo, "base"); err != nil {
		return nil, err
	}
	if err := requireSpec(revInfo, "revision"); err != nil {
		return nil, err
	}

	// Both endpoint lists, read directly. They catch what the rule engine is
	// designed to skip (see removedEndpoints) and a revision that lost all its
	// endpoints, which is a broken download or a placeholder, not a real API.
	oldOps, newOps, err := endpointKeys(base, revision)
	if errors.Is(err, errIncomplete) {
		// Some paths are defined in other files, so the lists are partial. The
		// rule engine still compares fully; only the extra safety net is off.
		oldOps, newOps = nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLoad, err)
	}
	if len(newOps) == 0 && len(oldOps) > 0 {
		return nil, fmt.Errorf("%w: the revision lists no endpoints at all (a truncated download or a placeholder), so it was not compared", specload.ErrNotSpec)
	}

	report, sources, err := diff.GetWithOperationsSourcesMap(diff.NewConfig(), baseInfo, revInfo)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLoad, err)
	}

	cfg := checker.NewConfig(checker.GetAllChecks())
	found := checker.CheckBackwardCompatibilityUntilLevel(cfg, report, sources, checker.INFO)

	localize := checker.NewDefaultLocalizer()
	out := make([]change.Change, 0, len(found))
	for _, f := range found {
		c := change.Change{
			API:         opts.API,
			FromVersion: baseInfo.Version,
			ToVersion:   revInfo.Version,
			RuleID:      f.GetId(),
			Severity:    severityFor(f.GetId(), f.GetLevel(), f.GetArgs()),
			Method:      f.GetOperation(),
			Path:        f.GetPath(),
			OperationID: f.GetOperationId(),
			Summary:     f.GetUncolorizedText(localize),
			Detail:      f.GetComment(localize),
			Attributes:  f.GetAttributes(),
			Sunset:      sunsetFrom(f.GetArgs()),
		}
		if !c.AtLeast(min) {
			continue
		}
		out = append(out, c)
	}

	out = append(out, removedEndpoints(out, oldOps, newOps, opts, baseInfo.Version, revInfo.Version)...)
	out = group(out)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity.Rank() != out[j].Severity.Rank() {
			return out[i].Severity.Rank() > out[j].Severity.Rank()
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out, nil
}

// group merges findings that are the same change seen through many
// operations. A schema shared by hundreds of endpoints produces hundreds of
// findings with identical text; they become one Change that lists every
// operation it touches. Findings about the operation itself (an endpoint
// removed or deprecated) are never merged, so two different endpoints can't be
// confused with each other.
func group(cs []change.Change) []change.Change {
	index := map[string]int{}
	opIDs := map[string]string{} // "METHOD /path" -> operation id
	out := make([]change.Change, 0, len(cs))
	for _, c := range cs {
		c.Summary = stripMediaType(c.Summary)
		op := c.Operation()
		if op != "" && c.OperationID != "" {
			opIDs[op] = c.OperationID
		}
		// A finding that names what changed is the same change wherever it
		// appears, so it merges across operations. One that names nothing is
		// about its own operation, but the same sentence repeated for the same
		// operation (one per media type) is still a single change.
		key := c.RuleID + "\x00" + c.Summary
		if !namesWhatChanged(c.Summary) {
			key += "\x00" + op
		}
		i, seen := index[key]
		if !seen {
			index[key] = len(out)
			if op != "" {
				c.Operations = []string{op}
			}
			out = append(out, c)
			continue
		}
		if op != "" && !slices.Contains(out[i].Operations, op) {
			out[i].Operations = append(out[i].Operations, op)
		}
	}
	for i := range out {
		sort.Strings(out[i].Operations)
		// The first operation, alphabetically, is the primary location, so
		// output doesn't depend on the order the checks happened to run in.
		if len(out[i].Operations) > 0 {
			out[i].Method, out[i].Path, _ = strings.Cut(out[i].Operations[0], " ")
			out[i].OperationID = opIDs[out[i].Operations[0]]
		}
	}
	return out
}

// namesWhatChanged reports whether a finding's sentence identifies the thing
// that changed (a property, a scope, an enum value), written between
// backticks. Such findings are the same change wherever they appear, so they
// merge across operations. A sentence that names nothing, like "api path
// removed without deprecation", describes the operation itself; merging those
// would bury the list of removed endpoints, which is what a reader wants.
func namesWhatChanged(summary string) bool {
	return strings.Contains(summary, "`")
}

var mediaTypeSuffix = regexp.MustCompile(` \(media type: [^)]*\)`)

// stripMediaType removes the "(media type: ...)" note oasdiff appends. A
// provider that serves the same response as JSON, YAML, protobuf and CBOR
// would otherwise report one change four times.
func stripMediaType(summary string) string {
	return mediaTypeSuffix.ReplaceAllString(summary, "")
}

// removedRules are the findings that say an endpoint is gone.
var removedRules = map[string]bool{
	"api-path-removed-without-deprecation": true, "api-path-removed-with-deprecation": true,
	"api-removed-without-deprecation": true, "api-removed-with-deprecation": true,
	"api-removed-before-sunset": true, "api-path-removed-before-sunset": true,
}

// removedEndpoints reports endpoints that exist in the base and not in the
// revision but that the rule engine did not report. It skips, by design,
// removals it considers properly announced (deprecated with a sunset date that
// has passed) and removals of operations marked alpha or draft. That is the
// publisher's view; a caller still breaks when the endpoint disappears.
func removedEndpoints(found []change.Change, oldOps, newOps map[string]inventory.Operation, opts Options, from, to string) []change.Change {
	reported := map[string]bool{}
	for _, c := range found {
		if removedRules[c.RuleID] {
			reported[normOp(c.Method, c.Path)] = true
		}
	}
	var out []change.Change
	for key, o := range oldOps {
		if _, still := newOps[key]; still || reported[key] {
			continue
		}
		summary := "endpoint removed"
		if o.Deprecated {
			summary = "endpoint removed (it had been marked deprecated)"
		}
		out = append(out, change.Change{
			API: opts.API, FromVersion: from, ToVersion: to,
			RuleID: "endpoint-removed", Severity: change.Breaking,
			Method: o.Method, Path: o.Path, OperationID: o.ID, Summary: summary,
		})
	}
	return out
}

// endpointKeys lists both specs' endpoints keyed by method and path, with
// parameter names ignored, so /b/{id} renamed to /b/{bId} is the same endpoint.
func endpointKeys(base, revision []byte) (map[string]inventory.Operation, map[string]inventory.Operation, error) {
	read := func(doc []byte) (map[string]inventory.Operation, error) {
		inv, err := inventory.List(doc)
		if err != nil {
			return nil, err
		}
		if inv.Unresolved > 0 {
			return nil, errIncomplete
		}
		m := map[string]inventory.Operation{}
		for _, o := range inv.Operations {
			m[normOp(o.Method, o.Path)] = o
		}
		return m, nil
	}
	o, err := read(base)
	if err != nil {
		return nil, nil, fmt.Errorf("base spec: %w", err)
	}
	n, err := read(revision)
	if err != nil {
		return nil, nil, fmt.Errorf("revision spec: %w", err)
	}
	return o, n, nil
}

// errIncomplete means an endpoint list could not be read in full.
var errIncomplete = errors.New("endpoint list is incomplete")

var pathParamRE = regexp.MustCompile(`\{[^}]*\}`)

// normOp keys an operation by method and path with parameter names removed.
func normOp(method, path string) string {
	return strings.ToUpper(method) + " " + pathParamRE.ReplaceAllString(path, "{}")
}

// readError classifies a failure to read a document. A document that is not a
// spec at all must stay a hard error, since comparing it would read as "every
// endpoint was removed". Anything else is a real spec Unbroke could not convert.
func readError(which string, err error) error {
	if errors.Is(err, specload.ErrNotSpec) {
		return fmt.Errorf("read %s spec: %w", which, err)
	}
	return fmt.Errorf("%w: %s spec: %v", ErrLoad, which, err)
}

func requireSpec(info *load.SpecInfo, label string) error {
	if info == nil || info.Spec == nil || info.Spec.OpenAPI == "" || info.Spec.Info == nil {
		return fmt.Errorf("%s document is not an OpenAPI description (no openapi version or info block)", label)
	}
	return nil
}

// severityOverrides adjusts oasdiff's levels to the point of view of someone
// who calls the API. oasdiff rates a change as the publisher checking its own
// deprecation policy, so a properly announced removal is "info". For a caller
// an announced removal is the earliest warning there is, and a removal that
// already happened is a break no matter how politely it was announced.
var severityOverrides = map[string]change.Severity{
	"endpoint-deprecated":                        change.Warning,
	"endpoint-deprecated-with-sunset":            change.Warning,
	"request-parameter-deprecated":               change.Warning,
	"request-property-deprecated":                change.Warning,
	"request-property-deprecated-with-sunset":    change.Warning,
	"response-property-deprecated":               change.Warning,
	"response-property-deprecated-with-sunset":   change.Warning,
	"request-parameter-removed-with-deprecation": change.Warning,
	"api-removed-with-deprecation":               change.Breaking,
	"api-path-removed-with-deprecation":          change.Breaking,
	"api-removed-before-sunset":                  change.Breaking,
	"api-path-removed-before-sunset":             change.Breaking,
	"request-parameter-removed-before-sunset":    change.Warning,

	// oasdiff also checks the publisher's deprecation hygiene: a sunset date that
	// is missing, unparseable, too soon, or already past. For a caller each of
	// these is just a deprecation (the endpoint still exists), so a warning.
	// Without this, a deprecation dated in the past would read as "breaking",
	// and the same diff would change rating depending on the day it was run.
	"api-deprecated-sunset-missing":                   change.Warning,
	"api-deprecated-sunset-parse":                     change.Warning,
	"api-path-sunset-parse":                           change.Warning,
	"api-sunset-date-too-small":                       change.Warning,
	"api-sunset-date-changed-too-small":               change.Warning,
	"request-parameter-deprecated-sunset-missing":     change.Warning,
	"request-parameter-sunset-date-too-small":         change.Warning,
	"request-parameter-sunset-date-changed-too-small": change.Warning,
	"request-parameter-sunset-parse":                  change.Warning,
	"request-property-deprecated-sunset-missing":      change.Warning,
	"request-property-deprecated-sunset-invalid":      change.Warning,
	"request-property-sunset-date-too-small":          change.Warning,
	"response-property-deprecated-sunset-missing":     change.Warning,
	"response-property-deprecated-sunset-invalid":     change.Warning,
	"response-property-sunset-date-too-small":         change.Warning,
	// A sunset date taken away usually means the deprecation was lifted.
	"sunset-deleted":                   change.Info,
	"request-parameter-sunset-deleted": change.Info,

	// Additions to a response widen what a caller may receive. They break
	// only clients that reject values they have not seen (strict enums,
	// closed unions), so they are informational unless a repo opts in.
	"response-property-enum-value-added": change.Info,
	"response-property-any-of-added":     change.Info,
	"response-body-any-of-added":         change.Info,
	"response-property-max-length-unset": change.Info,
	"response-property-one-of-added":     change.Info,
	"response-body-one-of-added":         change.Info,

	// A response field that becomes optional or nullable breaks only a caller
	// that assumed it was always present. That is a risk to check, not a
	// certain break, so it is a warning.
	"response-property-became-optional": change.Warning,
	"response-property-became-nullable": change.Warning,
}

func severityFor(ruleID string, l checker.Level, args []any) change.Severity {
	if s, ok := severityOverrides[ruleID]; ok {
		return s
	}
	// A response type loosened to "any" widens what a caller may receive,
	// like the additions above. Any other type change stays as rated.
	if (ruleID == "response-property-type-changed" || ruleID == "response-body-type-changed") &&
		revisionTypeIsAny(args) {
		return change.Info
	}
	switch l {
	case checker.ERR:
		return change.Breaking
	case checker.WARN:
		return change.Warning
	default:
		return change.Info
	}
}

// revisionTypeIsAny reports whether a type-change finding says the new type is
// "any". The finding's arguments end with the base type, the revision type,
// and the response status, in that order.
func revisionTypeIsAny(args []any) bool {
	if len(args) < 3 {
		return false
	}
	rev, _ := args[len(args)-2].(string)
	base, _ := args[len(args)-3].(string)
	return rev == "any" && base != "any"
}

// sunsetFrom pulls the announced removal date out of a check's arguments.
// oasdiff passes it as a civil date whose String form is YYYY-MM-DD, so this
// reads that without importing the date package.
func sunsetFrom(args []any) *time.Time {
	for _, a := range args {
		s, ok := a.(fmt.Stringer)
		if !ok {
			continue
		}
		if t, err := time.Parse("2006-01-02", s.String()); err == nil {
			return &t
		}
	}
	return nil
}
