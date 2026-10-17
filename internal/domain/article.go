// Package domain holds core types independent of transport and storage.
package domain

import "time"

// ExtractionStatus describes the outcome of fetching and extracting an article.
type ExtractionStatus string

const (
	ExtractionOK               ExtractionStatus = "ok"
	ExtractionNoContent        ExtractionStatus = "no_content"
	ExtractionFetchFailed      ExtractionStatus = "fetch_failed"
	ExtractionBlocked          ExtractionStatus = "blocked"
	ExtractionRobotsDisallowed ExtractionStatus = "robots_disallowed"
	ExtractionUnsupported      ExtractionStatus = "unsupported"
)

// RenderingPolicy controls whether a headless browser may be used.
type RenderingPolicy string

const (
	RenderHTTPOnly RenderingPolicy = "http_only"
	RenderAuto     RenderingPolicy = "auto"
	RenderAlways   RenderingPolicy = "always"
)

// Article is the normalized result of fetching one URL.
type Article struct {
	RequestedURL     string           `json:"requested_url"`
	FinalURL         string           `json:"final_url"`
	CanonicalURL     string           `json:"canonical_url,omitempty"`
	Title            string           `json:"title,omitempty"`
	Publisher        string           `json:"publisher,omitempty"`
	Author           string           `json:"author,omitempty"`
	PublishedAt      *time.Time       `json:"published_at,omitempty"`
	Text             string           `json:"text,omitempty"`
	Description      string           `json:"description,omitempty"`
	Language         string           `json:"language,omitempty"`
	HTTPStatus       int              `json:"http_status"`
	ContentType      string           `json:"content_type,omitempty"`
	FetchedAt        time.Time        `json:"fetched_at"`
	Rendered         bool             `json:"rendered"`
	ExtractionStatus ExtractionStatus `json:"extraction_status"`
	ErrorDetail      string           `json:"error_detail,omitempty"`
}

// FetchRequest asks the crawler for one article.
type FetchRequest struct {
	URL           string
	Rendering     RenderingPolicy
	SourceID      string
	Timeout       time.Duration
	CorrelationID string
}
