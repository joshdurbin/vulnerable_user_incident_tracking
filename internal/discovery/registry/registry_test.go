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

func TestCrawlerBackedTypesAbsentWithoutCrawler(t *testing.T) {
	r := New(Deps{HTTP: http.DefaultClient})
	if _, err := r.Provider(discovery.TypePolicePress); err == nil {
		t.Fatal("police_press needs the crawler")
	}
	if _, err := r.Provider(discovery.TypeRSS); err != nil {
		t.Fatal(err)
	}
}
