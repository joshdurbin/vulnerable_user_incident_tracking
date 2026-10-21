//go:build integration

package search

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/testutil"
)

var (
	pg    *testutil.Postgres
	os_   *testutil.OpenSearch
	store *storage.Store
	cl    *Client
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	if pg, err = testutil.StartPostgres(ctx); err != nil {
		println("cannot start postgres:", err.Error())
		os.Exit(1)
	}
	if os_, err = testutil.StartOpenSearch(ctx); err != nil {
		println("cannot start opensearch:", err.Error())
		os.Exit(1)
	}
	if store, err = storage.Open(ctx, pg.DSN); err != nil {
		println(err.Error())
		os.Exit(1)
	}
	if cl, err = New(Config{URL: os_.URL}); err != nil {
		println(err.Error())
		os.Exit(1)
	}
	code := m.Run()
	store.Close()
	os_.Terminate(ctx)
	pg.Terminate(ctx)
	os.Exit(code)
}

// fresh empties both stores so each test starts alone.
func fresh(t *testing.T) *Indexer {
	t.Helper()
	pg.Reset(t, store)
	require.NoError(t, cl.Drop(context.Background()))
	return &Indexer{Store: store, Index: cl}
}

type inc struct {
	street, city, state, severity, circ string
	vru                                 []string
	hitRun                              string
	titles                              []string
	updated                             time.Time
}

// seed inserts an incident with linked articles, the way the resolver leaves them.
func seed(t *testing.T, i inc) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	// Like the application, take updated_at from the database clock (now()) unless a test sets it. The host
	// and the database container can disagree by a little, and a time from the host can lie in the database's future.
	var updated *time.Time
	if !i.updated.IsZero() {
		updated = &i.updated
	}
	if i.hitRun == "" {
		i.hitRun = "no"
	}
	if i.vru == nil {
		i.vru = []string{}
	}
	var id uuid.UUID
	require.NoError(t, store.Pool.QueryRow(ctx, `INSERT INTO incidents (occurred_on, occurred_precision, city, state, street_address, location_precision,
		geom, vru_categories, severity, hit_and_run, circumstances, article_count, updated_at)
		VALUES ('2026-10-10', 'date', $1, $2, $3, 'street', ST_SetSRID(ST_MakePoint(-122.27, 37.80), 4326)::geography, $4, $5, $6, $7, $8, COALESCE($9, now()))
		RETURNING id`, i.city, i.state, i.street, i.vru, i.severity, i.hitRun, i.circ, len(i.titles), updated).Scan(&id))
	for n, title := range i.titles {
		var art uuid.UUID
		require.NoError(t, store.Pool.QueryRow(ctx, `INSERT INTO articles (requested_url, final_url, title, publisher, description, status)
			VALUES ($1, $1, $2, $3, 'desc', 'fetched') RETURNING id`, fmt.Sprintf("https://n%d.example/%s", n, uuid.NewString()), title, fmt.Sprintf("Publisher%d", n)).Scan(&art))
		var ex uuid.UUID
		require.NoError(t, store.Pool.QueryRow(ctx, `INSERT INTO article_extractions (article_id, model, prompt_version, schema_version, verdict, article_kind, result)
			VALUES ($1, 'm', 'v', 'v', 'confirmed', 'current_incident', '{}') RETURNING id`, art).Scan(&ex))
		_, err := store.Pool.Exec(ctx, `INSERT INTO incident_articles (incident_id, article_id, extraction_id, candidate_idx, match_status, matched_by) VALUES ($1,$2,$3,0,'created','rules')`, id, art, ex)
		require.NoError(t, err)
	}
	return id
}

func found(t *testing.T, q string) []string {
	t.Helper()
	require.NoError(t, cl.Refresh(context.Background()))
	hits, err := cl.Search(context.Background(), q, 20)
	require.NoError(t, err, "query %q", q)
	var ids []string
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	return ids
}

func TestSearchFindsIncidentsByPlaceWordsTyposAndArticleTitles(t *testing.T) {
	x := fresh(t)
	oak := seed(t, inc{street: "100 Broadway", city: "Oakland", state: "CA", severity: "fatal", vru: []string{"bicyclist"}, circ: "A cyclist was struck while crossing at night",
		titles: []string{"Cyclist killed on Broadway in Oakland"}})
	sf := seed(t, inc{street: "Market Street", city: "San Francisco", state: "CA", severity: "minor", vru: []string{"pedestrian"}, hitRun: "yes",
		titles: []string{"Pedestrian hurt by driver who fled"}})
	_, err := x.RunNow(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{oak.String()}, found(t, "broadway oakland"))
	assert.Equal(t, []string{oak.String()}, found(t, "braodway"), "a typo still finds it")
	assert.Equal(t, []string{oak.String()}, found(t, "fatal bicyclist"), "category words are searchable")
	assert.Equal(t, []string{sf.String()}, found(t, "hit and run pedestrian"))
	assert.Equal(t, []string{oak.String()}, found(t, "killed"), "article titles are searched")
	// Publishers are searched too. Both incidents have Publisher0, so both come back, the one that also matches "market" first.
	assert.Equal(t, []string{sf.String(), oak.String()}, found(t, "Publisher0 market"))
	assert.Empty(t, found(t, "submarine"))
	for _, nasty := range []string{`"unterminated`, `(((`, `a AND`, `*`, `\`, `field:value`, `   `} {
		assert.NotPanics(t, func() { found(t, nasty) }, nasty)
	}
}

func TestSearchRanksAStreetMatchAboveAWordInAHeadline(t *testing.T) {
	x := fresh(t)
	onStreet := seed(t, inc{street: "Telegraph Avenue", city: "Oakland", state: "CA", severity: "serious"})
	inTitle := seed(t, inc{street: "Grove Street", city: "Berkeley", state: "CA", severity: "serious", titles: []string{"Crash near Telegraph Avenue closes lane"}})
	_, err := x.RunNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{onStreet.String(), inTitle.String()}, found(t, "telegraph"))
}

func TestMergedAndWithdrawnIncidentsLeaveTheIndex(t *testing.T) {
	x := fresh(t)
	ctx := context.Background()
	keep := seed(t, inc{street: "Main Street", city: "Fresno", state: "CA", severity: "fatal"})
	merged := seed(t, inc{street: "Main Street", city: "Fresno", state: "CA", severity: "fatal"})
	withdrawn := seed(t, inc{street: "Main Street", city: "Fresno", state: "CA", severity: "fatal"})
	_, err := x.RunNow(ctx)
	require.NoError(t, err)
	assert.Len(t, found(t, "fresno"), 3)

	// The application bumps updated_at with every change; so does this.
	_, err = store.Pool.Exec(ctx, `UPDATE incidents SET merged_into = $1, updated_at = now() WHERE id = $2`, keep, merged)
	require.NoError(t, err)
	_, err = store.Pool.Exec(ctx, `UPDATE incidents SET verification_status = 'withdrawn', updated_at = now() WHERE id = $1`, withdrawn)
	require.NoError(t, err)
	res, err := x.RunNow(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Removed)
	assert.Equal(t, []string{keep.String()}, found(t, "fresno"))

	n, err := cl.Count(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
}

func TestTheWatermarkMakesRunsIncrementalAndResetRebuilds(t *testing.T) {
	x := fresh(t)
	ctx := context.Background()
	seed(t, inc{street: "A Street", city: "Oakland", state: "CA", severity: "minor"})
	res, err := x.RunNow(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Indexed)

	res, err = x.RunNow(ctx)
	require.NoError(t, err)
	assert.Zero(t, res.Indexed, "nothing changed, nothing written")

	st, err := StatusOf(ctx, store)
	require.NoError(t, err)
	assert.Zero(t, st.Pending)
	require.NotNil(t, st.Watermark)

	seed(t, inc{street: "B Street", city: "Oakland", state: "CA", severity: "minor"})
	st, err = StatusOf(ctx, store)
	require.NoError(t, err)
	assert.EqualValues(t, 1, st.Pending, "the new incident is waiting")
	res, err = x.RunNow(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Indexed, "only the new one")

	require.NoError(t, Reset(ctx, store))
	st, err = StatusOf(ctx, store)
	require.NoError(t, err)
	assert.EqualValues(t, 2, st.Pending, "after a reset everything is pending")
	assert.Nil(t, st.Watermark)
	res, err = x.RunNow(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Indexed)
	assert.Len(t, found(t, "oakland"), 2, "rewriting is idempotent: no duplicates")
}

// Incidents written in the same transaction share an updated_at. The watermark must still visit every one.
func TestBatchesNeverSkipIncidentsThatShareATimestamp(t *testing.T) {
	x := fresh(t)
	x.Batch, x.MaxBatches = 2, 1
	ctx := context.Background()
	same := time.Now().Add(-time.Minute).Truncate(time.Microsecond)
	for i := 0; i < 7; i++ {
		seed(t, inc{street: fmt.Sprintf("%d Elm Street", i), city: "Davis", state: "CA", severity: "minor", updated: same})
	}
	runs := 0
	for {
		res, err := x.RunNow(ctx)
		require.NoError(t, err)
		runs++
		require.LessOrEqual(t, runs, 10)
		if !res.More && res.Indexed == 0 {
			break
		}
	}
	assert.Len(t, found(t, "davis"), 7)
}

func TestVeryRecentChangesAreHeldBackBySafetyLag(t *testing.T) {
	x := fresh(t)
	ctx := context.Background()
	seed(t, inc{street: "New Street", city: "Napa", state: "CA", severity: "minor"})
	res, err := x.Run(ctx) // default 10 s lag
	require.NoError(t, err)
	assert.Zero(t, res.Indexed, "a transaction still open could otherwise be skipped for good")
	res, err = x.RunNow(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Indexed)
}

func TestApplyReportsARejectedDocument(t *testing.T) {
	fresh(t)
	ctx := context.Background()
	require.NoError(t, cl.EnsureIndex(ctx))
	// The mapping is strict: an unknown field must fail loudly, not vanish.
	err := cl.Apply(ctx, []Doc{{ID: uuid.NewString(), Severity: "minor", HitAndRun: "no", Verification: "unverified", OccurredOn: strPtr("not a date")}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bulk index")
	assert.NoError(t, cl.Apply(ctx, nil, []string{uuid.NewString()}), "deleting what is not there is fine")
}

func strPtr(s string) *string { return &s }
