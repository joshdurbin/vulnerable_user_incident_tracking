// Package discovery finds candidate article URLs. Every source type (a search
// API, a feed, a sitemap, a crawled index page) is a Provider behind one
// interface, selected by a source's configured type, so sources are added by
// configuration and new types by one package plus one registry line.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Source types, matching discovery_sources.source_type.
const (
	TypeGDELT       = "gdelt"
	TypeGoogleNews  = "googlenews"
	TypeRSS         = "rss"
	TypeAtom        = "atom"
	TypeSitemap     = "sitemap"
	TypeNewsSitemap = "news_sitemap"
	TypeIndexPage   = "index_page"
	TypePolicePress = "police_press"
)

// Source is a configured discovery source (a row of discovery_sources).
type Source struct {
	Slug         string
	Name         string
	Type         string
	URL          string // feed / sitemap / index page URL; unused by search providers
	Coverage     Coverage
	PollInterval time.Duration
	Crawl        CrawlPolicy
	// Runtime state (read from the database; ignored by providers).
	Enabled             bool
	LastSuccessAt       *time.Time
	LastAttemptAt       *time.Time
	ConsecutiveFailures int
	ErrorState          string
}

// Coverage says where a source is relevant. It is metadata for targeting and
// reporting; nothing in the domain model is California-specific.
type Coverage struct {
	Country string   `json:"country,omitempty" yaml:"country"`
	States  []string `json:"states,omitempty" yaml:"states"`
	Cities  []string `json:"cities,omitempty" yaml:"cities"`
}

// CrawlPolicy bounds HTML-crawling providers.
type CrawlPolicy struct {
	AllowedDomains []string `json:"allowed_domains,omitempty" yaml:"allowed_domains"`
	URLPatterns    []string `json:"url_patterns,omitempty" yaml:"url_patterns"`
	MaxPages       int      `json:"max_pages,omitempty" yaml:"max_pages"`
	MaxDepth       int      `json:"max_depth,omitempty" yaml:"max_depth"`
	Render         string   `json:"render,omitempty" yaml:"render"` // http_only|auto|always
}

// Query is a configurable search expression for search-style providers.
type Query struct {
	Family string // pedestrian, cyclist, ...
	Name   string
	Text   string
	Region string // optional provider-specific region hint
}

// Window is the half-open time range [Since, Until) to look in. Overlapping
// windows are expected: providers index late.
type Window struct {
	Since, Until time.Time
}

func (w Window) Duration() time.Duration { return w.Until.Sub(w.Since) }

// Request is everything a provider needs for one run.
type Request struct {
	Source  Source
	Queries []Query // search-style providers only
	Window  Window
}

// Candidate is one URL a provider found. Fields are raw provider data;
// normalization and deduplication happen afterwards in one place.
type Candidate struct {
	URL          string
	RawURL       string // URL exactly as the provider returned it (kept when URL is rewritten/resolved)
	Title        string
	PublishedAt  *time.Time // as reported by the provider, may be the index time
	Provider     string     // provider type that found it
	SourceSlug   string
	QueryName    string
	Publisher    string // publisher name or domain if known
	PublisherURL string
	Language     string
	Country      string
	// NeedsResolution marks an intermediary URL (e.g. a Google News redirect) that
	// is not the publisher's article URL. It must never be crawled as-is.
	NeedsResolution bool
}

// Result is one provider run. A run that partly failed returns what it found
// plus Warnings; it returns an error only when nothing usable came back.
type Result struct {
	Candidates []Candidate
	Warnings   []string
	// Truncated is true if a provider limit cut results off that could not be
	// worked around (e.g. a minimum window size still returned the cap).
	Truncated bool
	Requests  int // upstream requests made, for cost and rate accounting
}

func (r *Result) Warnf(format string, a ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, a...))
}

// Provider discovers candidate URLs from one kind of source. It is the whole contract:
// Describe is the provider's input side (which Source fields it reads, so configuration
// can be validated and edited generically), Discover turns a Request into a Result.
type Provider interface {
	// Type is the source_type this implementation primarily serves.
	Type() string
	// Describe returns the descriptor for one of the types the provider is registered under.
	Describe(sourceType string) Descriptor
	Discover(ctx context.Context, req Request) (*Result, error)
}

// RateLimitedError is returned by providers when the upstream asks us to slow down
// and retries were exhausted. Workflows treat it as retryable after RetryAfter.
type RateLimitedError struct {
	Provider   string
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("%s: rate limited (retry after %s)", e.Provider, e.RetryAfter)
}

// ErrNotFound and ErrAlreadyExists are returned by the repository for a missing or duplicate
// source or query.
var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
)

// ErrUnknownType is returned for a source type with no registered provider.
var ErrUnknownType = errors.New("no provider registered for source type")

// Registry maps source types to providers.
type Registry struct {
	providers map[string]Provider
}

func NewRegistry() *Registry { return &Registry{providers: map[string]Provider{}} }

// Register adds a provider for each type it serves. Registering a type twice is a programming error.
func (r *Registry) Register(p Provider, types ...string) {
	if len(types) == 0 {
		types = []string{p.Type()}
	}
	for _, t := range types {
		if _, dup := r.providers[t]; dup {
			panic("discovery: provider already registered for " + t)
		}
		r.providers[t] = p
	}
}

func (r *Registry) Provider(sourceType string) (Provider, error) {
	p, ok := r.providers[sourceType]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, sourceType)
	}
	return p, nil
}

// Describe returns the descriptor for a registered source type.
func (r *Registry) Describe(sourceType string) (Descriptor, bool) {
	p, ok := r.providers[sourceType]
	if !ok {
		return Descriptor{}, false
	}
	return p.Describe(sourceType), true
}

// Descriptors returns every registered type's descriptor, sorted by type.
func (r *Registry) Descriptors() []Descriptor {
	types := r.Types()
	out := make([]Descriptor, 0, len(types))
	for _, t := range types {
		d, _ := r.Describe(t)
		out = append(out, d)
	}
	return out
}

func (r *Registry) Types() []string {
	out := make([]string, 0, len(r.providers))
	for t := range r.providers {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Discover runs the provider for req.Source.Type.
func (r *Registry) Discover(ctx context.Context, req Request) (*Result, error) {
	p, err := r.Provider(req.Source.Type)
	if err != nil {
		return nil, err
	}
	res, err := p.Discover(ctx, req)
	if res != nil {
		for i := range res.Candidates {
			c := &res.Candidates[i]
			if c.RawURL == "" {
				c.RawURL = c.URL
			}
			if c.Provider == "" {
				c.Provider = req.Source.Type
			}
			if c.SourceSlug == "" {
				c.SourceSlug = req.Source.Slug
			}
		}
	}
	return res, err
}
