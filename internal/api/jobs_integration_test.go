//go:build integration

package api

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/pipeline"
)

func backfillBody(sources ...string) map[string]any {
	return map[string]any{"sources": sources, "from": "2026-01-01", "to": "2026-01-08"}
}

func TestStartBackfillValidatesThenStartsWithDefaults(t *testing.T) {
	wf := &fakeWF{}
	ts, _ := newTestServer(t, wf, token)
	var created SourceDetail
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/sources", token, feedSource("alpha"), &created))

	cases := map[string]map[string]any{
		"no sources":     {"sources": []string{}, "from": "2026-01-01", "to": "2026-01-08"},
		"unknown source": backfillBody("nope"),
		"repeated":       backfillBody("alpha", "alpha"),
		"empty range":    {"sources": []string{"alpha"}, "from": "2026-01-08", "to": "2026-01-08"},
		"backwards":      {"sources": []string{"alpha"}, "from": "2026-01-09", "to": "2026-01-08"},
		"too long":       {"sources": []string{"alpha"}, "from": "2000-01-01", "to": "2026-01-08"},
		"bad date":       {"sources": []string{"alpha"}, "from": "yesterday", "to": "2026-01-08"},
		"huge cap":       {"sources": []string{"alpha"}, "from": "2026-01-01", "to": "2026-01-08", "max_articles": 10000000},
	}
	for name, body := range cases {
		var e Error
		assert.Equal(t, 400, call(t, ts, "POST", "/v1/admin/backfills", token, body, &e), name)
	}
	assert.Empty(t, wf.backfills, "nothing started for invalid requests")

	var job Job
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/backfills", token, backfillBody("alpha"), &job))
	assert.Equal(t, "backfill-fake", job.Id)
	assert.Equal(t, JobKindBackfill, job.Kind)
	require.Len(t, wf.backfills, 1)
	in := wf.backfills[0]
	assert.Equal(t, []string{"alpha"}, in.Slugs)
	assert.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), in.From)
	assert.Equal(t, 24*time.Hour, in.WindowSize, "default window")
	assert.Equal(t, 1000, in.MaxArticles, "default volume bound")

	body := backfillBody("alpha")
	body["window_hours"], body["max_articles"] = 6, 50
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/backfills", token, body, &job))
	assert.Equal(t, 6*time.Hour, wf.backfills[1].WindowSize)
	assert.Equal(t, 50, wf.backfills[1].MaxArticles)
}

func TestStartReprocessDefaultsAndBounds(t *testing.T) {
	wf := &fakeWF{}
	ts, _ := newTestServer(t, wf, token)
	var job Job
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/reprocesses", token, map[string]any{}, &job))
	assert.Equal(t, JobKindReprocess, job.Kind)
	assert.Equal(t, pipeline.ReprocessInput{Limit: 100, BatchSize: 20, MaxConcurrent: 4}, wf.reprocess[0])

	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/reprocesses", token, map[string]any{"limit": 0, "concurrency": 2}, &job))
	assert.Equal(t, 0, wf.reprocess[1].Limit, "0 means all")
	assert.Equal(t, 400, call(t, ts, "POST", "/v1/admin/reprocesses", token, map[string]any{"concurrency": 99}, nil))
	assert.Equal(t, 400, call(t, ts, "POST", "/v1/admin/reprocesses", token, map[string]any{"batch_size": 0}, nil))
}

func TestJobsListGetAndControl(t *testing.T) {
	now := time.Now().UTC()
	wf := &fakeWF{jobs: []pipeline.Job{
		{ID: "backfill-1", Kind: "backfill", Status: "running", StartedAt: now, Backfill: &pipeline.BackfillProgress{Phase: "paused", StepsDone: 2, TotalSteps: 7, Found: 30, Started: 12, MaxArticles: 100, Current: "alpha 2026-01-03..2026-01-04"}},
		{ID: "reprocess-1", Kind: "reprocess", Status: "completed", StartedAt: now, ClosedAt: &now, Reprocess: &pipeline.ReprocessResult{Examined: 9, Changed: 2, Failed: 1}},
	}}
	ts, _ := newTestServer(t, wf, token)

	var bf []Job
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/jobs?kind=backfill", token, nil, &bf))
	require.Len(t, bf, 1)
	assert.Equal(t, BackfillProgressPhase("paused"), bf[0].Backfill.Phase)
	assert.Equal(t, 12, bf[0].Backfill.ArticlesStarted)
	assert.Nil(t, bf[0].Reprocess)

	var rp Job
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/jobs/reprocess-1", token, nil, &rp))
	assert.Equal(t, ReprocessProgress{Examined: 9, Changed: 2, Failed: 1}, *rp.Reprocess)
	assert.NotNil(t, rp.ClosedAt)

	assert.Equal(t, 404, call(t, ts, "GET", "/v1/admin/jobs/nope", token, nil, nil))
	assert.Equal(t, 400, call(t, ts, "GET", "/v1/admin/jobs?kind=other", token, nil, nil))
	assert.Equal(t, 400, call(t, ts, "GET", "/v1/admin/jobs", token, nil, nil), "kind is required")

	assert.Equal(t, 204, call(t, ts, "POST", "/v1/admin/jobs/backfill-1/control", token, map[string]any{"action": "resume"}, nil))
	assert.Equal(t, []string{"backfill-1:resume"}, wf.controlled)
	assert.Equal(t, 400, call(t, ts, "POST", "/v1/admin/jobs/backfill-1/control", token, map[string]any{"action": "explode"}, nil))

	wf.jobErr = pipeline.ErrJobAction
	var e Error
	assert.Equal(t, 409, call(t, ts, "POST", "/v1/admin/jobs/backfill-1/control", token, map[string]any{"action": "pause"}, &e))
	assert.NotEmpty(t, e.Error)
	wf.jobErr = pipeline.ErrJobNotFound
	assert.Equal(t, 404, call(t, ts, "POST", "/v1/admin/jobs/gone/control", token, map[string]any{"action": "pause"}, nil))
}

// The same flow through the real Temporal wrapper. No worker runs, so the backfill just sits started:
// enough to check starting, reading, signalling and the error mapping against a real server.
func TestJobsThroughTemporal(t *testing.T) {
	pg.Reset(t, store)
	h, err := NewServer(store, TemporalWorkflows{C: tc}, Config{AdminToken: token, Log: zerolog.Nop()}).Handler()
	require.NoError(t, err)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	var created SourceDetail
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/sources", token, feedSource("beta"), &created))

	var job Job
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/backfills", token, backfillBody("beta"), &job))
	assert.Contains(t, job.Id, "backfill-")

	var got Job
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/jobs/"+job.Id, token, nil, &got))
	assert.Equal(t, job.Id, got.Id)
	assert.Equal(t, JobKindBackfill, got.Kind)
	assert.Equal(t, JobStatus("running"), got.Status)

	assert.Equal(t, 204, call(t, ts, "POST", "/v1/admin/jobs/"+job.Id+"/control", token, map[string]any{"action": "pause"}, nil))

	var rp Job
	require.Equal(t, 201, call(t, ts, "POST", "/v1/admin/reprocesses", token, map[string]any{}, &rp))
	var e Error
	assert.Equal(t, 409, call(t, ts, "POST", "/v1/admin/jobs/"+rp.Id+"/control", token, map[string]any{"action": "pause"}, &e), "a reprocess run cannot be paused")
	assert.NotEmpty(t, e.Error)
	assert.Equal(t, 404, call(t, ts, "GET", "/v1/admin/jobs/backfill-never-started", token, nil, nil))
	assert.Equal(t, 404, call(t, ts, "POST", "/v1/admin/jobs/not-a-job/control", token, map[string]any{"action": "cancel"}, nil))

	require.Equal(t, 204, call(t, ts, "POST", "/v1/admin/jobs/"+rp.Id+"/control", token, map[string]any{"action": "cancel"}, nil))
	var list []Job
	require.Eventually(t, func() bool {
		return call(t, ts, "GET", "/v1/admin/jobs?kind=backfill", token, nil, &list) == 200 && len(list) == 1 && list[0].Id == job.Id
	}, 15*time.Second, 200*time.Millisecond, "visibility lags creation by a moment")
}

func TestSplitIncidentMovesOneArticleOut(t *testing.T) {
	ts, r := newTestServer(t, &fakeWF{}, token)
	c := cand("2026-10-10", "Oakland", "CA", "100 Broadway", "fatal", []string{"pedestrian"}, nil, ip(1))
	first := seed(t, r, "a.example", c, f64(37.80), f64(-122.27))
	second := seed(t, r, "b.example", c, f64(37.80), f64(-122.27))
	require.Equal(t, first.IncidentID, second.IncidentID, "the two reports were merged")

	var detail IncidentDetail
	require.Equal(t, 200, get(t, ts, "/v1/incidents/"+first.IncidentID.String(), &detail))
	require.Len(t, detail.Articles, 2)
	moving := detail.Articles[1].ArticleId
	url := "/v1/admin/incidents/" + first.IncidentID.String() + "/split"

	assert.Equal(t, 400, call(t, ts, "POST", url, token, map[string]any{"article_id": moving, "actor": ""}, nil), "an actor is required")
	assert.Equal(t, 404, call(t, ts, "POST", "/v1/admin/incidents/"+uuid.NewString()+"/split", token, map[string]any{"article_id": moving, "actor": "me"}, nil))
	assert.Equal(t, 409, call(t, ts, "POST", url, token, map[string]any{"article_id": uuid.NewString(), "actor": "me"}, nil), "not linked to this incident")

	var out struct {
		NewIncidentId uuid.UUID `json:"new_incident_id"`
	}
	require.Equal(t, 200, call(t, ts, "POST", url, token, map[string]any{"article_id": moving, "actor": " Pat "}, &out))
	assert.NotEqual(t, first.IncidentID, out.NewIncidentId)

	var left, split IncidentDetail
	require.Equal(t, 200, get(t, ts, "/v1/incidents/"+first.IncidentID.String(), &left))
	require.Equal(t, 200, get(t, ts, "/v1/incidents/"+out.NewIncidentId.String(), &split))
	assert.Len(t, left.Articles, 1)
	assert.Len(t, split.Articles, 1)
	assert.Equal(t, moving, split.Articles[0].ArticleId)
	assert.EqualValues(t, 1, left.Incident.ArticleCount)
	var splitEvent *Event
	for i := range split.Events {
		if split.Events[i].Type == "split" {
			splitEvent = &split.Events[i]
		}
	}
	require.NotNil(t, splitEvent, "the split is in the new incident's audit trail")
	assert.Equal(t, "Pat", splitEvent.Actor, "the actor is trimmed and recorded")

	// One article left: nothing more to split off.
	assert.Equal(t, 409, call(t, ts, "POST", url, token, map[string]any{"article_id": left.Articles[0].ArticleId, "actor": "me"}, nil))
}
