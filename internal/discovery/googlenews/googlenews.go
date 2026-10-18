// Package googlenews discovers articles through Google News RSS search feeds.
//
// Verified behavior: items link to opaque news.google.com/rss/articles/<id>
// redirects, never to the publisher, but each item carries <source url=...>.
// Candidates are returned with NeedsResolution set; a Resolver chain tries to
// recover the publisher URL, and anything unresolved is kept (never crawled
// through Google) so a later retry or another provider can match it.
package googlenews

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/mmcdole/gofeed/rss"
	"golang.org/x/time/rate"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
)

const DefaultBaseURL = "https://news.google.com/rss/search"

// Resolver turns a Google News article URL into the publisher's URL.
type Resolver interface {
	Name() string
	// Resolve returns the publisher URL or an error. It must not return a news.google.com URL.
	Resolve(ctx context.Context, googleURL string) (string, error)
}

type Provider struct {
	baseURL   string
	client    *http.Client
	limiter   *rate.Limiter
	resolvers []Resolver
}

type Option func(*Provider)

func WithBaseURL(u string) Option        { return func(p *Provider) { p.baseURL = u } }
func WithLimiter(l *rate.Limiter) Option { return func(p *Provider) { p.limiter = l } }
func WithResolvers(r ...Resolver) Option { return func(p *Provider) { p.resolvers = r } }

func New(client *http.Client, opts ...Option) *Provider {
	p := &Provider{baseURL: DefaultBaseURL, client: client, limiter: rate.NewLimiter(rate.Every(2*time.Second), 1),
		resolvers: []Resolver{LegacyDecoder{}}}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *Provider) Type() string { return discovery.TypeGoogleNews }

var titleSuffix = regexp.MustCompile(`\s+-\s+[^-]{1,80}$`)

func (p *Provider) Discover(ctx context.Context, req discovery.Request) (*discovery.Result, error) {
	res := &discovery.Result{}
	if len(req.Queries) == 0 {
		return nil, fmt.Errorf("googlenews: no queries configured")
	}
	hl, gl, ceid := locale(req.Source.Coverage)
	var firstErr error
	ok := 0
	for _, q := range req.Queries {
		items, err := p.fetch(ctx, res, q, req.Window, hl, gl, ceid)
		if err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			res.Warnings = append(res.Warnings, fmt.Sprintf("query %q failed: %v", q.Name, err))
			if firstErr == nil {
				firstErr = err
			}
			var rl *discovery.RateLimitedError
			if errors.As(err, &rl) {
				res.Warnings = append(res.Warnings, "rate limited by Google News: remaining queries skipped this run")
				break
			}
			continue
		}
		ok++
		for _, it := range items {
			if it.PublishedAt != nil && (it.PublishedAt.Before(req.Window.Since) || !it.PublishedAt.Before(req.Window.Until)) {
				continue // the feed only filters by day; trim to the exact window
			}
			it.QueryName = q.Name
			p.resolve(ctx, res, &it)
			res.Candidates = append(res.Candidates, it)
		}
	}
	if ok == 0 {
		return res, firstErr
	}
	return res, nil
}

func locale(c discovery.Coverage) (hl, gl, ceid string) {
	country := strings.ToUpper(c.Country)
	if country == "" {
		country = "US"
	}
	return "en-" + country, country, country + ":en"
}

// fetch builds the search URL. Google's after:/before: operators are day-granular,
// so the window is widened by a day and trimmed client-side.
func (p *Provider) fetch(ctx context.Context, res *discovery.Result, q discovery.Query, w discovery.Window, hl, gl, ceid string) ([]discovery.Candidate, error) {
	text := fmt.Sprintf("%s after:%s before:%s", q.Text, w.Since.AddDate(0, 0, -1).UTC().Format("2006-01-02"), w.Until.AddDate(0, 0, 1).UTC().Format("2006-01-02"))
	v := url.Values{"q": {text}, "hl": {hl}, "gl": {gl}, "ceid": {ceid}}
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
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &discovery.RateLimitedError{Provider: discovery.TypeGoogleNews, RetryAfter: time.Minute}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("googlenews: http %d", resp.StatusCode)
	}
	fp := gofeed.NewParser()
	fp.RSSTranslator = &sourceTranslator{}
	feed, err := fp.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("googlenews: parse feed: %w", err)
	}
	out := make([]discovery.Candidate, 0, len(feed.Items))
	for _, it := range feed.Items {
		if it.Link == "" {
			continue
		}
		pub := it.Custom["source"]
		c := discovery.Candidate{
			URL: it.Link, Title: cleanTitle(it.Title, pub), Provider: discovery.TypeGoogleNews, Publisher: pub,
			PublisherURL: it.Custom["source_url"], NeedsResolution: true, Country: gl,
		}
		if it.PublishedParsed != nil {
			t := it.PublishedParsed.UTC()
			c.PublishedAt = &t
		}
		out = append(out, c)
	}
	return out, nil
}

func cleanTitle(title, publisher string) string {
	if publisher != "" {
		return strings.TrimSpace(strings.TrimSuffix(title, " - "+publisher))
	}
	return strings.TrimSpace(titleSuffix.ReplaceAllString(title, ""))
}

// resolve runs the resolver chain; on success the candidate points at the publisher.
func (p *Provider) resolve(ctx context.Context, res *discovery.Result, c *discovery.Candidate) {
	if !strings.Contains(c.URL, "news.google.com") {
		c.NeedsResolution = false
		return
	}
	for _, r := range p.resolvers {
		u, err := r.Resolve(ctx, c.URL)
		if err != nil || u == "" || strings.Contains(u, "news.google.com") {
			continue
		}
		c.RawURL, c.URL, c.NeedsResolution = c.URL, u, false
		return
	}
}

// sourceTranslator keeps RSS <source url="...">Publisher</source>, which gofeed's
// default translator drops.
type sourceTranslator struct{ def gofeed.DefaultRSSTranslator }

func (t *sourceTranslator) Translate(feed interface{}) (*gofeed.Feed, error) {
	f, err := t.def.Translate(feed)
	if err != nil {
		return nil, err
	}
	if rf, ok := feed.(*rss.Feed); ok && len(rf.Items) == len(f.Items) {
		for i, it := range rf.Items {
			if it.Source == nil {
				continue
			}
			if f.Items[i].Custom == nil {
				f.Items[i].Custom = map[string]string{}
			}
			f.Items[i].Custom["source"] = strings.TrimSpace(it.Source.Title)
			f.Items[i].Custom["source_url"] = it.Source.URL
		}
	}
	return f, nil
}
