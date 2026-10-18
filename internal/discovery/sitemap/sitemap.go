// Package sitemap discovers articles from standard XML sitemaps (including
// sitemap indexes) and Google News sitemaps.
//
// Plain sitemaps and indexes use gopher-parse-sitemap. That library has no
// support for the news: extension, so news sitemaps are decoded with the
// standard library against the documented schema.
package sitemap

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	gps "github.com/oxffaa/gopher-parse-sitemap"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
)

const (
	maxBytes         = 50 << 20 // sitemap spec limit (uncompressed)
	defaultMaxURLs   = 500
	maxChildSitemaps = 10
)

type Provider struct {
	client *http.Client
	typ    string
}

// New returns a provider for TypeSitemap or TypeNewsSitemap.
func New(client *http.Client, sourceType string) *Provider {
	return &Provider{client: client, typ: sourceType}
}

func (p *Provider) Type() string { return p.typ }

type entry struct {
	loc, title, publisher, lang string
	when                        *time.Time
}

func (p *Provider) Discover(ctx context.Context, req discovery.Request) (*discovery.Result, error) {
	res := &discovery.Result{}
	if req.Source.URL == "" {
		return nil, fmt.Errorf("sitemap: source %q has no url", req.Source.Slug)
	}
	limit := req.Source.Crawl.MaxPages
	if limit <= 0 {
		limit = defaultMaxURLs
	}
	body, err := p.get(ctx, res, req.Source.URL)
	if err != nil {
		return nil, err
	}
	var entries []entry
	if isIndex(body) {
		children, err := parseIndex(body)
		if err != nil {
			return nil, err
		}
		// Newest child sitemaps first; undated children sort last.
		sort.SliceStable(children, func(i, j int) bool { return after(children[i].when, children[j].when) })
		if len(children) > maxChildSitemaps {
			res.Warnf("sitemap index has %d children; reading the %d most recent", len(children), maxChildSitemaps)
			children = children[:maxChildSitemaps]
		}
		for _, ch := range children {
			if ch.when != nil && ch.when.Before(req.Window.Since) {
				continue // untouched since before the window
			}
			cb, err := p.get(ctx, res, ch.loc)
			if err != nil {
				res.Warnf("child sitemap %s: %v", ch.loc, err)
				continue
			}
			es, err := p.parseURLs(cb)
			if err != nil {
				res.Warnf("child sitemap %s: %v", ch.loc, err)
				continue
			}
			entries = append(entries, es...)
		}
	} else {
		entries, err = p.parseURLs(body)
		if err != nil {
			return nil, err
		}
	}
	for _, e := range entries {
		if e.when != nil && (e.when.Before(req.Window.Since) || !e.when.Before(req.Window.Until)) {
			continue
		}
		if len(res.Candidates) >= limit {
			res.Truncated = true
			res.Warnf("stopped at %d URLs (crawl max_pages)", limit)
			break
		}
		res.Candidates = append(res.Candidates, discovery.Candidate{
			URL: e.loc, Title: e.title, PublishedAt: e.when, Provider: req.Source.Type, Publisher: e.publisher, Language: e.lang,
		})
	}
	return res, nil
}

func after(a, b *time.Time) bool {
	switch {
	case a == nil:
		return false
	case b == nil:
		return true
	}
	return a.After(*b)
}

func (p *Provider) get(ctx context.Context, res *discovery.Result, u string) ([]byte, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
		return nil, &discovery.RateLimitedError{Provider: p.typ, RetryAfter: time.Minute}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sitemap %s: http %d", u, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b { // .xml.gz
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		if b, err = io.ReadAll(io.LimitReader(zr, maxBytes+1)); err != nil {
			return nil, err
		}
	}
	if len(b) > maxBytes {
		return nil, fmt.Errorf("sitemap %s exceeds %d bytes", u, maxBytes)
	}
	return b, nil
}

func isIndex(b []byte) bool {
	head := b
	if len(head) > 2048 {
		head = head[:2048]
	}
	return bytes.Contains(head, []byte("<sitemapindex"))
}

func parseIndex(b []byte) ([]entry, error) {
	var out []entry
	err := gps.ParseIndex(bytes.NewReader(b), func(e gps.IndexEntry) error {
		out = append(out, entry{loc: strings.TrimSpace(e.GetLocation()), when: lastMod(e.GetLastModified())})
		return nil
	})
	return out, err
}

// requireRoot fails unless the document's root element is want, so an HTML error
// or consent page is reported as a failure instead of looking like an empty sitemap.
func requireRoot(b []byte, want string) error {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("not a sitemap: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			if se.Name.Local != want {
				return fmt.Errorf("not a sitemap: root element is <%s>, want <%s>", se.Name.Local, want)
			}
			return nil
		}
	}
}

func (p *Provider) parseURLs(b []byte) ([]entry, error) {
	if err := requireRoot(b, "urlset"); err != nil {
		return nil, err
	}
	if p.typ == discovery.TypeNewsSitemap {
		return parseNews(b)
	}
	var out []entry
	err := gps.Parse(bytes.NewReader(b), func(e gps.Entry) error {
		out = append(out, entry{loc: strings.TrimSpace(e.GetLocation()), when: lastMod(e.GetLastModified())})
		return nil
	})
	return out, err
}

func lastMod(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// parseNews decodes <urlset> with the Google News <news:news> extension.
func parseNews(b []byte) ([]entry, error) {
	var doc struct {
		URLs []struct {
			Loc     string `xml:"loc"`
			LastMod string `xml:"lastmod"`
			News    struct {
				Publication struct {
					Name     string `xml:"name"`
					Language string `xml:"language"`
				} `xml:"publication"`
				Date  string `xml:"publication_date"`
				Title string `xml:"title"`
			} `xml:"news"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("news sitemap: %w", err)
	}
	var out []entry
	for _, u := range doc.URLs {
		e := entry{loc: strings.TrimSpace(u.Loc), title: strings.TrimSpace(u.News.Title),
			publisher: strings.TrimSpace(u.News.Publication.Name), lang: u.News.Publication.Language}
		for _, s := range []string{u.News.Date, u.LastMod} {
			if t := parseTime(s); t != nil {
				e.when = t
				break
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func parseTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
