package crawllinks

import (
	"context"
	"errors"
	"testing"

	"github.com/joshdurbin/fuck_cars/internal/discovery"
	"github.com/joshdurbin/fuck_cars/internal/domain"
)

type fake struct {
	req domain.DiscoverLinksRequest
	out *domain.DiscoverLinksResult
	err error
}

func (f *fake) DiscoverLinks(_ context.Context, r domain.DiscoverLinksRequest) (*domain.DiscoverLinksResult, error) {
	f.req = r
	return f.out, f.err
}

func src() discovery.Source {
	return discovery.Source{Slug: "oak-pd", Name: "Oakland PD", Type: discovery.TypePolicePress, URL: "https://pd.example.gov/press",
		Crawl: discovery.CrawlPolicy{AllowedDomains: []string{"example.gov"}, URLPatterns: []string{`/press/\d+`}, MaxPages: 3, MaxDepth: 2, Render: "auto"}}
}

func TestMapsCrawlPolicyToBoundedRequest(t *testing.T) {
	f := &fake{out: &domain.DiscoverLinksResult{Links: []domain.DiscoveredLink{{URL: "https://pd.example.gov/press/7", Title: "Release 7"}}, Truncated: true}}
	res, err := New(f, discovery.TypePolicePress).Discover(context.Background(), discovery.Request{Source: src()})
	if err != nil || len(res.Candidates) != 1 || !res.Truncated || res.Candidates[0].Publisher != "Oakland PD" {
		t.Fatalf("%+v %v", res, err)
	}
	r := f.req
	if r.SeedURL != "https://pd.example.gov/press" || r.MaxPages != 3 || r.MaxDepth != 2 || r.Rendering != domain.RenderAuto ||
		r.AllowedDomains[0] != "example.gov" || r.MaxDuration == 0 {
		t.Fatalf("%+v", r)
	}
}

func TestDefaultsAreBounded(t *testing.T) {
	f := &fake{out: &domain.DiscoverLinksResult{}}
	s := src()
	s.Crawl = discovery.CrawlPolicy{}
	if _, err := New(f, discovery.TypeIndexPage).Discover(context.Background(), discovery.Request{Source: s}); err != nil {
		t.Fatal(err)
	}
	if f.req.MaxPages != defaultPages || f.req.MaxDepth != defaultDepth || f.req.Rendering != domain.RenderHTTPOnly {
		t.Fatalf("%+v", f.req)
	}
}

func TestErrorHandling(t *testing.T) {
	if _, err := New(&fake{err: errors.New("unavailable")}, discovery.TypeIndexPage).Discover(context.Background(), discovery.Request{Source: src()}); err == nil {
		t.Fatal("rpc error")
	}
	// Crawler-level failure with no links is an error; with links it is a warning.
	if _, err := New(&fake{out: &domain.DiscoverLinksResult{ErrorDetail: "blocked by robots"}}, discovery.TypeIndexPage).Discover(context.Background(), discovery.Request{Source: src()}); err == nil {
		t.Fatal("expected error")
	}
	res, err := New(&fake{out: &domain.DiscoverLinksResult{ErrorDetail: "depth 2 failed", Links: []domain.DiscoveredLink{{URL: "https://x/1"}}}}, discovery.TypeIndexPage).
		Discover(context.Background(), discovery.Request{Source: src()})
	if err != nil || len(res.Warnings) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	s := src()
	s.URL = ""
	if _, err := New(&fake{}, discovery.TypeIndexPage).Discover(context.Background(), discovery.Request{Source: s}); err == nil {
		t.Fatal("missing url")
	}
}
