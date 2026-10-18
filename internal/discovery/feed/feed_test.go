package feed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
)

const rssDoc = `<?xml version="1.0"?><rss version="2.0"><channel><title>Oakland Gazette</title><language>en-us</language>
<item><title>Pedestrian killed on Foothill Blvd</title><link>https://gazette.example.com/a1?utm_source=rss</link><pubDate>Fri, 16 Oct 2026 14:00:00 GMT</pubDate></item>
<item><title>Old story</title><link>https://gazette.example.com/old</link><pubDate>Mon, 01 Jun 2026 14:00:00 GMT</pubDate></item>
<item><title>No date story</title><link>https://gazette.example.com/nodate</link></item>
<item><title>Linkless</title></item>
</channel></rss>`

const atomDoc = `<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>PD Press</title>
<entry><title>Traffic fatality update</title><link rel="alternate" href="https://pd.example.gov/press/42"/><updated>2026-10-16T09:00:00Z</updated></entry>
<entry><title>Ancient</title><link href="https://pd.example.gov/press/1"/><updated>2020-01-01T00:00:00Z</updated></entry></feed>`

func window() discovery.Window {
	return discovery.Window{Since: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)}
}

func run(t *testing.T, status int, body, typ string) (*discovery.Result, error) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); w.Write([]byte(body)) }))
	defer srv.Close()
	return New(srv.Client()).Discover(context.Background(), discovery.Request{Source: discovery.Source{Slug: "s", Type: typ, URL: srv.URL}, Window: window()})
}

func TestRSSFiltersWindowKeepsUndated(t *testing.T) {
	res, err := run(t, 200, rssDoc, discovery.TypeRSS)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("want in-window + undated, got %+v", res.Candidates)
	}
	c := res.Candidates[0]
	if c.Title != "Pedestrian killed on Foothill Blvd" || c.Publisher != "Oakland Gazette" || c.PublishedAt == nil || c.Language != "en-us" {
		t.Fatalf("%+v", c)
	}
	if res.Candidates[1].PublishedAt != nil || len(res.Warnings) != 1 {
		t.Fatalf("undated item should be kept with a warning: %+v", res)
	}
}

func TestAtomParsedByTheSameProvider(t *testing.T) {
	res, err := run(t, 200, atomDoc, discovery.TypeAtom)
	if err != nil || len(res.Candidates) != 1 || res.Candidates[0].URL != "https://pd.example.gov/press/42" || res.Candidates[0].Provider != "atom" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestErrors(t *testing.T) {
	if _, err := run(t, 500, "", discovery.TypeRSS); err == nil {
		t.Fatal("500")
	}
	if _, err := run(t, 200, "<html>not a feed</html>", discovery.TypeRSS); err == nil {
		t.Fatal("garbage")
	}
	var rl *discovery.RateLimitedError
	if _, err := run(t, 429, "", discovery.TypeRSS); err == nil || !asRL(err, &rl) {
		t.Fatalf("429 -> %v", err)
	}
	if _, err := New(http.DefaultClient).Discover(context.Background(), discovery.Request{Source: discovery.Source{Slug: "x"}}); err == nil {
		t.Fatal("missing url")
	}
}

func asRL(err error, target **discovery.RateLimitedError) bool {
	e, ok := err.(*discovery.RateLimitedError)
	*target = e
	return ok
}
