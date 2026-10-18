package pgrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/sourcefile"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage/dbgen"
)

// FinishRun stores a run and updates the source's health fields in one transaction. A
// partial run (the provider failed part way but returned candidates) counts as a success
// for health, and its error stays on the run row.
func (r *Repo) FinishRun(ctx context.Context, rec discovery.RunRecord) error {
	return r.store.InTx(ctx, func(q *dbgen.Queries) error {
		var errText *string
		if rec.Error != "" {
			errText = &rec.Error
		}
		warnings := rec.Warnings
		if warnings == nil {
			warnings = []string{}
		}
		since, until := nonZero(rec.Window.Since), nonZero(rec.Window.Until)
		if err := q.InsertDiscoveryRun(ctx, dbgen.InsertDiscoveryRunParams{
			StartedAt: rec.StartedAt, FinishedAt: rec.FinishedAt, WindowSince: since, WindowUntil: until, Status: string(rec.Status),
			Found: int32(rec.Found), UniqueUrls: int32(rec.Unique), NewUrls: int32(rec.New), KnownUrls: int32(rec.Known),
			PendingUrls: int32(rec.Pending), Unresolved: int32(rec.Unresolved), Requests: int32(rec.Requests),
			Truncated: rec.Truncated, Warnings: warnings, Error: errText, Slug: rec.Slug,
		}); err != nil {
			return err
		}
		at := rec.FinishedAt
		if rec.Status != discovery.RunFailed {
			return q.MarkSourceSuccess(ctx, dbgen.MarkSourceSuccessParams{At: &at, Slug: rec.Slug})
		}
		msg := rec.Error
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return q.MarkSourceFailure(ctx, dbgen.MarkSourceFailureParams{At: &at, Err: &msg, Slug: rec.Slug})
	})
}

func nonZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func fromRun(x dbgen.DiscoveryRun, slug string) discovery.RunRecord {
	rec := discovery.RunRecord{
		Slug: slug, StartedAt: x.StartedAt, FinishedAt: x.FinishedAt, Status: discovery.RunStatus(x.Status),
		Found: int(x.Found), Unique: int(x.UniqueUrls), New: int(x.NewUrls), Known: int(x.KnownUrls), Pending: int(x.PendingUrls),
		Unresolved: int(x.Unresolved), Requests: int(x.Requests), Truncated: x.Truncated, Warnings: x.Warnings,
	}
	if x.WindowSince != nil {
		rec.Window.Since = *x.WindowSince
	}
	if x.WindowUntil != nil {
		rec.Window.Until = *x.WindowUntil
	}
	if x.Error != nil {
		rec.Error = *x.Error
	}
	return rec
}

// Runs returns a source's most recent runs, newest first.
func (r *Repo) Runs(ctx context.Context, slug string, limit int) ([]discovery.RunRecord, error) {
	rows, err := r.store.Queries().ListDiscoveryRuns(ctx, dbgen.ListDiscoveryRunsParams{Slug: slug, MaxRows: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]discovery.RunRecord, 0, len(rows))
	for _, x := range rows {
		out = append(out, fromRun(x, slug))
	}
	return out, nil
}

// RunStats returns per-source run statistics since the given time, keyed by slug. Sources
// with no runs appear with zero values.
func (r *Repo) RunStats(ctx context.Context, since time.Time) (map[string]discovery.RunStats, error) {
	rows, err := r.store.Queries().SourceRunStats(ctx, since)
	if err != nil {
		return nil, err
	}
	out := make(map[string]discovery.RunStats, len(rows))
	for _, x := range rows {
		out[x.Slug] = discovery.RunStats{
			Runs: x.Runs, Successes: x.Successes, Partials: x.Partials, Failures: x.Failures,
			AvgSeconds: x.AvgSeconds, P95Seconds: x.P95Seconds, NewURLs: x.NewUrls, Requests: x.Requests,
		}
	}
	return out, nil
}

// PruneRuns deletes run history older than the cutoff.
func (r *Repo) PruneRuns(ctx context.Context, before time.Time) (int64, error) {
	return r.store.Queries().PruneDiscoveryRuns(ctx, before)
}

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

type sourceCols struct {
	url      *string
	coverage []byte
	crawl    []byte
	interval int32
}

func encodeSource(s discovery.Source) (sourceCols, error) {
	cov, err := json.Marshal(s.Coverage)
	if err != nil {
		return sourceCols{}, err
	}
	crawl, err := json.Marshal(s.Crawl)
	if err != nil {
		return sourceCols{}, err
	}
	c := sourceCols{coverage: cov, crawl: crawl, interval: int32(s.PollInterval / time.Second)}
	if s.URL != "" {
		c.url = &s.URL
	}
	return c, nil
}

// CreateSource inserts a new source. It returns discovery.ErrAlreadyExists if the slug is taken.
func (r *Repo) CreateSource(ctx context.Context, s discovery.Source) (discovery.Source, error) {
	c, err := encodeSource(s)
	if err != nil {
		return discovery.Source{}, err
	}
	row, err := r.store.Queries().InsertSource(ctx, dbgen.InsertSourceParams{
		Slug: s.Slug, Name: s.Name, SourceType: s.Type, Url: c.url, Coverage: c.coverage,
		PollIntervalSeconds: c.interval, Enabled: s.Enabled, CrawlPolicy: c.crawl,
	})
	if isUniqueViolation(err) {
		return discovery.Source{}, fmt.Errorf("%w: %s", discovery.ErrAlreadyExists, s.Slug)
	}
	if err != nil {
		return discovery.Source{}, err
	}
	return toSource(row)
}

// UpdateSource replaces a source's configuration (the slug identifies it and cannot change).
// It returns discovery.ErrNotFound if there is no such source.
func (r *Repo) UpdateSource(ctx context.Context, s discovery.Source) (discovery.Source, error) {
	c, err := encodeSource(s)
	if err != nil {
		return discovery.Source{}, err
	}
	row, err := r.store.Queries().UpdateSource(ctx, dbgen.UpdateSourceParams{
		Name: s.Name, SourceType: s.Type, Url: c.url, Coverage: c.coverage, PollIntervalSeconds: c.interval,
		Enabled: s.Enabled, CrawlPolicy: c.crawl, Slug: s.Slug,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return discovery.Source{}, fmt.Errorf("%w: %s", discovery.ErrNotFound, s.Slug)
	}
	if err != nil {
		return discovery.Source{}, err
	}
	return toSource(row)
}

// DeleteSource removes a source and its run history. Sightings it produced stay, unattributed.
func (r *Repo) DeleteSource(ctx context.Context, slug string) error {
	n, err := r.store.Queries().DeleteSource(ctx, slug)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", discovery.ErrNotFound, slug)
	}
	return nil
}

func toQuerySpec(q dbgen.DiscoveryQuery) discovery.QuerySpec {
	out := discovery.QuerySpec{Family: q.Family, Name: q.Name, Text: q.QueryText, Providers: q.Providers, Enabled: q.Enabled}
	if out.Providers == nil {
		out.Providers = []string{}
	}
	if q.Region != nil {
		out.Region = *q.Region
	}
	return out
}

// AllQueries returns every configured query, enabled or not.
func (r *Repo) AllQueries(ctx context.Context) ([]discovery.QuerySpec, error) {
	rows, err := r.store.Queries().ListAllQueries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]discovery.QuerySpec, 0, len(rows))
	for _, q := range rows {
		out = append(out, toQuerySpec(q))
	}
	return out, nil
}

func regionPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func providersOrEmpty(p []string) []string {
	if p == nil {
		return []string{}
	}
	return p
}

// CreateQuery inserts a query. It returns discovery.ErrAlreadyExists if the name is taken.
func (r *Repo) CreateQuery(ctx context.Context, q discovery.QuerySpec) (discovery.QuerySpec, error) {
	row, err := r.store.Queries().InsertQuery(ctx, dbgen.InsertQueryParams{
		Family: q.Family, Name: q.Name, QueryText: q.Text, Providers: providersOrEmpty(q.Providers), Region: regionPtr(q.Region), Enabled: q.Enabled,
	})
	if isUniqueViolation(err) {
		return discovery.QuerySpec{}, fmt.Errorf("%w: %s", discovery.ErrAlreadyExists, q.Name)
	}
	if err != nil {
		return discovery.QuerySpec{}, err
	}
	return toQuerySpec(row), nil
}

// UpdateQuery replaces a query's definition (the name identifies it). It returns discovery.ErrNotFound if absent.
func (r *Repo) UpdateQuery(ctx context.Context, q discovery.QuerySpec) (discovery.QuerySpec, error) {
	row, err := r.store.Queries().UpdateQuery(ctx, dbgen.UpdateQueryParams{
		Family: q.Family, QueryText: q.Text, Providers: providersOrEmpty(q.Providers), Region: regionPtr(q.Region), Enabled: q.Enabled, Name: q.Name,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return discovery.QuerySpec{}, fmt.Errorf("%w: %s", discovery.ErrNotFound, q.Name)
	}
	if err != nil {
		return discovery.QuerySpec{}, err
	}
	return toQuerySpec(row), nil
}

// DeleteQuery removes a query.
func (r *Repo) DeleteQuery(ctx context.Context, name string) error {
	n, err := r.store.Queries().DeleteQuery(ctx, name)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", discovery.ErrNotFound, name)
	}
	return nil
}

// Import writes a validated source file into the database. With overwrite false, rows that
// already exist are left exactly as they are. It returns how many rows were written.
func (r *Repo) Import(ctx context.Context, f sourcefile.File, overwrite bool) (sources, queries int, err error) {
	err = r.store.InTx(ctx, func(q *dbgen.Queries) error {
		for _, sp := range f.Sources {
			c, err := encodeSource(sp.ToSource())
			if err != nil {
				return fmt.Errorf("source %s: %w", sp.Slug, err)
			}
			var n int64
			if overwrite {
				if _, err = q.UpsertSource(ctx, dbgen.UpsertSourceParams{
					Slug: sp.Slug, Name: sp.Name, SourceType: sp.Type, Url: c.url, Coverage: c.coverage,
					PollIntervalSeconds: c.interval, Enabled: sp.IsEnabled(), CrawlPolicy: c.crawl,
				}); err == nil {
					n = 1
				}
			} else {
				n, err = q.InsertSourceIfMissing(ctx, dbgen.InsertSourceIfMissingParams{
					Slug: sp.Slug, Name: sp.Name, SourceType: sp.Type, Url: c.url, Coverage: c.coverage,
					PollIntervalSeconds: c.interval, Enabled: sp.IsEnabled(), CrawlPolicy: c.crawl,
				})
			}
			if err != nil {
				return fmt.Errorf("source %s: %w", sp.Slug, err)
			}
			sources += int(n)
		}
		for _, qs := range f.Queries {
			var n int64
			if overwrite {
				if _, err = q.UpsertQuery(ctx, dbgen.UpsertQueryParams{
					Family: qs.Family, Name: qs.Name, QueryText: qs.Text, Providers: providersOrEmpty(qs.Providers), Region: regionPtr(qs.Region), Enabled: qs.IsEnabled(),
				}); err == nil {
					n = 1
				}
			} else {
				n, err = q.InsertQueryIfMissing(ctx, dbgen.InsertQueryIfMissingParams{
					Family: qs.Family, Name: qs.Name, QueryText: qs.Text, Providers: providersOrEmpty(qs.Providers), Region: regionPtr(qs.Region), Enabled: qs.IsEnabled(),
				})
			}
			if err != nil {
				return fmt.Errorf("query %s: %w", qs.Name, err)
			}
			queries += int(n)
		}
		return nil
	})
	return
}
