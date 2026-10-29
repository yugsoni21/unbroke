// Package history finds what a public spec repository looked like at an
// earlier date, so Unbroke can compare "now" with "six months ago" for any
// GitHub-hosted API without having been watching it back then.
//
// One lookup per repository is enough: the commit that was current on a given
// day fixes the content of every file in the repo on that day, so all of a
// provider's spec files can be fetched at that same commit.
package history

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// GitBase, if set, is where repositories are cloned from (for example
	// "https://github.com"). Lookups then follow the default branch's own
	// history in a local clone, which is both correct for repositories that
	// merge pull requests and free of the API's rate limit.
	GitBase string
}

// New returns a client that reads GITHUB_TOKEN from the environment.
func New(cacheDir string) *Client {
	return &Client{
		HTTP:     &http.Client{Timeout: time.Minute},
		Token:    os.Getenv("GITHUB_TOKEN"),
		BaseURL:  "https://api.github.com",
		CacheDir: cacheDir,
		GitBase:  "https://github.com",
	}
}

// CommitAt returns the commit the repository's default branch pointed at, at
// time at.
//
// The GitHub API's "until" filter cannot answer this: it returns the newest
// commit dated before at among all ancestors, which in a repository that
// merges pull requests is often a commit from a feature branch merged later,
// so its files are not what the default branch held that day. A local clone
// can follow the default branch's first-parent history, which is exact. The
// API is only a fallback, when git is unavailable or the clone fails.
func (c *Client) CommitAt(ctx context.Context, repo string, at time.Time) (Commit, error) {
	if c.GitBase != "" && c.CacheDir != "" {
		if cm, err := c.commitAtGit(ctx, repo, at); err == nil {
			return cm, nil
		}
	}
	return c.commitAtAPI(ctx, repo, at)
}

// commitAtAPI asks the GitHub API. It rounds down to the hour so reruns share
// a cache entry, and rounding down means the answer is never later than at.
func (c *Client) commitAtAPI(ctx context.Context, repo string, at time.Time) (Commit, error) {
	day := at.UTC().Truncate(time.Hour)
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
	name := strings.ReplaceAll(repo, "/", "__") + "@" + day.Format("2006-01-02T15") + ".json"
	return filepath.Join(c.CacheDir, name)
}

// commitAtGit answers from a local clone that holds commits only (no files),
// fetched back to a week before at, and follows the default branch's
// first-parent history.
func (c *Client) commitAtGit(ctx context.Context, repo string, at time.Time) (Commit, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return Commit{}, err
	}
	if !repoName.MatchString(repo) {
		return Commit{}, fmt.Errorf("unexpected repository name %q", repo)
	}
	dir := filepath.Join(c.CacheDir, "clones", strings.ReplaceAll(repo, "/", "__")+".git")
	since := at.Add(-7 * 24 * time.Hour).UTC().Format("2006-01-02")
	url := strings.TrimRight(c.GitBase, "/") + "/" + repo + ".git"

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		tmp := dir + ".tmp"
		_ = os.RemoveAll(tmp)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return Commit{}, err
		}
		// Start shallow; history is deepened below only as far as needed.
		if _, err := gitIn(ctx, "", "clone", "--bare", "--quiet", "--single-branch", "--filter=tree:0", "--depth=1", url, tmp); err != nil {
			_ = os.RemoveAll(tmp)
			return Commit{}, err
		}
		if err := os.Rename(tmp, dir); err != nil {
			_ = os.RemoveAll(tmp)
			return Commit{}, err
		}
	}
	if _, err := gitIn(ctx, dir, "fetch", "--quiet", "--filter=tree:0", "origin", "+HEAD:refs/unbroke/tip"); err != nil {
		return Commit{}, err
	}
	// Reach back to the date in one step when the repository is active. It fails
	// harmlessly when nothing is that recent, which the loop below handles.
	_, _ = gitIn(ctx, dir, "fetch", "--quiet", "--filter=tree:0", "--shallow-since="+since, "origin", "+HEAD:refs/unbroke/tip")

	before := "--before=" + at.UTC().Format(time.RFC3339)
	var sha string
	for step := 100; ; step *= 4 {
		var err error
		if sha, err = gitIn(ctx, dir, "rev-list", "--first-parent", "-1", before, "refs/unbroke/tip"); err != nil {
			return Commit{}, err
		}
		if sha != "" {
			break
		}
		// The default branch did not change for a while before the date, so the
		// commit it pointed at is older than what has been fetched. Deepen until
		// it is found or the start of history is reached.
		shallow, err := gitIn(ctx, dir, "rev-parse", "--is-shallow-repository")
		if err != nil {
			return Commit{}, err
		}
		if shallow != "true" || step > 100*1024 {
			return Commit{}, ErrNoCommit
		}
		if _, err := gitIn(ctx, dir, "fetch", "--quiet", "--filter=tree:0", fmt.Sprintf("--deepen=%d", step), "origin", "+HEAD:refs/unbroke/tip"); err != nil {
			return Commit{}, err
		}
	}
	when, err := gitIn(ctx, dir, "log", "-1", "--format=%cI", sha)
	if err != nil {
		return Commit{}, err
	}
	t, err := time.Parse(time.RFC3339, when)
	if err != nil {
		return Commit{}, err
	}
	return Commit{SHA: sha, Date: t}, nil
}

var repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}
