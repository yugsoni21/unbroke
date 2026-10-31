package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yugsoni21/unbroke/internal/catalog"
)

func body(t *testing.T, r *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The page carries the API token, so it is only handed to someone who holds the launch key
// that was printed in the terminal. Another user or process on this machine can reach the
// port, but cannot use the page to run commands.
func TestThePageNeedsTheLaunchKey(t *testing.T) {
	s, ts := newTestServer(t)
	for name, path := range map[string]string{"no key": "/", "empty key": "/?k=", "wrong key": "/?k=nope", "almost": "/?k=" + s.launchKey[:len(s.launchKey)-1]} {
		r := get(t, ts, path, "", "")
		b := body(t, r)
		if r.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, r.StatusCode)
		}
		if strings.Contains(b, s.token) || strings.Contains(b, "<html") {
			t.Errorf("%s: the refusal leaked the page or the token", name)
		}
		if !strings.Contains(b, "link printed in the terminal") {
			t.Errorf("%s: the refusal does not say what to do: %q", name, b)
		}
		if len(r.Header.Values("Set-Cookie")) != 0 {
			t.Errorf("%s: a cookie was set for a visitor without the key", name)
		}
	}
	r := get(t, ts, "/?k="+s.launchKey, "", "")
	if r.StatusCode != 200 || !strings.Contains(body(t, r), s.token) {
		t.Fatalf("the right key should load the page, got %d", r.StatusCode)
	}
}

func TestTheKeyBecomesAPrivateCookieSoReloadsWork(t *testing.T) {
	s, ts := newTestServer(t)
	r := get(t, ts, "/?k="+s.launchKey+"&scan=/tmp/x", "", "")
	var c *http.Cookie
	for _, k := range r.Cookies() {
		if k.Name == s.cookie {
			c = k
		}
	}
	if c == nil || c.Value != s.launchKey {
		t.Fatalf("no cookie carrying the key: %v", r.Cookies())
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie is not HttpOnly + SameSite=Strict + Path=/: %+v", c)
	}
	if r.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Error("the page should not leak its address in a Referer header")
	}
	// A reload (cookie only, key no longer in the address) still works...
	req, _ := http.NewRequest("GET", ts.URL+"/", nil)
	req.AddCookie(c)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("reload with the cookie: status %d", resp.StatusCode)
	}
	// ...and a cookie with the wrong value does not.
	req, _ = http.NewRequest("GET", ts.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: s.cookie, Value: "guess"})
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("a wrong cookie got status %d", resp2.StatusCode)
	}
}

func TestTheKeyIsNotTheAPIToken(t *testing.T) {
	s, ts := newTestServer(t)
	if s.launchKey == s.token || s.launchKey == "" {
		t.Fatal("the launch key must be its own secret")
	}
	// Holding the launch key is not enough to call the API.
	req, _ := http.NewRequest("GET", ts.URL+"/api/state", nil)
	req.Header.Set("X-Unbroke-Token", s.launchKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("the launch key was accepted as an API token: %d", resp.StatusCode)
	}
}

// The folder name comes from the command line and can hold anything. Whatever it holds, it must
// land in the page as text, never as markup.
func TestAHostileFolderNameCannotInjectIntoThePage(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	payload := `"><script>alert(1)</script><img src=x onerror=alert(2)> ' &`
	s, err := New(`v"><b>1</b>`, payload, cat)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	s.hosts = map[string]bool{strings.TrimPrefix(ts.URL, "http://"): true}
	r := get(t, ts, "/?k="+s.launchKey, "", "")
	b := body(t, r)
	for _, bad := range []string{`<script>alert(1)</script>`, `<img src=x onerror`, `"><b>1</b>`} {
		if strings.Contains(b, bad) {
			t.Errorf("unescaped %q reached the page", bad)
		}
	}
	for _, want := range []string{`&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;`, `&#39;`} {
		if !strings.Contains(b, want) {
			t.Errorf("expected the escaped form %q in the page", want)
		}
	}
}
