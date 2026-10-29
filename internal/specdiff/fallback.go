package specdiff

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/yug/unbroke/internal/change"
	"github.com/yug/unbroke/internal/inventory"
)

// CompareResilient is Compare with a safety net. Providers publish specs that
// do not validate (a reference to a schema that does not exist, a field of the
// wrong type), usually in older versions. A strict parser rejects those, which
// would leave Unbroke with nothing to say about an API it should be watching.
//
// When the full comparison fails because a spec could not be loaded, this
// compares the two endpoint lists instead. That cannot see changes inside
// requests and responses, but it still catches removed and newly deprecated
// endpoints, which are the changes that break callers outright. The returned
// note says so whenever the reduced comparison was used, and is empty
// otherwise.
func CompareResilient(base, revision []byte, opts Options) ([]change.Change, string, error) {
	changes, err := Compare(base, revision, opts)
	if err == nil {
		return changes, "", nil
	}
	if !errors.Is(err, ErrLoad) {
		return nil, "", err
	}
	reduced, ferr := compareEndpoints(base, revision, opts)
	if ferr != nil {
		return nil, "", fmt.Errorf("%w (and the endpoint-level fallback failed: %v)", err, ferr)
	}
	reason := strings.TrimPrefix(err.Error(), ErrLoad.Error()+": ")
	return reduced, "full comparison unavailable because a spec is malformed (" + shorten(reason) + "); showing endpoint-level changes only", nil
}

func shorten(s string) string {
	const max = 160
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// compareEndpoints diffs two specs by their lists of endpoints.
func compareEndpoints(base, revision []byte, opts Options) ([]change.Change, error) {
	min := opts.MinSeverity
	if min == "" {
		min = change.Warning
	}
	oldInv, err := inventory.List(base)
	if err != nil {
		return nil, fmt.Errorf("base spec: %w", err)
	}
	newInv, err := inventory.List(revision)
	if err != nil {
		return nil, fmt.Errorf("revision spec: %w", err)
	}
	// Paths defined in other files are invisible here, and would read as removed.
	if oldInv.Unresolved > 0 || newInv.Unresolved > 0 {
		return nil, errors.New("some paths are defined in other files, so the endpoint lists are incomplete")
	}
	// A revision with no endpoints at all is a failed download or an empty
	// document, not an API that removed everything.
	if len(newInv.Operations) == 0 && len(oldInv.Operations) > 0 {
		return nil, errors.New("revision lists no endpoints")
	}

	// Keyed with parameter names ignored: /b/{id} renamed to /b/{bId} is the
	// same endpoint, not a removal plus an addition.
	old := map[string]inventory.Operation{}
	for _, o := range oldInv.Operations {
		old[normOp(o.Method, o.Path)] = o
	}
	cur := map[string]inventory.Operation{}
	for _, o := range newInv.Operations {
		cur[normOp(o.Method, o.Path)] = o
	}

	mk := func(o inventory.Operation, rule string, sev change.Severity, summary string) change.Change {
		return change.Change{
			API: opts.API, FromVersion: oldInv.Version, ToVersion: newInv.Version,
			RuleID: rule, Severity: sev,
			Method: o.Method, Path: o.Path, OperationID: o.ID,
			Operations: []string{o.String()}, Summary: summary,
		}
	}
	var out []change.Change
	for key, o := range old {
		n, still := cur[key]
		switch {
		case !still:
			out = append(out, mk(o, "endpoint-removed", change.Breaking, "endpoint removed"))
		case n.Deprecated && !o.Deprecated:
			out = append(out, mk(n, "endpoint-newly-deprecated", change.Warning, "endpoint newly marked deprecated"))
		}
	}
	for key, o := range cur {
		if _, existed := old[key]; !existed {
			out = append(out, mk(o, "endpoint-added", change.Info, "endpoint added"))
		}
	}

	kept := out[:0]
	for _, c := range out {
		if c.AtLeast(min) {
			kept = append(kept, c)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() > b.Severity.Rank()
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Method < b.Method
	})
	return kept, nil
}
