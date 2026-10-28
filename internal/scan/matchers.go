package scan

import (
	"regexp"
	"strings"
	"unicode"
)

// Target is one endpoint to look for.
type Target struct {
	Method string
	Path   string
}

// Operation renders the target as "METHOD /path".
func (t Target) Operation() string { return t.Method + " " + t.Path }

// Kind says how a call was recognised.
type Kind string

const (
	// KindPath is a URL path written out in the code.
	KindPath Kind = "path"
	// KindRoute is a "METHOD /path" string.
	KindRoute Kind = "route"
	// KindSDK is a call such as stripe.refunds.create( recognised by shape.
	KindSDK Kind = "sdk"
)

// Confidence is how sure the scanner is that a line calls the endpoint.
type Confidence string

const (
	High   Confidence = "high"
	Medium Confidence = "medium"
)

// pathParam matches whatever code puts where a path parameter goes.
const pathParam = "(?:\\$\\{[^}]*\\}|#\\{[^}]*\\}|\\{[^}]*\\}|[^/\\s\"'`?#]+)"

type matcher struct {
	target Target
	path   *regexp.Regexp // URL path, possibly with parameters
	concat *regexp.Regexp // "/v1/things/" + id, for templates ending in a parameter
	route  *regexp.Regexp // "GET /v1/things"
	sdk    *sdkSpec       // things.create(

	// ambiguous is true when other endpoints of the same API share this one's
	// resource name and kind of call, so the bare name alone proves nothing.
	// "sessions.create" could be checkout, billing portal or others.
	ambiguous bool
	// rivals match the fully qualified form of those other endpoints; a line
	// that matches one of them belongs to that endpoint, not this one.
	rivals []*regexp.Regexp
}

func newMatcher(t Target, universe []Target, cache map[Target]*sdkSpec) *matcher {
	m := &matcher{target: t}
	segs := splitPath(t.Path)

	if literalLen(segs) >= 5 {
		var b strings.Builder
		for _, s := range segs {
			b.WriteString("/")
			b.WriteString(segmentRegexp(s))
		}
		m.path = regexp.MustCompile(b.String())

		if n := len(segs); n > 0 && isParam(segs[n-1]) {
			var c strings.Builder
			for _, s := range segs[:n-1] {
				c.WriteString("/")
				c.WriteString(segmentRegexp(s))
			}
			c.WriteString("/[\"'`]")
			m.concat = regexp.MustCompile(c.String())
		}
		m.route = regexp.MustCompile("(?i)[\"'`]\\s*" + regexp.QuoteMeta(t.Method) + "\\s+" + m.path.String())
	}
	m.sdk = analyzeCached(t, cache)
	if m.sdk != nil && len(universe) > 0 {
		for _, u := range universe {
			if u == t {
				continue
			}
			o := analyzeCached(u, cache)
			if o == nil || o.class != m.sdk.class || o.last != m.sdk.last || o.key == m.sdk.key {
				continue
			}
			m.ambiguous = true
			if o.parent != nil {
				m.rivals = append(m.rivals, o.full)
			}
		}
	}
	return m
}

func analyzeCached(t Target, cache map[Target]*sdkSpec) *sdkSpec {
	if sp, ok := cache[t]; ok {
		return sp
	}
	sp := analyzeSDK(t)
	cache[t] = sp
	return sp
}

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func isParam(seg string) bool { return strings.Contains(seg, "{") }

func literalLen(segs []string) int {
	n := 0
	for _, s := range segs {
		if !isParam(s) {
			n += len(s)
		}
	}
	return n
}

// segmentRegexp turns one path segment into a pattern. Parameters match
// anything a program would put there; a trailing ".json" is optional because
// SDKs often add it themselves.
func segmentRegexp(seg string) string {
	var b strings.Builder
	for i := 0; i < len(seg); {
		if seg[i] == '{' {
			j := strings.IndexByte(seg[i:], '}')
			if j < 0 {
				b.WriteString(regexp.QuoteMeta(seg[i:]))
				break
			}
			b.WriteString(pathParam)
			i += j + 1
			continue
		}
		j := strings.IndexByte(seg[i:], '{')
		if j < 0 {
			j = len(seg) - i
		}
		lit := seg[i : i+j]
		lit = strings.TrimSuffix(lit, ".json")
		b.WriteString(regexp.QuoteMeta(lit))
		if strings.HasSuffix(seg[i:i+j], ".json") {
			b.WriteString(`(?:\.json)?`)
		}
		i += j
	}
	return b.String()
}

// pathEndsHere reports whether the character after a match means the path is
// complete, as opposed to a longer path or a longer word.
func pathEndsHere(line string, end int) bool {
	if end >= len(line) {
		return true
	}
	c := line[end]
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-', c == '{', c == '$', c == '%', c == ':':
		return false
	case c == '/':
		// A path followed by "/" is a longer path, or a collection URL being
		// extended with an id ("/v1/things/" + id), which is the item endpoint.
		return false
	}
	return true
}

// ---- SDK shape matching ----

// verbs are the method names SDKs use for each kind of operation.
var (
	listVerbs   = []string{"list", "all", "getall", "find", "search", "query", "fetch", "getlist"}
	getVerbs    = []string{"get", "retrieve", "find", "fetch", "read", "show"}
	createVerbs = []string{"create", "new", "add", "post", "insert", "send", "submit"}
	updateVerbs = []string{"update", "modify", "edit", "set", "post", "save", "patch"}
	putVerbs    = []string{"update", "replace", "put", "set", "upsert"}
	patchVerbs  = []string{"update", "patch", "modify"}
	deleteVerbs = []string{"delete", "del", "remove", "destroy", "cancel", "archive"}
)

var versionSeg = regexp.MustCompile(`^(v\d+(\.\d+)*|\d{4}-\d{2}-\d{2}|api|rest|latest)$`)

// sdkSpec describes how an SDK call to one endpoint looks.
type sdkSpec struct {
	full   *regexp.Regexp // parent.resource.verb( (parent only when the path has one)
	bare   *regexp.Regexp // resource.verb(, with or without a parent
	parent *regexp.Regexp // the parent word alone, nil if there is none
	class  string         // kind of call: list, get, create, update, delete, action:x
	last   string         // the resource word, lower-case
	key    string         // identifies the pair (parent, resource) so twins compare equal
}

func analyzeSDK(t Target) *sdkSpec {
	segs := splitPath(t.Path)
	if len(segs) == 0 {
		return nil
	}
	// Drop version prefixes and file extensions.
	var words []string
	var lastIsParam bool
	var action string
	for i, s := range segs {
		if isParam(s) {
			lastIsParam = true
			continue
		}
		s = strings.TrimSuffix(s, ".json")
		if versionSeg.MatchString(strings.ToLower(s)) {
			continue
		}
		// "/things/{id}/confirm": a verb after a parameter is an action.
		// "/things/{id}/comments" is a nested collection, a plural noun.
		if i > 0 && isParam(segs[i-1]) && i == len(segs)-1 && strings.EqualFold(t.Method, "POST") && !pluralNoun(s) {
			action = s
			continue
		}
		words = append(words, s)
		lastIsParam = false
	}
	if len(words) == 0 {
		return nil
	}
	if len(words) > 2 {
		words = words[len(words)-2:]
	}

	var verbs []string
	var class string
	switch {
	case action != "":
		verbs, class = []string{action}, "action:"+strings.ToLower(action)
	case t.Method == "GET" && lastIsParam:
		verbs, class = getVerbs, "get"
	case t.Method == "GET":
		verbs, class = listVerbs, "list"
	case t.Method == "POST" && lastIsParam:
		verbs, class = updateVerbs, "update"
	case t.Method == "POST":
		verbs, class = createVerbs, "create"
	case t.Method == "PUT":
		verbs, class = putVerbs, "put"
	case t.Method == "PATCH":
		verbs, class = patchVerbs, "patch"
	case t.Method == "DELETE":
		verbs, class = deleteVerbs, "delete"
	default:
		return nil
	}

	const sep = `\s*(?:\.|::|->)\s*`
	res := nameRegexp(words[len(words)-1])
	vs := make([]string, len(verbs))
	for i, v := range verbs {
		vs[i] = nameRegexp(v)
	}
	tail := sep + `(?:` + strings.Join(vs, "|") + `)\b`

	sp := &sdkSpec{
		bare:  regexp.MustCompile(`(?i)\b` + res + tail),
		class: class,
		last:  strings.ToLower(words[len(words)-1]),
		key:   strings.ToLower(strings.Join(words, "/")),
	}
	sp.full = sp.bare
	if len(words) == 2 {
		par := nameRegexp(words[0])
		sp.parent = regexp.MustCompile(`(?i)` + par)
		sp.full = regexp.MustCompile(`(?i)\b` + par + sep + res + tail)
	}
	return sp
}

// pluralNoun guesses whether a path word names a collection (comments,
// issues) rather than an action (confirm, cancel).
func pluralNoun(w string) bool {
	w = strings.ToLower(w)
	return strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && len(w) > 3
}

// nameRegexp matches a resource name however a language spells it: snake,
// camel, Pascal, singular or plural. "payment_intents" matches paymentIntent,
// PaymentIntents and payment_intent.
func nameRegexp(name string) string {
	parts := splitWords(name)
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re := strings.Join(parts, "_?")
	// Singular and plural share a stem.
	last := parts[len(parts)-1]
	if strings.HasSuffix(last, "ies") && len(last) > 4 {
		stem := strings.Join(parts[:len(parts)-1], "_?")
		if stem != "" {
			stem += "_?"
		}
		return stem + last[:len(last)-3] + "(?:y|ies)"
	}
	if strings.HasSuffix(last, "s") && len(last) > 2 {
		stem := strings.Join(parts[:len(parts)-1], "_?")
		if stem != "" {
			stem += "_?"
		}
		return stem + last[:len(last)-1] + "s?"
	}
	return re + "s?"
}

// splitWords breaks snake_case, kebab-case and camelCase into lower-case words.
func splitWords(s string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = nil
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r == '_' || r == '-' || r == ' ' || r == '.':
			flush()
		case unicode.IsUpper(r) && i > 0 && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1])):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	if len(words) == 0 {
		words = []string{strings.ToLower(s)}
	}
	return words
}
