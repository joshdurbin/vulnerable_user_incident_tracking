package main

import (
	"context"
	"sync"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/crawler"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/domain"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/pipeline"
	"go.temporal.io/sdk/client"
)

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
