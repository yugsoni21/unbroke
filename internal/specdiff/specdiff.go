// Package specdiff compares two versions of an OpenAPI description and
// returns the differences as Mend change records.
//
// The comparison and the breaking-change rules come from oasdiff, which has
// several hundred checks for things like removed endpoints, new required
// parameters, narrowed enums, and removed response fields. This package
// only loads specs, runs those checks, and translates the results.
package specdiff

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oasdiff/oasdiff/checker"
	"github.com/oasdiff/oasdiff/diff"
	"github.com/oasdiff/oasdiff/load"

	"github.com/yug/mend/internal/change"
	"github.com/yug/mend/internal/specload"
)

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
		return nil, fmt.Errorf("read base spec: %w", err)
	}
	revision, _, err = specload.Normalize(revision)
	if err != nil {
		return nil, fmt.Errorf("read revision spec: %w", err)
	}

	loader := openapi3.NewLoader()
	baseInfo, err := load.NewSpecInfoFromData(loader, base, "base")
	if err != nil {
		return nil, fmt.Errorf("load base spec: %w", err)
	}
	// A loader caches by location, so give the second spec its own.
	revInfo, err := load.NewSpecInfoFromData(openapi3.NewLoader(), revision, "revision")
	if err != nil {
		return nil, fmt.Errorf("load revision spec: %w", err)
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

	report, sources, err := diff.GetWithOperationsSourcesMap(diff.NewConfig(), baseInfo, revInfo)
	if err != nil {
		return nil, fmt.Errorf("diff specs: %w", err)
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
// operation it touches. Findings about one specific operation (an endpoint
// removed or deprecated, a query parameter changed) are never merged, so two
// different endpoints can't be confused with each other.
func group(cs []change.Change) []change.Change {
	index := map[string]int{}
	out := make([]change.Change, 0, len(cs))
	for _, c := range cs {
		op := c.Operation()
		if !fromSharedSchema(c.RuleID) {
			if op != "" {
				c.Operations = []string{op}
			}
			out = append(out, c)
			continue
		}
		key := c.RuleID + "\x00" + c.Summary
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
		}
	}
	return out
}

// fromSharedSchema reports whether a rule describes a change to a schema
// element, which can be reached from many operations, as opposed to a change
// to one operation itself.
func fromSharedSchema(ruleID string) bool {
	return strings.HasPrefix(ruleID, "response-") ||
		strings.HasPrefix(ruleID, "request-property-") ||
		ruleID == "new-required-request-property"
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

	// Additions to a response widen what a caller may receive. They break
	// only clients that reject values they have not seen (strict enums,
	// closed unions), so they are informational unless a repo opts in.
	"response-property-enum-value-added": change.Info,
	"response-property-any-of-added":     change.Info,
	"response-body-any-of-added":         change.Info,
	"response-property-max-length-unset": change.Info,
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
