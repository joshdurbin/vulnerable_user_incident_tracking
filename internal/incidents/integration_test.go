//go:build integration

package incidents

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/domain"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage/dbgen"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/testutil"
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

func setup(t *testing.T) (*Resolver, context.Context) {
	t.Helper()
	pg.Reset(t, store)
	return NewResolver(store, DefaultConfig(), zerolog.Nop()), context.Background()
}

// newArticle inserts an article and a (unique) extraction for it.
func newArticle(t *testing.T, publisher string, published time.Time) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	q := store.Queries()
	title, text := publisher+" story", "body"
	a, err := q.InsertArticle(ctx, dbgen.InsertArticleParams{
		RequestedUrl: "https://" + publisher + "/" + uuid.NewString(), FinalUrl: "https://" + publisher + "/x", Title: &title,
		Publisher: &publisher, PublishedAt: &published, BodyText: &text, Status: "fetched", FetchedAt: published,
	})
	if err != nil {
		t.Fatal(err)
	}
	ex, err := q.UpsertExtraction(ctx, dbgen.UpsertExtractionParams{
		ArticleID: a.ID, Model: "m", PromptVersion: "v1", SchemaVersion: "v1", Verdict: "confirmed", ArticleKind: "current_incident",
		Result: []byte(`{}`), Warnings: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a.ID, ex.ID
}

func input(t *testing.T, publisher, published string, c domain.IncidentCandidate, lat, lon *float64) Input {
	t.Helper()
	pub, _ := time.Parse(time.RFC3339, published)
	art, ex := newArticle(t, publisher, pub)
	return Input{ArticleID: art, ExtractionID: ex, Index: 0, Obs: Observation{
		Candidate: c, ArticleKind: domain.KindCurrent, Publisher: publisher, ObservedAt: pub, Lat: lat, Lon: lon,
		GeocodeSource: "test", GeocodePrecision: "rooftop",
	}}
}

func countIncidents(t *testing.T) int {
	var n int
	if err := store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM incidents WHERE merged_into IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func f(v float64) *float64 { return &v }

// ---- Milestone 3 acceptance: one canonical incident, distinct incidents stay separate ----

func TestMultipleArticlesOneCollisionProduceOneIncident(t *testing.T) {
	r, ctx := setup(t)
	a, err := r.Resolve(ctx, input(t, "gazette.example", "2026-10-14T18:00:00Z", pasadena(nil), nil, nil))
	if err != nil || a.Outcome != OutcomeCreated {
		t.Fatalf("%+v %v", a, err)
	}
	b, err := r.Resolve(ctx, input(t, "tribune.example", "2026-10-15T09:00:00Z", pasadena(func(c *domain.IncidentCandidate) {
		c.StreetAddress, c.Severity, c.HitAndRun = "Colorado and Lake", "unknown", "unknown"
	}), nil, nil))
	if err != nil || b.Outcome != OutcomeMatched || b.IncidentID != a.IncidentID {
		t.Fatalf("second article should match: %+v %v", b, err)
	}
	if n := countIncidents(t); n != 1 {
		t.Fatalf("want 1 incident, got %d", n)
	}
	inc, _ := store.Queries().GetIncident(ctx, a.IncidentID)
	if inc.ArticleCount != 2 || inc.VerificationStatus != "corroborated" {
		t.Fatalf("%+v", inc)
	}
	arts, _ := store.Queries().ListIncidentArticles(ctx, a.IncidentID)
	if len(arts) != 2 {
		t.Fatalf("articles linked: %d", len(arts))
	}
}

func TestDistinctIncidentsStaySeparate(t *testing.T) {
	r, ctx := setup(t)
	clovis := domain.IncidentCandidate{OccurredAt: "2026-10-10", OccurredAtPrecision: "date", City: "Clovis", State: "CA", StreetAddress: "Herndon Avenue",
		LocationPrecision: "street", VRUCategories: []string{"pedestrian"}, Severity: "minor"}
	sanger := domain.IncidentCandidate{OccurredAt: "2026-10-11", OccurredAtPrecision: "date", City: "Sanger", State: "CA", StreetAddress: "Jensen Avenue",
		LocationPrecision: "street", VRUCategories: []string{"pedestrian"}, Severity: "fatal", FatalityCount: ip(1)}
	sameSpotLater := pasadena(func(c *domain.IncidentCandidate) { c.OccurredAt = "2026-10-20" })
	for _, c := range []domain.IncidentCandidate{clovis, sanger, pasadena(nil), sameSpotLater} {
		if _, err := r.Resolve(ctx, input(t, "p.example", "2026-10-21T00:00:00Z", c, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if n := countIncidents(t); n != 4 {
		t.Fatalf("want 4 distinct incidents, got %d", n)
	}
}

func TestInjuryLaterBecomesFatalityKeepsHistory(t *testing.T) {
	r, ctx := setup(t)
	first, _ := r.Resolve(ctx, input(t, "a.example", "2026-10-14T18:00:00Z", pasadena(func(c *domain.IncidentCandidate) { c.FatalityCount = ip(0) }), nil, nil))
	second, err := r.Resolve(ctx, input(t, "b.example", "2026-10-16T18:00:00Z", pasadena(func(c *domain.IncidentCandidate) { c.Severity, c.FatalityCount = "fatal", ip(1) }), nil, nil))
	if err != nil || second.IncidentID != first.IncidentID || second.Outcome != OutcomeMatched {
		t.Fatalf("%+v %v", second, err)
	}
	q := store.Queries()
	inc, _ := q.GetIncident(ctx, first.IncidentID)
	if inc.Severity != "fatal" || inc.FatalityCount == nil || *inc.FatalityCount != 1 || inc.Version < 2 {
		t.Fatalf("%+v", inc)
	}
	obs, _ := q.ListObservations(ctx, first.IncidentID)
	if len(obs) != 2 {
		t.Fatalf("history must be preserved, got %d observations", len(obs))
	}
	evs, _ := q.ListEvents(ctx, first.IncidentID)
	var types []string
	var sawChange bool
	for _, e := range evs {
		types = append(types, e.EventType)
		var d struct{ Changes map[string][]any }
		json.Unmarshal(e.Data, &d)
		if c, ok := d.Changes["fatality_count"]; ok && len(c) == 2 {
			sawChange = true
		}
	}
	if !sawChange || types[0] != "created" {
		t.Fatalf("audit trail missing fatality change: %v", types)
	}
}

func TestResolveIsIdempotent(t *testing.T) {
	r, ctx := setup(t)
	in := input(t, "a.example", "2026-10-14T18:00:00Z", pasadena(nil), nil, nil)
	first, err := r.Resolve(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.Resolve(ctx, in) // a retried activity
	if err != nil || again.Outcome != OutcomeDuplicate || again.IncidentID != first.IncidentID {
		t.Fatalf("%+v %v", again, err)
	}
	var n int
	store.Pool.QueryRow(ctx, `SELECT count(*) FROM incident_observations`).Scan(&n)
	if n != 1 || countIncidents(t) != 1 {
		t.Fatalf("retry created duplicates: observations=%d", n)
	}
}

func TestConcurrentArticlesAboutSameCrashCreateOneIncident(t *testing.T) {
	r, ctx := setup(t)
	const workers = 10
	inputs := make([]Input, workers)
	for i := range inputs {
		inputs[i] = input(t, "pub"+uuid.NewString()[:6]+".example", "2026-10-14T18:00:00Z", pasadena(nil), nil, nil)
	}
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for _, in := range inputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Resolve(ctx, in); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := countIncidents(t); n != 1 {
		t.Fatalf("race created %d incidents, want 1", n)
	}
	var arts int
	store.Pool.QueryRow(ctx, `SELECT count(*) FROM incident_articles`).Scan(&arts)
	if arts != workers {
		t.Fatalf("expected %d article links, got %d", workers, arts)
	}
}

func TestSpatialMatchingAndNoFabricatedCoordinates(t *testing.T) {
	r, ctx := setup(t)
	// Street text differs entirely, but the coordinates are ~40 m apart: PostGIS finds and matches it.
	a, _ := r.Resolve(ctx, input(t, "a.example", "2026-10-14T18:00:00Z", pasadena(func(c *domain.IncidentCandidate) { c.StreetAddress = "E Colorado Blvd" }), f(34.14590), f(-118.14420)))
	b, err := r.Resolve(ctx, input(t, "b.example", "2026-10-14T20:00:00Z", pasadena(func(c *domain.IncidentCandidate) { c.StreetAddress = "Lake Ave intersection" }), f(34.14620), f(-118.14440)))
	if err != nil || b.Outcome != OutcomeMatched || b.IncidentID != a.IncidentID {
		t.Fatalf("near coordinates should match: %+v %v", b, err)
	}
	// Same text, but ~6 km away: distinct.
	c, err := r.Resolve(ctx, input(t, "c.example", "2026-10-14T21:00:00Z", pasadena(nil), f(34.20), f(-118.10)))
	if err != nil || c.IncidentID == a.IncidentID {
		t.Fatalf("far coordinates must not match: %+v %v", c, err)
	}
	// A city-level report has no geometry: coordinates must remain NULL.
	d, _ := r.Resolve(ctx, input(t, "d.example", "2026-10-14T22:00:00Z", domain.IncidentCandidate{OccurredAt: "2026-10-14", City: "Modesto", State: "CA",
		LocationPrecision: "city", VRUCategories: []string{"pedestrian"}, Severity: "serious"}, nil, nil))
	inc, _ := store.Queries().GetIncident(ctx, d.IncidentID)
	if inc.Lat != nil || inc.Lon != nil {
		t.Fatalf("coordinates were fabricated for a city-only incident: %v,%v", inc.Lat, inc.Lon)
	}
	pt, _ := store.Queries().GetIncident(ctx, a.IncidentID)
	if pt.Lat == nil || *pt.Lat < 34.1 || *pt.Lat > 34.2 {
		t.Fatalf("geocoded point lost: %v", pt.Lat)
	}
}

func TestLowerLaterCountRecordedAsConflict(t *testing.T) {
	r, ctx := setup(t)
	first, _ := r.Resolve(ctx, input(t, "a.example", "2026-10-14T18:00:00Z", pasadena(func(c *domain.IncidentCandidate) { c.InjuredCount = ip(2) }), nil, nil))
	if _, err := r.Resolve(ctx, input(t, "b.example", "2026-10-15T18:00:00Z", pasadena(func(c *domain.IncidentCandidate) { c.InjuredCount = ip(1) }), nil, nil)); err != nil {
		t.Fatal(err)
	}
	q := store.Queries()
	inc, _ := q.GetIncident(ctx, first.IncidentID)
	if *inc.InjuredCount != 2 {
		t.Fatalf("lower count must not overwrite: %d", *inc.InjuredCount)
	}
	evs, _ := q.ListEvents(ctx, first.IncidentID)
	found := false
	for _, e := range evs {
		found = found || e.EventType == "conflict"
	}
	if !found {
		t.Fatal("expected a conflict event")
	}
}

// ---- Review queue, merge audit and split ----

func ambiguousPair(t *testing.T, r *Resolver, ctx context.Context) (*Result, *Result) {
	// The second report has no date: plausible duplicate, but not safe to merge automatically.
	a, err := r.Resolve(ctx, input(t, "a.example", "2026-10-14T18:00:00Z", pasadena(nil), nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Resolve(ctx, input(t, "b.example", "2026-10-15T18:00:00Z", pasadena(func(c *domain.IncidentCandidate) { c.OccurredAt, c.OccurredAtPrecision = "", "unknown" }), nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestPossibleMatchGoesToReviewNotMerged(t *testing.T) {
	r, ctx := setup(t)
	a, b := ambiguousPair(t, r, ctx)
	if b.Outcome != OutcomeReview || b.IncidentID == a.IncidentID || len(countReviews(t)) != 1 {
		t.Fatalf("%+v reviews=%d", b, len(countReviews(t)))
	}
	if n := countIncidents(t); n != 2 {
		t.Fatalf("a possible match must not auto-merge, got %d incidents", n)
	}
}

func countReviews(t *testing.T) []uuid.UUID {
	rows, err := store.Queries().ListPendingReviews(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func TestApproveReviewMergesWithAuditTrail(t *testing.T) {
	r, ctx := setup(t)
	a, b := ambiguousPair(t, r, ctx)
	target, err := r.ApproveReview(ctx, countReviews(t)[0], "josh")
	if err != nil || target != a.IncidentID {
		t.Fatalf("%v %v", target, err)
	}
	if n := countIncidents(t); n != 1 {
		t.Fatalf("incidents after merge: %d", n)
	}
	q := store.Queries()
	merged, _ := q.GetIncident(ctx, b.IncidentID)
	if merged.MergedInto == nil || *merged.MergedInto != a.IncidentID {
		t.Fatalf("provisional should point at survivor: %+v", merged.MergedInto)
	}
	inc, _ := q.GetIncident(ctx, a.IncidentID)
	if inc.ArticleCount != 2 || inc.VerificationStatus != "reviewed" {
		t.Fatalf("%+v", inc)
	}
	var kinds []string
	for _, id := range []uuid.UUID{a.IncidentID, b.IncidentID} {
		evs, _ := q.ListEvents(ctx, id)
		for _, e := range evs {
			kinds = append(kinds, e.EventType+":"+e.Actor)
		}
	}
	want := map[string]bool{"merged:josh": false, "review_approved:josh": false}
	for _, k := range kinds {
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Fatalf("missing audit event %s in %v", k, kinds)
		}
	}
	if _, err := r.ApproveReview(ctx, uuid.New(), "x"); err == nil {
		t.Fatal("unknown review should error")
	}
}

func TestRejectReviewKeepsIncidentsSeparate(t *testing.T) {
	r, ctx := setup(t)
	_, _ = ambiguousPair(t, r, ctx)
	id := countReviews(t)[0]
	if err := r.RejectReview(ctx, id, "josh"); err != nil {
		t.Fatal(err)
	}
	if n := countIncidents(t); n != 2 || len(countReviews(t)) != 0 {
		t.Fatalf("incidents=%d pending=%d", n, len(countReviews(t)))
	}
	if err := r.RejectReview(ctx, id, "josh"); err != ErrNotPending {
		t.Fatalf("second decision should fail with ErrNotPending, got %v", err)
	}
}

func TestSplitUndoesAMerge(t *testing.T) {
	r, ctx := setup(t)
	a, b := ambiguousPair(t, r, ctx)
	if _, err := r.ApproveReview(ctx, countReviews(t)[0], "josh"); err != nil {
		t.Fatal(err)
	}
	q := store.Queries()
	arts, _ := q.ListIncidentArticles(ctx, a.IncidentID)
	if len(arts) != 2 {
		t.Fatal("setup: expected 2 articles")
	}
	// Split the second article back out.
	var second uuid.UUID
	for _, ar := range arts {
		if ar.ExtractionID != uuid.Nil && ar.ArticleID != arts[0].ArticleID {
			second = ar.ArticleID
		}
	}
	newID, err := r.SplitArticle(ctx, a.IncidentID, second, "josh")
	if err != nil {
		t.Fatal(err)
	}
	if n := countIncidents(t); n != 2 {
		t.Fatalf("after split want 2 live incidents, got %d", n)
	}
	for _, id := range []uuid.UUID{a.IncidentID, newID} {
		inc, _ := q.GetIncident(ctx, id)
		if inc.ArticleCount != 1 {
			t.Fatalf("incident %s article_count=%d", id, inc.ArticleCount)
		}
	}
	_ = b
	if _, err := r.SplitArticle(ctx, a.IncidentID, second, "josh"); err == nil {
		t.Fatal("splitting a single-article incident must fail")
	}
}

func TestExtractionUpsertIsIdempotent(t *testing.T) {
	setup(t)
	art, ex := newArticle(t, "x.example", time.Now())
	again, err := store.Queries().UpsertExtraction(context.Background(), dbgen.UpsertExtractionParams{
		ArticleID: art, Model: "m", PromptVersion: "v1", SchemaVersion: "v1", Verdict: "confirmed", ArticleKind: "current_incident",
		Result: []byte(`{}`), Warnings: []string{},
	})
	if err != nil || again.ID != ex || again.Inserted {
		t.Fatalf("same versions must return the existing row without inserting: %+v %v", again, err)
	}
	other, _ := store.Queries().UpsertExtraction(context.Background(), dbgen.UpsertExtractionParams{
		ArticleID: art, Model: "m", PromptVersion: "v2", SchemaVersion: "v1", Verdict: "confirmed", ArticleKind: "current_incident",
		Result: []byte(`{}`), Warnings: []string{},
	})
	if other.ID == ex || !other.Inserted {
		t.Fatal("a new prompt version must create a new extraction")
	}
}

// A wire story republished by three outlets is one source, not three: it must not make an incident
// "corroborated" or inflate the source count.
func TestCopiesOfOneArticleCountAsOneSource(t *testing.T) {
	r, ctx := setup(t)
	in1 := input(t, "gazette.example", "2026-10-14T18:00:00Z", pasadena(nil), nil, nil)
	a, err := r.Resolve(ctx, in1)
	require.NoError(t, err)

	// Two reprints of the first article, each its own row pointing at it.
	for _, pub := range []string{"herald.example", "tribune.example"} {
		in := input(t, pub, "2026-10-14T20:00:00Z", pasadena(nil), nil, nil)
		kind := "near_duplicate"
		_, err := store.Pool.Exec(ctx, `UPDATE articles SET duplicate_of = $1, duplicate_kind = $2 WHERE id = $3`, in1.ArticleID, kind, in.ArticleID)
		require.NoError(t, err)
		b, err := r.Resolve(ctx, in)
		require.NoError(t, err)
		require.Equal(t, a.IncidentID, b.IncidentID)
	}
	inc, err := store.Queries().GetIncident(ctx, a.IncidentID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, inc.ArticleCount, "three articles, one source")
	assert.Equal(t, "unverified", inc.VerificationStatus)

	arts, err := store.Queries().ListIncidentArticlesAPI(ctx, a.IncidentID)
	require.NoError(t, err)
	assert.Len(t, arts, 3, "every article is still listed")

	// A genuinely independent report corroborates it.
	c, err := r.Resolve(ctx, input(t, "chronicle.example", "2026-10-15T09:00:00Z", pasadena(nil), nil, nil))
	require.NoError(t, err)
	require.Equal(t, a.IncidentID, c.IncidentID)
	inc, _ = store.Queries().GetIncident(ctx, a.IncidentID)
	assert.EqualValues(t, 2, inc.ArticleCount)
	assert.Equal(t, "corroborated", inc.VerificationStatus)
}
