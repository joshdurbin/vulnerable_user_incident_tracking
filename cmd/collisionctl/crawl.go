package main

import (
	"context"
	"sync"

	"github.com/joshdurbin/fuck_cars/internal/crawler"
	"github.com/joshdurbin/fuck_cars/internal/domain"
	"github.com/joshdurbin/fuck_cars/internal/pipeline"
	"go.temporal.io/sdk/client"
)

// articleFetcher returns the injected fetcher (tests) or one that calls the
// Python crawler worker through Temporal.
func (a *app) articleFetcher() (crawler.Fetcher, func(), error) {
	if a.fetcher != nil {
		return a.fetcher, func() {}, nil
	}
	tc, err := a.temporal()
	if err != nil {
		return nil, nil, err
	}
	return crawler.New(tc, pipeline.TQWorkflow), tc.Close, nil
}

// lazyCrawler connects to Temporal on first use.
type lazyCrawler struct {
	a    *app
	once sync.Once
	tc   client.Client
	c    *crawler.Client
	err  error
}

func (l *lazyCrawler) DiscoverLinks(ctx context.Context, req domain.DiscoverLinksRequest) (*domain.DiscoverLinksResult, error) {
	l.once.Do(func() {
		l.tc, l.err = l.a.temporal()
		if l.err == nil {
			l.c = crawler.New(l.tc, pipeline.TQWorkflow)
		}
	})
	if l.err != nil {
		return nil, l.err
	}
	return l.c.DiscoverLinks(ctx, req)
}

func (l *lazyCrawler) close() {
	if l.tc != nil {
		l.tc.Close()
	}
}
