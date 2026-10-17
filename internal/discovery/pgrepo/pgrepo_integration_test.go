//go:build integration

package pgrepo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/joshdurbin/fuck_cars/internal/discovery"
	"github.com/joshdurbin/fuck_cars/internal/discovery/feed"
	"github.com/joshdurbin/fuck_cars/internal/discovery/seed"
	"github.com/joshdurbin/fuck_cars/internal/storage"
	"github.com/joshdurbin/fuck_cars/internal/storage/dbgen"
	"github.com/joshdurbin/fuck_cars/internal/testutil"
)

var (
	pg    *testutil.Postgres
	store *storage.Store
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	if pg, err = testutil.StartPostgres(ctx); err != nil {
		println("cannot start postgres:", err.Error())
		os.Exit(1)
	}
	if store, err = storage.Open(ctx, pg.DSN); err != nil {
		println(err.Error())
		os.Exit(1)
	}
	code := m.Run()
	store.Close()
	pg.Terminate(ctx)
	os.Exit(code)
}

const rss = `<?xml version="1.0"?><rss version="2.0"><channel><title>Gazette</title>
<item><title>Ped killed</title><link>https://gazette.example.com/a?utm_source=rss</link><pubDate>Fri, 16 Oct 2026 14:00:00 GMT</pubDate></item>
<item><title>Cyclist hurt</title><link>https://gazette.example.com/b</link><pubDate>Fri, 16 Oct 2026 15:00:00 GMT</pubDate></item>
</channel></rss>`

func setup(t *testing.T) (*Repo, *discovery.Service, string) {
	t.Helper()
	pg.Reset(t, store)
	repo := New(store)
	f, err := seed.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(rss)) }))
	t.Cleanup(srv.Close)
	f.Sources = append(f.Sources, seed.SourceSeed{Slug: "test-feed", Name: "Test Feed", Type: "rss", URL: srv.URL, PollInterval: 30 * time.Minute})
	if _, _, err := repo.Sync(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	reg := discovery.NewRegistry()
	reg.Register(feed.New(srv.Client()), discovery.TypeRSS)
	return repo, discovery.NewService(reg, repo), "test-feed"
}

var win = discovery.Window{Since: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)}

func TestSyncIsIdempotentAndLoadsSourcesAndQueries(t *testing.T) {
	repo, _, _ := setup(t)
	f, _ := seed.Defaults()
	if s, q, err := repo.Sync(context.Background(), f); err != nil || s != len(f.Sources) || q != len(f.Queries) {
		t.Fatalf("%d %d %v", s, q, err)
	}
	all, _ := repo.Sources(context.Background(), false)
	if len(all) != len(f.Sources)+1 {
		t.Fatalf("sources: %d", len(all))
	}
	qs, err := repo.Queries(context.Background(), "gdelt")
	if err != nil || len(qs) != len(f.Queries) {
		t.Fatalf("queries %d %v", len(qs), err)
	}
	g, _ := repo.Source(context.Background(), "oaklandside")
	if g.Type != "rss" || g.Coverage.Cities[0] != "Oakland" || g.PollInterval != 30*time.Minute || !g.Enabled {
		t.Fatalf("%+v", g)
	}
	if _, err := repo.Source(context.Background(), "nope"); err == nil {
		t.Fatal("unknown source must error")
	}
}

func TestRunRecordsNewURLsThenRerunFindsNothingNew(t *testing.T) {
	repo, svc, slug := setup(t)
	ctx := context.Background()
	first, err := svc.RunSlug(ctx, slug, win, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.New) != 2 || first.Known != 0 {
		t.Fatalf("%+v", first)
	}
	if first.New[0].URL != "https://gazette.example.com/a" { // tracking param stripped
		t.Fatalf("not normalized: %s", first.New[0].URL)
	}
	second, err := svc.RunSlug(ctx, slug, win, false) // overlapping window: same articles again
	if err != nil || len(second.New) != 0 || second.Known != 2 {
		t.Fatalf("rerun must find nothing new: %+v %v", second, err)
	}
	var urls, disc int
	store.Pool.QueryRow(ctx, `SELECT count(*) FROM discovered_urls`).Scan(&urls)
	store.Pool.QueryRow(ctx, `SELECT count(*) FROM article_discoveries`).Scan(&disc)
	if urls != 2 || disc != 2 {
		t.Fatalf("urls=%d discoveries=%d (rerun must not duplicate provenance)", urls, disc)
	}
	src, _ := repo.Source(ctx, slug)
	if src.LastSuccessAt == nil || src.ConsecutiveFailures != 0 {
		t.Fatalf("%+v", src)
	}
}

func TestSourceFailureIsTrackedAndClearedOnSuccess(t *testing.T) {
	repo, svc, slug := setup(t)
	ctx := context.Background()
	src, _ := repo.Source(ctx, slug)
	src.URL = "http://127.0.0.1:1/dead" // nothing listens here
	if _, err := svc.Run(ctx, discovery.RunRequest{Source: src, Window: win}); err == nil {
		t.Fatal("expected failure")
	}
	got, _ := repo.Source(ctx, slug)
	if got.ConsecutiveFailures != 1 || got.ErrorState == "" {
		t.Fatalf("%+v", got)
	}
	if _, err := svc.RunSlug(ctx, slug, win, false); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.Source(ctx, slug)
	if got.ConsecutiveFailures != 0 || got.ErrorState != "" {
		t.Fatalf("error state not cleared: %+v", got)
	}
}

func TestSameArticleFromTwoProvidersKeepsBothSightings(t *testing.T) {
	repo, _, _ := setup(t)
	ctx := context.Background()
	gd, _ := repo.Source(ctx, "gdelt-us")
	feedSrc, _ := repo.Source(ctx, "test-feed")
	pub := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	mk := func(provider, url string) []discovery.Deduped {
		u, _ := discovery.Dedupe([]discovery.Candidate{{URL: url, Provider: provider, Title: "t", PublishedAt: &pub, QueryName: map[string]string{"gdelt": "hit-and-run"}[provider]}})
		return u
	}
	r1, err := repo.Record(ctx, gd, mk("gdelt", "https://gazette.example.com/x?utm_source=gdelt"))
	if err != nil || len(r1.New) != 1 || r1.Provenance != 1 {
		t.Fatalf("%+v %v", r1, err)
	}
	r2, err := repo.Record(ctx, feedSrc, mk("rss", "http://gazette.example.com/x"))
	if err != nil || len(r2.New) != 0 || r2.Known != 1 || r2.Provenance != 1 {
		t.Fatalf("second provider should add provenance only: %+v %v", r2, err)
	}
	var providers []string
	rows, _ := store.Pool.Query(ctx, `SELECT provider FROM article_discoveries ORDER BY provider`)
	for rows.Next() {
		var p string
		rows.Scan(&p)
		providers = append(providers, p)
	}
	if strings.Join(providers, ",") != "gdelt,rss" {
		t.Fatalf("providers %v", providers)
	}
	var qid *uuid.UUID
	store.Pool.QueryRow(ctx, `SELECT query_id FROM article_discoveries WHERE provider='gdelt'`).Scan(&qid)
	if qid == nil {
		t.Fatal("query provenance not recorded")
	}
}

func TestStatsShowWhichSourceIsValuable(t *testing.T) {
	repo, _, _ := setup(t)
	ctx := context.Background()
	gd, _ := repo.Source(ctx, "gdelt-us")
	fs, _ := repo.Source(ctx, "test-feed")
	disc := time.Now().UTC()
	pub := disc.Add(-30 * time.Minute)
	rec := func(src discovery.Source, provider string, urls ...string) {
		var cs []discovery.Candidate
		for _, u := range urls {
			cs = append(cs, discovery.Candidate{URL: u, Provider: provider, PublishedAt: &pub})
		}
		d, _ := discovery.Dedupe(cs)
		if _, err := repo.Record(ctx, src, d); err != nil {
			t.Fatal(err)
		}
	}
	rec(gd, "gdelt", "https://a.example.com/1", "https://a.example.com/2", "https://a.example.com/3")
	rec(fs, "rss", "https://a.example.com/1") // overlap with gdelt on /1
	// Make /2 a fetched, relevant article that became an incident.
	q := store.Queries()
	var urlID uuid.UUID
	store.Pool.QueryRow(ctx, `SELECT id FROM discovered_urls WHERE normalized_url='https://a.example.com/2'`).Scan(&urlID)
	art, err := q.InsertArticle(ctx, dbgen.InsertArticleParams{DiscoveredUrlID: &urlID, RequestedUrl: "https://a.example.com/2", FinalUrl: "https://a.example.com/2", Status: "fetched", FetchedAt: disc})
	if err != nil {
		t.Fatal(err)
	}
	ex, _ := q.UpsertExtraction(ctx, dbgen.UpsertExtractionParams{ArticleID: art.ID, Model: "m", PromptVersion: "v1", SchemaVersion: "v1", Verdict: "confirmed", ArticleKind: "current_incident", Result: []byte(`{}`), Warnings: []string{}})
	var incID uuid.UUID
	store.Pool.QueryRow(ctx, `INSERT INTO incidents (severity) VALUES ('fatal') RETURNING id`).Scan(&incID)
	store.Pool.Exec(ctx, `INSERT INTO incident_articles (incident_id, article_id, extraction_id, candidate_idx, match_status, matched_by) VALUES ($1,$2,$3,0,'created','rules')`, incID, art.ID, ex.ID)

	stats, err := repo.Stats(ctx, disc.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]discovery.SourceStats{}
	for _, s := range stats {
		by[s.Provider] = s
	}
	g, r := by["gdelt"], by["rss"]
	if g.Discoveries != 3 || g.UniqueURLs != 3 || g.ExclusiveURLs != 2 || g.ArticlesFetched != 1 || g.FetchOK != 1 || g.RelevantArticles != 1 || g.Incidents != 1 {
		t.Fatalf("gdelt %+v", g)
	}
	if r.Discoveries != 1 || r.ExclusiveURLs != 0 || r.Incidents != 0 {
		t.Fatalf("rss %+v", r)
	}
	if g.AvgDelaySeconds < 1700 || g.AvgDelaySeconds > 1900 {
		t.Fatalf("delay %v", g.AvgDelaySeconds)
	}
}

func TestSyncNewAddsMissingAndNeverOverwritesOperatorChanges(t *testing.T) {
	repo, _, _ := setup(t)
	ctx := context.Background()
	f, err := seed.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Sync(ctx, f); err != nil {
		t.Fatal(err)
	}
	slug := f.Sources[0].Slug
	if _, err := store.Pool.Exec(ctx, `UPDATE discovery_sources SET enabled = false, poll_interval_seconds = 99999 WHERE slug = $1`, slug); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, `DELETE FROM discovery_sources WHERE slug = $1`, f.Sources[1].Slug); err != nil {
		t.Fatal(err)
	}
	ns, nq, err := repo.SyncNew(ctx, f)
	if err != nil || ns != 1 || nq != 0 {
		t.Fatalf("want 1 source added and no queries, got %d, %d, %v", ns, nq, err)
	}
	var enabled bool
	var interval int
	if err := store.Pool.QueryRow(ctx, `SELECT enabled, poll_interval_seconds FROM discovery_sources WHERE slug = $1`, slug).Scan(&enabled, &interval); err != nil {
		t.Fatal(err)
	}
	if enabled || interval != 99999 {
		t.Fatalf("operator changes were overwritten: enabled=%v interval=%d", enabled, interval)
	}
}
