// Package feed discovers articles from publisher RSS and Atom feeds (parsed by gofeed).
package feed

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
)

type Provider struct {
	client *http.Client
	maxLen int64
}

func New(client *http.Client) *Provider { return &Provider{client: client, maxLen: 10 << 20} }

// Type returns "rss"; register the same provider for "atom" too (gofeed reads both).
func (p *Provider) Type() string { return discovery.TypeRSS }

func (p *Provider) Discover(ctx context.Context, req discovery.Request) (*discovery.Result, error) {
	res := &discovery.Result{}
	if req.Source.URL == "" {
		return nil, fmt.Errorf("feed: source %q has no url", req.Source.Slug)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.Source.URL, nil)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.8, */*;q=0.5")
	res.Requests++
	resp, err := p.client.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &discovery.RateLimitedError{Provider: req.Source.Type, RetryAfter: time.Minute}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("feed %s: http %d", req.Source.Slug, resp.StatusCode)
	}
	parsed, err := gofeed.NewParser().Parse(io.LimitReader(resp.Body, p.maxLen))
	if err != nil {
		return nil, fmt.Errorf("feed %s: parse: %w", req.Source.Slug, err)
	}
	publisher := strings.TrimSpace(parsed.Title)
	undated := 0
	for _, it := range parsed.Items {
		if it.Link == "" {
			continue
		}
		var when *time.Time
		switch {
		case it.PublishedParsed != nil:
			when = it.PublishedParsed
		case it.UpdatedParsed != nil:
			when = it.UpdatedParsed
		}
		if when != nil {
			t := when.UTC()
			if t.Before(req.Window.Since) || !t.Before(req.Window.Until) {
				continue
			}
			when = &t
		} else {
			undated++ // keep: later dedupe against known URLs drops repeats
		}
		res.Candidates = append(res.Candidates, discovery.Candidate{
			URL: it.Link, Title: strings.TrimSpace(it.Title), PublishedAt: when, Provider: req.Source.Type,
			Publisher: publisher, Language: parsed.Language,
		})
	}
	if undated > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d items had no date and were kept", undated))
	}
	return res, nil
}
