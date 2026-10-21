package main

import (
	"errors"
	"time"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/googlenews"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/registry"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/sourcefile"
)

// discoveryRegistry returns the injected registry or all real providers.
func (a *app) discoveryRegistry() (*discovery.Registry, func(), error) {
	if a.registry != nil {
		return a.registry, func() {}, nil
	}
	// Crawl-backed source types (index pages, press pages) reach the Python worker through Temporal.
	// The connection is made lazily so RSS, GDELT and Google News runs need no cluster.
	c := lazyCrawler{a: a}
	search := discovery.NewPlainHTTPClient(discovery.DefaultUserAgent, 45*time.Second)
	deps := registry.Deps{
		HTTP:       discovery.NewHTTPClient(discovery.DefaultUserAgent, 30*time.Second),
		SearchHTTP: search,
		Crawler:    &c,
	}
	if a.cfg.ResolveGoogleNews {
		// Opt-in: relies on an undocumented Google endpoint (see googlenews.BatchExecuteResolver).
		deps.GoogleResolvers = []googlenews.Resolver{googlenews.LegacyDecoder{}, googlenews.NewBatchExecuteResolver(search)}
	}
	return registry.New(deps), c.close, nil
}

// loadSourceFile reads a sources/queries YAML file. There are no built-in sources: they live
// in the database, so running without one needs a file.
func loadSourceFile(path string) (sourcefile.File, error) {
	if path == "" {
		return sourcefile.File{}, errors.New("no database configured: give --file with the sources to use, or --database-url / COLLISION_DATABASE_URL")
	}
	return sourcefile.Load(path)
}
