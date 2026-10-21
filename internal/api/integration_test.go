//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.temporal.io/sdk/client"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/domain"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/incidents"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/pipeline"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage/dbgen"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/testutil"
)

var (
	pg    *testutil.Postgres
	store *storage.Store
	tc    client.Client
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
	tcont, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "temporalio/temporal:latest", Cmd: []string{"server", "start-dev", "--ip", "0.0.0.0", "--headless"},
			ExposedPorts: []string{"7233/tcp"}, WaitingFor: wait.ForListeningPort("7233/tcp").WithStartupTimeout(120 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		println("cannot start temporal:", err.Error())
		os.Exit(1)
	}
	host, _ := tcont.Host(ctx)
	port, _ := tcont.MappedPort(ctx, "7233/tcp")
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(time.Second) {
		if tc, err = pipeline.Dial(host+":"+port.Port(), "default", zerolog.Nop()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			println("cannot connect to temporal:", err.Error())
			os.Exit(1)
		}
	}
	code := m.Run()
	tc.Close()
	_ = tcont.Terminate(ctx)
	store.Close()
	pg.Terminate(ctx)
	os.Exit(code)
}

type fakeWF struct {
	decisions []pipeline.ReviewDecision
	incident  uuid.UUID
	err       error
	schedule  *ScheduleState
	actions   []string

	// admin operations
	applied    []applyCall
	applyErr   error
	deleted    []string
	deleteErr  error
	synced     []string // slugs passed to SyncSchedules
	syncRes    [3]int
	syncErr    error
	testRes    *pipeline.SourceTestResult
	testErr    error
	testedWith []discovery.Source

	// bulk jobs
	backfills  []pipeline.BackfillInput
	reprocess  []pipeline.ReprocessInput
	jobs       []pipeline.Job
	jobErr     error
	controlled []string // "id:action"
}

type applyCall struct {
	Slug           string
	EnabledChanged bool
}

func (f *fakeWF) DecideReview(_ context.Context, inc uuid.UUID, d pipeline.ReviewDecision) (*pipeline.ReviewOutcome, error) {
	f.decisions, f.incident = append(f.decisions, d), inc
	if f.err != nil {
		return nil, f.err
	}
	return &pipeline.ReviewOutcome{ReviewID: d.ReviewID, Approved: d.Approve, Target: uuid.New()}, nil
}
func (f *fakeWF) Schedule(context.Context, string) (*ScheduleState, error) {
	if f.schedule == nil {
		return nil, ErrNoSchedule
	}
	return f.schedule, nil
}
func (f *fakeWF) ControlSchedule(_ context.Context, slug, action string) error {
	f.actions = append(f.actions, slug+":"+action)
	return nil
}
func (f *fakeWF) ApplySource(_ context.Context, src discovery.Source, changed bool) error {
	f.applied = append(f.applied, applyCall{src.Slug, changed})
	return f.applyErr
}
func (f *fakeWF) DeleteSchedule(_ context.Context, slug string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, slug)
	return nil
}
func (f *fakeWF) SyncSchedules(_ context.Context, srcs []discovery.Source) (int, int, int, error) {
	for _, s := range srcs {
		f.synced = append(f.synced, s.Slug)
	}
	return f.syncRes[0], f.syncRes[1], f.syncRes[2], f.syncErr
}
func (f *fakeWF) TestSource(_ context.Context, src discovery.Source) (*pipeline.SourceTestResult, error) {
	f.testedWith = append(f.testedWith, src)
	return f.testRes, f.testErr
}

func (f *fakeWF) StartBackfill(_ context.Context, in pipeline.BackfillInput) (string, error) {
	f.backfills = append(f.backfills, in)
	return "backfill-fake", f.jobErr
}
func (f *fakeWF) StartReprocess(_ context.Context, in pipeline.ReprocessInput) (string, error) {
	f.reprocess = append(f.reprocess, in)
	return "reprocess-fake", f.jobErr
}
func (f *fakeWF) ListJobs(_ context.Context, kind string, _ int) ([]pipeline.Job, error) {
	var out []pipeline.Job
	for _, j := range f.jobs {
		if j.Kind == kind {
			out = append(out, j)
		}
	}
	return out, f.jobErr
}
func (f *fakeWF) GetJob(_ context.Context, id string) (*pipeline.Job, error) {
	for _, j := range f.jobs {
		if j.ID == id {
			return &j, f.jobErr
		}
	}
	return nil, pipeline.ErrJobNotFound
}
func (f *fakeWF) ControlJob(_ context.Context, id, action string) error {
	f.controlled = append(f.controlled, id+":"+action)
	return f.jobErr
}

const token = "s3cret"

func newTestServer(t *testing.T, wf *fakeWF, adminToken string) (*httptest.Server, *incidents.Resolver) {
	t.Helper()
	pg.Reset(t, store)
	h, err := NewServer(store, wf, Config{AdminToken: adminToken, Log: zerolog.Nop()}).Handler()
	require.NoError(t, err)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, incidents.NewResolver(store, incidents.DefaultConfig(), zerolog.Nop())
}

func f64(v float64) *float64 { return &v }
func ip(v int) *int          { return &v }

// seed resolves one candidate into an incident, with its own article.
func seed(t *testing.T, r *incidents.Resolver, publisher string, c domain.IncidentCandidate, lat, lon *float64) incidents.Result {
	t.Helper()
	ctx := context.Background()
	q := store.Queries()
	title, text := publisher+" story", "body"
	pub := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	a, err := q.InsertArticle(ctx, dbgen.InsertArticleParams{
		RequestedUrl: "https://" + publisher + "/" + uuid.NewString(), FinalUrl: "https://" + publisher + "/x", Title: &title,
		Publisher: &publisher, PublishedAt: &pub, BodyText: &text, Status: "fetched", FetchedAt: pub,
	})
	require.NoError(t, err)
	ex, err := q.UpsertExtraction(ctx, dbgen.UpsertExtractionParams{ArticleID: a.ID, Model: "m", PromptVersion: "v1", SchemaVersion: "v1",
		Verdict: "confirmed", ArticleKind: "current_incident", Result: []byte(`{}`), Warnings: []string{}})
	require.NoError(t, err)
	res, err := r.Resolve(ctx, incidents.Input{ArticleID: a.ID, ExtractionID: ex.ID, Obs: incidents.Observation{
		Candidate: c, ArticleKind: domain.KindCurrent, Publisher: publisher, ObservedAt: pub, Lat: lat, Lon: lon, GeocodeSource: "test", GeocodePrecision: "rooftop",
	}})
	require.NoError(t, err)
	return *res
}

func cand(date, city, state, street, sev string, vru []string, inj, fat *int) domain.IncidentCandidate {
	return domain.IncidentCandidate{
		OccurredAt: date, OccurredAtPrecision: "date", City: city, State: state, StreetAddress: street, LocationPrecision: "street",
		VRUCategories: vru, VehicleCategories: []string{"passenger_car"}, InjuredCount: inj, FatalityCount: fat,
		Severity: sev, HitAndRun: "no", FactsStatus: "confirmed",
	}
}

func get(t *testing.T, ts *httptest.Server, path string, out any) int {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == 200 {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
	}
	return resp.StatusCode
}

func post(t *testing.T, ts *httptest.Server, path, tok string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func seedMany(t *testing.T, r *incidents.Resolver) {
	seed(t, r, "a.example", cand("2026-10-10", "Oakland", "CA", "100 Broadway", "fatal", []string{"pedestrian"}, nil, ip(1)), f64(37.80), f64(-122.27))
	seed(t, r, "b.example", cand("2026-09-02", "Oakland", "CA", "900 Telegraph Ave", "serious", []string{"bicyclist"}, ip(1), nil), f64(37.81), f64(-122.26))
	seed(t, r, "c.example", cand("2026-09-20", "Los Angeles", "CA", "1 Sunset Blvd", "minor", []string{"pedestrian"}, ip(2), nil), f64(34.06), f64(-118.24))
	seed(t, r, "d.example", cand("2026-08-15", "Portland", "OR", "55 Burnside St", "fatal", []string{"scooter_rider"}, nil, ip(1)), nil, nil) // no coordinates
}

func TestListIncidentsFiltersAndPaging(t *testing.T) {
	ts, r := newTestServer(t, &fakeWF{}, token)
	seedMany(t, r)

	var all IncidentList
	require.Equal(t, 200, get(t, ts, "/v1/incidents", &all))
	assert.EqualValues(t, 4, all.Total)
	assert.Equal(t, "Oakland", *all.Items[0].City, "newest first")

	var ca IncidentList
	get(t, ts, "/v1/incidents?state=ca", &ca)
	assert.EqualValues(t, 3, ca.Total, "state filter is case-insensitive")

	var oakland IncidentList
	get(t, ts, "/v1/incidents?bbox=-122.4,37.7,-122.1,37.9", &oakland)
	assert.EqualValues(t, 2, oakland.Total, "bbox keeps only incidents inside it; those without coordinates are excluded")

	var ped IncidentList
	get(t, ts, "/v1/incidents?vru=pedestrian,scooter_rider", &ped)
	assert.EqualValues(t, 3, ped.Total, "vru matches any of the listed categories")

	var fatal IncidentList
	get(t, ts, "/v1/incidents?severity=fatal&from=2026-10-01&to=2026-10-31", &fatal)
	assert.EqualValues(t, 1, fatal.Total)

	var page IncidentList
	get(t, ts, "/v1/incidents?limit=2&offset=2", &page)
	assert.Len(t, page.Items, 2)
	assert.EqualValues(t, 4, page.Total)
	var past IncidentList
	get(t, ts, "/v1/incidents?limit=2&offset=50", &past)
	assert.Empty(t, past.Items)
	assert.EqualValues(t, 4, past.Total, "total survives an offset past the end")
}

func TestInvalidRequestsAreRejectedBeforeTheHandler(t *testing.T) {
	ts, _ := newTestServer(t, &fakeWF{}, token)
	for _, path := range []string{
		"/v1/incidents?bbox=1,2,3",
		"/v1/incidents?bbox=10,10,5,5",
		"/v1/incidents?severity=bogus",
		"/v1/incidents?limit=100000",
		"/v1/incidents?from=yesterday",
		"/v1/incidents?from=2026-10-10&to=2026-10-01",
		"/v1/stats/breakdown?by=password",
		"/v1/stats/breakdown",
		"/v1/incidents/not-a-uuid",
	} {
		assert.Equal(t, 400, get(t, ts, path, nil), path)
	}
	assert.Equal(t, 404, get(t, ts, "/v1/incidents/"+uuid.NewString(), nil))
}

func TestIncidentDetailShowsEvidenceTrail(t *testing.T) {
	ts, r := newTestServer(t, &fakeWF{}, token)
	a := seed(t, r, "a.example", cand("2026-10-10", "Oakland", "CA", "100 Broadway", "serious", []string{"pedestrian"}, ip(1), nil), f64(37.80), f64(-122.27))
	b := seed(t, r, "b.example", cand("2026-10-10", "Oakland", "CA", "Broadway and 1st", "fatal", []string{"pedestrian"}, ip(1), ip(1)), f64(37.80), f64(-122.27))
	require.Equal(t, a.IncidentID, b.IncidentID, "second article matched the first")

	var d IncidentDetail
	require.Equal(t, 200, get(t, ts, "/v1/incidents/"+a.IncidentID.String(), &d))
	assert.Equal(t, IncidentSeverity("fatal"), d.Incident.Severity, "later reports raise severity")
	assert.Len(t, d.Articles, 2)
	assert.Len(t, d.Observations, 2)
	assert.NotEmpty(t, d.Events)
	assert.Equal(t, pipeline.IncidentWorkflowID(a.IncidentID.String()), d.WorkflowId)
	assert.Equal(t, 37.80, *d.Incident.Lat)
}

func TestStats(t *testing.T) {
	ts, r := newTestServer(t, &fakeWF{}, token)
	seedMany(t, r)

	var sum StatsSummary
	require.Equal(t, 200, get(t, ts, "/v1/stats/summary", &sum))
	assert.EqualValues(t, 4, sum.Incidents)
	assert.EqualValues(t, 2, sum.Fatalities)
	assert.EqualValues(t, 3, sum.Injured)
	assert.EqualValues(t, 2, sum.FatalIncidents)
	assert.EqualValues(t, 3, sum.WithCoordinates)

	var ts1 []TimeseriesPoint
	require.Equal(t, 200, get(t, ts, "/v1/stats/timeseries?bucket=month", &ts1))
	require.Len(t, ts1, 3)
	assert.Equal(t, "2026-08-01", ts1[0].Bucket.Format("2006-01-02"))
	assert.EqualValues(t, 2, ts1[1].Incidents, "September has two incidents")

	var by []BreakdownRow
	require.Equal(t, 200, get(t, ts, "/v1/stats/breakdown?by=vru", &by))
	got := map[string]int64{}
	for _, row := range by {
		got[row.Key] = row.Incidents
	}
	assert.Equal(t, map[string]int64{"pedestrian": 2, "bicyclist": 1, "scooter_rider": 1}, got)

	require.Equal(t, 200, get(t, ts, "/v1/stats/breakdown?by=state&limit=1", &by))
	require.Len(t, by, 1)
	assert.Equal(t, "CA", by[0].Key)
	assert.EqualValues(t, 3, by[0].Incidents)
}

func possibleDuplicate(t *testing.T, r *incidents.Resolver) (reviewID, newInc uuid.UUID) {
	// The second report has the same place and victim but no date: plausible, not certain, so it goes to review.
	seed(t, r, "a.example", cand("2026-10-10", "Oakland", "CA", "100 Broadway", "serious", []string{"pedestrian"}, ip(1), nil), nil, nil)
	undated := cand("", "Oakland", "CA", "100 Broadway", "serious", []string{"pedestrian"}, ip(1), nil)
	undated.OccurredAtPrecision = "unknown"
	res := seed(t, r, "b.example", undated, nil, nil)
	require.Equal(t, incidents.OutcomeReview, res.Outcome, "fixture must produce a review")
	pend, err := store.Queries().ListPendingReviews(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, pend, 1)
	return pend[0].ID, pend[0].NewIncidentID
}

func TestReviewsListAndDecisionRequireAdminToken(t *testing.T) {
	wf := &fakeWF{}
	ts, r := newTestServer(t, wf, token)
	rid, newInc := possibleDuplicate(t, r)

	var reviews []Review
	require.Equal(t, 200, get(t, ts, "/v1/reviews", &reviews))
	require.Len(t, reviews, 1)
	assert.Equal(t, rid, reviews[0].Id)
	assert.NotEqual(t, reviews[0].NewIncident.Id, reviews[0].CandidateIncident.Id)

	path := "/v1/reviews/" + rid.String() + "/decision"
	body := map[string]any{"approve": true, "actor": "josh"}
	code, _ := post(t, ts, path, "", body)
	assert.Equal(t, 401, code, "no token")
	code, _ = post(t, ts, path, "wrong", body)
	assert.Equal(t, 401, code, "wrong token")
	assert.Empty(t, wf.decisions, "nothing reached the workflow")

	code, _ = post(t, ts, path, token, map[string]any{"approve": true, "actor": ""})
	assert.Equal(t, 400, code, "actor is required")

	code, res := post(t, ts, path, token, body)
	require.Equal(t, 200, code)
	assert.Equal(t, true, res["approved"])
	require.Len(t, wf.decisions, 1)
	assert.Equal(t, newInc, wf.incident, "decision goes to the new incident's workflow")
	assert.Equal(t, "josh", wf.decisions[0].Actor)

	code, _ = post(t, ts, "/v1/reviews/"+uuid.NewString()+"/decision", token, body)
	assert.Equal(t, 404, code)

	wf.err = ErrNotPending
	code, _ = post(t, ts, path, token, body)
	assert.Equal(t, 409, code, "workflow says it was already decided")
}

func TestAdminRoutesAreDisabledWithoutAConfiguredToken(t *testing.T) {
	ts, r := newTestServer(t, &fakeWF{}, "")
	rid, _ := possibleDuplicate(t, r)
	code, _ := post(t, ts, "/v1/reviews/"+rid.String()+"/decision", "", map[string]any{"approve": true, "actor": "x"})
	assert.Equal(t, 401, code)
	code, _ = post(t, ts, "/v1/reviews/"+rid.String()+"/decision", "anything", map[string]any{"approve": true, "actor": "x"})
	assert.Equal(t, 401, code)
}

func sp(s string) *string { return &s }

func TestPipelineSummaryAndHealth(t *testing.T) {
	ts, r := newTestServer(t, &fakeWF{}, token)
	seedMany(t, r)
	var p PipelineSummary
	require.Equal(t, 200, get(t, ts, "/v1/ops/pipeline", &p))
	assert.EqualValues(t, 4, p.Counts.Articles)
	assert.EqualValues(t, 4, p.Counts.Extractions)
	assert.Equal(t, 200, get(t, ts, "/healthz", nil))
	assert.Equal(t, 200, get(t, ts, "/readyz", nil))
}

// The decision goes through the real path: API -> update-with-start -> IncidentWorkflow
// (started by the update, since no workflow exists yet) -> activity -> resolver -> database.
func TestApprovingAReviewMergesThroughTemporal(t *testing.T) {
	ts, r := newTestServer(t, nil, token)
	// Replace the server with one wired to real Temporal.
	ts.Close()
	h, err := NewServer(store, TemporalWorkflows{C: tc}, Config{AdminToken: token, Log: zerolog.Nop()}).Handler()
	require.NoError(t, err)
	ts = httptest.NewServer(h)
	defer ts.Close()

	rid, newInc := possibleDuplicate(t, r)
	acts := &pipeline.Activities{DB: store, Resolver: r, Temporal: tc, Log: zerolog.Nop()}
	for _, w := range pipeline.NewWorkers(tc, acts, pipeline.DefaultWorkerConfig()) {
		require.NoError(t, w.Start())
		defer w.Stop()
	}

	path := "/v1/reviews/" + rid.String() + "/decision"
	code, res := post(t, ts, path, token, map[string]any{"approve": true, "actor": "josh"})
	require.Equal(t, 200, code, "%v", res)
	assert.Equal(t, true, res["approved"])

	var mergedInto *uuid.UUID
	var status string
	require.NoError(t, store.Pool.QueryRow(context.Background(), `SELECT merged_into FROM incidents WHERE id = $1`, newInc).Scan(&mergedInto))
	require.NotNil(t, mergedInto, "the provisional incident was merged into the existing one")
	require.NoError(t, store.Pool.QueryRow(context.Background(), `SELECT status FROM match_reviews WHERE id = $1`, rid).Scan(&status))
	assert.Equal(t, "approved", status)
	assert.Equal(t, res["target"], mergedInto.String())

	// Deciding again is refused, not applied twice.
	code, _ = post(t, ts, path, token, map[string]any{"approve": true, "actor": "josh"})
	assert.Equal(t, 409, code)

	var n int
	require.NoError(t, store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM incident_events WHERE event_type = 'review_approved'`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestMetricsReflectRequestsAndPipelineState(t *testing.T) {
	pg.Reset(t, store)
	reg := prometheus.NewRegistry()
	reg.MustRegister(NewPipelineCollector(store))
	h, err := NewServer(store, &fakeWF{}, Config{AdminToken: token, Log: zerolog.Nop(), Metrics: NewHTTPMetrics(reg)}).Handler()
	require.NoError(t, err)
	ts := httptest.NewServer(h)
	defer ts.Close()
	r := incidents.NewResolver(store, incidents.DefaultConfig(), zerolog.Nop())
	seedMany(t, r)
	_, err = store.Queries().UpsertSource(context.Background(), dbgen.UpsertSourceParams{Slug: "s1", Name: "S1", SourceType: "rss", Url: sp("https://x/feed"),
		Coverage: []byte(`{}`), PollIntervalSeconds: 1800, Enabled: true, CrawlPolicy: []byte(`{}`)})
	require.NoError(t, err)

	get(t, ts, "/v1/incidents", nil)
	get(t, ts, "/v1/incidents/"+uuid.NewString(), nil) // 404
	get(t, ts, "/v1/incidents?severity=bogus", nil)    // rejected by validation

	mf, err := reg.Gather()
	require.NoError(t, err)
	vals := map[string]float64{}
	for _, f := range mf {
		for _, m := range f.GetMetric() {
			key := f.GetName()
			for _, l := range m.GetLabel() {
				key += "|" + l.GetName() + "=" + l.GetValue()
			}
			switch {
			case m.GetGauge() != nil:
				vals[key] = m.GetGauge().GetValue()
			case m.GetCounter() != nil:
				vals[key] = m.GetCounter().GetValue()
			}
		}
	}
	assert.Equal(t, 1.0, vals["collision_pipeline_collector_up"])
	assert.Equal(t, 4.0, vals["collision_incidents"])
	assert.Equal(t, 4.0, vals["collision_articles|status=fetched"])
	assert.Equal(t, 4.0, vals["collision_llm_extractions_total|model=m"])
	assert.Equal(t, 0.0, vals["collision_source_last_success_timestamp_seconds|source=s1"], "never succeeded")
	assert.Equal(t, 1800.0, vals["collision_source_poll_interval_seconds|source=s1"])
	assert.Equal(t, 1.0, vals["collision_http_requests_total|method=GET|route=/v1/incidents|status=200"])
	assert.Equal(t, 1.0, vals["collision_http_requests_total|method=GET|route=/v1/incidents/{id}|status=404"])
	assert.Equal(t, 1.0, vals["collision_http_requests_total|method=GET|route=/v1/*|status=400"], "requests the validator rejects never reach a route")
}
