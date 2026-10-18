package registry

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/domain"
)

type noopLinks struct{}

func (noopLinks) DiscoverLinks(context.Context, domain.DiscoverLinksRequest) (*domain.DiscoverLinksResult, error) {
	return &domain.DiscoverLinksResult{}, nil
}

func TestAllSourceTypesRegistered(t *testing.T) {
	r := New(Deps{HTTP: http.DefaultClient, Crawler: noopLinks{}})
	want := []string{"atom", "gdelt", "googlenews", "index_page", "news_sitemap", "police_press", "rss", "sitemap"}
	if got := r.Types(); !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for _, typ := range want {
		p, err := r.Provider(typ)
		if err != nil {
			t.Fatal(err)
		}
		_ = p
	}
}

func TestEveryTypeIsDescribedAndCrawlTypesDescribeWithoutACrawler(t *testing.T) {
	r := New(Deps{HTTP: http.DefaultClient}) // as in the API process: no crawler, nothing runs
	for _, d := range r.Descriptors() {
		if d.Type == "" || d.Label == "" || d.Description == "" {
			t.Errorf("incomplete descriptor: %+v", d)
		}
		if d.URL != discovery.URLRequired && d.URL != discovery.URLNone {
			t.Errorf("%s: bad URL use %q", d.Type, d.URL)
		}
		if d.UsesQueries && d.URL != discovery.URLNone {
			t.Errorf("%s: a search provider takes no URL", d.Type)
		}
	}
	d, ok := r.Describe(discovery.TypePolicePress)
	if !ok || !d.Crawl.RequireDomains {
		t.Fatalf("police_press must describe itself and require allowed domains: %+v", d)
	}
	if _, err := r.Discover(context.Background(), discovery.Request{Source: discovery.Source{Slug: "p", Type: discovery.TypePolicePress, URL: "https://x.test/"}}); err == nil {
		t.Fatal("running a crawled type without a crawler must fail clearly, not panic")
	}
}
