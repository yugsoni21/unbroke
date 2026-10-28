package scan

import (
	"regexp"
	"strings"
)

// importish matches lines that bring a library into scope.
var importish = regexp.MustCompile(`(?i)^\s*(import\b|from\s+\S+\s+import\b|require\b|use\b|using\b|#include\b|extern\s+crate\b|@import\b|load\b)|require\s*\(|import\s*\(|\bgem\s+['"]|\bimplementation\s|\bcompile\s|\bapi\s+['"]|\brequire_once\b`)

// DetectUse reports, for each hint set, how many files show the project using
// that provider: a dependency manifest naming it, an import of its library, or
// a URL on its domain. A mention in prose or a comment is not enough, since
// "github.com" appears in nearly every repository.
func DetectUse(root string, hintsByID map[string][]string) (map[string]int, error) {
	counts := map[string]int{}
	err := walk(root, 0, func(f file) {
		text := strings.ToLower(string(f.data))
		isManifest := manifests[f.base]
		for id, hints := range hintsByID {
			if !containsAny(text, hints) {
				continue
			}
			if (isManifest && anyHintWord(text, hints)) || usesHint(text, hints) {
				counts[id]++
			}
		}
	})
	return counts, err
}

// usesHint reports whether any line is an import of, or a URL on, a hint.
func usesHint(lowerText string, hints []string) bool {
	for _, line := range strings.Split(lowerText, "\n") {
		if len(line) > 400 || isComment(strings.TrimSpace(line)) {
			continue
		}
		for _, h := range hints {
			if !hasHintWord(line, h) {
				continue
			}
			if importish.MatchString(line) || strings.Contains(line, "://") && urlOn(line, h) {
				return true
			}
		}
	}
	return false
}

// urlOn reports whether the hint appears inside a URL's host.
func urlOn(line, hint string) bool {
	i := strings.Index(line, "://")
	for i >= 0 {
		rest := line[i+3:]
		end := strings.IndexAny(rest, "/\"'` ?#)")
		if end < 0 {
			end = len(rest)
		}
		if strings.Contains(rest[:end], hint) {
			return true
		}
		next := strings.Index(rest, "://")
		if next < 0 {
			break
		}
		i = i + 3 + next
	}
	return false
}

func anyHintWord(text string, hints []string) bool {
	for _, h := range hints {
		if hasHintWord(text, h) {
			return true
		}
	}
	return false
}

// hasHintWord reports whether hint occurs in s as a whole word. "godo" must not
// be found inside "godotenv", nor "box" inside "sandbox". Punctuation such as
// '-', '/', '.', '_' and '@' counts as a boundary, so "stripe-go",
// "@stripe/stripe-js" and "com.stripe.model" all match "stripe".
func hasHintWord(s, hint string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], hint)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(hint)
		if (start == 0 || !isAlnum(s[start-1])) && (end == len(s) || !isAlnum(s[end])) {
			return true
		}
		i = start + 1
	}
}

func isAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
