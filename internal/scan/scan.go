package scan

import (
	"regexp"
	"sort"
	"strings"
)

// Match is one place in the code that calls a target endpoint.
type Match struct {
	File       string     `json:"file"`
	Line       int        `json:"line"`
	Text       string     `json:"text"`
	Operation  string     `json:"operation"`
	Kind       Kind       `json:"kind"`
	Confidence Confidence `json:"confidence"`
	// Window is the code around the call, used to tell whether a particular
	// request field or response field appears near it.
	Window string `json:"-"`
}

// Options control a scan.
type Options struct {
	// Hints are lower-case substrings. When set, a file is only searched if it
	// mentions at least one, which keeps a generic "/users" or "list(" in an
	// unrelated file from counting as a call to someone's API.
	Hints []string
	// ImportWords are names (such as "github") that, when an import line contains
	// one as a whole word, show the file works with the provider even if it never
	// writes a hint itself, for example by importing GITHUB_URL from a config file.
	ImportWords []string
	// Universe is every endpoint of the API being searched, not just the ones
	// of interest. It lets the scanner notice that a name like "sessions" is
	// shared by several endpoints and ask for more evidence before choosing one.
	Universe []Target
	// WindowLines is how many lines either side of a match to keep. Default 25.
	WindowLines  int
	MaxFileBytes int64
}

// Scan searches the repository at root for calls to the targets.
func Scan(root string, targets []Target, opts Options) ([]Match, error) {
	win := opts.WindowLines
	if win <= 0 {
		win = 25
	}
	cache := map[Target]*sdkSpec{}
	ms := make([]*matcher, 0, len(targets))
	for _, t := range targets {
		ms = append(ms, newMatcher(t, opts.Universe, cache))
	}

	var out []Match
	err := walk(root, opts.MaxFileBytes, func(f file) {
		text := string(f.data)
		lines := strings.Split(text, "\n")
		if len(opts.Hints) > 0 && !containsAny(strings.ToLower(text), opts.Hints) && !importsAny(lines, opts.ImportWords) {
			return
		}
		parents := map[*matcher]bool{} // per file: does it mention the parent resource?
		for i, line := range lines {
			if len(line) > 600 { // minified or generated
				continue
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || isComment(trimmed) {
				continue
			}
			lower := strings.ToLower(line)
			for _, m := range ms {
				if kind, conf, ok := m.matchLine(line, lower, lines, i, text, parents); ok {
					out = append(out, Match{
						File: f.rel, Line: i + 1, Text: clip(trimmed, 160),
						Operation: m.target.Operation(), Kind: kind, Confidence: conf,
						Window: window(lines, i, win),
					})
				}
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return dedupe(dropShadowed(out)), nil
}

// dropShadowed removes a path match that is only the tail of a longer path
// matched on the same line: a call to /chat/completions is not also a call to
// /completions.
func dropShadowed(ms []Match) []Match {
	type key struct {
		file string
		line int
	}
	byLine := map[key][]int{}
	for i, m := range ms {
		if m.Kind == KindPath {
			k := key{m.File, m.Line}
			byLine[k] = append(byLine[k], i)
		}
	}
	drop := map[int]bool{}
	for _, idxs := range byLine {
		for _, i := range idxs {
			mi, pi, _ := strings.Cut(ms[i].Operation, " ")
			for _, j := range idxs {
				if i == j {
					continue
				}
				mj, pj, _ := strings.Cut(ms[j].Operation, " ")
				if mi == mj && len(pj) > len(pi) && strings.HasSuffix(pj, pi) {
					drop[i] = true
				}
			}
		}
	}
	// The same URL can belong to several methods. When one is plainly in use on
	// the line (the method appears nearby), the others are not.
	type pk struct {
		file string
		line int
		path string
	}
	groups := map[pk][]int{}
	for i, m := range ms {
		if m.Kind == KindPath {
			_, pth, _ := strings.Cut(m.Operation, " ")
			k := pk{m.File, m.Line, pth}
			groups[k] = append(groups[k], i)
		}
	}
	for _, idxs := range groups {
		if len(idxs) < 2 {
			continue
		}
		hasHigh := false
		for _, i := range idxs {
			if ms[i].Confidence == High {
				hasHigh = true
			}
		}
		if hasHigh {
			for _, i := range idxs {
				if ms[i].Confidence != High {
					drop[i] = true
				}
			}
		}
	}
	if len(drop) == 0 {
		return ms
	}
	out := ms[:0:0]
	for i, m := range ms {
		if !drop[i] {
			out = append(out, m)
		}
	}
	return out
}

// matchLine reports how, if at all, one line calls the matcher's endpoint.
func (m *matcher) matchLine(line, lower string, lines []string, idx int, text string, parents map[*matcher]bool) (Kind, Confidence, bool) {
	pathPossible := m.pathNeedle == "" || strings.Contains(lower, m.pathNeedle)
	if m.route != nil && pathPossible {
		for _, loc := range m.route.FindAllStringIndex(line, -1) {
			if pathEndsHere(line, loc[1]) {
				return KindRoute, High, true
			}
		}
	}
	if m.path != nil && pathPossible {
		for _, loc := range m.path.FindAllStringIndex(line, -1) {
			if pathEndsHere(line, loc[1]) {
				return KindPath, pathConfidence(m.target.Method, lines, idx), true
			}
		}
	}
	if m.rel != nil && pathPossible {
		for _, loc := range m.rel.FindAllStringIndex(line, -1) {
			if pathEndsHere(line, loc[1]) {
				return KindPath, Medium, true
			}
		}
	}
	if m.concat != nil && pathPossible {
		if m.concat.MatchString(line) {
			return KindPath, pathConfidence(m.target.Method, lines, idx), true
		}
	}
	if m.sdk != nil && (m.sdkNeedle == "" || strings.Contains(lower, m.sdkNeedle)) {
		// Only a spelled-out parent proves the match on its own. Without one,
		// "full" and "bare" are the same pattern and the rival check must run.
		if m.sdk.parent != nil && m.sdk.full.MatchString(line) {
			return KindSDK, Medium, true
		}
		if m.sdk.bare.MatchString(line) && m.bareIsEnough(line, lines, idx, parents) {
			return KindSDK, Medium, true
		}
	}
	// Some SDKs name each method after the operation (CreateMessage).
	if m.id != nil && strings.Contains(lower, m.idNeedle) && m.id.MatchString(line) {
		return KindSDK, Medium, true
	}
	return "", "", false
}

// bareIsEnough decides whether "resource.verb(" without its parent is good
// evidence. If the name is unique in the API, it is. If other endpoints share
// it, the parent must show up where it means something: next to the call, or in
// an import. The line must also not be spelled out as one of the rivals.
func (m *matcher) bareIsEnough(line string, lines []string, idx int, parents map[*matcher]bool) bool {
	if !m.ambiguous {
		return true
	}
	// For a chain split across lines the rival's parent may be on the line above.
	ctx := line
	if isContinuation(line) {
		lo := idx - 2
		if lo < 0 {
			lo = 0
		}
		ctx = strings.Join(lines[lo:idx+1], "\n")
	}
	for _, r := range m.rivals {
		if r.MatchString(ctx) {
			return false
		}
	}
	if m.sdk.parent == nil {
		return true
	}
	// A chained call can be split across lines:
	//     await openai.chat
	//         .completions.create(
	// Only then does the line above say anything about this one.
	if isContinuation(lines[idx]) {
		lo := idx - 2
		if lo < 0 {
			lo = 0
		}
		if m.sdk.parent.MatchString(strings.Join(lines[lo:idx+1], "\n")) {
			return true
		}
	}
	has, seen := parents[m]
	if !seen {
		has = importsParent(lines, m.sdk.parentWord)
		parents[m] = has
	}
	return has
}

// importsParent reports whether the file brings the parent resource into scope:
// an import line naming it, or a line that is just a quoted import path, which
// is how Go and similar languages list imports, with no keyword on each line.
func importsParent(lines []string, word *regexp.Regexp) bool {
	for i, l := range lines {
		if len(l) > 300 {
			continue
		}
		if (importish.MatchString(l) || (i < 120 && quotedPath.MatchString(l))) && word.MatchString(l) {
			return true
		}
	}
	return false
}

// quotedPath matches a line holding only a quoted path, with an optional alias.
var quotedPath = regexp.MustCompile(`^\s*(?:[A-Za-z_.]+\s+)?["'` + "`" + `][^"'` + "`" + `]*/[^"'` + "`" + `]*["'` + "`" + `]\s*;?\s*$`)

// isContinuation reports whether a line continues a chain begun on the line above.
func isContinuation(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, ".") || strings.HasPrefix(t, "->") || strings.HasPrefix(t, "::")
}

// pathConfidence: a URL alone does not say which HTTP method is used, so it is
// high confidence only when the method is visible nearby.
func pathConfidence(method string, lines []string, idx int) Confidence {
	lo, hi := idx-3, idx+3
	if lo < 0 {
		lo = 0
	}
	if hi >= len(lines) {
		hi = len(lines) - 1
	}
	near := strings.ToLower(strings.Join(lines[lo:hi+1], "\n"))
	if strings.Contains(near, strings.ToLower(method)) {
		return High
	}
	return Medium
}

func window(lines []string, idx, n int) string {
	lo, hi := idx-n, idx+n
	if lo < 0 {
		lo = 0
	}
	if hi >= len(lines) {
		hi = len(lines) - 1
	}
	return strings.Join(lines[lo:hi+1], "\n")
}

var commentStart = regexp.MustCompile(`^(//|#|\*|/\*|--|<!--)`)

func isComment(trimmed string) bool { return commentStart.MatchString(trimmed) }

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// importsAny reports whether some import line contains one of the words as a
// whole word.
func importsAny(lines []string, words []string) bool {
	if len(words) == 0 {
		return false
	}
	for _, l := range lines {
		if len(l) > 300 || !importish.MatchString(l) {
			continue
		}
		low := strings.ToLower(l)
		for _, w := range words {
			if hasHintWord(low, w) {
				return true
			}
		}
	}
	return false
}

func containsAny(s string, subs []string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// dedupe keeps one match per file, line and operation, preferring the more
// certain way of recognising it.
func dedupe(ms []Match) []Match {
	rank := func(m Match) int {
		r := 0
		if m.Confidence == High {
			r += 2
		}
		if m.Kind == KindRoute {
			r++
		}
		return r
	}
	best := map[string]Match{}
	for _, m := range ms {
		key := m.File + "\x00" + itoa(m.Line) + "\x00" + m.Operation
		if cur, ok := best[key]; !ok || rank(m) > rank(cur) {
			best[key] = m
		}
	}
	out := make([]Match, 0, len(best))
	for _, m := range best {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Operation < out[j].Operation
	})
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
