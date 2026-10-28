// Package history finds what a public spec repository looked like at an
// earlier date, so Unbroke can compare "now" with "six months ago" for any
// GitHub-hosted API without having been watching it back then.
//
// One lookup per repository is enough: the commit that was current on a given
// day fixes the content of every file in the repo on that day, so all of a
// provider's spec files can be fetched at that same commit.
package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Commit identifies a point in a repository's history.
type Commit struct {
	SHA  string    `json:"sha"`
	Date time.Time `json:"date"`
}

// RateLimitError means GitHub refused the lookup because the hourly quota for
// anonymous requests is used up. Setting GITHUB_TOKEN raises it.
type RateLimitError struct{ Reset time.Time }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("GitHub rate limit reached; resets at %s (set GITHUB_TOKEN to raise the limit)", e.Reset.Local().Format("15:04"))
}

// ErrNoCommit means the repository had no commits on or before the date.
var ErrNoCommit = errors.New("repository has no commit on or before that date")

// Client looks up historical commits.
type Client struct {
	HTTP *http.Client
	// Token is an optional GitHub token; it only raises the rate limit.
	Token string
	// BaseURL defaults to the GitHub API; tests point it elsewhere.
	BaseURL string
	// CacheDir remembers answers so reruns spend no quota. Empty disables it.
	CacheDir string
}

// New returns a client that reads GITHUB_TOKEN from the environment.
func New(cacheDir string) *Client {
	return &Client{
		HTTP:     &http.Client{Timeout: time.Minute},
		Token:    os.Getenv("GITHUB_TOKEN"),
		BaseURL:  "https://api.github.com",
		CacheDir: cacheDir,
	}
}

// CommitAt returns the newest commit made on or before at.
func (c *Client) CommitAt(ctx context.Context, repo string, at time.Time) (Commit, error) {
	// Round down to the day so repeated runs share one cache entry.
	day := at.UTC().Truncate(24 * time.Hour).Add(24*time.Hour - time.Second)
	cacheFile := c.cacheFile(repo, day)
	if cacheFile != "" {
		if b, err := os.ReadFile(cacheFile); err == nil {
			var cm Commit
			if json.Unmarshal(b, &cm) == nil && cm.SHA != "" {
				return cm, nil
			}
		}
	}

	q := url.Values{"until": {day.Format(time.RFC3339)}, "per_page": {"1"}}
	u := fmt.Sprintf("%s/repos/%s/commits?%s", c.BaseURL, repo, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Commit{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "unbroke/0.1 (api change monitor)")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Commit{}, fmt.Errorf("look up %s history: %w", repo, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			reset := time.Now().Add(time.Hour)
			if n, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				reset = time.Unix(n, 0)
			}
			return Commit{}, &RateLimitError{Reset: reset}
		}
	}
	if resp.StatusCode != http.StatusOK {
		return Commit{}, fmt.Errorf("look up %s history: %s", repo, resp.Status)
	}

	var commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
		return Commit{}, fmt.Errorf("read %s history: %w", repo, err)
	}
	if len(commits) == 0 {
		return Commit{}, ErrNoCommit
	}
	cm := Commit{SHA: commits[0].SHA, Date: commits[0].Commit.Committer.Date}

	if cacheFile != "" {
		if b, err := json.Marshal(cm); err == nil {
			_ = os.MkdirAll(filepath.Dir(cacheFile), 0o755)
			_ = os.WriteFile(cacheFile, b, 0o644)
		}
	}
	return cm, nil
}

// RawURL is the download address of a file at a commit.
func RawURL(repo, sha, path string) string {
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", repo, sha, path)
}

func (c *Client) cacheFile(repo string, day time.Time) string {
	if c.CacheDir == "" {
		return ""
	}
	name := strings.ReplaceAll(repo, "/", "__") + "@" + day.Format("2006-01-02") + ".json"
	return filepath.Join(c.CacheDir, name)
}
