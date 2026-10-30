// Package ui serves Unbroke's web interface on the local machine.
//
// It is the same scanner and fix agent the command line uses, behind a small
// HTTP API; nothing in the interface is mocked. Because this server can read
// files and, on request, change a repository, it is locked down: it binds to
// the loopback interface only, rejects requests whose Host header is not
// loopback (which stops DNS-rebinding), and requires a random token that only
// the page it serves knows, so another website cannot drive it.
package ui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yug/unbroke/internal/affect"
	"github.com/yug/unbroke/internal/catalog"
	"github.com/yug/unbroke/internal/change"
	"github.com/yug/unbroke/internal/fix"
	"github.com/yug/unbroke/internal/llm"
	"github.com/yug/unbroke/internal/track"
	"github.com/yug/unbroke/internal/workflow"
)

//go:embed web/index.html
var indexHTML string

// Server is the web interface.
type Server struct {
	Version     string
	DefaultRoot string
	Catalog     *catalog.Catalog

	token string
	hosts map[string]bool

	mu    sync.Mutex
	roots map[string]bool             // directories the user has pointed at
	busy  map[string]bool             // repositories with a fix run in progress
	cache map[string]*workflow.Result // last scan per root and provider

	history *historyStore // nil until EnableHistory: nothing is saved then

	// These default to the real implementations; tests replace them.
	deps      func() (track.Deps, error)
	scanFn    func(ctx context.Context, s *Server, req ScanRequest, emit func(Event)) error
	hasModel  func() bool
	newModel  func() (llm.Completer, error)
	gitOutput func(dir string, args ...string) (string, error)
}

// New builds a server.
func New(version, defaultRoot string, cat *catalog.Catalog) (*Server, error) {
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	return &Server{
		Version: version, DefaultRoot: defaultRoot, Catalog: cat,
		token: hex.EncodeToString(tok),
		roots: map[string]bool{}, cache: map[string]*workflow.Result{}, busy: map[string]bool{},
		deps:   workflow.NewDeps,
		scanFn: realScan,
		hasModel: func() bool {
			return strings.TrimSpace(os.Getenv("UNBROKE_ANTHROPIC_API_KEY")) != ""
		},
		newModel: func() (llm.Completer, error) {
			m, err := llm.FromEnv()
			if err != nil {
				return nil, err
			}
			return m, nil
		},
		gitOutput: gitOut,
	}, nil
}

// Token is the secret the served page presents with every API call.
func (s *Server) Token() string { return s.token }

// Listen opens the listener. Only loopback addresses are accepted.
func (s *Server) Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("refusing to listen on %q: the interface can read files and change repositories, so it only binds to localhost", host)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	bound, port, _ := net.SplitHostPort(ln.Addr().String())
	s.hosts = map[string]bool{
		"127.0.0.1:" + port: true, "localhost:" + port: true, "[::1]:" + port: true,
		net.JoinHostPort(bound, port): true,
	}
	return ln, nil
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api/state", s.api(s.state))
	mux.HandleFunc("GET /api/catalog", s.api(s.catalogList))
	mux.HandleFunc("GET /api/snippet", s.api(s.snippet))
	mux.HandleFunc("POST /api/scan", s.api(s.scan))
	mux.HandleFunc("POST /api/fix/preview", s.api(s.fixPreview))
	mux.HandleFunc("POST /api/fix/run", s.api(s.fixRun))
	mux.HandleFunc("GET /api/history", s.api(s.historyList))
	mux.HandleFunc("GET /api/history/{id}", s.api(s.historyGet))
	mux.HandleFunc("DELETE /api/history/{id}", s.api(s.historyDelete))
	mux.HandleFunc("DELETE /api/history", s.api(s.historyClear))
	return s.hostGuard(mux)
}

// hostGuard rejects requests that were not addressed to this server by a
// loopback name. A page on another site that has been tricked into resolving
// to 127.0.0.1 still carries its own hostname, so it is refused here.
func (s *Server) hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.hosts) > 0 && !s.hosts[r.Host] {
			http.Error(w, "unexpected Host header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// api wraps a handler with the token check and request size limit.
func (s *Server) api(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Unbroke-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "missing or wrong token", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		h(w, r)
	}
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	page := strings.NewReplacer(
		"{{TOKEN}}", s.token,
		"{{ROOT}}", htmlAttr(s.DefaultRoot),
		"{{VERSION}}", htmlAttr(s.Version),
	).Replace(indexHTML)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fmt.Fprint(w, page)
}

func htmlAttr(s string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// ---- state and catalog ----

type stateDTO struct {
	Version   string `json:"version"`
	Providers int    `json:"providers"`
	Specs     int    `json:"specs"`
	HasModel  bool   `json:"has_model"`
	Root      string `json:"root"`
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, stateDTO{
		Version: s.Version, Providers: len(s.Catalog.Providers), Specs: s.Catalog.SpecCount(),
		HasModel: s.hasModel(), Root: s.DefaultRoot,
	})
}

type providerInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Company  string `json:"company"`
	Category string `json:"category"`
	Specs    int    `json:"specs"`
	History  string `json:"history"`
	Files    int    `json:"files,omitempty"`
}

func infoOf(p catalog.Provider, files int) providerInfo {
	h := "snapshot"
	if p.Specs[0].HasHistory() {
		h = "git"
	}
	return providerInfo{p.ID, p.Name, p.Company, p.Category, len(p.Specs), h, files}
}

func (s *Server) catalogList(w http.ResponseWriter, r *http.Request) {
	out := make([]providerInfo, 0, len(s.Catalog.Providers))
	for _, p := range s.Catalog.Providers {
		out = append(out, infoOf(p, 0))
	}
	writeJSON(w, out)
}

// ---- scan ----

// ScanRequest is the body of POST /api/scan.
type ScanRequest struct {
	Path     string `json:"path"`
	Since    string `json:"since"`
	Provider string `json:"provider"`
}

// Event is one line of a streamed response.
type Event struct {
	Type      string          `json:"type"` // stage, detected, result, done, error, fix
	Provider  string          `json:"provider,omitempty"`
	Text      string          `json:"text,omitempty"`
	Providers []providerInfo  `json:"providers,omitempty"`
	Result    *providerResult `json:"result,omitempty"`
	Fix       *fixDTO         `json:"fix,omitempty"`
}

type hitDTO struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Text       string `json:"text"`
	Kind       string `json:"kind"`
	Confidence string `json:"confidence"`
	Field      string `json:"field,omitempty"`
	Operation  string `json:"operation"`
}

type findingDTO struct {
	Severity   string   `json:"severity"`
	Summary    string   `json:"summary"`
	RuleID     string   `json:"rule_id"`
	Relevance  string   `json:"relevance"`
	Operations []string `json:"operations"`
	Sunset     string   `json:"sunset,omitempty"`
	Hits       []hitDTO `json:"hits"`
}

type providerResult struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Since     string       `json:"since"`
	Total     int          `json:"total"`
	Compared  int          `json:"compared"`
	Affecting int          `json:"affecting"`
	Touching  int          `json:"touching"`
	CallSites int          `json:"call_sites"`
	Files     int          `json:"files"`
	Notes     []string     `json:"notes"`
	Findings  []findingDTO `json:"findings"`
}

func resultDTO(r workflow.Result) *providerResult {
	out := &providerResult{
		ID: r.Provider.ID, Name: r.Provider.Name, Since: r.Since, Total: r.Total,
		Compared: r.Compared, Files: r.FilesUsed, Notes: append([]string{}, r.Notes...), Findings: []findingDTO{},
	}
	for _, f := range r.Findings {
		d := findingDTO{
			Severity: string(f.Change.Severity), Summary: f.Change.Summary, RuleID: f.Change.RuleID,
			Relevance: string(f.Relevance), Operations: f.Change.Operations, Hits: []hitDTO{},
		}
		if len(d.Operations) == 0 && f.Change.Operation() != "" {
			d.Operations = []string{f.Change.Operation()}
		}
		if f.Change.Sunset != nil {
			d.Sunset = f.Change.Sunset.Format("2006-01-02")
		}
		for _, h := range f.Hits {
			d.Hits = append(d.Hits, hitDTO{h.File, h.Line, h.Text, string(h.Kind), string(h.Confidence), h.Field, h.Operation})
		}
		if f.Relevance == affect.CallsEndpoint {
			out.Touching++
		} else {
			out.Affecting++
			out.CallSites += len(f.Hits)
		}
		out.Findings = append(out.Findings, d)
	}
	return out
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	var req ScanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	stream(w, r, func(ctx context.Context, emit func(Event)) {
		var results []*providerResult
		seeing := func(ev Event) {
			if ev.Type == "result" && ev.Result != nil {
				results = append(results, ev.Result)
			}
			emit(ev)
		}
		if err := s.scanFn(ctx, s, req, seeing); err != nil {
			emit(Event{Type: "error", Text: err.Error()})
			return
		}
		// Only a scan that finished is kept; one cut short would show half a picture.
		if ctx.Err() == nil {
			if root, err := expandDir(req.Path); err == nil {
				s.recordScan(root, req.Since, results)
			}
		}
		emit(Event{Type: "done"})
	})
}

func realScan(ctx context.Context, s *Server, req ScanRequest, emit func(Event)) error {
	root, err := s.resolveRoot(req.Path)
	if err != nil {
		return err
	}
	since := req.Since
	if since == "" {
		since = "6m"
	}
	dur, err := workflow.ParseSince(since)
	if err != nil {
		return err
	}
	emit(Event{Type: "stage", Text: "Looking for the APIs this code uses"})
	dets, err := workflow.Choose(s.Catalog, root, req.Provider)
	if err != nil {
		return err
	}
	infos := make([]providerInfo, 0, len(dets))
	for _, d := range dets {
		infos = append(infos, infoOf(d.P, d.Files))
	}
	emit(Event{Type: "detected", Providers: infos})
	if len(dets) == 0 {
		return nil
	}
	deps, err := s.deps()
	if err != nil {
		return err
	}
	for _, d := range dets {
		if err := ctx.Err(); err != nil {
			return err // the page went away or the scan ran out of time
		}
		id := d.P.ID
		res, err := workflow.ScanProvider(ctx, deps, d.P, root, dur, change.Warning, func(m string) {
			emit(Event{Type: "stage", Provider: id, Text: m})
		})
		res.FilesUsed = d.Files
		if err != nil {
			res.Notes = append(res.Notes, err.Error())
		}
		s.mu.Lock()
		cp := res
		s.cache[cacheKey(root, id, since)] = &cp
		s.mu.Unlock()
		emit(Event{Type: "result", Provider: id, Result: resultDTO(res)})
	}
	return nil
}

func cacheKey(root, provider, since string) string { return root + "\x00" + provider + "\x00" + since }

// resolveRoot turns what the user typed into a real directory and remembers
// that they chose it, which is what later lets files under it be read.
func (s *Server) resolveRoot(p string) (string, error) {
	real, err := expandDir(p)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.roots[real] = true
	s.mu.Unlock()
	return real, nil
}

// expandDir turns what a person typed into a real directory path, resolving
// "~" and symlinks.
func expandDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("enter the path to a codebase")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%s does not exist", p)
	}
	if st, err := os.Stat(real); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", p)
	}
	return real, nil
}

// ---- code snippets ----

type snippetLine struct {
	N    int    `json:"n"`
	Text string `json:"text"`
}

type snippetDTO struct {
	File  string        `json:"file"`
	Line  int           `json:"line"`
	Lines []snippetLine `json:"lines"`
}

func (s *Server) snippet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	root, err := s.knownRoot(q.Get("root"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	file := q.Get("file")
	line, err := strconv.Atoi(q.Get("line"))
	if err != nil || line < 1 {
		http.Error(w, "bad line", http.StatusBadRequest)
		return
	}
	lines, err := readLines(root, file, line, 7)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, snippetDTO{File: file, Line: line, Lines: lines})
}

// knownRoot returns root only if the user has scanned it in this session.
func (s *Server) knownRoot(root string) (string, error) {
	real, err := expandDir(root)
	if err != nil {
		return "", errors.New("unknown directory")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.roots[real] {
		return "", errors.New("this directory has not been scanned in this session")
	}
	return real, nil
}

// maxSnippetFile is the largest file a snippet is read from.
const maxSnippetFile = 8 << 20

// readLines returns the lines around line from a file inside root, refusing any
// path that escapes it, including through symlinks.
func readLines(root, rel string, line, radius int) ([]snippetLine, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return nil, errors.New("bad file")
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return nil, errors.New("file not found")
	}
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return nil, errors.New("file is outside the scanned directory")
	}
	if st, err := os.Stat(real); err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("file not found")
	} else if st.Size() > maxSnippetFile {
		// The scanner never reads files this big, so no finding points into one.
		return nil, errors.New("file is too large to show")
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return nil, errors.New("file not found")
	}
	all := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	lo, hi := line-radius, line+radius
	if lo < 1 {
		lo = 1
	}
	if hi > len(all) {
		hi = len(all)
	}
	// Never null: a line past the end (the file changed since the scan) gives
	// an empty list.
	out := []snippetLine{}
	for n := lo; n <= hi; n++ {
		t := all[n-1]
		if len(t) > 400 {
			cut := 400
			for cut > 0 && !utf8.RuneStart(t[cut]) {
				cut--
			}
			t = t[:cut] + "…"
		}
		out = append(out, snippetLine{N: n, Text: strings.TrimRight(t, "\r")})
	}
	return out, nil
}

// ---- fix ----

// FixRequest is the body of the fix endpoints.
type FixRequest struct {
	Path     string `json:"path"`
	Since    string `json:"since"`
	Provider string `json:"provider"`
	TestCmd  string `json:"test_cmd"`
	NoTests  bool   `json:"no_tests"`
}

type previewFile struct {
	File      string `json:"file"`
	CallSites int    `json:"call_sites"`
	Bytes     int    `json:"bytes"`
	Prompt    string `json:"prompt"`
}

type previewDTO struct {
	Provider string        `json:"provider"`
	Files    []previewFile `json:"files"`
	Bytes    int           `json:"bytes"`
}

type fileOutcome struct {
	File        string   `json:"file"`
	Changed     bool     `json:"changed"`
	Confidence  string   `json:"confidence,omitempty"`
	Explanation string   `json:"explanation,omitempty"`
	Rejected    []string `json:"rejected,omitempty"`
	Error       string   `json:"error,omitempty"`
}

type fixDTO struct {
	Provider  string        `json:"provider"`
	Base      string        `json:"base"`
	Branch    string        `json:"branch"`
	Commit    string        `json:"commit"`
	Discarded string        `json:"discarded"`
	TestsRan  bool          `json:"tests_ran"`
	TestsPass bool          `json:"tests_pass"`
	TestCmd   string        `json:"test_cmd"`
	TestTail  string        `json:"test_output"`
	Touched   []string      `json:"test_touched,omitempty"`
	Files     []fileOutcome `json:"files"`
	Diff      string        `json:"diff"`
	PRBody    string        `json:"pr_body"`
}

// plan returns the files to change for a provider, scanning first if this
// session has not already.
func (s *Server) plan(ctx context.Context, req FixRequest) (string, *workflow.Result, []fix.FileTask, error) {
	// Only a directory the person scanned in this session, so a fix can never
	// land somewhere other than the code they were looking at.
	root, err := s.knownRoot(req.Path)
	if err != nil {
		return "", nil, nil, errors.New("scan this directory first; Unbroke only proposes fixes for code you have scanned in this session")
	}
	if req.Provider == "" {
		return "", nil, nil, errors.New("choose which API to fix for")
	}
	since := req.Since
	if since == "" {
		since = "6m"
	}
	p, ok := s.Catalog.Find(req.Provider)
	if !ok {
		return "", nil, nil, fmt.Errorf("no provider %q", req.Provider)
	}
	s.mu.Lock()
	res := s.cache[cacheKey(root, p.ID, since)]
	s.mu.Unlock()
	if res == nil {
		dur, err := workflow.ParseSince(since)
		if err != nil {
			return "", nil, nil, err
		}
		deps, err := s.deps()
		if err != nil {
			return "", nil, nil, err
		}
		r, err := workflow.ScanProvider(ctx, deps, *p, root, dur, change.Warning, nil)
		if err != nil {
			return "", nil, nil, err
		}
		res = &r
	}
	tasks := fix.Plan(res.Findings, res.Extra, 5)
	if len(tasks) == 0 {
		return "", nil, nil, errors.New("nothing to fix: none of this API's changes affect code in this directory")
	}
	return root, res, tasks, nil
}

func (s *Server) fixPreview(w http.ResponseWriter, r *http.Request) {
	var req FixRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	root, res, tasks, err := s.plan(ctx, req)
	if err != nil {
		jsonError(w, err, http.StatusUnprocessableEntity)
		return
	}
	rep, err := fix.Run(ctx, tasks, fix.RunOptions{Root: root, Provider: res.Provider.Name, DryRun: true})
	if err != nil {
		jsonError(w, err, http.StatusInternalServerError)
		return
	}
	out := previewDTO{Provider: res.Provider.Name}
	for _, t := range tasks {
		p := rep.Prompts[t.File]
		out.Files = append(out.Files, previewFile{File: t.File, CallSites: len(t.Hits), Bytes: len(p), Prompt: p})
		out.Bytes += len(p)
	}
	writeJSON(w, out)
}

func (s *Server) fixRun(w http.ResponseWriter, r *http.Request) {
	var req FixRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	streamFor(w, r, 30*time.Minute, func(ctx context.Context, emit func(Event)) {
		if strings.TrimSpace(req.TestCmd) == "" && !req.NoTests {
			emit(Event{Type: "error", Text: "Say how to check the change: enter your test command, or choose to skip tests."})
			return
		}
		model, err := s.newModel()
		if err != nil {
			emit(Event{Type: "error", Text: err.Error()})
			return
		}
		root, res, tasks, err := s.plan(ctx, req)
		if err != nil {
			emit(Event{Type: "error", Text: err.Error()})
			return
		}
		if !s.claim(root) {
			emit(Event{Type: "error", Text: "A fix is already running for this repository. Wait for it to finish."})
			return
		}
		defer s.release(root)
		// A dirty tree or a detached HEAD is reported before anything is sent.
		if err := fix.Preflight(ctx, root, false); err != nil {
			emit(Event{Type: "error", Text: err.Error()})
			return
		}
		emit(Event{Type: "stage", Text: fmt.Sprintf("Asking the model about %d file(s)", len(tasks))})
		rep, err := fix.Run(ctx, tasks, fix.RunOptions{
			Root: root, Provider: res.Provider.Name, Model: model, TestCmd: strings.TrimSpace(req.TestCmd),
			Progress: func(m string) { emit(Event{Type: "stage", Text: m}) },
		})
		if err != nil {
			emit(Event{Type: "error", Text: err.Error()})
			return
		}
		d := &fixDTO{
			Provider: res.Provider.Name, Base: rep.BaseBranch, Branch: rep.Branch, Commit: rep.Commit,
			Discarded: rep.Discarded, TestsRan: rep.TestsRan, TestsPass: rep.TestsPass,
			TestCmd: rep.TestCmd, TestTail: rep.TestOutput, Touched: rep.TestTouched, Files: []fileOutcome{},
		}
		for _, f := range rep.Files {
			o := fileOutcome{File: f.File, Changed: f.Changed(), Confidence: f.Confidence, Explanation: f.Explanation, Rejected: f.Rejected}
			if f.Err != nil {
				o.Error = f.Err.Error()
			}
			d.Files = append(d.Files, o)
		}
		if rep.Branch != "" {
			if diff, err := s.gitOutput(root, "diff", rep.BaseBranch+"..."+rep.Branch); err == nil {
				if len(diff) > 200_000 {
					diff = diff[:200_000] + "\n… diff truncated …"
				}
				d.Diff = diff
			}
			d.PRBody = fix.PRBody(res.Provider.Name, res.Since, rep, tasks)
		}
		s.recordFix(root, req.Since, d)
		emit(Event{Type: "fix", Fix: d})
		emit(Event{Type: "done"})
	})
}

// claim marks a repository as having a fix run in progress; two runs at once
// would switch branches and restore files underneath each other.
func (s *Server) claim(root string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[root] {
		return false
	}
	s.busy[root] = true
	return true
}

func (s *Server) release(root string) {
	s.mu.Lock()
	delete(s.busy, root)
	s.mu.Unlock()
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// ---- plumbing ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// stream runs fn and writes the events it emits as they happen, one JSON
// object per line, so the page can show real progress.
func stream(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, emit func(Event))) {
	streamFor(w, r, 10*time.Minute, fn)
}

// streamFor is stream with a chosen deadline. A fix run can make five model
// calls and then run a slow test suite, so it gets longer than a scan.
func streamFor(w http.ResponseWriter, r *http.Request, limit time.Duration, fn func(ctx context.Context, emit func(Event))) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no")
	var mu sync.Mutex
	enc := json.NewEncoder(w)
	emit := func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(e)
		fl.Flush()
	}
	ctx, cancel := context.WithTimeout(r.Context(), limit)
	defer cancel()
	fn(ctx, emit)
}
