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
	"sort"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oasdiff/oasdiff/checker"
	"github.com/oasdiff/oasdiff/diff"
	"github.com/oasdiff/oasdiff/load"

	"github.com/yug/mend/internal/change"
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
			Severity:    severityFor(f.GetId(), f.GetLevel()),
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
}

func severityFor(ruleID string, l checker.Level) change.Severity {
	if s, ok := severityOverrides[ruleID]; ok {
		return s
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
