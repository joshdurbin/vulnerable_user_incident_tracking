// Package gdelt discovers articles through the GDELT DOC 2.0 API.
//
// Verified behavior: mode=artlist&format=json returns at most 250 articles per
// call with no pagination, so a window that returns the cap is split in half and
// both halves are fetched. The API asks for about one request per 5 seconds;
// the provider enforces that with a shared limiter.
package gdelt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/joshdurbin/fuck_cars/internal/discovery"
)

const (
	DefaultBaseURL = "https://api.gdeltproject.org/api/v2/doc/doc"
	maxRecords     = 250
	timeLayout     = "20060102150405"
	seenLayout     = "20060102T150405Z"
)

type Provider struct {
	baseURL   string
	client    *http.Client
	limiter   *rate.Limiter
	minWindow time.Duration // never split below this; avoids unbounded recursion
	maxSplits int
}

// Option customizes a Provider.
type Option func(*Provider)

func WithBaseURL(u string) Option          { return func(p *Provider) { p.baseURL = u } }
func WithLimiter(l *rate.Limiter) Option   { return func(p *Provider) { p.limiter = l } }
func WithMinWindow(d time.Duration) Option { return func(p *Provider) { p.minWindow = d } }

func New(client *http.Client, opts ...Option) *Provider {
	p := &Provider{baseURL: DefaultBaseURL, client: client, limiter: rate.NewLimiter(rate.Every(5*time.Second), 1),
		minWindow: 15 * time.Minute, maxSplits: 6}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *Provider) Type() string { return discovery.TypeGDELT }

type article struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	SeenDate string `json:"seendate"`
	Domain   string `json:"domain"`
	Language string `json:"language"`
	Country  string `json:"sourcecountry"`
}

func (p *Provider) Discover(ctx context.Context, req discovery.Request) (*discovery.Result, error) {
	res := &discovery.Result{}
	if len(req.Queries) == 0 {
		return nil, fmt.Errorf("gdelt: no queries configured")
	}
	var firstErr error
	ok := 0
	for _, q := range req.Queries {
		text := buildQuery(q, req.Source.Coverage)
		if err := p.fetchWindow(ctx, res, q, text, req.Window, 0); err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			res.Warnings = append(res.Warnings, fmt.Sprintf("query %q failed: %v", q.Name, err))
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ok++
	}
	if ok == 0 {
		return res, firstErr
	}
	return res, nil
}

// buildQuery adds GDELT filter operators. Multi-word OR groups must be parenthesized by the query author.
func buildQuery(q discovery.Query, cov discovery.Coverage) string {
	parts := []string{q.Text}
	country := cov.Country
	if country == "" {
		country = "US"
	}
	parts = append(parts, "sourcecountry:"+country, "sourcelang:english")
	return strings.Join(parts, " ")
}

func (p *Provider) fetchWindow(ctx context.Context, res *discovery.Result, q discovery.Query, text string, w discovery.Window, depth int) error {
	arts, err := p.fetch(ctx, res, text, w)
	if err != nil {
		return err
	}
	if len(arts) >= maxRecords {
		if w.Duration() > 2*p.minWindow && depth < p.maxSplits {
			mid := w.Since.Add(w.Duration() / 2)
			if err := p.fetchWindow(ctx, res, q, text, discovery.Window{Since: w.Since, Until: mid}, depth+1); err != nil {
				return err
			}
			return p.fetchWindow(ctx, res, q, text, discovery.Window{Since: mid, Until: w.Until}, depth+1)
		}
		res.Truncated = true
		res.Warnings = append(res.Warnings, fmt.Sprintf("query %q: window %s..%s still returned the %d-article cap; results truncated",
			q.Name, w.Since.Format(time.RFC3339), w.Until.Format(time.RFC3339), maxRecords))
	}
	for _, a := range arts {
		if a.URL == "" {
			continue
		}
		c := discovery.Candidate{
			URL: a.URL, Title: strings.TrimSpace(a.Title), Provider: discovery.TypeGDELT, QueryName: q.Name,
			Publisher: a.Domain, Language: a.Language, Country: a.Country,
		}
		if t, err := time.Parse(seenLayout, a.SeenDate); err == nil {
			c.PublishedAt = &t // GDELT's first-seen time, a proxy for publication
		}
		res.Candidates = append(res.Candidates, c)
	}
	return nil
}

func (p *Provider) fetch(ctx context.Context, res *discovery.Result, text string, w discovery.Window) ([]article, error) {
	v := url.Values{}
	v.Set("query", text)
	v.Set("mode", "artlist")
	v.Set("format", "json")
	v.Set("maxrecords", fmt.Sprint(maxRecords))
	v.Set("sort", "datedesc")
	v.Set("startdatetime", w.Since.UTC().Format(timeLayout))
	v.Set("enddatetime", w.Until.UTC().Format(timeLayout))

	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	res.Requests++
	resp, err := p.client.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &discovery.RateLimitedError{Provider: discovery.TypeGDELT, RetryAfter: 5 * time.Second}
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("gdelt: http %d: %s", resp.StatusCode, snippet(body))
	}
	// GDELT reports query errors as plain text with HTTP 200; an empty result is "{}".
	trim := strings.TrimSpace(string(body))
	if trim == "" || trim == "{}" {
		return nil, nil
	}
	if !strings.HasPrefix(trim, "{") {
		return nil, fmt.Errorf("gdelt: query rejected: %s", snippet(body))
	}
	var out struct {
		Articles []article `json:"articles"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("gdelt: decode: %w", err)
	}
	return out.Articles, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
