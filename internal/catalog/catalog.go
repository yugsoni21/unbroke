// Package catalog is the list of public APIs Unbroke watches: who publishes
// them, where the specification lives, and whether its history is public.
//
// The list is data (providers.yaml), embedded in the binary. `unbroke catalog
// verify` downloads and parses every entry, so a spec that moves or breaks
// shows up as a failure instead of silently going stale.
package catalog

import (
	_ "embed"
	"fmt"
	"sort"

	"go.yaml.in/yaml/v3"
)

//go:embed providers.yaml
var embedded []byte

// Spec is one specification file belonging to a provider.
type Spec struct {
	Name string `yaml:"name"`
	// Repo and Path locate a spec in a public GitHub repository. These have
	// full change history, so Unbroke can compare any two past versions.
	Repo string `yaml:"repo"`
	Path string `yaml:"path"`
	// URL locates a spec the provider hosts itself. These have no public
	// history; Unbroke builds one by snapshotting.
	URL string `yaml:"url"`
	// Large marks specs big enough (tens of MB) to be skipped unless asked for.
	Large bool `yaml:"large"`
}

// SourceURL is where to download the current version of the spec.
func (s Spec) SourceURL() string {
	if s.URL != "" {
		return s.URL
	}
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/HEAD/%s", s.Repo, s.Path)
}

// HasHistory reports whether past versions of the spec are publicly available.
func (s Spec) HasHistory() bool { return s.Repo != "" }

// Provider is one company's API, made of one or more specifications.
type Provider struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Company  string `yaml:"company"`
	Category string `yaml:"category"`
	Notes    string `yaml:"notes"`
	// Archived is the date the provider stopped publishing updates to its spec.
	// Comparing such a spec can never show a change, so it is reported as
	// unchecked rather than as "no changes".
	Archived string `yaml:"archived"`
	Specs    []Spec `yaml:"specs"`
}

// Unlocated is a well-known company whose public spec was not found.
type Unlocated struct {
	Name string `yaml:"name"`
	Note string `yaml:"note"`
}

// Catalog is the whole list.
type Catalog struct {
	Providers []Provider  `yaml:"providers"`
	Unlocated []Unlocated `yaml:"unlocated"`
}

// Load reads the embedded catalog.
func Load() (*Catalog, error) { return Parse(embedded) }

// Parse reads a catalog from YAML and checks it for mistakes that would
// otherwise only surface as confusing download errors later.
func Parse(data []byte) (*Catalog, error) {
	var c Catalog
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	seen := map[string]bool{}
	for _, p := range c.Providers {
		if p.ID == "" || p.Name == "" {
			return nil, fmt.Errorf("catalog entry %q is missing an id or name", p.Name)
		}
		if seen[p.ID] {
			return nil, fmt.Errorf("catalog has two providers with id %q", p.ID)
		}
		seen[p.ID] = true
		if len(p.Specs) == 0 {
			return nil, fmt.Errorf("provider %s has no specs", p.ID)
		}
		for _, s := range p.Specs {
			if (s.Repo == "") == (s.URL == "") {
				return nil, fmt.Errorf("provider %s spec %q needs exactly one of repo+path or url", p.ID, s.Name)
			}
			if s.Repo != "" && s.Path == "" {
				return nil, fmt.Errorf("provider %s spec %q has a repo but no path", p.ID, s.Name)
			}
		}
	}
	return &c, nil
}

// Find returns the provider with the given id.
func (c *Catalog) Find(id string) (*Provider, bool) {
	for i := range c.Providers {
		if c.Providers[i].ID == id {
			return &c.Providers[i], true
		}
	}
	return nil, false
}

// Categories returns the distinct categories, sorted.
func (c *Catalog) Categories() []string {
	set := map[string]bool{}
	for _, p := range c.Providers {
		set[p.Category] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SpecCount is the total number of specification files.
func (c *Catalog) SpecCount() int {
	n := 0
	for _, p := range c.Providers {
		n += len(p.Specs)
	}
	return n
}
