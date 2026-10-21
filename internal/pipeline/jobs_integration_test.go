//go:build integration

package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/domain"
)

// gatedDiscoverer finds nothing, but holds every run until released, so a backfill can be observed
// part-way through.
type gatedDiscoverer struct {
	gate chan struct{}
	once sync.Once
}

func newGate() *gatedDiscoverer     { return &gatedDiscoverer{gate: make(chan struct{})} }
func (g *gatedDiscoverer) release() { g.once.Do(func() { close(g.gate) }) }
func (g *gatedDiscoverer) Source(_ context.Context, slug string) (discovery.Source, error) {
	return discovery.Source{Slug: slug, Type: "rss", Enabled: true}, nil
}
func (g *gatedDiscoverer) Run(ctx context.Context, _ discovery.RunRequest) (*discovery.Report, error) {
	select {
	case <-g.gate:
		return &discovery.Report{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func jobsEnv(t *testing.T, d Discoverer, cl Classifier) {
	t.Helper()
	reset(t)
	a := newActivities(&fakeClassifier{})
	a.Discovery = d
	if cl != nil {
		a.Classifier = cl
	}
	ws := startWorkers(t, a, nil)
	t.Cleanup(func() { stop(ws) })
}

func job(t *testing.T, id string) *Job {
	t.Helper()
	j, err := GetJob(context.Background(), tc, id)
	require.NoError(t, err)
	return j
}

func TestBackfillCanBeStartedWatchedPausedResumedAndFinishes(t *testing.T) {
	g := newGate()
	jobsEnv(t, g, nil)
	ctx := context.Background()
	id, err := StartBackfill(ctx, tc, BackfillInput{
		Slugs: []string{"s"}, From: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
		WindowSize: 24 * time.Hour, MaxArticles: 10,
	}, Options{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		j := job(t, id)
		return j.Backfill != nil && j.Backfill.TotalSteps == 3
	}, 15*time.Second, 100*time.Millisecond, "progress is reported while it runs")
	j := job(t, id)
	assert.Equal(t, JobBackfill, j.Kind)
	assert.Equal(t, "running", j.Status)

	require.NoError(t, ControlJob(ctx, tc, id, JobPause))
	g.release() // the step in flight finishes; the pause holds it before the next
	require.Eventually(t, func() bool {
		j := job(t, id)
		return j.Backfill != nil && j.Backfill.Phase == "paused" && j.Backfill.StepsDone == 1
	}, 15*time.Second, 100*time.Millisecond)

	require.NoError(t, ControlJob(ctx, tc, id, JobResume))
	require.Eventually(t, func() bool { return job(t, id).Status == "completed" }, 15*time.Second, 100*time.Millisecond)
	done := job(t, id)
	assert.Equal(t, "done", done.Backfill.Phase)
	assert.Equal(t, 3, done.Backfill.StepsDone)
	require.NotNil(t, done.ClosedAt)

	err = ControlJob(ctx, tc, id, JobCancel)
	assert.ErrorIs(t, err, ErrJobAction, "a finished run cannot be controlled")

	// Temporal's visibility store lags a moment behind creation.
	require.Eventually(t, func() bool {
		jobs, err := ListJobs(ctx, tc, JobBackfill, 10)
		return err == nil && len(jobs) == 1 && jobs[0].ID == id
	}, 15*time.Second, 200*time.Millisecond)
	other, err := ListJobs(ctx, tc, JobReprocess, 10)
	require.NoError(t, err)
	assert.Empty(t, other, "reprocess runs are listed apart from backfills")
}

func TestBackfillCancel(t *testing.T) {
	g := newGate()
	jobsEnv(t, g, nil)
	ctx := context.Background()
	id, err := StartBackfill(ctx, tc, BackfillInput{
		Slugs: []string{"s"}, From: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC), MaxArticles: 10,
	}, Options{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return job(t, id).Backfill != nil }, 15*time.Second, 100*time.Millisecond)

	require.NoError(t, ControlJob(ctx, tc, id, JobCancel))
	g.release()
	require.Eventually(t, func() bool { return job(t, id).Status == "completed" }, 15*time.Second, 100*time.Millisecond)
	j := job(t, id)
	assert.Equal(t, "cancelled", j.Backfill.Phase)
	assert.Less(t, j.Backfill.StepsDone, j.Backfill.TotalSteps, "it stopped early")
}

func TestJobLookupsAreLimitedToJobs(t *testing.T) {
	jobsEnv(t, newGate(), nil)
	ctx := context.Background()
	_, err := GetJob(ctx, tc, "nope-123")
	assert.ErrorIs(t, err, ErrJobNotFound)
	_, err = GetJob(ctx, tc, "backfill-does-not-exist")
	assert.ErrorIs(t, err, ErrJobNotFound)
	// Only workflows of the two job types count, even under a job-like ID.
	id, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "backfill-impostor", TaskQueue: TQWorkflow}, RetentionWorkflow)
	require.NoError(t, err)
	_, err = GetJob(ctx, tc, id.GetID())
	assert.ErrorIs(t, err, ErrJobNotFound)
	assert.ErrorIs(t, ControlJob(ctx, tc, id.GetID(), JobCancel), ErrJobNotFound)
}

// blockedClassifier holds every classification until released.
type blockedClassifier struct {
	fakeClassifier
	gate chan struct{}
}

func (b *blockedClassifier) Classify(ctx context.Context, a *domain.Article) (*domain.ExtractionResult, error) {
	select {
	case <-b.gate:
		return b.fakeClassifier.Classify(ctx, a)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestReprocessIsListedAndCompletes(t *testing.T) {
	jobsEnv(t, newGate(), nil)
	ctx := context.Background()
	id, err := StartReprocess(ctx, tc, ReprocessInput{Limit: 10, BatchSize: 5, MaxConcurrent: 2}, Options{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return job(t, id).Status == "completed" }, 15*time.Second, 100*time.Millisecond)
	j := job(t, id)
	assert.Equal(t, JobReprocess, j.Kind)
	require.NotNil(t, j.Reprocess)
	assert.Zero(t, j.Reprocess.Examined, "nothing needed reprocessing")
	assert.ErrorIs(t, ControlJob(ctx, tc, id, JobPause), ErrJobAction)

	require.Eventually(t, func() bool {
		jobs, err := ListJobs(ctx, tc, JobReprocess, 10)
		return err == nil && len(jobs) == 1 && jobs[0].ID == id
	}, 15*time.Second, 200*time.Millisecond)
}

func TestReprocessCanBeCancelledButNotPaused(t *testing.T) {
	bc := &blockedClassifier{gate: make(chan struct{})}
	jobsEnv(t, newGate(), bc)
	t.Cleanup(func() { close(bc.gate) })
	ctx := context.Background()
	store1(t, newActivities(&fakeClassifier{}), "https://gazette.test/s", "", wire("reprocess"))

	id, err := StartReprocess(ctx, tc, ReprocessInput{Limit: 10, BatchSize: 5, MaxConcurrent: 1}, Options{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { j := job(t, id); return j.Status == "running" && j.Reprocess != nil }, 15*time.Second, 100*time.Millisecond)

	assert.ErrorIs(t, ControlJob(ctx, tc, id, JobPause), ErrJobAction, "reprocess runs have no pause")
	require.NoError(t, ControlJob(ctx, tc, id, JobCancel))
	require.Eventually(t, func() bool { return job(t, id).Status == "cancelled" }, 15*time.Second, 100*time.Millisecond)
}
