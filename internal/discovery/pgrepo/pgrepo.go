// Package pgrepo is the PostgreSQL implementation of discovery.Repository,
// built entirely on the sqlc-generated queries.
package pgrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/seed"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage/dbgen"
)

type Repo struct{ store *storage.Store }

func New(s *storage.Store) *Repo { return &Repo{store: s} }

var _ discovery.Repository = (*Repo)(nil)

func toSource(r dbgen.DiscoverySource) (discovery.Source, error) {
	s := discovery.Source{
		Slug: r.Slug, Name: r.Name, Type: r.SourceType, Enabled: r.Enabled, PollInterval: time.Duration(r.PollIntervalSeconds) * time.Second,
		RatePerMin: int(r.RateLimitPerMinute), LastSuccessAt: r.LastSuccessAt, ConsecutiveFailures: int(r.ConsecutiveFailures),
	}
	if r.Url != nil {
		s.URL = *r.Url
	}
	if r.ErrorState != nil {
		s.ErrorState = *r.ErrorState
	}
	if err := json.Unmarshal(r.Coverage, &s.Coverage); err != nil {
		return s, fmt.Errorf("source %s coverage: %w", r.Slug, err)
	}
	if err := json.Unmarshal(r.CrawlPolicy, &s.Crawl); err != nil {
		return s, fmt.Errorf("source %s crawl_policy: %w", r.Slug, err)
	}
	return s, nil
}

func (r *Repo) Source(ctx context.Context, slug string) (discovery.Source, error) {
	row, err := r.store.Queries().GetSourceBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return discovery.Source{}, fmt.Errorf("unknown source %q", slug)
	}
	if err != nil {
		return discovery.Source{}, err
	}
	return toSource(row)
}

func (r *Repo) Sources(ctx context.Context, enabledOnly bool) ([]discovery.Source, error) {
	rows, err := r.store.Queries().ListSources(ctx, enabledOnly)
	if err != nil {
		return nil, err
	}
	out := make([]discovery.Source, 0, len(rows))
	for _, row := range rows {
		s, err := toSource(row)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *Repo) Queries(ctx context.Context, providerType string) ([]discovery.Query, error) {
	rows, err := r.store.Queries().ListQueriesForProvider(ctx, providerType)
	if err != nil {
		return nil, err
	}
	out := make([]discovery.Query, 0, len(rows))
	for _, q := range rows {
		dq := discovery.Query{Family: q.Family, Name: q.Name, Text: q.QueryText}
		if q.Region != nil {
			dq.Region = *q.Region
		}
		out = append(out, dq)
	}
	return out, nil
}

// Record persists unique URLs and every sighting of each, in one transaction.
func (r *Repo) Record(ctx context.Context, src discovery.Source, items []discovery.Deduped) (discovery.RecordResult, error) {
	var out discovery.RecordResult
	err := r.store.InTx(ctx, func(q *dbgen.Queries) error {
		srcRow, err := q.GetSourceBySlug(ctx, src.Slug)
		var srcID *uuid.UUID
		switch {
		case err == nil:
			srcID = &srcRow.ID
		case errors.Is(err, pgx.ErrNoRows): // ad-hoc source (dry config): record without a source link
		default:
			return err
		}
		queryIDs := map[string]*uuid.UUID{}
		queryID := func(name string) (*uuid.UUID, error) {
			if name == "" {
				return nil, nil
			}
			if id, ok := queryIDs[name]; ok {
				return id, nil
			}
			id, err := q.GetQueryIDByName(ctx, name)
			if errors.Is(err, pgx.ErrNoRows) {
				queryIDs[name] = nil
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			queryIDs[name] = &id
			return &id, nil
		}
		var known []discovery.NewURL
		for _, it := range items {
			u, err := q.UpsertDiscoveredURL(ctx, dbgen.UpsertDiscoveredURLParams{NormalizedUrl: it.Normalized, Domain: it.Host})
			if err != nil {
				return err
			}
			nu := discovery.NewURL{ID: u.ID, URL: it.Normalized, Host: it.Host, Title: it.Primary.Title}
			if u.Inserted {
				out.New = append(out.New, nu)
			} else {
				out.Known++
				known = append(known, nu)
			}
			for _, c := range it.Found {
				qid, err := queryID(c.QueryName)
				if err != nil {
					return err
				}
				raw := c.RawURL
				if raw == "" {
					raw = c.URL
				}
				var title *string
				if c.Title != "" {
					title = &c.Title
				}
				n, err := q.InsertArticleDiscovery(ctx, dbgen.InsertArticleDiscoveryParams{
					DiscoveredUrlID: u.ID, Provider: orDefault(c.Provider, src.Type), SourceID: srcID, QueryID: qid,
					PublishedAt: c.PublishedAt, RawTitle: title, RawUrl: raw,
				})
				if err != nil {
					return err
				}
				out.Provenance += int(n)
			}
		}
		if len(known) > 0 {
			ids := make([]uuid.UUID, len(known))
			for i, k := range known {
				ids[i] = k.ID
			}
			missing, err := q.ListURLsWithoutArticle(ctx, ids)
			if err != nil {
				return err
			}
			byID := map[uuid.UUID]discovery.NewURL{}
			for _, k := range known {
				byID[k.ID] = k
			}
			for _, m := range missing {
				out.Pending = append(out.Pending, byID[m.ID])
			}
		}
		return nil
	})
	return out, err
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (r *Repo) MarkResult(ctx context.Context, slug string, at time.Time, runErr error) error {
	q := r.store.Queries()
	if runErr == nil {
		return q.MarkSourceSuccess(ctx, dbgen.MarkSourceSuccessParams{At: &at, Slug: slug})
	}
	msg := runErr.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return q.MarkSourceFailure(ctx, dbgen.MarkSourceFailureParams{At: &at, Err: &msg, Slug: slug})
}

func (r *Repo) Stats(ctx context.Context, since time.Time) ([]discovery.SourceStats, error) {
	rows, err := r.store.Queries().DiscoveryStats(ctx, since)
	if err != nil {
		return nil, err
	}
	out := make([]discovery.SourceStats, 0, len(rows))
	for _, x := range rows {
		out = append(out, discovery.SourceStats{
			Provider: x.Provider, SourceSlug: x.SourceSlug, Discoveries: x.Discoveries, UniqueURLs: x.UniqueUrls,
			ExclusiveURLs: x.ExclusiveUrls, AvgDelaySeconds: x.AvgDelaySeconds, ArticlesFetched: x.ArticlesFetched,
			FetchOK: x.FetchOk, RelevantArticles: x.RelevantArticles, Incidents: x.Incidents,
		})
	}
	return out, nil
}

// Sync upserts a validated seed file (sources and queries) into the database.
func (r *Repo) Sync(ctx context.Context, f seed.File) (sources, queries int, err error) {
	err = r.store.InTx(ctx, func(q *dbgen.Queries) error {
		for _, s := range f.Sources {
			cov, _ := json.Marshal(s.Coverage)
			crawl, _ := json.Marshal(s.Crawl)
			var u *string
			if s.URL != "" {
				u = &s.URL
			}
			rate := int32(s.RateLimitPerMinute)
			if rate == 0 {
				rate = 30
			}
			if _, err := q.UpsertSource(ctx, dbgen.UpsertSourceParams{
				Slug: s.Slug, Name: s.Name, SourceType: s.Type, Url: u, Coverage: cov,
				PollIntervalSeconds: int32(s.PollInterval / time.Second), Enabled: s.IsEnabled(), CrawlPolicy: crawl, RateLimitPerMinute: rate,
			}); err != nil {
				return fmt.Errorf("source %s: %w", s.Slug, err)
			}
			sources++
		}
		for _, qs := range f.Queries {
			var region *string
			if qs.Region != "" {
				region = &qs.Region
			}
			provs := qs.Providers
			if provs == nil {
				provs = []string{}
			}
			if _, err := q.UpsertQuery(ctx, dbgen.UpsertQueryParams{
				Family: qs.Family, Name: qs.Name, QueryText: qs.Text, Providers: provs, Region: region, Enabled: qs.IsEnabled(),
			}); err != nil {
				return fmt.Errorf("query %s: %w", qs.Name, err)
			}
			queries++
		}
		return nil
	})
	return
}

// SyncNew adds sources and queries from the seed that do not exist yet and leaves existing
// rows exactly as they are, so re-running it after an operator disabled a source or edited
// a query changes nothing. It returns how many rows were added.
func (r *Repo) SyncNew(ctx context.Context, f seed.File) (sources, queries int, err error) {
	err = r.store.InTx(ctx, func(q *dbgen.Queries) error {
		for _, s := range f.Sources {
			cov, _ := json.Marshal(s.Coverage)
			crawl, _ := json.Marshal(s.Crawl)
			var u *string
			if s.URL != "" {
				u = &s.URL
			}
			rate := int32(s.RateLimitPerMinute)
			if rate == 0 {
				rate = 30
			}
			n, err := q.InsertSourceIfMissing(ctx, dbgen.InsertSourceIfMissingParams{
				Slug: s.Slug, Name: s.Name, SourceType: s.Type, Url: u, Coverage: cov,
				PollIntervalSeconds: int32(s.PollInterval / time.Second), Enabled: s.IsEnabled(), CrawlPolicy: crawl, RateLimitPerMinute: rate,
			})
			if err != nil {
				return fmt.Errorf("source %s: %w", s.Slug, err)
			}
			sources += int(n)
		}
		for _, qs := range f.Queries {
			var region *string
			if qs.Region != "" {
				region = &qs.Region
			}
			provs := qs.Providers
			if provs == nil {
				provs = []string{}
			}
			n, err := q.InsertQueryIfMissing(ctx, dbgen.InsertQueryIfMissingParams{
				Family: qs.Family, Name: qs.Name, QueryText: qs.Text, Providers: provs, Region: region, Enabled: qs.IsEnabled(),
			})
			if err != nil {
				return fmt.Errorf("query %s: %w", qs.Name, err)
			}
			queries += int(n)
		}
		return nil
	})
	return
}
