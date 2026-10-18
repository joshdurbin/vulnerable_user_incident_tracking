package sitemap

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
)

func window() discovery.Window {
	return discovery.Window{Since: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)}
}

const urlset = `<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
<url><loc>https://gazette.example.com/a</loc><lastmod>2026-10-16T10:00:00Z</lastmod></url>
<url><loc>https://gazette.example.com/old</loc><lastmod>2025-01-01</lastmod></url>
<url><loc>https://gazette.example.com/undated</loc></url></urlset>`

const newsSet = `<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:news="http://www.google.com/schemas/sitemap-news/0.9">
<url><loc>https://gazette.example.com/n1</loc><news:news><news:publication><news:name>Example Gazette</news:name><news:language>en</news:language></news:publication>
<news:publication_date>2026-10-16T08:30:00-07:00</news:publication_date><news:title>Cyclist hit in crosswalk</news:title></news:news></url>
<url><loc>https://gazette.example.com/n-old</loc><news:news><news:publication><news:name>Example Gazette</news:name><news:language>en</news:language></news:publication>
<news:publication_date>2026-09-01</news:publication_date><news:title>Old</news:title></news:news></url></urlset>`

func serve(t *testing.T, routes map[string]string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func discover(t *testing.T, typ, url string, crawl discovery.CrawlPolicy) (*discovery.Result, error) {
	return New(http.DefaultClient, typ).Discover(context.Background(), discovery.Request{
		Source: discovery.Source{Slug: "s", Type: typ, URL: url, Crawl: crawl}, Window: window()})
}

func TestPlainSitemapWindowAndUndated(t *testing.T) {
	srv := serve(t, map[string]string{"/sitemap.xml": urlset})
	res, err := discover(t, discovery.TypeSitemap, srv.URL+"/sitemap.xml", discovery.CrawlPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	urls := map[string]bool{}
	for _, c := range res.Candidates {
		urls[c.URL] = true
	}
	if len(urls) != 2 || !urls["https://gazette.example.com/a"] || !urls["https://gazette.example.com/undated"] || urls["https://gazette.example.com/old"] {
		t.Fatalf("%v", urls)
	}
}

func TestNewsSitemapKeepsTitleAndPublisher(t *testing.T) {
	srv := serve(t, map[string]string{"/news.xml": newsSet})
	res, err := discover(t, discovery.TypeNewsSitemap, srv.URL+"/news.xml", discovery.CrawlPolicy{})
	if err != nil || len(res.Candidates) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	c := res.Candidates[0]
	if c.Title != "Cyclist hit in crosswalk" || c.Publisher != "Example Gazette" || c.Language != "en" || c.PublishedAt.UTC().Hour() != 15 {
		t.Fatalf("%+v", c)
	}
}

func TestSitemapIndexReadsRecentChildrenOnly(t *testing.T) {
	var base string
	index := func() string {
		return fmt.Sprintf(`<?xml version="1.0"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
<sitemap><loc>%s/new.xml</loc><lastmod>2026-10-16T12:00:00Z</lastmod></sitemap>
<sitemap><loc>%s/stale.xml</loc><lastmod>2024-01-01</lastmod></sitemap></sitemapindex>`, base, base)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.xml":
			w.Write([]byte(index()))
		case "/new.xml":
			w.Write([]byte(urlset))
		case "/stale.xml":
			t.Error("stale child sitemap must not be fetched")
		}
	}))
	defer srv.Close()
	base = srv.URL
	res, err := discover(t, discovery.TypeSitemap, srv.URL+"/index.xml", discovery.CrawlPolicy{})
	if err != nil || len(res.Candidates) != 2 || res.Requests != 2 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestGzippedSitemap(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(urlset))
	zw.Close()
	srv := serve(t, map[string]string{"/s.xml.gz": buf.String()})
	res, err := discover(t, discovery.TypeSitemap, srv.URL+"/s.xml.gz", discovery.CrawlPolicy{})
	if err != nil || len(res.Candidates) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestMaxPagesTruncates(t *testing.T) {
	srv := serve(t, map[string]string{"/s.xml": urlset})
	res, err := discover(t, discovery.TypeSitemap, srv.URL+"/s.xml", discovery.CrawlPolicy{MaxPages: 1})
	if err != nil || len(res.Candidates) != 1 || !res.Truncated {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestErrors(t *testing.T) {
	srv := serve(t, map[string]string{"/bad.xml": "<html>nope</html>"})
	if _, err := discover(t, discovery.TypeNewsSitemap, srv.URL+"/bad.xml", discovery.CrawlPolicy{}); err == nil {
		t.Fatal("bad news xml")
	}
	if _, err := discover(t, discovery.TypeSitemap, srv.URL+"/bad.xml", discovery.CrawlPolicy{}); err == nil {
		t.Fatal("html served as a plain sitemap must be an error, not an empty result")
	}
	if _, err := discover(t, discovery.TypeSitemap, srv.URL+"/missing.xml", discovery.CrawlPolicy{}); err == nil {
		t.Fatal("404")
	}
	if _, err := discover(t, discovery.TypeSitemap, "", discovery.CrawlPolicy{}); err == nil {
		t.Fatal("no url")
	}
}
