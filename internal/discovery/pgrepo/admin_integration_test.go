//go:build integration

package pgrepo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
)

func newSource(slug string) discovery.Source {
	return discovery.Source{
		Slug: slug, Name: "Source " + slug, Type: discovery.TypeRSS, URL: "https://example.org/" + slug + ".xml",
		Coverage: discovery.Coverage{Country: "US", States: []string{"CA"}}, PollInterval: 30 * time.Minute, Enabled: true,
		Crawl: discovery.CrawlPolicy{MaxPages: 5},
	}
}

func TestSourceCRUD(t *testing.T) {
	pg.Reset(t, store)
	repo, ctx := New(store), context.Background()

	got, err := repo.CreateSource(ctx, newSource("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != "alpha" || got.Coverage.States[0] != "CA" || got.Crawl.MaxPages != 5 || got.PollInterval != 30*time.Minute || !got.Enabled || got.LastAttemptAt != nil {
		t.Fatalf("%+v", got)
	}
	if _, err := repo.CreateSource(ctx, newSource("alpha")); !errors.Is(err, discovery.ErrAlreadyExists) {
		t.Fatalf("duplicate slug: %v", err)
	}

	upd := newSource("alpha")
	upd.Name, upd.URL, upd.Enabled, upd.PollInterval = "Renamed", "https://example.org/new.xml", false, time.Hour
	got, err = repo.UpdateSource(ctx, upd)
	if err != nil || got.Name != "Renamed" || got.URL != "https://example.org/new.xml" || got.Enabled || got.PollInterval != time.Hour {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := repo.UpdateSource(ctx, newSource("ghost")); !errors.Is(err, discovery.ErrNotFound) {
		t.Fatalf("update of a missing source: %v", err)
	}

	// The set of types belongs to the registered providers, not to a database constraint, so a
	// type added by a new provider needs no migration.
	odd := newSource("future")
	odd.Type = "carrier_pigeon"
	if _, err := repo.CreateSource(ctx, odd); err != nil {
		t.Fatalf("the database must not restrict source types: %v", err)
	}

	if err := repo.DeleteSource(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteSource(ctx, "alpha"); !errors.Is(err, discovery.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := repo.Source(ctx, "alpha"); !errors.Is(err, discovery.ErrNotFound) {
		t.Fatalf("lookup after delete: %v", err)
	}
}

func TestDeletingASourceKeepsWhatItFound(t *testing.T) {
	repo, svc, slug := setup(t)
	ctx := context.Background()
	if _, err := svc.RunSlug(ctx, slug, win, false); err != nil {
		t.Fatal(err)
	}
	var before, linked int
	_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM article_discoveries`).Scan(&before)
	if before == 0 {
		t.Fatal("expected sightings")
	}
	if err := repo.DeleteSource(ctx, slug); err != nil {
		t.Fatal(err)
	}
	var after int
	_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM article_discoveries`).Scan(&after)
	_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM article_discoveries WHERE source_id IS NOT NULL`).Scan(&linked)
	if after != before || linked != 0 {
		t.Fatalf("sightings must survive unattributed: before=%d after=%d still linked=%d", before, after, linked)
	}
	var runs int
	_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM discovery_runs`).Scan(&runs)
	if runs != 0 {
		t.Fatalf("run history goes with the source, got %d", runs)
	}
}

func rec(slug string, at time.Time, status discovery.RunStatus, errText string) discovery.RunRecord {
	return discovery.RunRecord{
		Slug: slug, StartedAt: at, FinishedAt: at.Add(2 * time.Second), Status: status, Found: 10, Unique: 8, New: 3, Known: 5,
		Requests: 2, Warnings: []string{"w"}, Error: errText,
		Window: discovery.Window{Since: at.Add(-time.Hour), Until: at},
	}
}

func TestRunsDriveHealthAndStats(t *testing.T) {
	pg.Reset(t, store)
	repo, ctx := New(store), context.Background()
	if _, err := repo.CreateSource(ctx, newSource("h")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)

	if h := discovery.AssessHealth(mustSource(t, repo, "h"), false, now); h.State != discovery.HealthWaiting {
		t.Fatalf("a source with no run is waiting, got %+v", h)
	}
	for i := 0; i < 3; i++ {
		if err := repo.FinishRun(ctx, rec("h", now.Add(time.Duration(i-5)*time.Minute), discovery.RunFailed, "upstream 503")); err != nil {
			t.Fatal(err)
		}
	}
	src := mustSource(t, repo, "h")
	if src.ConsecutiveFailures != 3 || src.ErrorState != "upstream 503" || src.LastAttemptAt == nil || src.LastSuccessAt != nil {
		t.Fatalf("%+v", src)
	}
	if h := discovery.AssessHealth(src, false, now); h.State != discovery.HealthFailing {
		t.Fatalf("three failures in a row: %+v", h)
	}

	// A partial run (usable candidates, plus an error) counts as working and keeps its error on the run.
	if err := repo.FinishRun(ctx, rec("h", now.Add(-time.Minute), discovery.RunPartial, "one query failed")); err != nil {
		t.Fatal(err)
	}
	src = mustSource(t, repo, "h")
	if src.ConsecutiveFailures != 0 || src.LastSuccessAt == nil {
		t.Fatalf("%+v", src)
	}
	if h := discovery.AssessHealth(src, false, now); h.State != discovery.HealthHealthy {
		t.Fatalf("%+v", h)
	}

	runs, err := repo.Runs(ctx, "h", 10)
	if err != nil || len(runs) != 4 || runs[0].Status != discovery.RunPartial || runs[0].Error != "one query failed" || runs[0].Duration() != 2*time.Second ||
		runs[0].Window.Since.IsZero() || len(runs[0].Warnings) != 1 || runs[3].Status != discovery.RunFailed {
		t.Fatalf("newest first with every field kept: %+v %v", runs, err)
	}
	if l, _ := repo.Runs(ctx, "h", 2); len(l) != 2 {
		t.Fatalf("limit ignored: %d", len(l))
	}

	stats, err := repo.RunStats(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	s := stats["h"]
	if s.Runs != 4 || s.Failures != 3 || s.Partials != 1 || s.Successes != 0 || s.NewURLs != 12 || s.Requests != 8 || s.AvgSeconds != 2 || s.P95Seconds != 2 {
		t.Fatalf("%+v", s)
	}
	if got := s.SuccessRate(); got != 0.25 {
		t.Fatalf("success rate %v", got)
	}
	// A source with no runs in the window still appears, with zeros.
	if _, err := repo.CreateSource(ctx, newSource("quiet")); err != nil {
		t.Fatal(err)
	}
	stats, _ = repo.RunStats(ctx, now.Add(-24*time.Hour))
	if q, ok := stats["quiet"]; !ok || q.Runs != 0 {
		t.Fatalf("quiet source missing or non-zero: %+v ok=%v", q, ok)
	}

	// Old history is pruned.
	if err := repo.FinishRun(ctx, rec("h", now.Add(-100*24*time.Hour), discovery.RunSuccess, "")); err != nil {
		t.Fatal(err)
	}
	n, err := repo.PruneRuns(ctx, now.Add(-90*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d %v", n, err)
	}
}

func mustSource(t *testing.T, repo *Repo, slug string) discovery.Source {
	t.Helper()
	s, err := repo.Source(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestServiceRunRecordsHistory(t *testing.T) {
	repo, svc, slug := setup(t)
	ctx := context.Background()
	if _, err := svc.RunSlug(ctx, slug, win, false); err != nil {
		t.Fatal(err)
	}
	runs, err := repo.Runs(ctx, slug, 5)
	if err != nil || len(runs) != 1 {
		t.Fatalf("%+v %v", runs, err)
	}
	r := runs[0]
	if r.Status != discovery.RunSuccess || r.Found != 2 || r.New != 2 || r.Requests < 0 || !r.Window.Since.Equal(win.Since) {
		t.Fatalf("%+v", r)
	}
	if _, err := svc.RunSlug(ctx, slug, win, true); err != nil { // dry runs leave no trace
		t.Fatal(err)
	}
	if runs, _ = repo.Runs(ctx, slug, 5); len(runs) != 1 {
		t.Fatalf("a dry run must not be recorded: %d", len(runs))
	}
}

func TestQueryCRUD(t *testing.T) {
	pg.Reset(t, store)
	repo, ctx := New(store), context.Background()
	q := discovery.QuerySpec{Family: "pedestrian", Name: "ped-test", Text: "(pedestrian OR walker) struck", Providers: nil, Enabled: true}
	got, err := repo.CreateQuery(ctx, q)
	if err != nil || got.Providers == nil || len(got.Providers) != 0 {
		t.Fatalf("an empty provider list means every search provider and must round-trip as []: %+v %v", got, err)
	}
	if _, err := repo.CreateQuery(ctx, q); !errors.Is(err, discovery.ErrAlreadyExists) {
		t.Fatalf("%v", err)
	}
	q.Text, q.Providers, q.Region, q.Enabled = "(cyclist OR bicyclist) hit", []string{"gdelt"}, "US", false
	got, err = repo.UpdateQuery(ctx, q)
	if err != nil || got.Text != q.Text || got.Providers[0] != "gdelt" || got.Region != "US" || got.Enabled {
		t.Fatalf("%+v %v", got, err)
	}
	all, err := repo.AllQueries(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("disabled queries are still listed for editing: %+v %v", all, err)
	}
	if forRun, _ := repo.Queries(ctx, "gdelt"); len(forRun) != 0 {
		t.Fatalf("a disabled query must not be run: %+v", forRun)
	}
	if _, err := repo.UpdateQuery(ctx, discovery.QuerySpec{Name: "ghost", Family: "x", Text: "x"}); !errors.Is(err, discovery.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if err := repo.DeleteQuery(ctx, "ped-test"); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteQuery(ctx, "ped-test"); !errors.Is(err, discovery.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}
