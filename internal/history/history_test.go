package history

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
		if got := r.URL.Query().Get("until"); !strings.HasPrefix(got, "2026-03-20T23:59:59") {
			t.Errorf("until = %q, want the end of 2026-03-20", got)
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

func TestCommitAt_AnswersAreCachedPerDay(t *testing.T) {
	c, hits := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(oneCommit)) })
	morning := time.Date(2026, 3, 20, 6, 0, 0, 0, time.UTC)
	evening := time.Date(2026, 3, 20, 22, 0, 0, 0, time.UTC)
	c.CommitAt(context.Background(), "o/r", morning)
	c.CommitAt(context.Background(), "o/r", evening)
	if *hits != 1 {
		t.Errorf("hits = %d, want 1: two times on the same day should share a lookup", *hits)
	}
	c.CommitAt(context.Background(), "o/r", evening.Add(48*time.Hour))
	if *hits != 2 {
		t.Errorf("hits = %d, want 2 after asking about another day", *hits)
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
