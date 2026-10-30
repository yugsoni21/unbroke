package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yug/unbroke/internal/llm"
)

func call(t *testing.T, base, method, path, token string) (int, map[string]json.RawMessage) {
	t.Helper()
	req, _ := http.NewRequest(method, base+path, nil)
	if token != "" {
		req.Header.Set("X-Unbroke-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func stubScan(s *Server) {
	s.scanFn = func(ctx context.Context, _ *Server, req ScanRequest, emit func(Event)) error {
		emit(Event{Type: "result", Provider: "stripe", Result: &providerResult{ID: "stripe", Name: "Stripe", Total: 27, Compared: 1, Affecting: 1, CallSites: 50,
			Findings: []findingDTO{{Severity: "warning", Summary: "removed `x`", Hits: []hitDTO{{File: "a.go", Line: 3}}}}}})
		return nil
	}
}

func entries(t *testing.T, base, token string) []HistoryEntry {
	t.Helper()
	_, out := call(t, base, "GET", "/api/history", token)
	var es []HistoryEntry
	if err := json.Unmarshal(out["entries"], &es); err != nil {
		t.Fatal(err)
	}
	return es
}

func TestHistoryIsOffUntilEnabled(t *testing.T) {
	s, ts := newTestServer(t)
	stubScan(s)
	events(t, post(t, ts, "/api/scan", s.token, ScanRequest{Path: t.TempDir()}))
	code, out := call(t, ts.URL, "GET", "/api/history", s.token)
	if code != 200 || string(out["enabled"]) != "false" || string(out["entries"]) != "[]" {
		t.Errorf("code %d out %s", code, out)
	}
	if code, _ := call(t, ts.URL, "GET", "/api/history/20260101T000000-deadbeef", s.token); code != 404 {
		t.Errorf("get with history off: %d", code)
	}
}

func TestAScanIsRecordedAndCanBeReopened(t *testing.T) {
	s, ts := newTestServer(t)
	dir := filepath.Join(t.TempDir(), "history")
	if err := s.EnableHistory(dir); err != nil {
		t.Fatal(err)
	}
	stubScan(s)
	root := t.TempDir()
	events(t, post(t, ts, "/api/scan", s.token, ScanRequest{Path: root, Since: "3m"}))

	es := entries(t, ts.URL, s.token)
	if len(es) != 1 || es[0].Kind != "scan" || es[0].Since != "3m" || len(es[0].Providers) != 1 || es[0].Providers[0].Affecting != 1 || es[0].Providers[0].CallSites != 50 {
		t.Fatalf("entries = %+v", es)
	}
	code, out := call(t, ts.URL, "GET", "/api/history/"+es[0].ID, s.token)
	if code != 200 || !strings.Contains(string(out["detail"]), `"a.go"`) {
		t.Errorf("reopen: %d %s", code, out)
	}
	// Files holding source lines are private to the user.
	if st, err := os.Stat(filepath.Join(dir, es[0].ID+".json")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("detail file: %v %v", st, err)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("history dir mode %v", st.Mode().Perm())
	}
}

func TestAFailedOrEmptyScanIsNotRecorded(t *testing.T) {
	s, ts := newTestServer(t)
	s.EnableHistory(t.TempDir())
	s.scanFn = func(ctx context.Context, _ *Server, req ScanRequest, emit func(Event)) error {
		emit(Event{Type: "detected"})
		return nil
	}
	events(t, post(t, ts, "/api/scan", s.token, ScanRequest{Path: t.TempDir()}))
	stubScan(s)
	s.scanFn = func(ctx context.Context, _ *Server, req ScanRequest, emit func(Event)) error {
		emit(Event{Type: "result", Provider: "stripe", Result: &providerResult{ID: "stripe"}})
		return context.DeadlineExceeded
	}
	events(t, post(t, ts, "/api/scan", s.token, ScanRequest{Path: t.TempDir()}))
	if es := entries(t, ts.URL, s.token); len(es) != 0 {
		t.Errorf("recorded %+v", es)
	}
}

func TestHistoryRequiresTheTokenAndRejectsOddIDs(t *testing.T) {
	s, ts := newTestServer(t)
	dir := t.TempDir()
	s.EnableHistory(filepath.Join(dir, "h"))
	stubScan(s)
	events(t, post(t, ts, "/api/scan", s.token, ScanRequest{Path: t.TempDir()}))
	if code, _ := call(t, ts.URL, "GET", "/api/history", ""); code != 403 {
		t.Errorf("no token: %d", code)
	}
	if code, _ := call(t, ts.URL, "DELETE", "/api/history", "wrong"); code != 403 {
		t.Errorf("delete without token: %d", code)
	}
	os.WriteFile(filepath.Join(dir, "secret.json"), []byte(`{"x":1}`), 0o600)
	for _, id := range []string{"..%2Fsecret", "../secret", "index", "20260101T000000-ZZZZZZZZ", "x"} {
		if code, _ := call(t, ts.URL, "GET", "/api/history/"+id, s.token); code == 200 {
			t.Errorf("id %q was served", id)
		}
		if code, _ := call(t, ts.URL, "DELETE", "/api/history/"+id, s.token); code == 200 {
			t.Errorf("id %q was deleted", id)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.json")); err != nil {
		t.Errorf("file outside the history was touched: %v", err)
	}
}

func TestHistoryDeleteOneAndClear(t *testing.T) {
	s, ts := newTestServer(t)
	dir := t.TempDir()
	s.EnableHistory(dir)
	stubScan(s)
	for i := 0; i < 3; i++ {
		events(t, post(t, ts, "/api/scan", s.token, ScanRequest{Path: t.TempDir()}))
	}
	es := entries(t, ts.URL, s.token)
	if len(es) != 3 {
		t.Fatalf("got %d", len(es))
	}
	if code, _ := call(t, ts.URL, "DELETE", "/api/history/"+es[1].ID, s.token); code != 200 {
		t.Errorf("delete: %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, es[1].ID+".json")); !os.IsNotExist(err) {
		t.Errorf("detail file left behind: %v", err)
	}
	if code, _ := call(t, ts.URL, "DELETE", "/api/history/"+es[1].ID, s.token); code != 404 {
		t.Errorf("second delete: %d", code)
	}
	if code, _ := call(t, ts.URL, "DELETE", "/api/history", s.token); code != 200 {
		t.Errorf("clear: %d", code)
	}
	if es := entries(t, ts.URL, s.token); len(es) != 0 {
		t.Errorf("after clear: %+v", es)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "2*.json"))
	if len(left) != 0 {
		t.Errorf("files left: %v", left)
	}
}

func TestHistoryKeepsOnlyTheNewestFifty(t *testing.T) {
	h, err := newHistoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{history: h}
	for i := 0; i < maxHistory+5; i++ {
		s.recordScan("/p", "6m", []*providerResult{{ID: "stripe", Name: "Stripe", Total: i}})
	}
	es := h.list()
	if len(es) != maxHistory {
		t.Fatalf("kept %d", len(es))
	}
	if es[0].Providers[0].Total != maxHistory+4 {
		t.Errorf("newest first expected, got total %d", es[0].Providers[0].Total)
	}
	files, _ := filepath.Glob(filepath.Join(h.dir, "2*.json"))
	if len(files) != maxHistory {
		t.Errorf("%d detail files for %d entries", len(files), maxHistory)
	}
}

func TestADamagedIndexStartsOver(t *testing.T) {
	h, _ := newHistoryStore(t.TempDir())
	os.WriteFile(filepath.Join(h.dir, "index.json"), []byte("{not json"), 0o600)
	if len(h.list()) != 0 {
		t.Fatal("expected an empty list")
	}
	s := &Server{history: h}
	s.recordScan("/p", "", []*providerResult{{ID: "stripe", Name: "Stripe"}})
	if es := h.list(); len(es) != 1 || es[0].Since != "6m" {
		t.Errorf("entries = %+v", es)
	}
}

func TestAFixRunIsRecordedEvenWhenNothingWasKept(t *testing.T) {
	s, ts := newTestServer(t)
	s.EnableHistory(t.TempDir())
	root := gitRepo(t)
	seed(s, root)
	s.newModel = func() (llm.Completer, error) {
		return fakeModel{`{"edits":[{"old":"{ charge: ch }","new":"{ payment_intent: pi }"}],"explanation":"x","confidence":"high"}`}, nil
	}
	events(t, post(t, ts, "/api/fix/run", s.token, FixRequest{Path: root, Provider: "stripe", TestCmd: "true"}))
	events(t, post(t, ts, "/api/fix/run", s.token, FixRequest{Path: root, Provider: "stripe", TestCmd: "false"}))

	es := entries(t, ts.URL, s.token)
	if len(es) != 2 {
		t.Fatalf("entries = %+v", es)
	}
	var kept, dropped *HistoryEntry
	for i := range es {
		if es[i].Fix.Branch != "" {
			kept = &es[i]
		} else {
			dropped = &es[i]
		}
	}
	if kept == nil || kept.Fix.Changed != 1 || !kept.Fix.TestsPass || kept.Fix.Commit == "" {
		t.Errorf("kept = %+v", kept)
	}
	if dropped == nil || dropped.Fix.Discarded == "" {
		t.Errorf("dropped = %+v", dropped)
	}
}
