// Package crawler is the Go side of the Python crawler. The two talk through
// Temporal: the Python worker serves the fetch_article and discover_links
// activities on the crawl task queue, and Go workflows call them by name. There
// is no separate RPC service. The wire types below are JSON-compatible with the
// dataclasses in crawler/src/collision_crawler/activities.py.
package crawler

import (
	"context"
	"time"

	"github.com/joshdurbin/fuck_cars/internal/domain"
)

// Activity names registered by the Python worker.
const (
	ActivityFetchArticle  = "fetch_article"
	ActivityDiscoverLinks = "discover_links"
)

// Names of the small workflows that wrap one crawler activity, for callers that
// are not themselves workflows (CLI, discovery providers). Registered by package pipeline.
const (
	WorkflowFetch = "CrawlFetchWorkflow"
	WorkflowLinks = "CrawlLinksWorkflow"
)

// Fetcher is the interface the pipeline depends on; tests substitute fakes.
type Fetcher interface {
	FetchArticle(ctx context.Context, req domain.FetchRequest) (*domain.Article, error)
}

// LinkDiscoverer is the crawler's bounded link-discovery capability.
type LinkDiscoverer interface {
	DiscoverLinks(ctx context.Context, req domain.DiscoverLinksRequest) (*domain.DiscoverLinksResult, error)
}

type FetchArticleRequest struct {
	URL           string  `json:"url"`
	Rendering     string  `json:"rendering"`
	SourceID      string  `json:"source_id,omitempty"`
	TimeoutS      float64 `json:"timeout_s,omitempty"`
	CorrelationID string  `json:"correlation_id,omitempty"`
}

type ArticleResponse struct {
	RequestedURL     string  `json:"requested_url"`
	FinalURL         string  `json:"final_url"`
	CanonicalURL     string  `json:"canonical_url"`
	Title            string  `json:"title"`
	Publisher        string  `json:"publisher"`
	Author           string  `json:"author"`
	PublishedAt      *string `json:"published_at"`
	Text             string  `json:"text"`
	Description      string  `json:"description"`
	Language         string  `json:"language"`
	HTTPStatus       int     `json:"http_status"`
	ContentType      string  `json:"content_type"`
	FetchedAt        string  `json:"fetched_at"`
	Rendered         bool    `json:"rendered"`
	ExtractionStatus string  `json:"extraction_status"`
	ErrorDetail      string  `json:"error_detail"`
}

type DiscoverLinksRequest struct {
	SeedURL        string   `json:"seed_url"`
	AllowedDomains []string `json:"allowed_domains"`
	URLPatterns    []string `json:"url_patterns"`
	MaxPages       int      `json:"max_pages"`
	MaxDepth       int      `json:"max_depth"`
	MaxDurationS   float64  `json:"max_duration_s"`
	Rendering      string   `json:"rendering"`
	CorrelationID  string   `json:"correlation_id,omitempty"`
}

type DiscoveredLink struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	FoundOn string `json:"found_on"`
	Depth   int    `json:"depth"`
}

type DiscoverLinksResponse struct {
	Links        []DiscoveredLink `json:"links"`
	PagesFetched int              `json:"pages_fetched"`
	Truncated    bool             `json:"truncated"`
	ErrorDetail  string           `json:"error_detail"`
}

func ToFetchRequest(r domain.FetchRequest) FetchArticleRequest {
	render := string(r.Rendering)
	if render == "" {
		render = string(domain.RenderHTTPOnly)
	}
	return FetchArticleRequest{URL: r.URL, Rendering: render, SourceID: r.SourceID, TimeoutS: r.Timeout.Seconds(), CorrelationID: r.CorrelationID}
}

var statusFromWire = map[string]domain.ExtractionStatus{
	"OK": domain.ExtractionOK, "NO_CONTENT": domain.ExtractionNoContent, "FETCH_FAILED": domain.ExtractionFetchFailed,
	"BLOCKED": domain.ExtractionBlocked, "ROBOTS_DISALLOWED": domain.ExtractionRobotsDisallowed, "UNSUPPORTED": domain.ExtractionUnsupported,
}

// ToArticle converts the wire form. An unknown status is treated as a failed
// fetch rather than success.
func (r ArticleResponse) ToArticle() *domain.Article {
	a := &domain.Article{
		RequestedURL: r.RequestedURL, FinalURL: r.FinalURL, CanonicalURL: r.CanonicalURL, Title: r.Title,
		Publisher: r.Publisher, Author: r.Author, Text: r.Text, Description: r.Description, Language: r.Language,
		HTTPStatus: r.HTTPStatus, ContentType: r.ContentType, Rendered: r.Rendered, ErrorDetail: r.ErrorDetail,
		ExtractionStatus: statusFromWire[r.ExtractionStatus],
	}
	if a.ExtractionStatus == "" {
		a.ExtractionStatus = domain.ExtractionFetchFailed
	}
	if r.PublishedAt != nil {
		if t, err := time.Parse(time.RFC3339, *r.PublishedAt); err == nil {
			a.PublishedAt = &t
		}
	}
	if t, err := time.Parse(time.RFC3339, r.FetchedAt); err == nil {
		a.FetchedAt = t
	}
	return a
}

func ToLinksRequest(r domain.DiscoverLinksRequest) DiscoverLinksRequest {
	render := string(r.Rendering)
	if render == "" {
		render = string(domain.RenderHTTPOnly)
	}
	return DiscoverLinksRequest{
		SeedURL: r.SeedURL, AllowedDomains: nonNil(r.AllowedDomains), URLPatterns: nonNil(r.URLPatterns),
		MaxPages: r.MaxPages, MaxDepth: r.MaxDepth, MaxDurationS: r.MaxDuration.Seconds(), Rendering: render, CorrelationID: r.CorrelationID,
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (r DiscoverLinksResponse) ToResult() *domain.DiscoverLinksResult {
	out := &domain.DiscoverLinksResult{PagesFetched: r.PagesFetched, Truncated: r.Truncated, ErrorDetail: r.ErrorDetail}
	for _, l := range r.Links {
		out.Links = append(out.Links, domain.DiscoveredLink{URL: l.URL, Title: l.Title, FoundOn: l.FoundOn, Depth: l.Depth})
	}
	return out
}
