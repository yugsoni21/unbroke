package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yug/unbroke/internal/affect"
	"github.com/yug/unbroke/internal/catalog"
	"github.com/yug/unbroke/internal/change"
	"github.com/yug/unbroke/internal/llm"
	"github.com/yug/unbroke/internal/scan"
	"github.com/yug/unbroke/internal/workflow"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New("test", "", cat)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	s.hosts = map[string]bool{strings.TrimPrefix(ts.URL, "http://"): true}
	return s, ts
}

func get(t *testing.T, ts *httptest.Server, path, token string, host string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	if token != "" {
		req.Header.Set("X-Unbroke-Token", token)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func post(t *testing.T, ts *httptest.Server, path, token string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(string(b)))
	req.Header.Set("X-Unbroke-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func events(t *testing.T, resp *http.Response) []Event {
	t.Helper()
	var out []Event
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("bad event line %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

func TestAPIRequiresTheToken(t *testing.T) {
	s, ts := newTestServer(t)
	for _, tc := range []struct{ name, token string }{{"none", ""}, {"wrong", "nope"}, {"almost", s.token[:len(s.token)-1]}} {
		if r := get(t, ts, "/api/state", tc.token, ""); r.StatusCode != http.StatusForbidden {
			t.Errorf("%s token: status %d, want 403", tc.name, r.StatusCode)
		}
	}
	if r := get(t, ts, "/api/state", s.token, ""); r.StatusCode != 200 {
		t.Errorf("right token: status %d", r.StatusCode)
	}
}

func TestPostsNeedTheTokenToo(t *testing.T) {
	_, ts := newTestServer(t)
	for _, p := range []string{"/api/scan", "/api/fix/preview", "/api/fix/run"} {
		if r := post(t, ts, p, "wrong", map[string]string{"path": "/tmp"}); r.StatusCode != http.StatusForbidden {
			t.Errorf("%s accepted a request with the wrong token: %d", p, r.StatusCode)
		}
	}
}

func TestRejectsForeignHostHeaders(t *testing.T) {
	s, ts := newTestServer(t)
	// A rebinding attack arrives addressed to the attacker's own hostname.
	for _, host := range []string{"evil.example.com", "evil.example.com:" + strings.Split(ts.URL, ":")[2], "127.0.0.1.evil.com"} {
		if r := get(t, ts, "/", "", host); r.StatusCode != http.StatusForbidden {
			t.Errorf("Host %q reached the page: %d", host, r.StatusCode)
		}
		if r := get(t, ts, "/api/state", s.token, host); r.StatusCode != http.StatusForbidden {
			t.Errorf("Host %q reached the API even with the token: %d", host, r.StatusCode)
		}
	}
}

func TestIndexCarriesTheTokenAndStrictHeaders(t *testing.T) {
	s, ts := newTestServer(t)
	r := get(t, ts, "/", "", "")
	body := new(strings.Builder)
	sc := bufio.NewScanner(r.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		body.WriteString(sc.Text() + "\n")
	}
	if !strings.Contains(body.String(), s.token) {
		t.Error("the served page does not carry the token")
	}
	if strings.Contains(body.String(), "{{") {
		t.Error("template placeholders left in the page")
	}
	csp := r.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "connect-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff")
	}
}

func TestListenOnlyOnLoopback(t *testing.T) {
	cat, _ := catalog.Load()
	s, _ := New("t", "", cat)
	for _, bad := range []string{"0.0.0.0:0", "192.168.1.5:0", ":0", "example.com:0"} {
		if ln, err := s.Listen(bad); err == nil {
			ln.Close()
			t.Errorf("Listen(%q) succeeded; it must refuse non-loopback addresses", bad)
		}
	}
	ln, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}

func TestSnippet(t *testing.T) {
	s, ts := newTestServer(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.js"), []byte("one\ntwo\nthree\nfour\nfive\n"), 0o644)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top secret\n"), 0o644)
	os.Symlink(outside, filepath.Join(root, "link"))
	os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "leak.txt"))
	if _, err := s.resolveRoot(root); err != nil {
		t.Fatal(err)
	}

	q := func(rootArg, file, line string) *http.Response {
		return get(t, ts, "/api/snippet?root="+rootArg+"&file="+file+"&line="+line, s.token, "")
	}
	r := q(root, "a.js", "3")
	var sn snippetDTO
	json.NewDecoder(r.Body).Decode(&sn)
	if r.StatusCode != 200 || len(sn.Lines) != 5 || sn.Lines[2].N != 3 || sn.Lines[2].Text != "three" {
		t.Fatalf("status %d, snippet %+v", r.StatusCode, sn)
	}

	for name, resp := range map[string]*http.Response{
		"parent traversal": q(root, "../"+filepath.Base(outside)+"/secret.txt", "1"),
		"absolute path":    q(root, outside+"/secret.txt", "1"),
		"symlinked dir":    q(root, "link/secret.txt", "1"),
		"symlinked file":   q(root, "leak.txt", "1"),
		"missing file":     q(root, "nope.js", "1"),
		"bad line":         q(root, "a.js", "0"),
	} {
		if resp.StatusCode == 200 {
			t.Errorf("%s: served a file it should refuse", name)
		}
	}
	// A directory the user never scanned cannot be read, however it is reached.
	if r := q(outside, "secret.txt", "1"); r.StatusCode != http.StatusForbidden {
		t.Errorf("an unscanned directory was readable: %d", r.StatusCode)
	}
	if r := q("/etc", "hosts", "1"); r.StatusCode != http.StatusForbidden {
		t.Errorf("/etc was readable: %d", r.StatusCode)
	}
}

func TestScanStreamsEventsAndReportsErrors(t *testing.T) {
	s, ts := newTestServer(t)
	s.scanFn = func(ctx context.Context, _ *Server, req ScanRequest, emit func(Event)) error {
		emit(Event{Type: "stage", Text: "working"})
		if req.Path == "bad" {
			return errors.New("bad is not a directory")
		}
		emit(Event{Type: "result", Provider: "stripe", Result: &providerResult{ID: "stripe", Name: "Stripe", Total: 27, Affecting: 1}})
		return nil
	}
	ev := events(t, post(t, ts, "/api/scan", s.token, ScanRequest{Path: "ok"}))
	if len(ev) != 3 || ev[0].Type != "stage" || ev[1].Result == nil || ev[1].Result.Total != 27 || ev[2].Type != "done" {
		t.Errorf("events = %+v", ev)
	}
	ev = events(t, post(t, ts, "/api/scan", s.token, ScanRequest{Path: "bad"}))
	if last := ev[len(ev)-1]; last.Type != "error" || !strings.Contains(last.Text, "not a directory") {
		t.Errorf("events = %+v", ev)
	}
}

func TestResolveRootMessages(t *testing.T) {
	cat, _ := catalog.Load()
	s, _ := New("t", "", cat)
	f := filepath.Join(t.TempDir(), "file.txt")
	os.WriteFile(f, []byte("x"), 0o644)
	for in, want := range map[string]string{"": "enter the path", "/definitely/not/here": "does not exist", f: "not a directory"} {
		if _, err := s.resolveRoot(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("resolveRoot(%q) = %v, want mention of %q", in, err, want)
		}
	}
}

// ---- the fix endpoints, with a fake model in a real git repository ----

type fakeModel struct{ reply string }

func (m fakeModel) Complete(ctx context.Context, system, user string) (string, error) {
	return m.reply, nil
}

func gitRepo(t *testing.T) string {
	t.Helper()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "T", "GIT_AUTHOR_EMAIL": "t@e.com", "GIT_COMMITTER_NAME": "T", "GIT_COMMITTER_EMAIL": "t@e.com"} {
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "pay.js"), []byte("const s = require('stripe');\ns.refunds.create({ charge: ch });\n"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	real, _ := filepath.EvalSymlinks(dir)
	return real
}

func seed(s *Server, root string) {
	p, _ := s.Catalog.Find("stripe")
	hit := affect.Hit{Match: scan.Match{File: "pay.js", Line: 2, Text: "s.refunds.create({ charge: ch });", Operation: "POST /v1/refunds"}, Field: "charge"}
	f := affect.Finding{Provider: "stripe", Relevance: affect.ChangedField, Hits: []affect.Hit{hit},
		Change: change.Change{RuleID: "request-property-removed", Severity: change.Warning, Summary: "removed the request property `charge`", Operations: []string{"POST /v1/refunds"}}}
	s.roots[root] = true
	s.cache[cacheKey(root, "stripe", "6m")] = &workflow.Result{Provider: *p, Since: "2026-04-28", Total: 5, Findings: []affect.Finding{f}, Compared: 1}
}

func TestFixPreviewShowsExactlyWhatWouldBeSent(t *testing.T) {
	s, ts := newTestServer(t)
	root := gitRepo(t)
	seed(s, root)
	r := post(t, ts, "/api/fix/preview", s.token, FixRequest{Path: root, Provider: "stripe"})
	var p previewDTO
	json.NewDecoder(r.Body).Decode(&p)
	if r.StatusCode != 200 || len(p.Files) != 1 || !strings.Contains(p.Files[0].Prompt, "s.refunds.create") || p.Bytes == 0 {
		t.Fatalf("status %d preview %+v", r.StatusCode, p)
	}
	if out, _ := exec.Command("git", "-C", root, "branch", "--list").Output(); strings.TrimSpace(string(out)) != "* main" {
		t.Errorf("a preview created a branch: %q", out)
	}
}

func TestFixRunNeedsATestDecisionAndAKey(t *testing.T) {
	s, ts := newTestServer(t)
	root := gitRepo(t)
	seed(s, root)

	ev := events(t, post(t, ts, "/api/fix/run", s.token, FixRequest{Path: root, Provider: "stripe"}))
	if last := ev[len(ev)-1]; last.Type != "error" || !strings.Contains(last.Text, "test") {
		t.Errorf("without a test decision: %+v", ev)
	}

	s.newModel = func() (llm.Completer, error) { return nil, llm.ErrNoKey }
	ev = events(t, post(t, ts, "/api/fix/run", s.token, FixRequest{Path: root, Provider: "stripe", NoTests: true}))
	if last := ev[len(ev)-1]; last.Type != "error" || !strings.Contains(last.Text, "UNBROKE_ANTHROPIC_API_KEY") {
		t.Errorf("without a key: %+v", ev)
	}
}

func TestFixRunCommitsAndReturnsTheDiff(t *testing.T) {
	s, ts := newTestServer(t)
	root := gitRepo(t)
	seed(s, root)
	reply := `{"edits":[{"old":"{ charge: ch }","new":"{ payment_intent: pi }"}],"explanation":"Use payment_intent.","confidence":"high"}`
	s.newModel = func() (llm.Completer, error) { return fakeModel{reply}, nil }

	ev := events(t, post(t, ts, "/api/fix/run", s.token, FixRequest{Path: root, Provider: "stripe", TestCmd: "true"}))
	var fx *fixDTO
	for _, e := range ev {
		if e.Fix != nil {
			fx = e.Fix
		}
		if e.Type == "error" {
			t.Fatalf("error event: %s", e.Text)
		}
	}
	if fx == nil || fx.Branch == "" || fx.Commit == "" || !fx.TestsRan || !fx.TestsPass {
		t.Fatalf("fix result = %+v", fx)
	}
	if !strings.Contains(fx.Diff, "+s.refunds.create({ payment_intent: pi });") || !strings.Contains(fx.Diff, "-s.refunds.create({ charge: ch });") {
		t.Errorf("diff does not show the change:\n%s", fx.Diff)
	}
	if !strings.Contains(fx.PRBody, "removed the request property `charge`") {
		t.Errorf("PR body = %q", fx.PRBody)
	}
}

func TestFixRunFailingTestsKeepNothing(t *testing.T) {
	s, ts := newTestServer(t)
	root := gitRepo(t)
	seed(s, root)
	reply := `{"edits":[{"old":"{ charge: ch }","new":"{ nope }"}],"explanation":"x","confidence":"low"}`
	s.newModel = func() (llm.Completer, error) { return fakeModel{reply}, nil }
	ev := events(t, post(t, ts, "/api/fix/run", s.token, FixRequest{Path: root, Provider: "stripe", TestCmd: "false"}))
	var fx *fixDTO
	for _, e := range ev {
		if e.Fix != nil {
			fx = e.Fix
		}
	}
	if fx == nil || fx.Branch != "" || fx.Discarded == "" || fx.Diff != "" {
		t.Fatalf("fix result = %+v", fx)
	}
	b, _ := os.ReadFile(filepath.Join(root, "pay.js"))
	if !strings.Contains(string(b), "charge: ch") {
		t.Error("the file was not restored after the tests failed")
	}
}
