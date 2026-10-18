package discovery

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Descriptor says what a source type is and which parts of a Source it uses. Each
// provider describes its own types, so the admin API and UI learn the available types
// and their form fields from the registry, and validation is driven by the descriptor
// rather than by lists of type names kept elsewhere.
type Descriptor struct {
	Type        string `json:"type"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// URL says whether Source.URL applies. URLHint is placeholder text for the form.
	URL     URLUse `json:"url"`
	URLHint string `json:"url_hint,omitempty"`
	// UsesQueries marks search-style providers: they run the configured queries instead of reading one URL.
	UsesQueries bool `json:"uses_queries"`
	// Crawl lists the CrawlPolicy fields the provider reads. The zero value means none.
	Crawl CrawlFields `json:"crawl"`
	// UsesCoverage marks providers that read Source.Coverage (for example to pick a search locale).
	UsesCoverage bool `json:"uses_coverage"`
}

// URLUse is whether a provider takes a URL.
type URLUse string

const (
	URLNone     URLUse = "none"
	URLRequired URLUse = "required"
)

// CrawlFields flags the CrawlPolicy fields a provider reads. RequireDomains makes
// allowed_domains mandatory, because crawls are always bounded.
type CrawlFields struct {
	AllowedDomains bool `json:"allowed_domains"`
	RequireDomains bool `json:"require_domains"`
	URLPatterns    bool `json:"url_patterns"`
	MaxPages       bool `json:"max_pages"`
	MaxDepth       bool `json:"max_depth"`
	Render         bool `json:"render"`
}

func (c CrawlFields) Any() bool {
	return c.AllowedDomains || c.URLPatterns || c.MaxPages || c.MaxDepth || c.Render
}

// MinPollInterval is the shortest schedule a source may have.
const MinPollInterval = time.Minute

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// ValidateSource returns every problem with src (not just the first). The set of valid
// types is whatever reg has registered.
func ValidateSource(reg *Registry, src Source) []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if !slugRE.MatchString(src.Slug) {
		add("slug must be 2-63 characters: lowercase letters, digits and dashes, starting with a letter or digit")
	}
	if strings.TrimSpace(src.Name) == "" {
		add("name is required")
	}
	d, ok := reg.Describe(src.Type)
	if !ok {
		add("unknown type %q (known: %s)", src.Type, strings.Join(reg.Types(), ", "))
		return errs
	}
	if src.PollInterval < MinPollInterval {
		add("poll interval must be at least %s, got %s", MinPollInterval, src.PollInterval)
	}
	switch {
	case d.URL == URLRequired:
		if u, err := url.Parse(src.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("type %s needs an absolute http(s) url, got %q", src.Type, src.URL)
		}
	case src.URL != "":
		add("type %s does not use a url", src.Type)
	}
	if d.Crawl.RequireDomains && len(src.Crawl.AllowedDomains) == 0 {
		add("type %s must set allowed domains (crawls are always bounded)", src.Type)
	}
	if src.Crawl.MaxPages < 0 || src.Crawl.MaxDepth < 0 {
		add("crawl max pages and max depth cannot be negative")
	}
	switch src.Crawl.Render {
	case "", "http_only", "auto", "always":
	default:
		add("crawl render must be http_only, auto or always, got %q", src.Crawl.Render)
	}
	for _, p := range src.Crawl.URLPatterns {
		if _, err := regexp.Compile(p); err != nil {
			add("crawl url pattern %q is not a valid regular expression: %v", p, err)
		}
	}
	return errs
}

// QuerySpec is a search query as configured (the stored form of Query).
type QuerySpec struct {
	Family    string   `json:"family"`
	Name      string   `json:"name"`
	Text      string   `json:"text"`
	Providers []string `json:"providers"` // empty = every search-style provider
	Region    string   `json:"region,omitempty"`
	Enabled   bool     `json:"enabled"`
}

var queryNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// ValidateQuery returns every problem with q. Providers must name registered search-style types.
func ValidateQuery(reg *Registry, q QuerySpec) []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if !queryNameRE.MatchString(q.Name) {
		add("name must be 2-63 characters: lowercase letters, digits and dashes")
	}
	if strings.TrimSpace(q.Family) == "" {
		add("family is required")
	}
	if strings.TrimSpace(q.Text) == "" {
		add("text is required")
	} else if err := CheckQuerySyntax(q.Text); err != nil {
		add("text: %v", err)
	}
	for _, p := range q.Providers {
		if d, ok := reg.Describe(p); !ok {
			add("unknown provider %q", p)
		} else if !d.UsesQueries {
			add("provider %q does not run search queries", p)
		}
	}
	return errs
}

// CheckQuerySyntax enforces the rules GDELT is strict about: balanced parentheses
// and quotes, and no bare OR outside a parenthesized group.
func CheckQuerySyntax(text string) error {
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
