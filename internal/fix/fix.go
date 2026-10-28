// Package fix turns findings into code changes: it asks a model to make the
// smallest edit that keeps a call site working against the changed API, applies
// the edit only if it matches the file exactly, runs the project's tests, and
// commits the result on a new branch.
//
// Nothing here pushes, opens a pull request, or touches a file that has no
// finding. Every edit is a literal replacement of text that occurs exactly once,
// so a model that misremembers the file changes nothing instead of something
// wrong.
package fix

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yug/mend/internal/affect"
	"github.com/yug/mend/internal/change"
)

// FileTask is the work for one source file.
type FileTask struct {
	File     string
	Hits     []affect.Hit
	Findings []affect.Finding
	// Related are additions to the same endpoints. When a field is removed,
	// the new field that replaces it was usually added in the same change.
	Related []change.Change
}

// Plan groups the findings that matter (not merely "calls the endpoint") by
// file, most urgent first, and keeps at most max files. extra are lesser
// changes from the same comparison, used only as context for the model.
func Plan(findings []affect.Finding, extra []change.Change, max int) []FileTask {
	byFile := map[string]*FileTask{}
	var order []string
	for _, f := range findings {
		if f.Relevance == affect.CallsEndpoint {
			continue
		}
		for _, h := range f.Hits {
			// A call site that merely calls the endpoint is not evidence the
			// changed field is used; only hits near the field are worth editing.
			if f.Relevance == affect.ChangedField && h.Field == "" {
				continue
			}
			t, ok := byFile[h.File]
			if !ok {
				t = &FileTask{File: h.File}
				byFile[h.File] = t
				order = append(order, h.File)
			}
			t.Hits = append(t.Hits, h)
			if !hasFinding(t.Findings, f) {
				t.Findings = append(t.Findings, f)
			}
		}
	}
	tasks := make([]FileTask, 0, len(order))
	for _, name := range order {
		t := *byFile[name]
		t.Related = relatedAdditions(t, extra)
		tasks = append(tasks, t)
	}
	sort.SliceStable(tasks, func(i, j int) bool { return top(tasks[i]) > top(tasks[j]) })
	if max > 0 && len(tasks) > max {
		tasks = tasks[:max]
	}
	return tasks
}

// maxRelated bounds how many additions are shown to the model.
const maxRelated = 12

// relatedAdditions picks the additions that touch the task's endpoints, those
// whose name shares a word with a field in question first.
func relatedAdditions(t FileTask, extra []change.Change) []change.Change {
	ops := map[string]bool{}
	sides := map[string]bool{}
	var stems []string
	for _, f := range t.Findings {
		// A field removed from a request is replaced by a field added to the
		// request; one removed from a response, by one added to the response.
		if strings.Contains(f.Change.RuleID, "request") {
			sides["request"] = true
		} else if strings.Contains(f.Change.RuleID, "response") {
			sides["response"] = true
		}
		for _, op := range f.Change.Operations {
			ops[op] = true
		}
		for _, w := range wordsIn(f.Change.Summary) {
			stems = append(stems, w)
		}
	}
	type scored struct {
		c     change.Change
		score int
	}
	var cands []scored
	for _, c := range extra {
		if !isFieldAddition(c.RuleID) || (len(sides) > 0 && !sides[sideOf(c.RuleID)]) {
			continue
		}
		hit := false
		for _, op := range c.Operations {
			if ops[op] {
				hit = true
			}
		}
		if !hit {
			continue
		}
		score := 0
		lower := strings.ToLower(c.Summary)
		for _, w := range stems {
			if len(w) > 3 && strings.Contains(lower, w) {
				score++
			}
		}
		cands = append(cands, scored{c, score})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	if len(cands) > maxRelated {
		cands = cands[:maxRelated]
	}
	out := make([]change.Change, len(cands))
	for i, c := range cands {
		out[i] = c.c
	}
	return out
}

// isFieldAddition reports whether a rule says a field or parameter was added,
// as opposed to an enum value, a union member or a limit, which never name a
// replacement.
func isFieldAddition(rule string) bool {
	if !strings.Contains(rule, "added") && !strings.HasPrefix(rule, "new-") {
		return false
	}
	for _, noise := range []string{"enum-value", "any-of", "one-of", "all-of", "max-length", "min-length", "pattern", "default", "example"} {
		if strings.Contains(rule, noise) {
			return false
		}
	}
	return strings.Contains(rule, "property") || strings.Contains(rule, "parameter")
}

func sideOf(rule string) string {
	if strings.Contains(rule, "response") {
		return "response"
	}
	return "request"
}

// wordsIn returns the lower-case words of the identifiers a sentence puts
// between backticks, split on '_' and '/'.
func wordsIn(summary string) []string {
	var out []string
	for _, m := range backticked.FindAllStringSubmatch(summary, -1) {
		for _, w := range strings.FieldsFunc(strings.ToLower(m[1]), func(r rune) bool { return r == '_' || r == '/' || r == '-' }) {
			out = append(out, w)
		}
	}
	return out
}

var backticked = regexp.MustCompile("`([^`]+)`")

func top(t FileTask) int {
	p := 0
	for _, f := range t.Findings {
		if f.Priority() > p {
			p = f.Priority()
		}
	}
	return p
}

func hasFinding(fs []affect.Finding, f affect.Finding) bool {
	for _, x := range fs {
		if x.Change.RuleID == f.Change.RuleID && x.Change.Summary == f.Change.Summary {
			return true
		}
	}
	return false
}

// SystemPrompt tells the model its job and the exact output format.
const SystemPrompt = `You update application code after a third-party API changed.

Make the smallest edit that keeps the code correct against the new API. Do not refactor, rename, reformat or "improve" anything else.

If the right fix cannot be determined from what you are given (for example a field was removed and nothing says what replaces it, or the change does not actually affect how this code uses the API), make no edit and explain why. Guessing is worse than doing nothing.

Reply with only a JSON object, no other text:
{"edits":[{"old":"text copied exactly from the file","new":"replacement text"}],"explanation":"one or two sentences for the reviewer","confidence":"high|medium|low"}

Each "old" must appear exactly once in the file and must be copied character for character, including indentation. Keep each edit as small as possible.`

// maxFullLines is the largest file sent whole; bigger files are sent as windows
// around the call sites.
const maxFullLines = 700

// BuildPrompt writes the user prompt for one file.
func BuildPrompt(root string, t FileTask, provider string) (string, error) {
	path, err := safeJoin(root, t.File)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")

	var b strings.Builder
	fmt.Fprintf(&b, "The %s API changed. These changes affect the file below.\n\n", provider)
	for i, f := range t.Findings {
		fmt.Fprintf(&b, "Change %d [%s]: %s\n", i+1, f.Change.Severity, f.Change.Summary)
		ops := f.Change.Operations
		if len(ops) == 0 {
			ops = []string{f.Change.Operation()}
		}
		fmt.Fprintf(&b, "  endpoint(s): %s\n", strings.Join(ops, ", "))
		if f.Change.Sunset != nil {
			fmt.Fprintf(&b, "  the provider says the old behavior stops on %s\n", f.Change.Sunset.Format("2006-01-02"))
		}
		if f.Change.Detail != "" {
			fmt.Fprintf(&b, "  note: %s\n", f.Change.Detail)
		}
	}
	if len(t.Related) > 0 {
		b.WriteString("\nOther changes to the same endpoint(s). If something was removed, its replacement may be among these:\n")
		for _, r := range t.Related {
			fmt.Fprintf(&b, "  - %s\n", r.Summary)
		}
	}
	b.WriteString("\nCall sites found in this file:\n")
	for _, h := range t.Hits {
		fmt.Fprintf(&b, "  line %d: %s", h.Line, h.Text)
		if h.Field != "" {
			fmt.Fprintf(&b, "   (near `%s`)", h.Field)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nFile: %s\n", t.File)
	if len(lines) <= maxFullLines {
		b.WriteString("<file>\n" + string(data) + "\n</file>\n")
	} else {
		b.WriteString("<file note=\"large file; only the parts around the call sites are shown\">\n")
		b.WriteString(windows(lines, t.Hits))
		b.WriteString("</file>\n")
	}
	return b.String(), nil
}

// windows renders the lines near each hit, merging overlaps.
func windows(lines []string, hits []affect.Hit) string {
	const span = 80
	type rng struct{ lo, hi int }
	var rs []rng
	for _, h := range hits {
		lo, hi := h.Line-1-span, h.Line-1+span
		if lo < 0 {
			lo = 0
		}
		if hi >= len(lines) {
			hi = len(lines) - 1
		}
		rs = append(rs, rng{lo, hi})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].lo < rs[j].lo })
	var merged []rng
	for _, r := range rs {
		if n := len(merged); n > 0 && r.lo <= merged[n-1].hi+1 {
			if r.hi > merged[n-1].hi {
				merged[n-1].hi = r.hi
			}
			continue
		}
		merged = append(merged, r)
	}
	var b strings.Builder
	for _, r := range merged {
		fmt.Fprintf(&b, "... (lines %d-%d) ...\n", r.lo+1, r.hi+1)
		b.WriteString(strings.Join(lines[r.lo:r.hi+1], "\n"))
		b.WriteString("\n")
	}
	return b.String()
}

// Edit replaces one exact piece of text.
type Edit struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// Proposal is the model's answer for one file.
type Proposal struct {
	Edits       []Edit `json:"edits"`
	Explanation string `json:"explanation"`
	Confidence  string `json:"confidence"`
}

// ParseProposal reads the model's reply, tolerating a code fence or a sentence
// around the JSON.
func ParseProposal(text string) (Proposal, error) {
	text = strings.TrimSpace(text)
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return Proposal{}, errors.New("the model's reply contained no JSON object")
	}
	var p Proposal
	if err := json.Unmarshal([]byte(text[start:end+1]), &p); err != nil {
		return Proposal{}, fmt.Errorf("the model's reply was not valid JSON: %w", err)
	}
	switch strings.ToLower(p.Confidence) {
	case "high", "medium", "low":
		p.Confidence = strings.ToLower(p.Confidence)
	default:
		p.Confidence = "low"
	}
	return p, nil
}

// FileResult is what happened to one file.
type FileResult struct {
	File        string
	Applied     []Edit
	Rejected    []string
	Explanation string
	Confidence  string
	Err         error
}

// Changed reports whether the file was modified.
func (r FileResult) Changed() bool { return len(r.Applied) > 0 }

// Apply makes the proposal's edits to the file, keeping only those whose "old"
// text occurs exactly once. It reports why each rejected edit was rejected.
func Apply(root, file string, p Proposal) (FileResult, error) {
	res := FileResult{File: file, Explanation: p.Explanation, Confidence: p.Confidence}
	path, err := safeJoin(root, file)
	if err != nil {
		return res, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return res, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return res, err
	}
	content := string(data)

	for i, e := range p.Edits {
		switch n := strings.Count(content, e.Old); {
		case e.Old == "":
			res.Rejected = append(res.Rejected, fmt.Sprintf("edit %d: empty text to replace", i+1))
		case e.Old == e.New:
			res.Rejected = append(res.Rejected, fmt.Sprintf("edit %d: replacement is identical", i+1))
		case n == 0:
			res.Rejected = append(res.Rejected, fmt.Sprintf("edit %d: the text to replace does not appear in the file", i+1))
		case n > 1:
			res.Rejected = append(res.Rejected, fmt.Sprintf("edit %d: the text to replace appears %d times, so the target is ambiguous", i+1, n))
		default:
			content = strings.Replace(content, e.Old, e.New, 1)
			res.Applied = append(res.Applied, e)
		}
	}
	if res.Changed() {
		if err := os.WriteFile(path, []byte(content), info.Mode().Perm()); err != nil {
			return res, err
		}
	}
	return res, nil
}

// safeJoin resolves a repo-relative path and refuses anything that escapes the
// repository root.
func safeJoin(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("refusing absolute path %q", rel)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	p := filepath.Join(abs, filepath.FromSlash(rel))
	if p != abs && !strings.HasPrefix(p, abs+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing path %q outside the repository", rel)
	}
	return p, nil
}
