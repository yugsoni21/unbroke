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

	// Names that are ordinary words, or shared with something else that is much
	// more common than the API ("mongodb" is a database driver; "paddle" is an ML
	// framework; "gitlab" is in half the clone URLs on the internet). Each is
	// detected through its own SDKs and API hosts instead.
	"merge":                   {"api.merge.dev", "merge.dev", "mergepythonclient", "@mergeapi/", "merge-api", "merge_api"},
	"render":                  {"api.render.com", "render-api", "@render-oss", "render.com/docs/api"},
	"front":                   {"api2.frontapp.com", "frontapp", "front-api", "@frontapp/"},
	"column":                  {"api.column.com", "column-api", "@column/"},
	"ramp":                    {"api.ramp.com", "ramp-api", "ramp_api"},
	"neon":                    {"console.neon.tech", "neon.tech", "@neondatabase", "neonctl", "neon-api"},
	"knock":                   {"@knocklabs", "knocklabs", "api.knock.app", "knock-node", "knockapi"},
	"moov":                    {"api.moov.io", "@moovio", "moov-io", "moovio"},
	"harbor":                  {"goharbor", "harbor_api", "harborapi", "harbor-api"},
	"paddle":                  {"paddle.com", "@paddle/", "paddle-node-sdk", "paddle_billing", "paddle-python-sdk"},
	"lob":                     {"api.lob.com", "@lob/", "lob-typescript-sdk", "lob-python", "lob-ruby"},
	"miro":                    {"api.miro.com", "@mirohq", "miro-api", "miro_api"},
	"mistral":                 {"mistralai", "api.mistral.ai", "@mistralai"},
	"replicate":               {"api.replicate.com", "replicate.com", "replicate-python", "import replicate", "from replicate"},
	"xai":                     {"api.x.ai", "xai-sdk", "xai_sdk"},
	"confluent":               {"api.confluent.cloud", "confluent-cloud", "confluent_cloud"},
	"linode":                  {"linode", "linode_api4", "linodego", "api.linode.com"},
	"hetzner":                 {"hetzner", "hcloud", "api.hetzner.cloud"},
	"flyio":                   {"fly.io", "flyio", "api.machines.dev"},
	"mongodb-atlas":           {"cloud.mongodb.com/api", "atlas-sdk", "mongodb-atlas", "atlas_admin"},
	"gitlab":                  {"python-gitlab", "go-gitlab", "@gitbeaker", "gitlab.com/api", "gitlab-api"},
	"bitbucket":               {"api.bitbucket.org", "bitbucket-api", "atlassian-python-api"},
	"jira-service-management": {"jira-service-management", "/rest/servicedeskapi", "servicedeskapi"},
	"argocd":                  {"argocd", "argo-cd", "argoproj/argo-cd"},
	"dropbox-sign":            {"dropbox-sign", "dropbox_sign", "hellosign", "@dropbox/sign"},
	"modern-treasury":         {"modern-treasury", "moderntreasury", "modern_treasury"},
	"mastercard-open-banking": {"finicity", "mastercard-open-banking", "api.finicity.com"},
	"zoho-crm":                {"zoho-crm", "zohocrm", "zoho_crm", "zohoapis.com/crm"},
	"coinbase-onramp":         {"coinbase-onramp", "api.developer.coinbase.com/onramp", "api.cdp.coinbase.com"},
	"huggingface":             {"huggingface", "huggingface_hub", "huggingface.co"},
	"ably":                    {"control.ably.net"},
	"spotify":                 {"spotify", "spotipy", "api.spotify.com"},
	// PlanetScale also publishes open-source libraries (vtprotobuf) that many Go projects depend on.
	"planetscale": {"api.planetscale.com", "planetscale-go", "planetscale/planetscale-go"},
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

// ImportWords are the names of the provider as they appear in an import of the
// project's own module ("from app.github.config import ..."). They are broader
// than Hints, so they are only trusted on import lines.
func (p Provider) ImportWords() []string {
	words := []string{strings.ToLower(p.ID)}
	if first := strings.ToLower(strings.Fields(p.Name)[0]); first != words[0] && len(first) > 3 {
		words = append(words, first)
	}
	// One- and two-letter ids are far too common as words.
	var out []string
	for _, w := range words {
		if len(w) >= 4 {
			out = append(out, w)
		}
	}
	return out
}
