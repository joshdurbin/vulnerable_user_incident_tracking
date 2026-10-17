// Package registry wires every provider into a discovery.Registry. It is the
// only place that knows the concrete implementations.
package registry

import (
	"net/http"

	"github.com/joshdurbin/fuck_cars/internal/crawler"
	"github.com/joshdurbin/fuck_cars/internal/discovery"
	"github.com/joshdurbin/fuck_cars/internal/discovery/crawllinks"
	"github.com/joshdurbin/fuck_cars/internal/discovery/feed"
	"github.com/joshdurbin/fuck_cars/internal/discovery/gdelt"
	"github.com/joshdurbin/fuck_cars/internal/discovery/googlenews"
	"github.com/joshdurbin/fuck_cars/internal/discovery/sitemap"
)

// Deps are the collaborators providers need.
type Deps struct {
	HTTP *http.Client // retrying client for feeds and sitemaps
	// SearchHTTP is a NON-retrying client for GDELT and Google News, whose strict
	// rate limits the providers enforce themselves. Defaults to HTTP if nil (tests).
	SearchHTTP *http.Client
	Crawler    crawler.LinkDiscoverer // nil disables crawler-backed source types
	// GoogleResolvers overrides the Google News resolver chain (default: legacy decoder only).
	GoogleResolvers []googlenews.Resolver
}

// New builds a registry with all available providers.
func New(d Deps) *discovery.Registry {
	r := discovery.NewRegistry()
	search := d.SearchHTTP
	if search == nil {
		search = d.HTTP
	}
	r.Register(gdelt.New(search))
	gn := []googlenews.Option{}
	if d.GoogleResolvers != nil {
		gn = append(gn, googlenews.WithResolvers(d.GoogleResolvers...))
	}
	r.Register(googlenews.New(search, gn...))
	r.Register(feed.New(d.HTTP), discovery.TypeRSS, discovery.TypeAtom)
	r.Register(sitemap.New(d.HTTP, discovery.TypeSitemap))
	r.Register(sitemap.New(d.HTTP, discovery.TypeNewsSitemap))
	if d.Crawler != nil {
		r.Register(crawllinks.New(d.Crawler, discovery.TypeIndexPage))
		r.Register(crawllinks.New(d.Crawler, discovery.TypePolicePress))
	}
	return r
}
