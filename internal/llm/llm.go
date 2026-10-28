// Package llm is the small slice of the Anthropic Messages API that Mend's fix
// agent needs.
//
// It reads its own settings, MEND_ANTHROPIC_API_KEY and optionally
// MEND_ANTHROPIC_BASE_URL and MEND_MODEL, and deliberately ignores the generic
// ANTHROPIC_* variables. Those are often set by an employer's tooling and point
// at a corporate gateway; code from a personal or customer repository must
// never be sent through credentials its owner did not choose.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Completer turns a system prompt and a user prompt into text.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// ErrNoKey means no API key was configured for Mend.
var ErrNoKey = errors.New("MEND_ANTHROPIC_API_KEY is not set; the fix agent needs an Anthropic API key of your own (Mend ignores ANTHROPIC_API_KEY on purpose, so it never sends your code through credentials someone else issued)")

// DefaultModel is used when MEND_MODEL is not set.
const DefaultModel = "claude-sonnet-5-5"

// Anthropic calls the Messages API.
type Anthropic struct {
	Key       string
	BaseURL   string
	Model     string
	MaxTokens int
	HTTP      *http.Client
}

// FromEnv builds a client from MEND_* variables only.
func FromEnv() (*Anthropic, error) {
	key := strings.TrimSpace(os.Getenv("MEND_ANTHROPIC_API_KEY"))
	if key == "" {
		return nil, ErrNoKey
	}
	base := strings.TrimRight(os.Getenv("MEND_ANTHROPIC_BASE_URL"), "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	model := os.Getenv("MEND_MODEL")
	if model == "" {
		model = DefaultModel
	}
	return &Anthropic{Key: key, BaseURL: base, Model: model, MaxTokens: 4096, HTTP: &http.Client{Timeout: 3 * time.Minute}}, nil
}

// Complete sends one request and returns the text of the reply.
func (a *Anthropic) Complete(ctx context.Context, system, user string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":      a.Model,
		"max_tokens": a.MaxTokens,
		"system":     system,
		"messages":   []map[string]any{{"role": "user", "content": user}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", a.Key)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("call model: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("read model response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		return "", fmt.Errorf("model returned %s: %s", resp.Status, msg)
	}

	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("read model response: %w", err)
	}
	var text strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	if text.Len() == 0 {
		return "", errors.New("model returned no text")
	}
	return text.String(), nil
}
