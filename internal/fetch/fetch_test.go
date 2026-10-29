package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func server(t *testing.T, status int, body string, hits *int32) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		if !strings.Contains(r.Header.Get("User-Agent"), "unbroke") {
			t.Errorf("User-Agent = %q, want it to identify unbroke", r.Header.Get("User-Agent"))
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestGet_CachesOnDisk(t *testing.T) {
	var hits int32
	s := server(t, 200, `{"openapi":"3.0.0"}`, &hits)
	f := New(t.TempDir(), time.Hour)
	for i := 0; i < 3; i++ {
		b, err := f.Get(context.Background(), s.URL)
		if err != nil || string(b) != `{"openapi":"3.0.0"}` {
			t.Fatalf("Get = %q, %v", b, err)
		}
	}
	if hits != 1 {
		t.Errorf("server was hit %d times, want 1 (the rest should come from the cache)", hits)
	}
}

func TestGet_ExpiredCacheIsRefetched(t *testing.T) {
	var hits int32
	s := server(t, 200, "x", &hits)
	f := New(t.TempDir(), time.Nanosecond)
	f.Get(context.Background(), s.URL)
	time.Sleep(5 * time.Millisecond)
	f.Get(context.Background(), s.URL)
	if hits != 2 {
		t.Errorf("hits = %d, want 2", hits)
	}
}

func TestGet_ErrorsAreNotCached(t *testing.T) {
	var hits int32
	s := server(t, 404, "nope", &hits)
	f := New(t.TempDir(), time.Hour)
	if _, err := f.Get(context.Background(), s.URL); err == nil {
		t.Fatal("expected an error for a 404")
	}
	f.Get(context.Background(), s.URL)
	if hits != 2 {
		t.Errorf("a failed download was cached; hits = %d", hits)
	}
}

func TestGet_RejectsEmptyAndOversizedBodies(t *testing.T) {
	var hits int32
	empty := server(t, 200, "", &hits)
	if _, err := New("", 0).Get(context.Background(), empty.URL); err == nil {
		t.Error("an empty body should be an error, since it would read as an API with no endpoints")
	}
	big := server(t, 200, strings.Repeat("a", 2048), &hits)
	f := New("", 0)
	f.MaxBytes = 1024
	if _, err := f.Get(context.Background(), big.URL); err == nil {
		t.Error("a body over the limit should be an error")
	}
}

func TestNotFound(t *testing.T) {
	var hits int32
	s404 := server(t, 404, "x", &hits)
	_, err := New("", 0).Get(context.Background(), s404.URL)
	if !NotFound(err) {
		t.Errorf("NotFound(%v) = false, want true for a 404", err)
	}
	s500 := server(t, 500, "x", &hits)
	_, err = New("", 0).Get(context.Background(), s500.URL)
	if err == nil || NotFound(err) {
		t.Errorf("a 500 must be an error that is not 'not found': %v", err)
	}
}

func TestForget(t *testing.T) {
	var hits int32
	s := server(t, 200, "body", &hits)
	f := New(t.TempDir(), time.Hour)
	f.Get(context.Background(), s.URL)
	f.Forget(s.URL)
	f.Get(context.Background(), s.URL)
	if hits != 2 {
		t.Errorf("hits = %d, want 2: a forgotten download must be fetched again", hits)
	}
	f.Forget("http://never-fetched.example")
}
