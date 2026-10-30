package ui

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"crypto/rand"
)

// maxHistory is how many past scans and fix runs are kept. Older ones are
// deleted as new ones arrive.
const maxHistory = 50

var historyID = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}-[0-9a-f]{8}$`)

// HistoryEntry is one line of the history: a scan or a fix run that finished.
type HistoryEntry struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"` // scan or fix
	Time      time.Time         `json:"time"`
	Path      string            `json:"path"`
	Since     string            `json:"since,omitempty"`
	Providers []historyProvider `json:"providers,omitempty"`
	Fix       *historyFix       `json:"fix,omitempty"`
}

type historyProvider struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Total     int    `json:"total"`
	Compared  int    `json:"compared"`
	Affecting int    `json:"affecting"`
	CallSites int    `json:"call_sites"`
}

type historyFix struct {
	Provider  string `json:"provider"`
	Base      string `json:"base,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Commit    string `json:"commit,omitempty"`
	Discarded string `json:"discarded,omitempty"`
	Changed   int    `json:"changed"`
	TestsRan  bool   `json:"tests_ran"`
	TestsPass bool   `json:"tests_pass"`
}

// historyStore keeps the history in a directory: index.json lists the entries
// newest first, and each entry's full result is its own file, so listing stays
// quick however large a scan was. Files are readable only by the user, because
// a stored scan contains lines of their source code.
type historyStore struct {
	dir string
	mu  sync.Mutex
}

func newHistoryStore(dir string) (*historyStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &historyStore{dir: dir}, nil
}

// EnableHistory turns on recording into dir. Without it nothing is saved.
func (s *Server) EnableHistory(dir string) error {
	h, err := newHistoryStore(dir)
	if err != nil {
		return err
	}
	s.history = h
	return nil
}

func (h *historyStore) readIndex() []HistoryEntry {
	b, err := os.ReadFile(filepath.Join(h.dir, "index.json"))
	if err != nil {
		return nil
	}
	var entries []HistoryEntry
	if json.Unmarshal(b, &entries) != nil {
		return nil // a damaged index starts the list over rather than failing
	}
	return entries
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

func (h *historyStore) writeIndex(entries []HistoryEntry) error {
	b, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(h.dir, "index.json"), b)
}

func (h *historyStore) detailPath(id string) string { return filepath.Join(h.dir, id+".json") }

func newHistoryID(t time.Time) (string, error) {
	r := make([]byte, 4)
	if _, err := rand.Read(r); err != nil {
		return "", err
	}
	return t.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(r), nil
}

// add stores an entry and its full result, and drops the oldest beyond the cap.
func (h *historyStore) add(e HistoryEntry, detail any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	id, err := newHistoryID(e.Time)
	if err != nil {
		return err
	}
	e.ID = id
	b, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(h.detailPath(id), b); err != nil {
		return err
	}
	entries := append([]HistoryEntry{e}, h.readIndex()...)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Time.After(entries[j].Time) })
	for len(entries) > maxHistory {
		old := entries[len(entries)-1]
		entries = entries[:len(entries)-1]
		os.Remove(h.detailPath(old.ID))
	}
	return h.writeIndex(entries)
}

func (h *historyStore) list() []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readIndex()
}

var errNoHistory = errors.New("that history entry no longer exists")

func (h *historyStore) get(id string) (HistoryEntry, json.RawMessage, error) {
	if !historyID.MatchString(id) {
		return HistoryEntry{}, nil, errNoHistory
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.readIndex() {
		if e.ID != id {
			continue
		}
		b, err := os.ReadFile(h.detailPath(id))
		if err != nil {
			return HistoryEntry{}, nil, errNoHistory
		}
		return e, json.RawMessage(b), nil
	}
	return HistoryEntry{}, nil, errNoHistory
}

func (h *historyStore) remove(id string) error {
	if !historyID.MatchString(id) {
		return errNoHistory
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entries := h.readIndex()
	kept := entries[:0:0]
	found := false
	for _, e := range entries {
		if e.ID == id {
			found = true
			continue
		}
		kept = append(kept, e)
	}
	if !found {
		return errNoHistory
	}
	os.Remove(h.detailPath(id))
	return h.writeIndex(kept)
}

func (h *historyStore) clear() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.readIndex() {
		os.Remove(h.detailPath(e.ID))
	}
	return h.writeIndex(nil)
}

// ---- recording ----

// recordScan saves a finished scan. A failure to save never fails the scan.
func (s *Server) recordScan(path, since string, results []*providerResult) {
	if s.history == nil || len(results) == 0 {
		return
	}
	if since == "" {
		since = "6m"
	}
	e := HistoryEntry{Kind: "scan", Time: time.Now().UTC(), Path: path, Since: since}
	for _, r := range results {
		e.Providers = append(e.Providers, historyProvider{r.ID, r.Name, r.Total, r.Compared, r.Affecting, r.CallSites})
	}
	if err := s.history.add(e, map[string]any{"results": results}); err != nil {
		fmt.Fprintln(os.Stderr, "unbroke: could not save this scan to the history:", err)
	}
}

// recordFix saves a finished fix run, including one that kept nothing.
func (s *Server) recordFix(path, since string, d *fixDTO) {
	if s.history == nil || d == nil {
		return
	}
	f := &historyFix{
		Provider: d.Provider, Base: d.Base, Branch: d.Branch, Commit: d.Commit, Discarded: d.Discarded,
		TestsRan: d.TestsRan, TestsPass: d.TestsPass,
	}
	for _, o := range d.Files {
		if o.Changed {
			f.Changed++
		}
	}
	e := HistoryEntry{Kind: "fix", Time: time.Now().UTC(), Path: path, Since: since, Fix: f}
	if err := s.history.add(e, d); err != nil {
		fmt.Fprintln(os.Stderr, "unbroke: could not save this fix to the history:", err)
	}
}

// ---- API ----

type historyListDTO struct {
	Enabled bool           `json:"enabled"`
	Entries []HistoryEntry `json:"entries"`
}

func (s *Server) historyList(w http.ResponseWriter, r *http.Request) {
	out := historyListDTO{Entries: []HistoryEntry{}}
	if s.history != nil {
		out.Enabled = true
		if es := s.history.list(); es != nil {
			out.Entries = es
		}
	}
	writeJSON(w, out)
}

func (s *Server) historyGet(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		jsonError(w, errNoHistory, http.StatusNotFound)
		return
	}
	e, detail, err := s.history.get(r.PathValue("id"))
	if err != nil {
		jsonError(w, err, http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"entry": e, "detail": detail})
}

func (s *Server) historyDelete(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		jsonError(w, errNoHistory, http.StatusNotFound)
		return
	}
	if err := s.history.remove(r.PathValue("id")); err != nil {
		jsonError(w, err, http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) historyClear(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		jsonError(w, errNoHistory, http.StatusNotFound)
		return
	}
	if err := s.history.clear(); err != nil {
		jsonError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
