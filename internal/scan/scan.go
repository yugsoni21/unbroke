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
		if len(opts.Hints) > 0 && !containsAny(strings.ToLower(text), opts.Hints) {
			return
		}
		lines := strings.Split(text, "\n")
		parents := map[*matcher]bool{} // per file: does it mention the parent resource?
		for i, line := range lines {
			if len(line) > 600 { // minified or generated
				continue
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || isComment(trimmed) {
				continue
			}
			for _, m := range ms {
				if kind, conf, ok := m.matchLine(line, lines, i, text, parents); ok {
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
	return dedupe(out), nil
}

// matchLine reports how, if at all, one line calls the matcher's endpoint.
func (m *matcher) matchLine(line string, lines []string, idx int, text string, parents map[*matcher]bool) (Kind, Confidence, bool) {
	if m.route != nil {
		for _, loc := range m.route.FindAllStringIndex(line, -1) {
			if pathEndsHere(line, loc[1]) {
				return KindRoute, High, true
			}
		}
	}
	if m.path != nil {
		for _, loc := range m.path.FindAllStringIndex(line, -1) {
			if pathEndsHere(line, loc[1]) {
				return KindPath, pathConfidence(m.target.Method, lines, idx), true
			}
		}
	}
	if m.concat != nil {
		if m.concat.MatchString(line) {
			return KindPath, pathConfidence(m.target.Method, lines, idx), true
		}
	}
	if m.sdk != nil {
		if m.sdk.full.MatchString(line) {
			return KindSDK, Medium, true
		}
		if m.sdk.bare.MatchString(line) && m.bareIsEnough(line, text, parents) {
			return KindSDK, Medium, true
		}
	}
	return "", "", false
}

// bareIsEnough decides whether "resource.verb(" without its parent is good
// evidence. If the name is unique in the API, it is. If other endpoints share
// it, the file must mention the parent (an import or a package name counts),
// and the line must not be spelled out as one of the rivals.
func (m *matcher) bareIsEnough(line, text string, parents map[*matcher]bool) bool {
	if !m.ambiguous {
		return true
	}
	for _, r := range m.rivals {
		if r.MatchString(line) {
			return false
		}
	}
	if m.sdk.parent == nil {
		return true
	}
	has, seen := parents[m]
	if !seen {
		has = m.sdk.parent.MatchString(text)
		parents[m] = has
	}
	return has
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
