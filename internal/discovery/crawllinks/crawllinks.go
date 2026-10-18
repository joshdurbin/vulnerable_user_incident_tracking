// Package crawllinks discovers articles on HTML index pages and police
// press-release pages. HTML fetching and parsing is the Python crawler's job;
// this provider only maps a source's crawl policy to a bounded DiscoverLinks call.
package crawllinks

import (
	"context"
	"fmt"
	"time"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/crawler"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/domain"
)

type Provider struct {
	crawler crawler.LinkDiscoverer
	typ     string
}

// New returns a provider for TypeIndexPage or TypePolicePress.
func New(c crawler.LinkDiscoverer, sourceType string) *Provider {
	return &Provider{crawler: c, typ: sourceType}
}

func (p *Provider) Type() string { return p.typ }

func (p *Provider) Describe(string) discovery.Descriptor {
	d := discovery.Descriptor{
		Type: p.typ, Label: "Index page",
		Description: "Crawls an index page and follows links to article pages, within the allowed domains and limits.",
		URL:         discovery.URLRequired, URLHint: "https://example.org/news/",
		Crawl: discovery.CrawlFields{AllowedDomains: true, RequireDomains: true, URLPatterns: true, MaxPages: true, MaxDepth: true, Render: true},
	}
	if p.typ == discovery.TypePolicePress {
		d.Label = "Police press releases"
		d.Description = "Crawls an agency's press-release listing and follows links to individual releases, within the allowed domains and limits."
		d.URLHint = "https://police.example.gov/press-releases/"
	}
	return d
}

const (
	defaultPages    = 10
	defaultDepth    = 1
	defaultDuration = 60 * time.Second
)

func (p *Provider) Discover(ctx context.Context, req discovery.Request) (*discovery.Result, error) {
	src := req.Source
	if p.crawler == nil {
		return nil, fmt.Errorf("%s: crawling is not available in this process", p.typ)
	}
	if src.URL == "" {
		return nil, fmt.Errorf("%s: source %q has no url", p.typ, src.Slug)
	}
	cp := src.Crawl
	if cp.MaxPages <= 0 {
		cp.MaxPages = defaultPages
	}
	if cp.MaxDepth <= 0 {
		cp.MaxDepth = defaultDepth
	}
	render := domain.RenderingPolicy(cp.Render)
	if render == "" {
		render = domain.RenderHTTPOnly
	}
	out, err := p.crawler.DiscoverLinks(ctx, domain.DiscoverLinksRequest{
		SeedURL: src.URL, AllowedDomains: cp.AllowedDomains, URLPatterns: cp.URLPatterns,
		MaxPages: cp.MaxPages, MaxDepth: cp.MaxDepth, MaxDuration: defaultDuration, Rendering: render,
	})
	if err != nil {
		return nil, err
	}
	res := &discovery.Result{Requests: 1, Truncated: out.Truncated}
	if out.ErrorDetail != "" {
		if len(out.Links) == 0 {
			return nil, fmt.Errorf("%s %s: %s", p.typ, src.Slug, out.ErrorDetail)
		}
		res.Warnings = append(res.Warnings, out.ErrorDetail)
	}
	for _, l := range out.Links {
		// Index pages carry no reliable dates; the dedupe step drops URLs already seen.
		res.Candidates = append(res.Candidates, discovery.Candidate{URL: l.URL, Title: l.Title, Provider: p.typ, Publisher: src.Name})
	}
	return res, nil
}
