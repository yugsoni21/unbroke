package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFromEnv_IgnoresTheGenericAnthropicVariables(t *testing.T) {
	// These are what an employer's tooling typically sets. Mend must not use them.
	t.Setenv("ANTHROPIC_API_KEY", "employer-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "employer-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.corp.example")
	t.Setenv("MEND_ANTHROPIC_API_KEY", "")
	if _, err := FromEnv(); !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v, want ErrNoKey: only MEND_ANTHROPIC_API_KEY may enable the fix agent", err)
	}
}

func TestFromEnv_UsesMendVariables(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.corp.example")
	t.Setenv("MEND_ANTHROPIC_API_KEY", "  my-key ")
	t.Setenv("MEND_ANTHROPIC_BASE_URL", "http://localhost:9/")
	t.Setenv("MEND_MODEL", "some-model")
	a, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if a.Key != "my-key" || a.BaseURL != "http://localhost:9" || a.Model != "some-model" {
		t.Errorf("got %+v", a)
	}
}

func TestFromEnv_Defaults(t *testing.T) {
	t.Setenv("MEND_ANTHROPIC_API_KEY", "k")
	t.Setenv("MEND_ANTHROPIC_BASE_URL", "")
	t.Setenv("MEND_MODEL", "")
	a, _ := FromEnv()
	if a.BaseURL != "https://api.anthropic.com" || a.Model != DefaultModel {
		t.Errorf("got %+v", a)
	}
}

func TestComplete(t *testing.T) {
	var gotKey, gotPath string
	var sent map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath = r.Header.Get("x-api-key"), r.URL.Path
		json.NewDecoder(r.Body).Decode(&sent)
		w.Write([]byte(`{"content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}]}`))
	}))
	defer s.Close()
	a := &Anthropic{Key: "k", BaseURL: s.URL, Model: "m", MaxTokens: 10, HTTP: s.Client()}
	out, err := a.Complete(context.Background(), "sys", "usr")
	if err != nil || out != "hello world" {
		t.Fatalf("got %q, %v", out, err)
	}
	if gotKey != "k" || gotPath != "/v1/messages" {
		t.Errorf("key = %q path = %q", gotKey, gotPath)
	}
	if sent["system"] != "sys" || sent["model"] != "m" {
		t.Errorf("request body = %v", sent)
	}
}

func TestComplete_Errors(t *testing.T) {
	status := 401
	body := `{"error":{"message":"invalid x-api-key"}}`
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer s.Close()
	a := &Anthropic{Key: "k", BaseURL: s.URL, HTTP: s.Client()}
	if _, err := a.Complete(context.Background(), "s", "u"); err == nil || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("err = %v, want the provider's message", err)
	}
	status, body = 200, `{"content":[]}`
	if _, err := a.Complete(context.Background(), "s", "u"); err == nil {
		t.Error("an empty reply should be an error")
	}
}
