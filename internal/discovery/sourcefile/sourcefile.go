// Package sourcefile reads and writes discovery sources and queries as YAML. It is the
// import/export format for the sources held in the database (`collisionctl sources
// import|export`); nothing is embedded or loaded implicitly, so what runs is exactly what
// is configured in the datastore.
package sourcefile

import (
	"fmt"
	"os"
	"sort"
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

// Marshal writes a file in the same form Parse reads.
func Marshal(f File) ([]byte, error) { return yaml.Marshal(f) }

// ToSource converts a spec to a discovery.Source.
func (s SourceSpec) ToSource() discovery.Source {
	return discovery.Source{
		Slug: s.Slug, Name: s.Name, Type: s.Type, URL: s.URL, Coverage: s.Coverage,
		PollInterval: s.PollInterval, Crawl: s.Crawl, Enabled: s.IsEnabled(),
	}
}

// FromSource converts a source to a spec, for export.
func FromSource(s discovery.Source) SourceSpec {
	en := s.Enabled
	return SourceSpec{Slug: s.Slug, Name: s.Name, Type: s.Type, URL: s.URL, Coverage: s.Coverage,
		PollInterval: s.PollInterval, Crawl: s.Crawl, Enabled: &en}
}

// ToSpec converts a query spec to the stored form.
func (q QuerySpec) ToSpec() discovery.QuerySpec {
	return discovery.QuerySpec{Family: q.Family, Name: q.Name, Text: q.Text, Providers: q.Providers, Region: q.Region, Enabled: q.IsEnabled()}
}

// QueryFromSpec converts a stored query to a file entry, for export.
func QueryFromSpec(q discovery.QuerySpec) QuerySpec {
	en := q.Enabled
	return QuerySpec{Family: q.Family, Name: q.Name, Text: q.Text, Providers: q.Providers, Region: q.Region, Enabled: &en}
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

// Merge returns base with over applied: entries match by slug (sources) or name (queries).
func Merge(base, over File) File {
	out := File{Sources: append([]SourceSpec(nil), base.Sources...), Queries: append([]QuerySpec(nil), base.Queries...)}
	for _, s := range over.Sources {
		replaced := false
		for i := range out.Sources {
			if out.Sources[i].Slug == s.Slug {
				out.Sources[i], replaced = s, true
			}
		}
		if !replaced {
			out.Sources = append(out.Sources, s)
		}
	}
	for _, q := range over.Queries {
		replaced := false
		for i := range out.Queries {
			if out.Queries[i].Name == q.Name {
				out.Queries[i], replaced = q, true
			}
		}
		if !replaced {
			out.Queries = append(out.Queries, q)
		}
	}
	return out
}

// Sorted returns f with sources by slug and queries by family then name, for stable exports.
func Sorted(f File) File {
	out := File{Sources: append([]SourceSpec(nil), f.Sources...), Queries: append([]QuerySpec(nil), f.Queries...)}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].Slug < out.Sources[j].Slug })
	sort.Slice(out.Queries, func(i, j int) bool {
		if out.Queries[i].Family != out.Queries[j].Family {
			return out.Queries[i].Family < out.Queries[j].Family
		}
		return out.Queries[i].Name < out.Queries[j].Name
	})
	return out
}
