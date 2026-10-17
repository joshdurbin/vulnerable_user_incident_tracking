// Package seed loads discovery sources and query families from YAML (embedded
// defaults plus optional user files), validates them, and is the only way
// sources enter the system, so adding a source never needs a code change.
package seed

import (
	"embed"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/joshdurbin/fuck_cars/internal/discovery"
)

//go:embed defaults/*.yaml
var defaults embed.FS

type SourceSeed struct {
	Slug               string                `yaml:"slug"`
	Name               string                `yaml:"name"`
	Type               string                `yaml:"type"`
	URL                string                `yaml:"url"`
	Coverage           discovery.Coverage    `yaml:"coverage"`
	PollInterval       time.Duration         `yaml:"poll_interval"`
	RateLimitPerMinute int                   `yaml:"rate_limit_per_minute"`
	Enabled            *bool                 `yaml:"enabled"`
	Crawl              discovery.CrawlPolicy `yaml:"crawl"`
}

type QuerySeed struct {
	Family    string   `yaml:"family"`
	Name      string   `yaml:"name"`
	Text      string   `yaml:"text"`
	Providers []string `yaml:"providers"`
	Region    string   `yaml:"region"`
	Enabled   *bool    `yaml:"enabled"`
}

type File struct {
	Sources []SourceSeed `yaml:"sources"`
	Queries []QuerySeed  `yaml:"queries"`
}

func (s SourceSeed) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }
func (q QuerySeed) IsEnabled() bool  { return q.Enabled == nil || *q.Enabled }

func parse(b []byte) (File, error) {
	var f File
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true) // a typo in a key is an error, not a silently ignored setting
	if err := dec.Decode(&f); err != nil {
		return File{}, err
	}
	return f, nil
}

// Defaults returns the embedded sources and queries.
func Defaults() (File, error) {
	var out File
	for _, name := range []string{"defaults/sources.yaml", "defaults/queries.yaml"} {
		b, err := defaults.ReadFile(name)
		if err != nil {
			return File{}, err
		}
		f, err := parse(b)
		if err != nil {
			return File{}, fmt.Errorf("%s: %w", name, err)
		}
		out = Merge(out, f)
	}
	return out, nil
}

// LoadFile reads a user seed file.
func LoadFile(path string) (File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	f, err := parse(b)
	if err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// Merge returns base with over applied: entries match by slug (sources) or name (queries).
func Merge(base, over File) File {
	out := File{Sources: append([]SourceSeed(nil), base.Sources...), Queries: append([]QuerySeed(nil), base.Queries...)}
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

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

var urlTypes = map[string]bool{
	discovery.TypeRSS: true, discovery.TypeAtom: true, discovery.TypeSitemap: true, discovery.TypeNewsSitemap: true,
	discovery.TypeIndexPage: true, discovery.TypePolicePress: true,
}
var crawlTypes = map[string]bool{discovery.TypeIndexPage: true, discovery.TypePolicePress: true}

// Validate returns every problem found (not just the first). knownTypes are the
// source types the running registry can serve.
func Validate(f File, knownTypes []string) []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	known := map[string]bool{}
	for _, t := range knownTypes {
		known[t] = true
	}
	seen := map[string]bool{}
	for _, s := range f.Sources {
		id := "source " + s.Slug
		switch {
		case !slugRE.MatchString(s.Slug):
			add("source %q: slug must be lowercase letters, digits and dashes", s.Slug)
			continue
		case seen[s.Slug]:
			add("%s: duplicate slug", id)
		}
		seen[s.Slug] = true
		if s.Name == "" {
			add("%s: name is required", id)
		}
		if !known[s.Type] {
			add("%s: unknown type %q (known: %s)", id, s.Type, strings.Join(knownTypes, ", "))
		}
		if urlTypes[s.Type] {
			if u, err := url.Parse(s.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				add("%s: type %s needs an absolute http(s) url, got %q", id, s.Type, s.URL)
			}
		}
		if crawlTypes[s.Type] && len(s.Crawl.AllowedDomains) == 0 {
			add("%s: crawled sources must set crawl.allowed_domains (crawls are always bounded)", id)
		}
		if s.PollInterval < time.Minute {
			add("%s: poll_interval must be at least 1m, got %s", id, s.PollInterval)
		}
		if s.RateLimitPerMinute < 0 {
			add("%s: rate_limit_per_minute cannot be negative", id)
		}
	}
	qseen := map[string]bool{}
	for _, q := range f.Queries {
		id := "query " + q.Name
		switch {
		case q.Name == "" || q.Family == "" || strings.TrimSpace(q.Text) == "":
			add("query %q: name, family and text are required", q.Name)
			continue
		case qseen[q.Name]:
			add("%s: duplicate name", id)
		}
		qseen[q.Name] = true
		if err := checkQuerySyntax(q.Text); err != nil {
			add("%s: %v", id, err)
		}
		for _, p := range q.Providers {
			if !known[p] {
				add("%s: unknown provider %q", id, p)
			}
		}
	}
	return errs
}

// checkQuerySyntax enforces the rules GDELT is strict about: balanced parentheses
// and no bare OR outside a parenthesized group.
func checkQuerySyntax(text string) error {
	depth := 0
	inQuote := false
	var word strings.Builder
	flush := func() error {
		w := word.String()
		word.Reset()
		if w == "OR" && depth == 0 {
			return fmt.Errorf("OR must be inside parentheses")
		}
		return nil
	}
	for _, r := range text {
		switch {
		case r == '"':
			inQuote = !inQuote
		case inQuote:
		case r == '(':
			if err := flush(); err != nil {
				return err
			}
			depth++
		case r == ')':
			if err := flush(); err != nil {
				return err
			}
			depth--
			if depth < 0 {
				return fmt.Errorf("unbalanced parentheses")
			}
		case r == ' ':
			if err := flush(); err != nil {
				return err
			}
		default:
			word.WriteRune(r)
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if depth != 0 || inQuote {
		return fmt.Errorf("unbalanced parentheses or quotes")
	}
	return nil
}

// ToSource converts a seed to a discovery.Source.
func (s SourceSeed) ToSource() discovery.Source {
	return discovery.Source{
		Slug: s.Slug, Name: s.Name, Type: s.Type, URL: s.URL, Coverage: s.Coverage, PollInterval: s.PollInterval,
		RatePerMin: s.RateLimitPerMinute, Crawl: s.Crawl, Enabled: s.IsEnabled(),
	}
}
