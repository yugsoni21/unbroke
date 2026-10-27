// Package change defines the structured record Mend produces for every
// difference it finds between two versions of a third-party API.
//
// Everything downstream (repo scanning, fix generation, pull request text,
// the public changes site) works from these records and never touches the
// tools that produced them, so the detection source can change without
// rippling through the rest of the product.
package change

import "time"

// Severity says how much a change matters to someone who calls the API.
type Severity string

const (
	// Breaking means existing, previously valid calls will fail or behave
	// differently.
	Breaking Severity = "breaking"
	// Warning means a call still works today but is likely to stop working,
	// or its behavior is at risk (a deprecation, a tightened constraint).
	Warning Severity = "warning"
	// Info means a change that needs no action (a new optional field).
	Info Severity = "info"
)

// Rank orders severities so callers can filter with "at least this severe".
func (s Severity) Rank() int {
	switch s {
	case Breaking:
		return 3
	case Warning:
		return 2
	default:
		return 1
	}
}

// Change is one difference between two versions of one API.
type Change struct {
	// API is the short name of the provider, for example "stripe".
	API string `json:"api"`
	// FromVersion and ToVersion are the spec versions that were compared.
	FromVersion string `json:"from_version,omitempty"`
	ToVersion   string `json:"to_version,omitempty"`

	// RuleID is the identifier of the check that fired. It is stable, so it
	// can be used to group, filter, and attach fix strategies.
	RuleID   string   `json:"rule_id"`
	Severity Severity `json:"severity"`

	// Method and Path locate the operation, for example POST /v1/refunds.
	// Both are empty for changes that are not tied to one operation.
	Method      string `json:"method,omitempty"`
	Path        string `json:"path,omitempty"`
	OperationID string `json:"operation_id,omitempty"`

	// Summary is one plain sentence describing what changed.
	Summary string `json:"summary"`
	// Detail carries extra explanation when the check provides it.
	Detail string `json:"detail,omitempty"`

	// Attributes holds check-specific facts such as a parameter name or a
	// removed enum value. Fix generation reads these.
	Attributes map[string]any `json:"attributes,omitempty"`

	// Sunset is the date the provider says the old behavior stops working,
	// when it said so. Nil means unknown.
	Sunset *time.Time `json:"sunset,omitempty"`
}

// Operation returns "METHOD /path", or an empty string when the change is
// not tied to one operation.
func (c Change) Operation() string {
	if c.Method == "" && c.Path == "" {
		return ""
	}
	if c.Method == "" {
		return c.Path
	}
	return c.Method + " " + c.Path
}

// AtLeast reports whether the change is at or above the given severity.
func (c Change) AtLeast(s Severity) bool {
	return c.Severity.Rank() >= s.Rank()
}
