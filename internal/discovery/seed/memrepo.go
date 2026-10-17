package seed

import (
	"context"
	"fmt"
	"time"

	"github.com/joshdurbin/fuck_cars/internal/discovery"
)

// MemoryRepo is a read-only discovery.Repository over seed files. It lets
// `discover --dry-run` and tests run with no database. Record treats every URL as
// new and persists nothing.
type MemoryRepo struct{ F File }

var _ discovery.Repository = MemoryRepo{}

func (m MemoryRepo) Source(_ context.Context, slug string) (discovery.Source, error) {
	for _, s := range m.F.Sources {
		if s.Slug == slug {
			return s.ToSource(), nil
		}
	}
	return discovery.Source{}, fmt.Errorf("unknown source %q", slug)
}

func (m MemoryRepo) Sources(_ context.Context, enabledOnly bool) ([]discovery.Source, error) {
	var out []discovery.Source
	for _, s := range m.F.Sources {
		if !enabledOnly || s.IsEnabled() {
			out = append(out, s.ToSource())
		}
	}
	return out, nil
}

func (m MemoryRepo) Queries(_ context.Context, providerType string) ([]discovery.Query, error) {
	var out []discovery.Query
	for _, q := range m.F.Queries {
		if !q.IsEnabled() {
			continue
		}
		if len(q.Providers) > 0 {
			match := false
			for _, p := range q.Providers {
				match = match || p == providerType
			}
			if !match {
				continue
			}
		}
		out = append(out, discovery.Query{Family: q.Family, Name: q.Name, Text: q.Text, Region: q.Region})
	}
	return out, nil
}

func (m MemoryRepo) Record(_ context.Context, _ discovery.Source, items []discovery.Deduped) (discovery.RecordResult, error) {
	var out discovery.RecordResult
	for _, it := range items {
		out.New = append(out.New, discovery.NewURL{URL: it.Normalized, Host: it.Host, Title: it.Primary.Title})
	}
	return out, nil
}

func (MemoryRepo) MarkResult(context.Context, string, time.Time, error) error { return nil }
func (MemoryRepo) Stats(context.Context, time.Time) ([]discovery.SourceStats, error) {
	return nil, fmt.Errorf("stats need a database")
}
