package catalog

import "strings"

// hintOverrides name the signals that show a codebase uses a provider, where
// the provider's id alone would be too common a word or too narrow a name.
// "github" appears in nearly every repository, so it is detected through the
// libraries and API host instead.
var hintOverrides = map[string][]string{
	"github":        {"octokit", "api.github.com", "@actions/github", "pygithub", "go-github", "github3"},
	"x":             {"api.twitter.com", "api.x.com", "twitter-api", "tweepy", "twitter4j"},
	"box":           {"box-node-sdk", "boxsdk", "box_sdk", "api.box.com", "box-python-sdk"},
	"jira":          {"jira", "api.atlassian.com"},
	"msgraph":       {"graph.microsoft.com", "microsoft-graph", "msgraph"},
	"docker":        {"dockerode", "docker-py", "docker.sock", "docker/docker/client"},
	"kubernetes":    {"kubernetes", "k8s.io", "kubectl"},
	"slack":         {"slack_sdk", "slack-sdk", "@slack/", "slack_bolt", "slack.com/api"},
	"elasticsearch": {"elasticsearch", "@elastic/"},
	"notion":        {"@notionhq", "notion-client", "api.notion.com"},
	"datadog":       {"datadog", "dd-trace", "datadogpy"},
	"vercel":        {"@vercel/", "api.vercel.com"},
	// "com.squareup.okhttp3" is Square Inc's HTTP library, not its payments API.
	"square":       {"api.squareup.com", "connect.squareup.com", "squareup.square", "square-connect", "square-go-sdk", "square.rb", "from square.", "from square import", `"square"`, "'square'"},
	"digitalocean": {"digitalocean", "godo", "pydo"},
	"sentry":       {"sentry"},
	"okta":         {"okta"},
	"hubspot":      {"hubspot"},
}

// Hints returns lower-case substrings whose presence in an import, a
// dependency file or a URL shows that a codebase uses this provider.
func (p Provider) Hints() []string {
	if h, ok := hintOverrides[p.ID]; ok {
		return h
	}
	hints := []string{strings.ToLower(p.ID)}
	if first := strings.ToLower(strings.Fields(p.Name)[0]); first != hints[0] && len(first) > 3 {
		hints = append(hints, first)
	}
	return hints
}
