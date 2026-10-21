//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/pgrepo"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/pipeline"
)

// call sends a request with the admin token (or none, if tok is empty) and decodes a JSON response into out.
func call(t *testing.T, ts *httptest.Server, method, path, tok string, body, out any) int {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, ts.URL+path, rd)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 && resp.StatusCode != http.StatusNoContent {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
	}
	if out != nil && resp.StatusCode >= 400 {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func feedSource(slug string) map[string]any {
	return map[string]any{
		"slug": slug, "name": "Feed " + slug, "type": "rss", "url": "https://example.org/" + slug + ".xml",
		"poll_interval_seconds": 1800, "enabled": true,
		"coverage": map[string]any{"country": "US", "states": []string{"CA"}},
	}
}

func TestEveryAdminRouteNeedsTheToken(t *testing.T) {
	ts, _ := newTestServer(t, &fakeWF{}, token)
	routes := []struct{ method, path string }{
		{"GET", "/v1/admin/source-types"},
		{"GET", "/v1/admin/sources"},
		{"POST", "/v1/admin/sources"},
		{"GET", "/v1/admin/sources/x"},
		{"PUT", "/v1/admin/sources/x"},
		{"DELETE", "/v1/admin/sources/x"},
		{"POST", "/v1/admin/sources/x/schedule"},
		{"GET", "/v1/admin/sources/x/runs"},
		{"POST", "/v1/admin/source-tests"},
		{"POST", "/v1/admin/schedules/sync"},
		{"GET", "/v1/admin/jobs?kind=backfill"},
		{"GET", "/v1/admin/jobs/x"},
		{"POST", "/v1/admin/jobs/x/control"},
		{"POST", "/v1/admin/backfills"},
		{"POST", "/v1/admin/reprocesses"},
		{"POST", "/v1/admin/incidents/6f1c1f8e-2d7a-4a55-9d0e-8a3f7d1c2b11/split"},
		{"GET", "/v1/admin/search"},
		{"POST", "/v1/admin/search/reindex"},
		{"GET", "/v1/admin/queries"},
		{"POST", "/v1/admin/queries"},
		{"PUT", "/v1/admin/queries/x"},
		{"DELETE", "/v1/admin/queries/x"},
	}
	for _, r := range routes {
		assert.Equal(t, 401, call(t, ts, r.method, r.path, "", feedSource("x"), nil), "%s %s without a token", r.method, r.path)
		assert.Equal(t, 401, call(t, ts, r.method, r.path, "wrong", feedSource("x"), nil), "%s %s with a wrong token", r.method, r.path)
	}
	// Reading source health is admin information; the public read API must not expose it.
	assert.Equal(t, 404, call(t, ts, "GET", "/v1/ops/sources", "", nil, nil), "the old public sources route is gone")
	assert.Equal(t, 200, call(t, ts, "GET", "/v1/ops/pipeline", "", nil, nil), "pipeline counters stay public")
}

func TestAdminRoutesAreDisabledWithoutAConfiguredToken_Sources(t *testing.T) {
	ts, _ := newTestServer(t, &fakeWF{}, "")
	assert.Equal(t, 401, call(t, ts, "GET", "/v1/admin/sources", "anything", nil, nil))
}

func TestSourceTypesComeFromTheProviders(t *testing.T) {
	ts, _ := newTestServer(t, &fakeWF{}, token)
	var types []SourceType
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/source-types", token, nil, &types))
	by := map[string]SourceType{}
	for _, ty := range types {
		by[ty.Type] = ty
	}
	require.Len(t, by, 8)
	assert.Equal(t, SourceTypeUrl("required"), by["rss"].Url)
	assert.False(t, by["rss"].UsesQueries)
	assert.True(t, by["gdelt"].UsesQueries)
	assert.Equal(t, SourceTypeUrl("none"), by["gdelt"].Url)
	assert.True(t, by["police_press"].Crawl.RequireDomains && by["police_press"].Crawl.Render)
	assert.True(t, by["sitemap"].Crawl.MaxPages)
	assert.False(t, by["sitemap"].Crawl.AllowedDomains)
}

func TestCreateUpdateDeleteSource(t *testing.T) {
	wf := &fakeWF{}
	ts, _ := newTestServer(t, wf, token)

	var created SourceDetail
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/sources", token, feedSource("alpha"), &created))
	assert.Equal(t, "alpha", created.Slug)
	assert.Equal(t, HealthStateWaiting, created.Health.State, "a source that never ran is waiting, not healthy")
	assert.Equal(t, "US", *created.Coverage.Country)
	assert.Nil(t, created.ScheduleError)
	assert.Equal(t, []applyCall{{"alpha", false}}, wf.applied, "creating a source creates its schedule")

	var dup Error
	assert.Equal(t, 409, call(t, ts, "POST", "/v1/admin/sources", token, feedSource("alpha"), &dup))
	assert.Contains(t, dup.Error, "alpha")

	// Every problem is reported together, and nothing is stored.
	bad := map[string]any{"slug": "Bad Slug", "name": " ", "type": "rss", "url": "ftp://nope", "poll_interval_seconds": 60, "enabled": true}
	var verr Error
	require.Equal(t, 400, call(t, ts, "POST", "/v1/admin/sources", token, bad, &verr))
	require.NotNil(t, verr.Problems)
	assert.GreaterOrEqual(t, len(*verr.Problems), 3, "slug, name and url: %v", *verr.Problems)
	bad2 := map[string]any{"slug": "typo", "name": "x", "type": "carrier_pigeon", "poll_interval_seconds": 600, "enabled": true}
	require.Equal(t, 400, call(t, ts, "POST", "/v1/admin/sources", token, bad2, &verr))
	assert.Contains(t, verr.Error, "unknown type")
	var list []SourceDetail
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/sources", token, nil, &list))
	require.Len(t, list, 1, "invalid sources are not stored")

	// An edit that does not touch enabled must not change the schedule's pause state.
	wf.applied = nil
	edit := feedSource("alpha")
	edit["name"], edit["poll_interval_seconds"] = "Renamed", 3600
	var updated SourceDetail
	require.Equal(t, 200, call(t, ts, "PUT", "/v1/admin/sources/alpha", token, edit, &updated))
	assert.Equal(t, "Renamed", updated.Name)
	assert.Equal(t, 3600, updated.PollIntervalSeconds)
	assert.Equal(t, []applyCall{{"alpha", false}}, wf.applied)

	// Disabling does.
	wf.applied = nil
	edit["enabled"] = false
	require.Equal(t, 200, call(t, ts, "PUT", "/v1/admin/sources/alpha", token, edit, &updated))
	assert.Equal(t, HealthStateDisabled, updated.Health.State)
	assert.Equal(t, []applyCall{{"alpha", true}}, wf.applied)

	assert.Equal(t, 400, call(t, ts, "PUT", "/v1/admin/sources/alpha", token, feedSource("other"), nil), "the slug cannot change")
	assert.Equal(t, 404, call(t, ts, "PUT", "/v1/admin/sources/ghost", token, feedSource("ghost"), nil))
	assert.Equal(t, 404, call(t, ts, "GET", "/v1/admin/sources/ghost", token, nil, nil))

	// Delete removes the schedule, then the source.
	assert.Equal(t, 204, call(t, ts, "DELETE", "/v1/admin/sources/alpha", token, nil, nil))
	assert.Equal(t, []string{"alpha"}, wf.deleted)
	assert.Equal(t, 404, call(t, ts, "GET", "/v1/admin/sources/alpha", token, nil, nil))
	assert.Equal(t, 404, call(t, ts, "DELETE", "/v1/admin/sources/alpha", token, nil, nil))
}

func TestSavedSourceSurvivesAScheduleFailure(t *testing.T) {
	wf := &fakeWF{applyErr: errors.New("temporal unreachable")}
	ts, _ := newTestServer(t, wf, token)
	var d SourceDetail
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/sources", token, feedSource("beta"), &d))
	require.NotNil(t, d.ScheduleError, "the person must be told the schedule is not in place")
	assert.Contains(t, *d.ScheduleError, "temporal unreachable")
	var got SourceDetail
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/sources/beta", token, nil, &got), "the source itself was saved")
}

func TestDeleteLeavesTheSourceIfItsScheduleCannotBeRemoved(t *testing.T) {
	wf := &fakeWF{}
	ts, _ := newTestServer(t, wf, token)
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/sources", token, feedSource("gamma"), nil))
	wf.deleteErr = ErrTemporalDown
	assert.Equal(t, 503, call(t, ts, "DELETE", "/v1/admin/sources/gamma", token, nil, nil))
	assert.Equal(t, 200, call(t, ts, "GET", "/v1/admin/sources/gamma", token, nil, nil),
		"deleting the source while its schedule lives would leave a schedule that fails every interval")
}

func TestSourceDetailShowsHealthRunsAndSchedule(t *testing.T) {
	paused := true
	wf := &fakeWF{}
	ts, _ := newTestServer(t, wf, token)
	for _, slug := range []string{"sick", "fine", "held"} {
		require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/sources", token, feedSource(slug), nil))
	}
	repo := pgrepo.New(store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	run := func(slug string, ago time.Duration, status discovery.RunStatus, errText string, newURLs int) {
		at := now.Add(-ago)
		require.NoError(t, repo.FinishRun(ctx, discovery.RunRecord{Slug: slug, StartedAt: at, FinishedAt: at.Add(3 * time.Second), Status: status,
			Found: 10, Unique: 9, New: newURLs, Requests: 2, Error: errText, Warnings: []string{"slow"}}))
	}
	for i := 3; i >= 1; i-- {
		run("sick", time.Duration(i)*time.Minute, discovery.RunFailed, "feed sick: http 503", 0)
	}
	run("fine", 2*time.Minute, discovery.RunSuccess, "", 4)
	run("fine", time.Minute, discovery.RunSuccess, "", 6)
	run("held", time.Minute, discovery.RunSuccess, "", 1)

	var list []SourceDetail
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/sources", token, nil, &list))
	by := map[string]SourceDetail{}
	for _, d := range list {
		by[d.Slug] = d
	}
	assert.Equal(t, HealthStateFailing, by["sick"].Health.State)
	assert.Contains(t, by["sick"].Health.Reason, "http 503")
	assert.EqualValues(t, 3, by["sick"].RunStats.Failures)
	assert.Equal(t, 0.0, by["sick"].RunStats.SuccessRate)
	assert.Equal(t, 3, by["sick"].ConsecutiveFailures)

	assert.Equal(t, HealthStateHealthy, by["fine"].Health.State)
	assert.EqualValues(t, 2, by["fine"].RunStats.Runs)
	assert.EqualValues(t, 10, by["fine"].RunStats.NewUrls)
	assert.Equal(t, 1.0, by["fine"].RunStats.SuccessRate)
	assert.InDelta(t, 3.0, by["fine"].RunStats.AvgSeconds, 0.01)

	// Run history, newest first.
	var runs []SourceRun
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/sources/fine/runs", token, nil, &runs))
	require.Len(t, runs, 2)
	assert.EqualValues(t, 6, runs[0].NewUrls)
	assert.Equal(t, []string{"slow"}, runs[0].Warnings)
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/sources/fine/runs?limit=1", token, nil, &runs))
	assert.Len(t, runs, 1)
	assert.Equal(t, 404, call(t, ts, "GET", "/v1/admin/sources/ghost/runs", token, nil, nil))
	assert.Equal(t, 400, call(t, ts, "GET", "/v1/admin/sources/fine/runs?limit=0", token, nil, nil))

	// A paused schedule shows as paused, not healthy.
	wf.schedule = &ScheduleState{Paused: &paused}
	var one SourceDetail
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/sources/held", token, nil, &one))
	assert.Equal(t, HealthStatePaused, one.Health.State)
	require.NotNil(t, one.Schedule)
}

func TestScheduleControlLivesUnderAdmin(t *testing.T) {
	wf := &fakeWF{}
	ts, _ := newTestServer(t, wf, token)
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/sources", token, feedSource("oaklandside"), nil))
	body := map[string]any{"action": "trigger"}
	assert.Equal(t, 401, call(t, ts, "POST", "/v1/admin/sources/oaklandside/schedule", "", body, nil))
	assert.Equal(t, 400, call(t, ts, "POST", "/v1/admin/sources/oaklandside/schedule", token, map[string]any{"action": "explode"}, nil))
	assert.Equal(t, 404, call(t, ts, "POST", "/v1/admin/sources/nope/schedule", token, body, nil))
	assert.Equal(t, 204, call(t, ts, "POST", "/v1/admin/sources/oaklandside/schedule", token, body, nil))
	assert.Equal(t, []string{"oaklandside:trigger"}, wf.actions)
}

func TestTestingASourceSavesNothing(t *testing.T) {
	wf := &fakeWF{testRes: &pipeline.SourceTestResult{
		OK: true, Found: 12, Unique: 10, Requests: 1, DurationMS: 840, Warnings: []string{"w"},
		Samples: []pipeline.TestSample{{URL: "https://example.org/a", Title: "A"}},
	}}
	ts, _ := newTestServer(t, wf, token)

	var res SourceTestResult
	require.Equal(t, 200, call(t, ts, "POST", "/v1/admin/source-tests", token, feedSource("draft"), &res))
	assert.True(t, res.Ok)
	assert.Equal(t, 12, res.Found)
	assert.EqualValues(t, 840, res.DurationMs)
	require.Len(t, res.Samples, 1)
	assert.Equal(t, "A", *res.Samples[0].Title)
	require.Len(t, wf.testedWith, 1)
	assert.Equal(t, "draft", wf.testedWith[0].Slug)
	assert.Equal(t, 30*time.Minute, wf.testedWith[0].PollInterval)
	var list []SourceDetail
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/sources", token, nil, &list))
	assert.Empty(t, list, "a test is not a save")

	// An invalid configuration is rejected before any worker is involved.
	wf.testedWith = nil
	assert.Equal(t, 400, call(t, ts, "POST", "/v1/admin/source-tests", token, map[string]any{"slug": "x", "name": "x", "type": "rss", "poll_interval_seconds": 600, "enabled": true}, nil))
	assert.Empty(t, wf.testedWith)

	// A provider failure is a result the UI can show, not an HTTP error.
	wf.testRes = &pipeline.SourceTestResult{OK: false, Error: "feed draft: http 503"}
	require.Equal(t, 200, call(t, ts, "POST", "/v1/admin/source-tests", token, feedSource("draft"), &res))
	assert.False(t, res.Ok)
	assert.Equal(t, "feed draft: http 503", *res.Error)
	assert.NotNil(t, res.Samples)

	// No discovery worker answering is a 503 with a useful message.
	wf.testErr = ErrWorkerUnavailable
	var e Error
	assert.Equal(t, 503, call(t, ts, "POST", "/v1/admin/source-tests", token, feedSource("draft"), &e))
	assert.Contains(t, e.Error, "worker")
}

func TestSyncSchedulesEndpoint(t *testing.T) {
	wf := &fakeWF{syncRes: [3]int{2, 5, 1}}
	ts, _ := newTestServer(t, wf, token)
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/sources", token, feedSource("one"), nil))
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/sources", token, feedSource("two"), nil))
	var out struct{ Created, Updated, Removed int }
	require.Equal(t, 200, call(t, ts, "POST", "/v1/admin/schedules/sync", token, nil, &out))
	assert.Equal(t, 2, out.Created)
	assert.Equal(t, 5, out.Updated)
	assert.Equal(t, 1, out.Removed)
	assert.ElementsMatch(t, []string{"one", "two"}, wf.synced)
}

func TestQueriesCRUDAndValidation(t *testing.T) {
	ts, _ := newTestServer(t, &fakeWF{}, token)
	q := map[string]any{"family": "pedestrian", "name": "ped-struck", "text": "(pedestrian OR walker) (struck OR hit)", "enabled": true}
	var got Query
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/queries", token, q, &got))
	require.NotNil(t, got.Providers, "an empty provider list round-trips as [], meaning every search provider")
	assert.Empty(t, *got.Providers)
	assert.Equal(t, 409, call(t, ts, "POST", "/v1/admin/queries", token, q, nil))

	var e Error
	assert.Equal(t, 400, call(t, ts, "POST", "/v1/admin/queries", token, map[string]any{"family": "p", "name": "bare-or", "text": "pedestrian OR walker", "enabled": true}, &e))
	assert.Contains(t, e.Error, "OR must be inside parentheses")
	assert.Equal(t, 400, call(t, ts, "POST", "/v1/admin/queries", token, map[string]any{"family": "p", "name": "wrong-prov", "text": "(a OR b)", "providers": []string{"rss"}, "enabled": true}, &e))
	assert.Contains(t, e.Error, "does not run search queries")

	q["text"], q["providers"], q["enabled"] = "(cyclist OR bicyclist) hit", []string{"gdelt"}, false
	require.Equal(t, 200, call(t, ts, "PUT", "/v1/admin/queries/ped-struck", token, q, &got))
	assert.Equal(t, "(cyclist OR bicyclist) hit", got.Text)
	assert.False(t, got.Enabled)
	assert.Equal(t, 400, call(t, ts, "PUT", "/v1/admin/queries/other-name", token, q, nil), "the name cannot change")
	q["name"] = "ghost"
	assert.Equal(t, 404, call(t, ts, "PUT", "/v1/admin/queries/ghost", token, q, nil))

	var all []Query
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/queries", token, nil, &all))
	require.Len(t, all, 1)
	assert.Equal(t, 204, call(t, ts, "DELETE", "/v1/admin/queries/ped-struck", token, nil, nil))
	assert.Equal(t, 404, call(t, ts, "DELETE", "/v1/admin/queries/ped-struck", token, nil, nil))
}
