package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
)

type stubProv struct {
	typ  string
	last discovery.Request
}

func (s *stubProv) Type() string { return s.typ }

// Describe mirrors the real providers closely enough for validation: search types take queries
// and no URL, crawled types need allowed domains, everything else needs a URL.
func (s *stubProv) Describe(t string) discovery.Descriptor {
	switch t {
	case discovery.TypeGDELT, discovery.TypeGoogleNews:
		return discovery.Descriptor{Type: t, URL: discovery.URLNone, UsesQueries: true}
	case discovery.TypeIndexPage, discovery.TypePolicePress:
		return discovery.Descriptor{Type: t, URL: discovery.URLRequired, Crawl: discovery.CrawlFields{AllowedDomains: true, RequireDomains: true}}
	}
	return discovery.Descriptor{Type: t, URL: discovery.URLRequired}
}
func (s *stubProv) Discover(_ context.Context, r discovery.Request) (*discovery.Result, error) {
	s.last = r
	pub := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	return &discovery.Result{Requests: 1, Candidates: []discovery.Candidate{
		{URL: "https://gazette.example.com/a?utm_source=x", Title: "Pedestrian killed", PublishedAt: &pub},
		{URL: "http://gazette.example.com/a", Title: "dup"},
	}}, nil
}

// example is the shipped example source file; tests use it as realistic configuration.
const example = "../../docs/examples/sources.yaml"

func testRegistry() (*discovery.Registry, *stubProv, *stubProv) {
	reg := discovery.NewRegistry()
	rss, gd := &stubProv{typ: "rss"}, &stubProv{typ: "gdelt"}
	reg.Register(rss, "rss", "atom", "googlenews", "sitemap", "news_sitemap", "index_page", "police_press")
	reg.Register(gd)
	return reg, rss, gd
}

func TestSourcesValidateExamples(t *testing.T) {
	reg, _, _ := testRegistry()
	out, _, err := run(t, deps{registry: reg}, "sources", "validate", "--file", example)
	if err != nil || !strings.Contains(out, "OK:") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestSourcesValidateCatchesBadFile(t *testing.T) {
	reg, _, _ := testRegistry()
	f := filepath.Join(t.TempDir(), "s.yaml")
	os.WriteFile(f, []byte("sources:\n  - {slug: bad, name: Bad, type: rss, poll_interval: 30m}\nqueries:\n  - {family: p, name: q, text: 'a OR b'}\n"), 0o600)
	_, errw, err := run(t, deps{registry: reg}, "sources", "validate", "--file", f)
	if err == nil || !strings.Contains(errw, "absolute http(s) url") || !strings.Contains(errw, "OR must be inside parentheses") {
		t.Fatalf("err=%v stderr=%s", err, errw)
	}
}
