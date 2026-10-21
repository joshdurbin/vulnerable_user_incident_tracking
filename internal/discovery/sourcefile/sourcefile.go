// Package sourcefile reads discovery sources and queries from YAML. It is the format of the seed
// list the API loads when the database has no sources; after that the database is the only
// source of truth, and nothing here is consulted again.
package sourcefile

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
)

// SourceSpec is one source as written in a file.
type SourceSpec struct {
	Slug         string                `yaml:"slug"`
	Name         string                `yaml:"name"`
	Type         string                `yaml:"type"`
	URL          string                `yaml:"url,omitempty"`
	Coverage     discovery.Coverage    `yaml:"coverage,omitempty"`
	PollInterval time.Duration         `yaml:"poll_interval"`
	Enabled      *bool                 `yaml:"enabled,omitempty"`
	Crawl        discovery.CrawlPolicy `yaml:"crawl,omitempty"`
}

// QuerySpec is one search query as written in a file.
type QuerySpec struct {
	Family    string   `yaml:"family"`
	Name      string   `yaml:"name"`
	Text      string   `yaml:"text"`
	Providers []string `yaml:"providers,omitempty"`
	Region    string   `yaml:"region,omitempty"`
	Enabled   *bool    `yaml:"enabled,omitempty"`
}

type File struct {
	Sources []SourceSpec `yaml:"sources,omitempty"`
	Queries []QuerySpec  `yaml:"queries,omitempty"`
}

func (s SourceSpec) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }
func (q QuerySpec) IsEnabled() bool  { return q.Enabled == nil || *q.Enabled }

// Parse reads a file strictly: a typo in a key is an error, not a silently ignored setting.
func Parse(b []byte) (File, error) {
	var f File
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return File{}, err
	}
	return f, nil
}

// Load reads a file from disk.
func Load(path string) (File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	f, err := Parse(b)
	if err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// ToSource converts a spec to a discovery.Source.
func (s SourceSpec) ToSource() discovery.Source {
	return discovery.Source{
		Slug: s.Slug, Name: s.Name, Type: s.Type, URL: s.URL, Coverage: s.Coverage,
		PollInterval: s.PollInterval, Crawl: s.Crawl, Enabled: s.IsEnabled(),
	}
}

// ToSpec converts a query spec to the stored form.
func (q QuerySpec) ToSpec() discovery.QuerySpec {
	return discovery.QuerySpec{Family: q.Family, Name: q.Name, Text: q.Text, Providers: q.Providers, Region: q.Region, Enabled: q.IsEnabled()}
}

// Validate returns every problem in the file (not just the first), using the registered
// providers' descriptors to decide what each source type requires.
func Validate(reg *discovery.Registry, f File) []error {
	var errs []error
	seen := map[string]bool{}
	for _, s := range f.Sources {
		for _, e := range discovery.ValidateSource(reg, s.ToSource()) {
			errs = append(errs, fmt.Errorf("source %q: %w", s.Slug, e))
		}
		if seen[s.Slug] {
			errs = append(errs, fmt.Errorf("source %q: duplicate slug", s.Slug))
		}
		seen[s.Slug] = true
	}
	qseen := map[string]bool{}
	for _, q := range f.Queries {
		for _, e := range discovery.ValidateQuery(reg, q.ToSpec()) {
			errs = append(errs, fmt.Errorf("query %q: %w", q.Name, e))
		}
		if qseen[q.Name] {
			errs = append(errs, fmt.Errorf("query %q: duplicate name", q.Name))
		}
		qseen[q.Name] = true
	}
	return errs
}
