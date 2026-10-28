package fix

import (
	"fmt"
	"strings"

	"github.com/yug/mend/internal/affect"
)

// Title is the pull request title.
func Title(provider string) string {
	return fmt.Sprintf("Update %s integration for API changes", provider)
}

// PRBody writes the pull request description: what changed in the API, what
// this change does about it, and how far it was checked. It says plainly where
// the reviewer should look harder.
func PRBody(provider, since string, rep *Report, tasks []FileTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mend found changes in the %s API", provider)
	if since != "" {
		fmt.Fprintf(&b, " (compared with %s)", since)
	}
	b.WriteString(" that affect code in this repository, and proposes the smallest edits that keep it working.\n\n")

	b.WriteString("## API changes\n\n")
	seen := map[string]bool{}
	for _, t := range tasks {
		for _, f := range t.Findings {
			key := f.Change.RuleID + f.Change.Summary
			if seen[key] {
				continue
			}
			seen[key] = true
			ops := f.Change.Operations
			if len(ops) == 0 {
				ops = []string{f.Change.Operation()}
			}
			fmt.Fprintf(&b, "- **%s**: %s (`%s`)\n", f.Change.Severity, f.Change.Summary, strings.Join(ops, "`, `"))
			if f.Change.Sunset != nil {
				fmt.Fprintf(&b, "  - the provider says this stops working on %s\n", f.Change.Sunset.Format("2006-01-02"))
			}
		}
	}

	b.WriteString("\n## What this changes\n\n")
	low := false
	for _, f := range rep.Files {
		if !f.Changed() {
			continue
		}
		fmt.Fprintf(&b, "- `%s` (%s confidence): %s\n", f.File, f.Confidence, f.Explanation)
		if f.Confidence != "high" {
			low = true
		}
	}
	var skipped []string
	for _, f := range rep.Files {
		if !f.Changed() {
			reason := "no safe edit was found"
			if f.Err != nil {
				reason = "could not be processed: " + f.Err.Error()
			} else if len(f.Rejected) > 0 {
				reason = "the proposed edit was not applied (" + strings.Join(f.Rejected, "; ") + ")"
			} else if f.Explanation != "" {
				reason = "no edit was made. The model said: " + f.Explanation
			}
			skipped = append(skipped, fmt.Sprintf("- `%s`: %s", f.File, reason))
		}
	}
	if len(skipped) > 0 {
		b.WriteString("\nLeft unchanged, for a person to decide:\n\n" + strings.Join(skipped, "\n") + "\n")
	}

	b.WriteString("\n## How it was checked\n\n")
	switch {
	case rep.TestsRan && rep.TestsPass:
		fmt.Fprintf(&b, "Ran `%s`: passed.\n", rep.TestCmd)
	case rep.TestsRan:
		fmt.Fprintf(&b, "Ran `%s`: failed.\n", rep.TestCmd)
	default:
		b.WriteString("The project's tests were **not run** by Mend. Run them before merging.\n")
	}

	b.WriteString("\n## Review notes\n\n")
	b.WriteString("- Mend rates API changes by comparing the provider's published specification. A specification can lag or differ from real behavior, so confirm against the provider's changelog.\n")
	b.WriteString("- Mend finds call sites by pattern, not by running your code. A call that builds its request dynamically may have been missed.\n")
	if low {
		b.WriteString("- At least one edit is below high confidence. Read those closely.\n")
	}
	b.WriteString("\n_Opened by [Mend](https://github.com/yug/mend)._\n")
	return b.String()
}

// Subject summarises a finding for logs.
func Subject(f affect.Finding) string {
	return fmt.Sprintf("[%s] %s", f.Change.Severity, f.Change.Summary)
}
