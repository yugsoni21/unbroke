package scan

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureRepo(t *testing.T) string {
	root := t.TempDir()
	write(t, root, "server.js", `const stripe = require('stripe')('sk_test');
// await stripe.refunds.create({ payment_intent: 'ignored in a comment' });
async function pay(pi, id) {
  const refund = await stripe.refunds.create({ payment_intent: pi });
  const intent = await stripe.paymentIntents.create({ amount: 100, currency: 'usd', payment_method_types: ['card'] });
  const got = await stripe.paymentIntents.confirm(intent.id);
  const c = await stripe.customers.retrieve(id);
  return [refund, intent, got, c];
}
`)
	write(t, root, "app.py", `import stripe
import requests

def go(pi):
    stripe.PaymentIntent.create(amount=100, currency="usd")
    r = requests.post("https://api.stripe.com/v1/refunds", data={"payment_intent": pi})
    return r
`)
	write(t, root, "main.go", `package main

import (
	"net/http"
	"github.com/stripe/stripe-go/v79/refund"
)

func main() {
	refund.New(&stripe.RefundParams{})
	req, _ := http.NewRequest("GET", "https://api.stripe.com/v1/customers/"+id, nil)
	_ = req
}
`)
	write(t, root, "pay.rb", `require 'stripe'
Stripe::Refund.create(payment_intent: pi)
`)
	write(t, root, "gh.ts", `import { Octokit } from "octokit";
const octokit = new Octokit();
await octokit.rest.issues.create({ owner, repo, title });
await octokit.request("GET /repos/{owner}/{repo}", { owner, repo });
`)
	// Mentions nothing about Stripe; must not be reported when hints are set.
	write(t, root, "models/refund.rb", `class Refund < ApplicationRecord
  def self.make
    Refund.create(amount: 1)
  end
end
`)
	write(t, root, "node_modules/stripe/index.js", `stripe.refunds.create(skipped)`)
	return root
}

var targets = []Target{
	{"POST", "/v1/refunds"},
	{"POST", "/v1/payment_intents"},
	{"POST", "/v1/payment_intents/{intent}/confirm"},
	{"GET", "/v1/customers/{customer}"},
	{"GET", "/v1/customers"},
	{"GET", "/repos/{owner}/{repo}"},
	{"POST", "/repos/{owner}/{repo}/issues"},
}

type hit struct {
	file string
	line int
	op   string
}

func hits(ms []Match) map[hit]Match {
	out := map[hit]Match{}
	for _, m := range ms {
		out[hit{m.File, m.Line, m.Operation}] = m
	}
	return out
}

func TestScan_FindsCallsAcrossLanguages(t *testing.T) {
	root := fixtureRepo(t)
	ms, err := Scan(root, targets, Options{Hints: []string{"stripe", "octokit"}})
	if err != nil {
		t.Fatal(err)
	}
	got := hits(ms)
	want := []struct {
		hit
		kind Kind
	}{
		{hit{"server.js", 4, "POST /v1/refunds"}, KindSDK},
		{hit{"server.js", 5, "POST /v1/payment_intents"}, KindSDK},
		{hit{"server.js", 6, "POST /v1/payment_intents/{intent}/confirm"}, KindSDK},
		{hit{"server.js", 7, "GET /v1/customers/{customer}"}, KindSDK},
		{hit{"app.py", 5, "POST /v1/payment_intents"}, KindSDK},
		{hit{"app.py", 6, "POST /v1/refunds"}, KindPath},
		{hit{"main.go", 9, "POST /v1/refunds"}, KindSDK},
		{hit{"main.go", 10, "GET /v1/customers/{customer}"}, KindPath},
		{hit{"pay.rb", 2, "POST /v1/refunds"}, KindSDK},
		{hit{"gh.ts", 3, "POST /repos/{owner}/{repo}/issues"}, KindSDK},
		{hit{"gh.ts", 4, "GET /repos/{owner}/{repo}"}, KindRoute},
	}
	for _, w := range want {
		m, ok := got[w.hit]
		if !ok {
			t.Errorf("missing %+v", w.hit)
			continue
		}
		if m.Kind != w.kind {
			t.Errorf("%+v kind = %s, want %s", w.hit, m.Kind, w.kind)
		}
	}
}

func TestScan_DoesNotReportWhatIsNotACall(t *testing.T) {
	root := fixtureRepo(t)
	ms, _ := Scan(root, targets, Options{Hints: []string{"stripe", "octokit"}})
	for _, m := range ms {
		switch {
		case m.File == "server.js" && m.Line == 2:
			t.Errorf("a commented-out call was reported: %+v", m)
		case m.File == "models/refund.rb":
			t.Errorf("an unrelated Refund.create in a file that never mentions Stripe was reported: %+v", m)
		case m.File == "main.go" && m.Line == 10 && m.Operation == "GET /v1/customers":
			t.Errorf("a URL extended with an id is the item endpoint, not the list endpoint: %+v", m)
		case len(m.File) > 12 && m.File[:12] == "node_modules":
			t.Errorf("dependency code was scanned: %+v", m)
		}
	}
}

func TestScan_WithoutHintsEveryFileIsSearched(t *testing.T) {
	root := fixtureRepo(t)
	ms, _ := Scan(root, []Target{{"POST", "/v1/refunds"}}, Options{})
	found := false
	for _, m := range ms {
		if m.File == "models/refund.rb" {
			found = true
		}
	}
	if !found {
		t.Error("with no hints the unrelated model should match; this proves the hint gate is what excludes it")
	}
}

func TestScan_PathConfidenceDependsOnVisibleMethod(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.py", "import stripe\nrequests.post(\"https://api.stripe.com/v1/refunds\")\n")
	write(t, root, "b.py", "import stripe\nurl = \"https://api.stripe.com/v1/refunds\"\nsend(url)\n")
	ms, _ := Scan(root, []Target{{"POST", "/v1/refunds"}}, Options{Hints: []string{"stripe"}})
	got := hits(ms)
	if m := got[hit{"a.py", 2, "POST /v1/refunds"}]; m.Confidence != High {
		t.Errorf("a.py: method visible, confidence = %q, want high", m.Confidence)
	}
	if m := got[hit{"b.py", 2, "POST /v1/refunds"}]; m.Confidence != Medium {
		t.Errorf("b.py: method not visible, confidence = %q, want medium", m.Confidence)
	}
}

func TestScan_WindowContainsSurroundingCode(t *testing.T) {
	root := t.TempDir()
	write(t, root, "x.js", "const stripe = require('stripe');\nstripe.refunds.create({\n  charge: ch,\n});\n")
	ms, _ := Scan(root, []Target{{"POST", "/v1/refunds"}}, Options{Hints: []string{"stripe"}, WindowLines: 3})
	if len(ms) != 1 {
		t.Fatalf("got %d matches", len(ms))
	}
	if !contains(ms[0].Window, "charge: ch") {
		t.Errorf("window = %q, want it to include the arguments on the following lines", ms[0].Window)
	}
}

func TestScan_SkipsBinaryAndHugeFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "bin.js", "stripe.refunds.create(\x00\x00)")
	write(t, root, "ok.js", "const s = require('stripe'); s.refunds.create({});")
	ms, _ := Scan(root, []Target{{"POST", "/v1/refunds"}}, Options{Hints: []string{"stripe"}, MaxFileBytes: 1000})
	if len(ms) != 1 || ms[0].File != "ok.js" {
		t.Errorf("got %+v, want only ok.js", ms)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestNameRegexp_MatchesHowLanguagesSpellThings(t *testing.T) {
	cases := []struct {
		name  string
		match []string
		no    []string
	}{
		{"payment_intents", []string{"paymentIntents", "PaymentIntent", "payment_intent", "payment_intents", "paymentintent"}, []string{"payment", "intents"}},
		{"refunds", []string{"refunds", "Refund", "refund"}, []string{"refunded"}},
		{"entries", []string{"entries", "entry", "Entry"}, []string{"entri"}},
	}
	for _, c := range cases {
		re := regexpFor(nameRegexp(c.name))
		for _, m := range c.match {
			if !re.MatchString(m) {
				t.Errorf("%s should match %q", c.name, m)
			}
		}
		for _, n := range c.no {
			if re.MatchString(n) {
				t.Errorf("%s should not match %q", c.name, n)
			}
		}
	}
}

func regexpFor(pattern string) interface{ MatchString(string) bool } {
	return anchored{pattern}
}

type anchored struct{ p string }

func (a anchored) MatchString(s string) bool {
	return compileAnchored(a.p).MatchString(s)
}

func TestSDKRegexp_ActionsAndNestedCollections(t *testing.T) {
	cases := []struct {
		target Target
		line   string
		want   bool
	}{
		{Target{"POST", "/v1/payment_intents/{intent}/confirm"}, "stripe.paymentIntents.confirm(id)", true},
		{Target{"POST", "/v1/payment_intents/{intent}/confirm"}, "stripe.paymentIntents.create({})", false},
		{Target{"POST", "/repos/{owner}/{repo}/issues"}, "octokit.rest.issues.create({})", true},
		{Target{"POST", "/repos/{owner}/{repo}/issues"}, "octokit.rest.issues.delete({})", false},
		{Target{"GET", "/v1/customers"}, "stripe.customers.list({limit: 3})", true},
		{Target{"GET", "/v1/customers"}, "stripe.customers.retrieve(id)", false},
		{Target{"GET", "/v1/customers/{customer}"}, "stripe.customers.retrieve(id)", true},
		{Target{"DELETE", "/v1/customers/{customer}"}, "stripe.customers.del(id)", true},
		{Target{"POST", "/chat/completions"}, "client.chat.completions.create(model=m)", true},
		{Target{"POST", "/2010-04-01/Accounts/{AccountSid}/Messages.json"}, "client.messages.create(to=n)", true},
		{Target{"POST", "/v1/refunds"}, "Stripe::Refund.create(x)", true},
		{Target{"POST", "/v1/refunds"}, "stripe.refunds.createRefund(x)", false},
	}
	for _, c := range cases {
		sp := analyzeSDK(c.target)
		if sp == nil {
			t.Errorf("%v: no regexp", c.target)
			continue
		}
		if got := sp.bare.MatchString(c.line); got != c.want {
			t.Errorf("%s vs %q = %v, want %v", c.target.Operation(), c.line, got, c.want)
		}
	}
}

var sessionsUniverse = []Target{
	{"POST", "/v1/checkout/sessions"},
	{"POST", "/v1/billing_portal/sessions"},
	{"POST", "/v1/financial_connections/sessions"},
}

func scanOne(t *testing.T, files map[string]string, target Target) map[string]bool {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		write(t, root, name, body)
	}
	ms, err := Scan(root, []Target{target}, Options{Hints: []string{"stripe"}, Universe: sessionsUniverse})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range ms {
		got[m.File] = true
	}
	return got
}

func TestScan_AmbiguousNamesAreAttributedToTheRightEndpoint(t *testing.T) {
	checkout := Target{"POST", "/v1/checkout/sessions"}
	portal := Target{"POST", "/v1/billing_portal/sessions"}

	files := map[string]string{
		// Qualified in the line itself.
		"node_checkout.js": "const stripe = require('stripe');\nstripe.checkout.sessions.create({});\n",
		"node_portal.js":   "const stripe = require('stripe');\nstripe.billingPortal.sessions.create({});\n",
		// Go names the package only in the import.
		"go_checkout.go": "import (\n\t\"github.com/stripe/stripe-go/v79/checkout/session\"\n)\nfunc f() { session.New(params) }\n",
		// Java: both classes imported, and the line spells out which it means.
		"Both.java": "import com.stripe.model.checkout.Session;\nclass B { void f() {\n  com.stripe.model.billingportal.Session s = com.stripe.model.billingportal.Session.create(p);\n} }\n",
		// Bare name, no sign of which resource: must not be guessed.
		"unknown.py": "import stripe\nstripe_session = Session.create(params)\n",
	}

	gotCheckout := scanOne(t, files, checkout)
	for _, want := range []string{"node_checkout.js", "go_checkout.go"} {
		if !gotCheckout[want] {
			t.Errorf("checkout endpoint should be found in %s", want)
		}
	}
	for _, not := range []string{"node_portal.js", "Both.java", "unknown.py"} {
		if gotCheckout[not] {
			t.Errorf("checkout endpoint wrongly reported in %s", not)
		}
	}

	gotPortal := scanOne(t, files, portal)
	if !gotPortal["node_portal.js"] || !gotPortal["Both.java"] {
		t.Errorf("billing portal endpoint should be found in node_portal.js and Both.java: %v", gotPortal)
	}
	for _, not := range []string{"node_checkout.js", "go_checkout.go", "unknown.py"} {
		if gotPortal[not] {
			t.Errorf("billing portal endpoint wrongly reported in %s", not)
		}
	}
}

func TestScan_UniqueNamesNeedNoParent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.js", "const s = require('stripe');\ns.refunds.create({});\n")
	universe := []Target{{"POST", "/v1/refunds"}, {"POST", "/v1/checkout/sessions"}}
	ms, _ := Scan(root, []Target{{"POST", "/v1/refunds"}}, Options{Hints: []string{"stripe"}, Universe: universe})
	if len(ms) != 1 {
		t.Errorf("got %d matches, want 1", len(ms))
	}
}

func TestDetectUse(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{"dependencies":{"stripe":"^14.0.0"}}`)
	write(t, root, "go.mod", "module x\nrequire github.com/joho/godotenv v1.5.1\n")
	write(t, root, "main.go", "package main\nimport \"github.com/joho/godotenv\"\n")
	write(t, root, "README.md", "We love Stripe and OpenAI.")
	write(t, root, "notes.py", "# see https://github.com/foo/bar and https://api.openai.com/docs\nx = 1\n")
	write(t, root, "client.py", "from octokit import Octokit\n")

	hints := map[string][]string{
		"stripe":       {"stripe"},
		"digitalocean": {"digitalocean", "godo", "pydo"},
		"openai":       {"openai"},
		"github":       {"octokit", "api.github.com"},
	}
	got, err := DetectUse(root, hints)
	if err != nil {
		t.Fatal(err)
	}
	if got["stripe"] != 1 {
		t.Errorf("stripe (named in package.json) = %d, want 1", got["stripe"])
	}
	if got["digitalocean"] != 0 {
		t.Errorf("godotenv must not count as DigitalOcean's godo; got %d", got["digitalocean"])
	}
	if got["github"] != 1 {
		t.Errorf("github (imports octokit) = %d, want 1; a github.com URL in a comment must not count", got["github"])
	}
	if got["openai"] != 0 {
		t.Errorf("openai appears only in prose and a comment URL in a .py comment line; got %d", got["openai"])
	}
}

func TestHasHintWord(t *testing.T) {
	for _, c := range []struct {
		s, h string
		want bool
	}{
		{"github.com/joho/godotenv", "godo", false},
		{"sandbox", "box", false},
		{"@stripe/stripe-js", "stripe", true},
		{"com.stripe.model", "stripe", true},
		{"import stripe", "stripe", true},
		{"stripe==5.0", "stripe", true},
		{"mystripe", "stripe", false},
	} {
		if got := hasHintWord(c.s, c.h); got != c.want {
			t.Errorf("hasHintWord(%q, %q) = %v, want %v", c.s, c.h, got, c.want)
		}
	}
}
