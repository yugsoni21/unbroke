package fix

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yug/unbroke/internal/affect"
	"github.com/yug/unbroke/internal/change"
	"github.com/yug/unbroke/internal/scan"
)

const payJS = "const stripe = require('stripe')('sk');\nasync function refund(ch) {\n  return stripe.refunds.create({ charge: ch, amount: 100 });\n}\n"

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	for name, body := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func task(file string, line int, field string) FileTask {
	c := change.Change{
		RuleID: "request-property-removed", Severity: change.Warning,
		Summary: "removed the request property `charge`", Operations: []string{"POST /v1/refunds"},
	}
	f := affect.Finding{Provider: "stripe", Change: c, Relevance: affect.ChangedField}
	h := affect.Hit{Match: scan.Match{File: file, Line: line, Text: "stripe.refunds.create({ charge: ch, amount: 100 });", Operation: "POST /v1/refunds"}, Field: field}
	f.Hits = []affect.Hit{h}
	return FileTask{File: file, Hits: f.Hits, Findings: []affect.Finding{f}}
}

type fakeModel struct {
	mu      sync.Mutex
	replies []string
	errs    []error
	calls   int
	prompts []string
}

func (m *fakeModel) Complete(ctx context.Context, system, user string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.calls
	m.calls++
	m.prompts = append(m.prompts, user)
	if i < len(m.errs) && m.errs[i] != nil {
		return "", m.errs[i]
	}
	return m.replies[i], nil
}

func edit(old, new string) string {
	b, _ := json.Marshal(Proposal{Edits: []Edit{{Old: old, New: new}}, Explanation: "Use payment_intent instead of charge.", Confidence: "high"})
	return string(b)
}

const goodEdit = "{ charge: ch, amount: 100 }"
const newEdit = "{ payment_intent: pi, amount: 100 }"

func TestParseProposal(t *testing.T) {
	cases := []struct {
		name, in string
		wantErr  bool
		edits    int
		conf     string
	}{
		{"plain", `{"edits":[{"old":"a","new":"b"}],"explanation":"x","confidence":"high"}`, false, 1, "high"},
		{"fenced", "```json\n{\"edits\":[],\"explanation\":\"x\",\"confidence\":\"medium\"}\n```", false, 0, "medium"},
		{"prose around", "Here is the fix:\n{\"edits\":[{\"old\":\"a\",\"new\":\"b\"}],\"explanation\":\"x\",\"confidence\":\"HIGH\"}\nHope it helps!", false, 1, "high"},
		{"unknown confidence becomes low", `{"edits":[],"confidence":"certain"}`, false, 0, "low"},
		{"no json", "I cannot help", true, 0, ""},
		{"broken json", `{"edits":[`, true, 0, ""},
	}
	for _, c := range cases {
		p, err := ParseProposal(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v", c.name, err)
			continue
		}
		if err == nil && (len(p.Edits) != c.edits || p.Confidence != c.conf) {
			t.Errorf("%s: got %d edits, confidence %q", c.name, len(p.Edits), p.Confidence)
		}
	}
}

func TestApply(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS, "dup.js": "x = 1;\nx = 1;\n"})
	cases := []struct {
		name     string
		file     string
		edit     Edit
		applied  int
		rejected string
	}{
		{"exact once", "pay.js", Edit{goodEdit, newEdit}, 1, ""},
		{"text not in file", "pay.js", Edit{"no such text", "y"}, 0, "does not appear"},
		{"ambiguous", "dup.js", Edit{"x = 1;", "x = 2;"}, 0, "appears 2 times"},
		{"identical", "pay.js", Edit{"stripe", "stripe"}, 0, "identical"},
		{"empty old", "pay.js", Edit{"", "y"}, 0, "empty"},
	}
	for _, c := range cases {
		res, err := Apply(root, c.file, Proposal{Edits: []Edit{c.edit}})
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(res.Applied) != c.applied {
			t.Errorf("%s: applied %d, want %d", c.name, len(res.Applied), c.applied)
		}
		if c.rejected != "" && (len(res.Rejected) != 1 || !strings.Contains(res.Rejected[0], c.rejected)) {
			t.Errorf("%s: rejected = %v, want mention of %q", c.name, res.Rejected, c.rejected)
		}
	}
	got, _ := os.ReadFile(filepath.Join(root, "pay.js"))
	if !strings.Contains(string(got), newEdit) {
		t.Error("the good edit was not written")
	}
	dup, _ := os.ReadFile(filepath.Join(root, "dup.js"))
	if string(dup) != "x = 1;\nx = 1;\n" {
		t.Errorf("an ambiguous edit changed the file: %q", dup)
	}
}

func TestApply_RefusesPathsOutsideTheRepo(t *testing.T) {
	root := newRepo(t, map[string]string{"a.js": "x"})
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	os.WriteFile(outside, []byte("secret"), 0o644)
	for _, bad := range []string{"../outside.txt", "/etc/passwd", "a/../../outside.txt"} {
		if _, err := Apply(root, bad, Proposal{Edits: []Edit{{"secret", "x"}}}); err == nil {
			t.Errorf("Apply accepted %q", bad)
		}
	}
	if b, _ := os.ReadFile(outside); string(b) != "secret" {
		t.Error("a file outside the repository was modified")
	}
}

func TestPlan(t *testing.T) {
	removed := affect.Finding{Relevance: affect.RemovedEndpoint, Change: change.Change{RuleID: "api-path-removed-without-deprecation", Severity: change.Breaking, Summary: "removed"},
		Hits: []affect.Hit{{Match: scan.Match{File: "old.js", Line: 1}}}}
	field := affect.Finding{Relevance: affect.ChangedField, Change: change.Change{RuleID: "r", Severity: change.Warning, Summary: "field"},
		Hits: []affect.Hit{{Match: scan.Match{File: "a.js", Line: 2}, Field: "charge"}, {Match: scan.Match{File: "b.js", Line: 3}}}}
	calls := affect.Finding{Relevance: affect.CallsEndpoint, Change: change.Change{RuleID: "r2", Summary: "calls"},
		Hits: []affect.Hit{{Match: scan.Match{File: "c.js", Line: 4}}}}

	tasks := Plan([]affect.Finding{field, calls, removed}, nil, 0)
	var files []string
	for _, tk := range tasks {
		files = append(files, tk.File)
	}
	if strings.Join(files, ",") != "old.js,a.js" {
		t.Errorf("files = %v, want old.js (removed endpoint, most urgent) then a.js; b.js has no changed field nearby and c.js only calls the endpoint", files)
	}
	if got := Plan([]affect.Finding{field, removed}, nil, 1); len(got) != 1 {
		t.Errorf("max=1 gave %d tasks", len(got))
	}
}

func TestBuildPrompt(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS})
	p, err := BuildPrompt(root, task("pay.js", 3, "charge"), "Stripe")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"removed the request property `charge`", "POST /v1/refunds", "line 3", "near `charge`", "<file>", "stripe.refunds.create"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
}

func TestBuildPrompt_LargeFilesAreSentAsWindows(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 2000; i++ {
		b.WriteString("line number " + itoa(i) + "\n")
	}
	root := newRepo(t, map[string]string{"big.js": b.String()})
	p, err := BuildPrompt(root, task("big.js", 1000, ""), "Stripe")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "line number 1000") || !strings.Contains(p, "large file") {
		t.Error("window around the call site is missing")
	}
	if strings.Contains(p, "line number 5\n") || strings.Contains(p, "line number 1900\n") {
		t.Error("lines far from any call site should not be sent")
	}
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

func TestRun_CommitsOnANewBranchAndReturnsToTheStartingOne(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS})
	m := &fakeModel{replies: []string{edit(goodEdit, newEdit)}}
	rep, err := Run(context.Background(), []FileTask{task("pay.js", 3, "charge")}, RunOptions{Root: root, Provider: "Stripe", Model: m, TestCmd: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Branch == "" || rep.Commit == "" || rep.Discarded != "" {
		t.Fatalf("report = %+v", rep)
	}
	if !rep.TestsRan || !rep.TestsPass {
		t.Errorf("tests ran=%v pass=%v", rep.TestsRan, rep.TestsPass)
	}
	if cur := gitIn(t, root, "rev-parse", "--abbrev-ref", "HEAD"); cur != "main" {
		t.Errorf("left on %q; the user should be back where they started", cur)
	}
	if body, _ := os.ReadFile(filepath.Join(root, "pay.js")); string(body) != payJS {
		t.Error("the working tree should be exactly as it was found")
	}
	if fixed := gitIn(t, root, "show", rep.Branch+":pay.js"); !strings.Contains(fixed, newEdit) {
		t.Errorf("the fix branch lacks the edit: %q", fixed)
	}
	if msg := gitIn(t, root, "log", "-1", "--format=%s", rep.Branch); msg != "Update Stripe integration for API changes" {
		t.Errorf("commit subject = %q", msg)
	}
	if st := gitIn(t, root, "status", "--porcelain"); st != "" {
		t.Errorf("working tree not clean: %q", st)
	}
}

func TestRun_ASecondRunOnTheSameDayGetsItsOwnBranch(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS})
	opts := func() RunOptions {
		return RunOptions{Root: root, Provider: "Stripe", Model: &fakeModel{replies: []string{edit(goodEdit, newEdit)}}}
	}
	first, err := Run(context.Background(), []FileTask{task("pay.js", 3, "charge")}, opts())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(context.Background(), []FileTask{task("pay.js", 3, "charge")}, opts())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first.Branch == second.Branch || !strings.HasSuffix(second.Branch, "-2") {
		t.Errorf("branches %q and %q", first.Branch, second.Branch)
	}
	// The second fix is based on main, not stacked on the first fix.
	if parent := gitIn(t, root, "rev-parse", second.Branch+"~1"); parent != gitIn(t, root, "rev-parse", "main") {
		t.Error("the second fix branch should start from main")
	}
}

func TestRun_AllowDirtyNeverEditsCommitsOrDiscardsTheUsersWork(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS, "notes.txt": "orig\n", "other.js": payJS})
	// Uncommitted work: one file Unbroke would like to edit, one it would not.
	os.WriteFile(filepath.Join(root, "pay.js"), []byte(payJS+"// my wip\n"), 0o644)
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("MY UNSAVED WORK\n"), 0o644)
	os.WriteFile(filepath.Join(root, "scratch.txt"), []byte("untracked\n"), 0o644)

	tasks := []FileTask{task("pay.js", 3, "charge"), task("other.js", 3, "charge")}

	// A run whose tests fail must leave everything as it was.
	m := &fakeModel{replies: []string{edit(goodEdit, newEdit), edit(goodEdit, newEdit)}}
	rep, err := Run(context.Background(), tasks, RunOptions{Root: root, Provider: "Stripe", Model: m, AllowDirty: true, TestCmd: "false"})
	if err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		for file, want := range map[string]string{"pay.js": payJS + "// my wip\n", "notes.txt": "MY UNSAVED WORK\n", "scratch.txt": "untracked\n", "other.js": payJS} {
			if got, _ := os.ReadFile(filepath.Join(root, file)); string(got) != want {
				t.Errorf("%s: %s = %q, want %q", when, file, got, want)
			}
		}
		if cur := gitIn(t, root, "rev-parse", "--abbrev-ref", "HEAD"); cur != "main" {
			t.Errorf("%s: left on %q", when, cur)
		}
	}
	check("after failed tests")
	if rep.Files[0].Err == nil || !strings.Contains(rep.Files[0].Err.Error(), "uncommitted") {
		t.Errorf("pay.js has uncommitted changes and must be left alone: %+v", rep.Files[0])
	}
	if m.calls != 1 {
		t.Errorf("model called %d times; it must not be asked about a dirty file", m.calls)
	}

	// A successful run commits only Unbroke's own edit.
	m = &fakeModel{replies: []string{edit(goodEdit, newEdit), edit(goodEdit, newEdit)}}
	rep, err = Run(context.Background(), tasks, RunOptions{Root: root, Provider: "Stripe", Model: m, AllowDirty: true, TestCmd: "true"})
	if err != nil || rep.Commit == "" {
		t.Fatalf("rep = %+v, err = %v", rep, err)
	}
	check("after a successful run")
	committed := gitIn(t, root, "show", "--name-only", "--format=", rep.Branch)
	if committed != "other.js" {
		t.Errorf("the commit contains %q; it must contain only Unbroke's edit to other.js", committed)
	}
}

func TestRun_CancellationStillPutsEverythingBack(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS})
	m := &fakeModel{replies: []string{edit(goodEdit, newEdit)}}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	rep, _ := Run(ctx, []FileTask{task("pay.js", 3, "charge")}, RunOptions{Root: root, Provider: "Stripe", Model: m, TestCmd: "sleep 5"})
	if rep.Branch != "" || !strings.Contains(rep.Discarded, "cancel") {
		t.Errorf("report = %+v", rep)
	}
	if cur := gitIn(t, root, "rev-parse", "--abbrev-ref", "HEAD"); cur != "main" {
		t.Errorf("left on %q after cancellation", cur)
	}
	if st := gitIn(t, root, "status", "--porcelain"); st != "" {
		t.Errorf("working tree not restored after cancellation: %q", st)
	}
	if b := gitIn(t, root, "branch", "--list", "unbroke/*"); b != "" {
		t.Errorf("branch left behind: %q", b)
	}
}

func TestRun_RefusesADetachedHead(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS})
	gitIn(t, root, "checkout", "-q", "--detach")
	m := &fakeModel{replies: []string{edit(goodEdit, newEdit)}}
	_, err := Run(context.Background(), []FileTask{task("pay.js", 3, "charge")}, RunOptions{Root: root, Provider: "Stripe", Model: m})
	if err == nil || !strings.Contains(err.Error(), "detached") {
		t.Fatalf("err = %v", err)
	}
	if m.calls != 0 {
		t.Error("the model must not be called")
	}
}

func TestRun_ARejectedCommitIsRolledBack(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS})
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	os.WriteFile(hook, []byte("#!/bin/sh\necho lint failed >&2\nexit 1\n"), 0o755)
	m := &fakeModel{replies: []string{edit(goodEdit, newEdit)}}
	rep, err := Run(context.Background(), []FileTask{task("pay.js", 3, "charge")}, RunOptions{Root: root, Provider: "Stripe", Model: m})
	if err == nil || !strings.Contains(err.Error(), "lint failed") {
		t.Fatalf("err = %v, want the hook's message", err)
	}
	if strings.Contains(err.Error(), "user.email") {
		t.Errorf("a hook failure is not an identity problem: %v", err)
	}
	if rep.Branch != "" || rep.Discarded == "" {
		t.Errorf("report = %+v", rep)
	}
	if cur := gitIn(t, root, "rev-parse", "--abbrev-ref", "HEAD"); cur != "main" {
		t.Errorf("left on %q", cur)
	}
	if st := gitIn(t, root, "status", "--porcelain"); st != "" {
		t.Errorf("not restored: %q", st)
	}
}

func TestRun_FilesTheTestsModifyArePutBack(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS, "snap.txt": "old\n"})
	m := &fakeModel{replies: []string{edit(goodEdit, newEdit)}}
	rep, err := Run(context.Background(), []FileTask{task("pay.js", 3, "charge")}, RunOptions{Root: root, Provider: "Stripe", Model: m, TestCmd: "echo regenerated > snap.txt"})
	if err != nil || rep.Commit == "" {
		t.Fatalf("rep = %+v err = %v", rep, err)
	}
	if len(rep.TestTouched) != 1 || rep.TestTouched[0] != "snap.txt" {
		t.Errorf("TestTouched = %v", rep.TestTouched)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "snap.txt")); string(got) != "old\n" {
		t.Errorf("snap.txt = %q, want it restored", got)
	}
	if committed := gitIn(t, root, "show", "--name-only", "--format=", rep.Branch); committed != "pay.js" {
		t.Errorf("commit contains %q", committed)
	}
}

func TestSafeJoin_RefusesSymlinks(t *testing.T) {
	root := newRepo(t, map[string]string{"a.js": "x"})
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	os.WriteFile(secret, []byte("do not send"), 0o600)
	os.Symlink(secret, filepath.Join(root, "link.js"))
	os.Symlink(outside, filepath.Join(root, "dirlink"))
	for _, rel := range []string{"link.js", "dirlink/secret"} {
		if _, err := safeJoin(root, rel); err == nil {
			t.Errorf("safeJoin(%q) followed a symlink out of the repository", rel)
		}
		if _, err := BuildPrompt(root, task(rel, 1, ""), "Stripe"); err == nil {
			t.Errorf("BuildPrompt read %q through a symlink", rel)
		}
		if _, err := Apply(root, rel, Proposal{Edits: []Edit{{"do not send", "x"}}}); err == nil {
			t.Errorf("Apply wrote %q through a symlink", rel)
		}
	}
	if b, _ := os.ReadFile(secret); string(b) != "do not send" {
		t.Error("a file outside the repository was modified")
	}
	if _, err := safeJoin(root, "a.js"); err != nil {
		t.Errorf("an ordinary file was refused: %v", err)
	}
}

func TestRun_FailingTestsDiscardEverything(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS})
	m := &fakeModel{replies: []string{edit(goodEdit, newEdit)}}
	rep, err := Run(context.Background(), []FileTask{task("pay.js", 3, "charge")}, RunOptions{Root: root, Provider: "Stripe", Model: m, TestCmd: "echo boom; false"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Discarded == "" || rep.Commit != "" || !rep.TestsRan || rep.TestsPass {
		t.Fatalf("report = %+v", rep)
	}
	if !strings.Contains(rep.TestOutput, "boom") {
		t.Errorf("test output = %q", rep.TestOutput)
	}
	if cur := gitIn(t, root, "rev-parse", "--abbrev-ref", "HEAD"); cur != "main" {
		t.Errorf("left on %q, want main", cur)
	}
	if branches := gitIn(t, root, "branch", "--list", "unbroke/*"); branches != "" {
		t.Errorf("branch left behind: %q", branches)
	}
	if body, _ := os.ReadFile(filepath.Join(root, "pay.js")); string(body) != payJS {
		t.Error("the file was not restored")
	}
	if st := gitIn(t, root, "status", "--porcelain"); st != "" {
		t.Errorf("working tree not clean: %q", st)
	}
}

func TestRun_NoUsableEditLeavesTheRepoAlone(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS})
	for name, reply := range map[string]string{
		"model declines":   `{"edits":[],"explanation":"nothing replaces charge","confidence":"low"}`,
		"edit doesn't fit": edit("text that is not in the file", "x"),
		"not json":         "sorry",
	} {
		m := &fakeModel{replies: []string{reply}}
		rep, err := Run(context.Background(), []FileTask{task("pay.js", 3, "charge")}, RunOptions{Root: root, Provider: "Stripe", Model: m})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rep.Discarded == "" || rep.Commit != "" {
			t.Errorf("%s: report = %+v", name, rep)
		}
		if cur := gitIn(t, root, "rev-parse", "--abbrev-ref", "HEAD"); cur != "main" {
			t.Errorf("%s: left on %q", name, cur)
		}
		if gitIn(t, root, "branch", "--list", "unbroke/*") != "" {
			t.Errorf("%s: branch left behind", name)
		}
	}
}

func TestRun_RefusesADirtyTree(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS})
	os.WriteFile(filepath.Join(root, "pay.js"), []byte(payJS+"// local work\n"), 0o644)
	m := &fakeModel{replies: []string{edit(goodEdit, newEdit)}}
	_, err := Run(context.Background(), []FileTask{task("pay.js", 3, "charge")}, RunOptions{Root: root, Provider: "Stripe", Model: m})
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("err = %v, want a refusal because of uncommitted changes", err)
	}
	if m.calls != 0 {
		t.Error("the model must not be called when the run is refused")
	}
}

func TestRun_AFailureOnOneFileDoesNotStopTheOthers(t *testing.T) {
	root := newRepo(t, map[string]string{"a.js": payJS, "b.js": payJS})
	m := &fakeModel{errs: []error{errors.New("model unavailable"), nil}, replies: []string{"", edit(goodEdit, newEdit)}}
	rep, err := Run(context.Background(), []FileTask{task("a.js", 3, "charge"), task("b.js", 3, "charge")}, RunOptions{Root: root, Provider: "Stripe", Model: m})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Files) != 2 || rep.Files[0].Err == nil || !rep.Files[1].Changed() {
		t.Fatalf("files = %+v", rep.Files)
	}
	if got := rep.Changed(); len(got) != 1 || got[0] != "b.js" {
		t.Errorf("changed = %v", got)
	}
}

func TestRun_DryRunChangesNothingAndNeverCallsTheModel(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS})
	m := &fakeModel{}
	rep, err := Run(context.Background(), []FileTask{task("pay.js", 3, "charge")}, RunOptions{Root: root, Provider: "Stripe", Model: m, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if m.calls != 0 || !strings.Contains(rep.Prompts["pay.js"], "stripe.refunds.create") {
		t.Errorf("calls = %d, prompts = %v", m.calls, rep.Prompts)
	}
	if gitIn(t, root, "branch", "--list") != "* main" {
		t.Error("a dry run must not create branches")
	}
}

func TestPRBody(t *testing.T) {
	tk := task("pay.js", 3, "charge")
	rep := &Report{
		TestCmd: "npm test", TestsRan: true, TestsPass: true,
		Files: []FileResult{
			{File: "pay.js", Applied: []Edit{{"a", "b"}}, Explanation: "Use payment_intent.", Confidence: "medium"},
			{File: "other.js", Explanation: "Nothing replaces the field.", Confidence: "low"},
		},
	}
	body := PRBody("Stripe", "2026-04-28", rep, []FileTask{tk})
	for _, want := range []string{
		"removed the request property `charge`", "`POST /v1/refunds`", "`pay.js` (medium confidence)",
		"Left unchanged", "`other.js`", "no edit was made. The model said: Nothing replaces the field.", "Ran `npm test`: passed", "changelog", "below high confidence",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("PR body is missing %q:\n%s", want, body)
		}
	}
	rep.TestsRan = false
	if body := PRBody("Stripe", "", rep, []FileTask{tk}); !strings.Contains(body, "not run") {
		t.Error("PR body must say when tests were not run")
	}
}

func TestParseRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/shop.git":      "acme/shop",
		"https://github.com/acme/shop":          "acme/shop",
		"git@github.com:acme/shop.git":          "acme/shop",
		"https://user@github.com/acme/shop.git": "acme/shop",
		"ssh://git@github.com/acme/shop.git":    "acme/shop",
	} {
		o, r, err := ParseRemote(in)
		if err != nil || o+"/"+r != want {
			t.Errorf("ParseRemote(%q) = %s/%s, %v; want %s", in, o, r, err, want)
		}
	}
	if _, _, err := ParseRemote("https://gitlab.com/a/b.git"); err == nil {
		t.Error("a non-GitHub remote should be rejected")
	}
}

func TestGitHub_OpenPR(t *testing.T) {
	var got map[string]any
	var auth string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/acme/shop":
			w.Write([]byte(`{"default_branch":"trunk"}`))
		case r.Method == "POST" && r.URL.Path == "/repos/acme/shop/pulls":
			json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(201)
			w.Write([]byte(`{"html_url":"https://github.com/acme/shop/pull/7"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	g := &GitHub{Token: "tok", BaseURL: s.URL, HTTP: s.Client()}
	base, err := g.DefaultBranch(context.Background(), "acme", "shop")
	if err != nil || base != "trunk" {
		t.Fatalf("DefaultBranch = %q, %v", base, err)
	}
	url, err := g.OpenPR(context.Background(), "acme", "shop", "unbroke/x", base, "Title", "Body")
	if err != nil || url != "https://github.com/acme/shop/pull/7" {
		t.Fatalf("OpenPR = %q, %v", url, err)
	}
	if auth != "Bearer tok" || got["head"] != "unbroke/x" || got["base"] != "trunk" || got["title"] != "Title" {
		t.Errorf("auth = %q, body = %v", auth, got)
	}
}

func TestGitHub_ErrorsCarryTheReason(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		w.Write([]byte(`{"message":"A pull request already exists"}`))
	}))
	defer s.Close()
	g := &GitHub{Token: "t", BaseURL: s.URL, HTTP: s.Client()}
	if _, err := g.OpenPR(context.Background(), "a", "b", "h", "m", "t", "b"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v", err)
	}
}

var _ = time.Second

func TestPlan_AttachesAdditionsToTheSameEndpointsAsReplacementCandidates(t *testing.T) {
	removed := affect.Finding{Relevance: affect.ChangedField, Change: change.Change{
		RuleID: "request-property-removed", Severity: change.Warning,
		Summary: "removed the request property `payment_method_types`", Operations: []string{"POST /v1/payment_intents"}},
		Hits: []affect.Hit{{Match: scan.Match{File: "a.js", Line: 2}, Field: "payment_method_types"}}}
	extra := []change.Change{
		{RuleID: "new-optional-request-property", Summary: "added the new optional request property `allowed_payment_method_types`", Operations: []string{"POST /v1/payment_intents"}},
		{RuleID: "new-optional-request-property", Summary: "added the new optional request property `statement_descriptor_suffix`", Operations: []string{"POST /v1/payment_intents"}},
		{RuleID: "new-optional-request-property", Summary: "added the new optional request property `unrelated`", Operations: []string{"POST /v1/charges"}},
		{RuleID: "response-property-enum-value-added", Summary: "added the enum value `x`", Operations: []string{"POST /v1/payment_intents"}},
		{RuleID: "response-optional-property-added", Summary: "added the optional property `shiny` to the response", Operations: []string{"POST /v1/payment_intents"}},
		{RuleID: "request-parameter-enum-value-added", Summary: "added the enum value `z` to a request parameter", Operations: []string{"POST /v1/payment_intents"}},
		{RuleID: "api-deprecated", Summary: "deprecated", Operations: []string{"POST /v1/payment_intents"}},
	}
	tasks := Plan([]affect.Finding{removed}, extra, 0)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d", len(tasks))
	}
	rel := tasks[0].Related
	if len(rel) < 2 || !strings.Contains(rel[0].Summary, "allowed_payment_method_types") {
		t.Fatalf("related = %+v; the addition sharing words with the removed field should come first", rel)
	}
	for _, r := range rel {
		if strings.Contains(r.Summary, "unrelated") {
			t.Error("an addition to a different endpoint was included")
		}
		if r.RuleID == "api-deprecated" {
			t.Error("a non-addition was included")
		}
		if strings.Contains(r.Summary, "shiny") {
			t.Error("a response-side addition was offered as the replacement for a removed request field")
		}
		if strings.Contains(r.RuleID, "enum-value") {
			t.Errorf("enum additions never name a replacement, but %s was included", r.RuleID)
		}
	}

	root := newRepo(t, map[string]string{"a.js": payJS})
	prompt, err := BuildPrompt(root, tasks[0], "Stripe")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "replacement may be among these") || !strings.Contains(prompt, "allowed_payment_method_types") {
		t.Errorf("prompt does not offer the candidates:\n%s", prompt)
	}
}

func TestRun_AllowDirtyNeverCommitsWhatTheUserStaged(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS, "notes.txt": "orig\n"})
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("STAGED WORK\n"), 0o644)
	gitIn(t, root, "add", "notes.txt")

	m := &fakeModel{replies: []string{edit(goodEdit, newEdit)}}
	rep, err := Run(context.Background(), []FileTask{task("pay.js", 3, "charge")}, RunOptions{Root: root, Provider: "Stripe", Model: m, AllowDirty: true})
	if err != nil || rep.Commit == "" {
		t.Fatalf("rep = %+v, err = %v", rep, err)
	}
	if committed := gitIn(t, root, "show", "--name-only", "--format=", rep.Branch); committed != "pay.js" {
		t.Errorf("the commit contains %q; the user's staged notes.txt must not be in it", committed)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "notes.txt")); string(got) != "STAGED WORK\n" {
		t.Errorf("notes.txt = %q; the user's staged work is gone", got)
	}
	if st := gitIn(t, root, "status", "--porcelain"); st != "M  notes.txt" {
		t.Errorf("status = %q; notes.txt should still be staged", st)
	}
}

func TestRun_ASubdirectoryRootStillSeesDirtyFiles(t *testing.T) {
	repo := newRepo(t, map[string]string{"app/pay.js": payJS, "app/other.js": payJS, "snap.txt": "old\n"})
	root := filepath.Join(repo, "app")
	os.WriteFile(filepath.Join(root, "pay.js"), []byte(payJS+"// my wip\n"), 0o644)

	tasks := []FileTask{task("pay.js", 3, "charge"), task("other.js", 3, "charge")}
	m := &fakeModel{replies: []string{edit(goodEdit, newEdit)}}
	// The test command runs in app/ and rewrites a file at the top of the repo.
	rep, err := Run(context.Background(), tasks, RunOptions{Root: root, Provider: "Stripe", Model: m, AllowDirty: true, TestCmd: "echo regenerated > ../snap.txt"})
	if err != nil || rep.Commit == "" {
		t.Fatalf("rep = %+v, err = %v", rep, err)
	}
	if rep.Files[0].Err == nil || !strings.Contains(rep.Files[0].Err.Error(), "uncommitted") {
		t.Errorf("app/pay.js has uncommitted changes and must be left alone: %+v", rep.Files[0])
	}
	if got, _ := os.ReadFile(filepath.Join(root, "pay.js")); string(got) != payJS+"// my wip\n" {
		t.Errorf("pay.js = %q; the user's work was changed", got)
	}
	if committed := gitIn(t, repo, "show", "--name-only", "--format=", rep.Branch); committed != "app/other.js" {
		t.Errorf("commit contains %q", committed)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "snap.txt")); string(got) != "old\n" {
		t.Errorf("snap.txt = %q; files the tests modified should be put back", got)
	}
	if len(rep.TestTouched) != 1 || rep.TestTouched[0] != "snap.txt" {
		t.Errorf("TestTouched = %v", rep.TestTouched)
	}
}

func TestRunShell_ATimeoutAlsoStopsTheCommandsChildren(t *testing.T) {
	start := time.Now()
	// The background sleep inherits the output pipe; killing only the shell
	// would leave it holding the pipe open for 30 seconds.
	_, err := runShell(context.Background(), t.TempDir(), "sleep 30 & sleep 30", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("runShell took %v to give up; the test command's children were left running", d)
	}
}

func TestRun_WhenEveryFileFailsTheReportSaysWhy(t *testing.T) {
	root := newRepo(t, map[string]string{"pay.js": payJS})
	m := &fakeModel{errs: []error{errors.New("model returned 401 Unauthorized: invalid x-api-key")}, replies: []string{""}}
	rep, err := Run(context.Background(), []FileTask{task("pay.js", 3, "charge")}, RunOptions{Root: root, Provider: "Stripe", Model: m})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Discarded, "401") {
		t.Errorf("Discarded = %q; it should carry the model's error, not blame the model's edits", rep.Discarded)
	}
}
