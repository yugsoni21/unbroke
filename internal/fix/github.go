package fix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// GitHub opens pull requests. It uses a token the user supplies in
// GITHUB_TOKEN and nothing else.
type GitHub struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

// NewGitHub returns a client for github.com.
func NewGitHub(token string) *GitHub {
	return &GitHub{Token: token, BaseURL: "https://api.github.com", HTTP: &http.Client{Timeout: time.Minute}}
}

var remoteRE = regexp.MustCompile(`github\.com[:/]+([^/\s]+)/([^/\s]+?)(?:\.git)?/?$`)

// ParseRemote extracts owner and repository from a GitHub remote URL.
func ParseRemote(url string) (owner, repo string, err error) {
	m := remoteRE.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", "", fmt.Errorf("%q is not a github.com remote", url)
	}
	return m[1], m[2], nil
}

// DefaultBranch returns the repository's default branch.
func (g *GitHub) DefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s", owner, repo), nil, &out); err != nil {
		return "", err
	}
	if out.DefaultBranch == "" {
		return "", errors.New("repository has no default branch")
	}
	return out.DefaultBranch, nil
}

// OpenPR opens a pull request and returns its URL.
func (g *GitHub) OpenPR(ctx context.Context, owner, repo, head, base, title, body string) (string, error) {
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", owner, repo),
		map[string]any{"title": title, "head": head, "base": base, "body": body}, &out)
	if err != nil {
		return "", err
	}
	return out.HTMLURL, nil
}

func (g *GitHub) do(ctx context.Context, method, path string, in, out any) error {
	var rdr io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("User-Agent", "unbroke/0.1")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		return fmt.Errorf("github %s %s: %s: %s", method, path, resp.Status, e.Message)
	}
	return json.Unmarshal(raw, out)
}
