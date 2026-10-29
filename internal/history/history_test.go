package history

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) (*Client, *int32) {
	t.Helper()
	var hits int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return &Client{HTTP: s.Client(), BaseURL: s.URL, CacheDir: t.TempDir()}, &hits
}

const oneCommit = `[{"sha":"abc123","commit":{"committer":{"date":"2026-03-17T10:00:00Z"}}}]`

func TestCommitAt_ParsesAndAsksForTheRightDate(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/stripe/openapi/commits" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("until"); got != "2026-03-20T15:00:00Z" {
			t.Errorf("until = %q, want 2026-03-20T15:00:00Z (rounded down, never after the time asked)", got)
		}
		if r.URL.Query().Get("per_page") != "1" {
			t.Error("should ask for a single commit")
		}
		w.Write([]byte(oneCommit))
	})
	cm, err := c.CommitAt(context.Background(), "stripe/openapi", time.Date(2026, 3, 20, 15, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if cm.SHA != "abc123" || !cm.Date.Equal(time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("commit = %+v", cm)
	}
}

func TestCommitAt_AnswersAreCachedPerHour(t *testing.T) {
	c, hits := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(oneCommit)) })
	early := time.Date(2026, 3, 20, 6, 5, 0, 0, time.UTC)
	later := time.Date(2026, 3, 20, 6, 55, 0, 0, time.UTC)
	c.CommitAt(context.Background(), "o/r", early)
	c.CommitAt(context.Background(), "o/r", later)
	if *hits != 1 {
		t.Errorf("hits = %d, want 1: two times in the same hour share a lookup", *hits)
	}
	c.CommitAt(context.Background(), "o/r", later.Add(2*time.Hour))
	if *hits != 2 {
		t.Errorf("hits = %d, want 2 after asking about another hour", *hits)
	}
}

func TestCommitAt_ReportsRateLimit(t *testing.T) {
	reset := time.Now().Add(20 * time.Minute).Unix()
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", itoa(reset))
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := c.CommitAt(context.Background(), "o/r", time.Now())
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want a RateLimitError", err)
	}
	if rl.Reset.Unix() != reset {
		t.Errorf("Reset = %v, want %v", rl.Reset.Unix(), reset)
	}
	if !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Errorf("message should tell the user how to raise the limit: %v", err)
	}
}

func TestCommitAt_NoCommitsBeforeDate(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	if _, err := c.CommitAt(context.Background(), "o/r", time.Now()); !errors.Is(err, ErrNoCommit) {
		t.Errorf("err = %v, want ErrNoCommit", err)
	}
}

func TestCommitAt_SendsTokenOnlyWhenSet(t *testing.T) {
	var auth string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(oneCommit))
	})
	c.CommitAt(context.Background(), "o/r", time.Now())
	if auth != "" {
		t.Errorf("Authorization = %q with no token set", auth)
	}
	c.Token = "tok"
	c.CacheDir = ""
	c.CommitAt(context.Background(), "o/r", time.Now())
	if auth != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", auth)
	}
}

func TestRawURL(t *testing.T) {
	if got, want := RawURL("stripe/openapi", "abc", "openapi/spec3.json"), "https://raw.githubusercontent.com/stripe/openapi/abc/openapi/spec3.json"; got != want {
		t.Errorf("RawURL = %q, want %q", got, want)
	}
}

func itoa(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{digits[n%10]}, b...)
		n /= 10
	}
	return string(b)
}

// gitRun runs git in dir with fixed dates, for building test histories.
func gitRun(t *testing.T, dir, date string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@e.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@e.com",
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCommitAtGit_FollowsTheDefaultBranchNotAMergedFeatureBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	src := t.TempDir()
	gitRun(t, src, "2026-01-01T00:00:00Z", "init", "-q", "-b", "main")
	gitRun(t, src, "2026-01-01T00:00:00Z", "config", "uploadpack.allowFilter", "true")
	write := func(body string) { os.WriteFile(filepath.Join(src, "spec.yaml"), []byte(body), 0o644) }

	write("v1")
	gitRun(t, src, "2026-01-01T00:00:00Z", "add", "-A")
	gitRun(t, src, "2026-01-01T00:00:00Z", "commit", "-q", "-m", "main v1")
	mainAtCutoff := gitRun(t, src, "2026-01-01T00:00:00Z", "rev-parse", "HEAD")

	// A feature branch commit dated before the cutoff, merged after it.
	gitRun(t, src, "2026-01-02T00:00:00Z", "checkout", "-q", "-b", "feature")
	write("unmerged feature work")
	gitRun(t, src, "2026-01-05T00:00:00Z", "commit", "-q", "-am", "feature")
	gitRun(t, src, "2026-01-05T00:00:00Z", "checkout", "-q", "main")
	gitRun(t, src, "2026-02-01T00:00:00Z", "merge", "-q", "--no-ff", "-m", "merge feature", "feature")

	cutoff := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	c := &Client{CacheDir: t.TempDir(), GitBase: "file://" + filepath.Dir(src)}
	repo := "x/" + filepath.Base(src)
	// The clone URL is <GitBase>/<repo>.git; make that path exist.
	os.MkdirAll(filepath.Join(filepath.Dir(src), "x"), 0o755)
	os.Symlink(src, filepath.Join(filepath.Dir(src), "x", filepath.Base(src)+".git"))

	cm, err := c.commitAtGit(context.Background(), repo, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if cm.SHA != mainAtCutoff {
		t.Errorf("got %s, want %s: on Jan 10 main still held v1; the feature commit was only merged on Feb 1", cm.SHA[:8], mainAtCutoff[:8])
	}

	// A second lookup reuses the clone and still answers correctly.
	cm2, err := c.commitAtGit(context.Background(), repo, time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if msg := gitRun(t, src, "2026-02-02T00:00:00Z", "log", "-1", "--format=%s", cm2.SHA); msg != "merge feature" {
		t.Errorf("after the merge, got %q, want the merge commit", msg)
	}
}

func TestCommitAtGit_RejectsOddRepositoryNames(t *testing.T) {
	c := &Client{CacheDir: t.TempDir(), GitBase: "https://github.com"}
	for _, bad := range []string{"../etc", "a/b/c", "a b/c", "-x/y", ""} {
		if _, err := c.commitAtGit(context.Background(), bad, time.Now()); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
