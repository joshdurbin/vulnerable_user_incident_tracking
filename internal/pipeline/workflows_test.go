package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/joshdurbin/fuck_cars/internal/crawler"
)

func newEnv(t *testing.T) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	// The Python worker's activity, registered by name so tests can mock it.
	env.RegisterActivityWithOptions(func(context.Context, crawler.FetchArticleRequest) (crawler.ArticleResponse, error) {
		return crawler.ArticleResponse{}, nil
	}, activity.RegisterOptions{Name: crawler.ActivityFetchArticle})
	env.RegisterWorkflow(ArticleWorkflow)
	env.RegisterWorkflow(IncidentWorkflow)
	env.RegisterWorkflow(ReprocessArticleWorkflow)
	return env
}

var anyCtx = mock.Anything

// mockFetch stubs the three steps of getting an article: look it up, fetch it (Python), store it.
func mockFetch(env *testsuite.TestWorkflowEnvironment, aid uuid.UUID, status string) {
	env.OnActivity(act.FindArticle, anyCtx, anyCtx).Return(&FetchResult{}, nil)
	env.OnActivity(crawler.ActivityFetchArticle, anyCtx, anyCtx).Return(crawler.ArticleResponse{ExtractionStatus: "OK", Text: "text"}, nil)
	env.OnActivity(act.StoreArticle, anyCtx, anyCtx).Return(&FetchResult{ArticleID: aid, Status: status}, nil)
}

func TestArticleWorkflow_HappyPath(t *testing.T) {
	env := newEnv(t)
	aid, eid, iid := uuid.New(), uuid.New(), uuid.New()
	mockFetch(env, aid, "fetched")
	env.OnActivity(act.Classify, anyCtx, ClassifyInput{ArticleID: aid}).Return(&ClassifyResult{ExtractionID: eid, Verdict: "confirmed", Incidents: 2, Model: "m"}, nil)
	env.OnActivity(act.Geocode, anyCtx, anyCtx).Return(GeoPoint{}, nil).Twice()
	env.OnActivity(act.ResolveIncident, anyCtx, anyCtx).Return(&ResolveOutput{IncidentID: iid, Outcome: "created"}, nil).Twice()
	env.OnActivity(act.NotifyIncident, anyCtx, anyCtx).Return(nil).Twice()

	env.ExecuteWorkflow(ArticleWorkflow, ArticleInput{URL: URLRef{ID: uuid.New(), URL: "https://x.test/a"}})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var r ArticleResult
	require.NoError(t, env.GetWorkflowResult(&r))
	assert.Equal(t, "done", r.Status)
	assert.Len(t, r.Incidents, 2)
	env.AssertExpectations(t)
}

func TestArticleWorkflow_StopsWhenNotFetched(t *testing.T) {
	env := newEnv(t)
	mockFetch(env, uuid.New(), "blocked")
	env.ExecuteWorkflow(ArticleWorkflow, ArticleInput{URL: URLRef{URL: "https://x.test/a"}})
	require.NoError(t, env.GetWorkflowError())
	var r ArticleResult
	require.NoError(t, env.GetWorkflowResult(&r))
	assert.Equal(t, "not_fetched", r.Status)
	env.AssertNotCalled(t, "Classify", anyCtx, anyCtx)
}

func TestArticleWorkflow_NonqualifyingSkipsResolution(t *testing.T) {
	env := newEnv(t)
	mockFetch(env, uuid.New(), "fetched")
	env.OnActivity(act.Classify, anyCtx, anyCtx).Return(&ClassifyResult{ExtractionID: uuid.New(), Verdict: "nonqualifying"}, nil)
	env.ExecuteWorkflow(ArticleWorkflow, ArticleInput{URL: URLRef{URL: "https://x.test/a"}})
	require.NoError(t, env.GetWorkflowError())
	var r ArticleResult
	require.NoError(t, env.GetWorkflowResult(&r))
	assert.Equal(t, "nonqualifying", r.Status)
}

// A budget stop is a clean outcome, not a failed workflow: it can be reprocessed later.
func TestArticleWorkflow_BudgetExceededIsNotAFailure(t *testing.T) {
	env := newEnv(t)
	mockFetch(env, uuid.New(), "fetched")
	env.OnActivity(act.Classify, anyCtx, anyCtx).Return(nil, temporal.NewNonRetryableApplicationError("over budget", ErrTypeBudget, nil)).Once()
	env.ExecuteWorkflow(ArticleWorkflow, ArticleInput{URL: URLRef{URL: "https://x.test/a"}})
	require.NoError(t, env.GetWorkflowError())
	var r ArticleResult
	require.NoError(t, env.GetWorkflowResult(&r))
	assert.Equal(t, "budget_exceeded", r.Status)
}

// A transient LLM failure is retried by Temporal and succeeds without failing the workflow.
func TestArticleWorkflow_RetriesTransientClassifyFailure(t *testing.T) {
	env := newEnv(t)
	mockFetch(env, uuid.New(), "fetched")
	env.OnActivity(act.Classify, anyCtx, anyCtx).Return(nil, errors.New("gateway 502")).Once()
	env.OnActivity(act.Classify, anyCtx, anyCtx).Return(&ClassifyResult{ExtractionID: uuid.New(), Verdict: "nonqualifying"}, nil).Once()
	env.ExecuteWorkflow(ArticleWorkflow, ArticleInput{URL: URLRef{URL: "https://x.test/a"}})
	require.NoError(t, env.GetWorkflowError())
	env.AssertNumberOfCalls(t, "Classify", 2)
}

func TestDiscoveryWorkflow_StartsChildrenAndHonorsCap(t *testing.T) {
	env := newEnv(t)
	env.RegisterWorkflow(DiscoveryWorkflow)
	var refs []URLRef
	for i := 0; i < 5; i++ {
		refs = append(refs, URLRef{ID: uuid.New(), URL: "https://x.test/" + uuid.NewString()})
	}
	env.OnActivity(act.DiscoverSource, anyCtx, anyCtx).Return(&DiscoverOutput{ToProcess: refs, Found: 9, New: 4, Pending: 1}, nil)
	started := 0
	env.OnWorkflow(ArticleWorkflow, anyCtx, anyCtx).Return(func(_ workflow.Context, in ArticleInput) (*ArticleResult, error) {
		started++
		return &ArticleResult{URL: in.URL.URL, Status: "done"}, nil
	})
	env.ExecuteWorkflow(DiscoveryWorkflow, DiscoveryInput{Slug: "s", MaxArticles: 3})
	require.NoError(t, env.GetWorkflowError())
	var r DiscoveryResult
	require.NoError(t, env.GetWorkflowResult(&r))
	assert.Equal(t, 3, r.Started)
	assert.Equal(t, 2, r.Deferred)
	assert.Equal(t, 9, r.Found)
}

func TestIncidentWorkflow_FollowsUpAfterTimerAndAnswersStatus(t *testing.T) {
	env := newEnv(t)
	iid := uuid.NewString()
	env.OnActivity(act.PendingReviews, anyCtx, iid).Return([]uuid.UUID{}, nil)
	var followUps int
	env.OnActivity(act.FollowUp, anyCtx, anyCtx).Return(func(_ context.Context, in FollowUpInput) (*FollowUpOutput, error) {
		followUps++
		return &FollowUpOutput{Checked: true}, nil
	})
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalArticleLinked, ArticleLinked{ArticleID: uuid.New(), Outcome: "created"})
	}, time.Minute)
	env.RegisterDelayedCallback(func() {
		v, err := env.QueryWorkflow(QueryStatus)
		require.NoError(t, err)
		var s IncidentStatus
		require.NoError(t, v.Get(&s))
		assert.Equal(t, 1, s.ArticlesLinked)
		assert.Equal(t, 0, s.FollowUpsDone)
		assert.NotEmpty(t, s.NextFollowUp)
	}, 2*time.Minute)

	env.ExecuteWorkflow(IncidentWorkflow, IncidentInput{IncidentID: iid, IdleTimeout: time.Hour})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, 2, followUps, "48h and 7d follow-ups both ran, driven by durable timers")
}

func TestIncidentWorkflow_ReviewUpdateValidatedAndApplied(t *testing.T) {
	env := newEnv(t)
	iid := uuid.NewString()
	rid, target := uuid.New(), uuid.New()
	env.OnActivity(act.PendingReviews, anyCtx, iid).Return([]uuid.UUID{rid}, nil)
	env.OnActivity(act.ApproveReview, anyCtx, anyCtx).Return(&ReviewOutcome{ReviewID: rid, Approved: true, Target: target}, nil)

	var rejected, completed bool
	env.RegisterDelayedCallback(func() {
		// Unknown review: the validator rejects it before anything runs or is recorded.
		env.UpdateWorkflow(UpdateDecideReview, "u1", &testsuite.TestUpdateCallback{
			OnReject:   func(err error) { rejected = true },
			OnAccept:   func() { t.Error("unknown review must not be accepted") },
			OnComplete: func(any, error) {},
		}, ReviewDecision{ReviewID: uuid.New(), Approve: true, Actor: "josh"})
		env.UpdateWorkflow(UpdateDecideReview, "u2", &testsuite.TestUpdateCallback{
			OnReject: func(err error) { t.Errorf("valid decision rejected: %v", err) },
			OnAccept: func() {},
			OnComplete: func(v any, err error) {
				require.NoError(t, err)
				completed = true
			},
		}, ReviewDecision{ReviewID: rid, Approve: true, Actor: "josh"})
	}, time.Minute)

	env.ExecuteWorkflow(IncidentWorkflow, IncidentInput{IncidentID: iid})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "approving a merge ends the incident's workflow")
	assert.True(t, rejected)
	assert.True(t, completed)
}

func TestBackfillWorkflow_BudgetStopsAndProgressIsReported(t *testing.T) {
	env := newEnv(t)
	env.RegisterWorkflow(BackfillWorkflow)
	env.OnActivity(act.DiscoverSource, anyCtx, anyCtx).Return(func(_ context.Context, in DiscoverInput) (*DiscoverOutput, error) {
		return &DiscoverOutput{Found: 3, ToProcess: []URLRef{
			{ID: uuid.New(), URL: "https://x.test/" + uuid.NewString()},
			{ID: uuid.New(), URL: "https://x.test/" + uuid.NewString()},
		}}, nil
	})
	env.OnWorkflow(ArticleWorkflow, anyCtx, anyCtx).Return(&ArticleResult{Status: "done"}, nil)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	env.ExecuteWorkflow(BackfillWorkflow, BackfillInput{Slugs: []string{"a", "b"}, From: from, To: from.AddDate(0, 0, 30), WindowSize: 24 * time.Hour, MaxArticles: 5})
	require.NoError(t, env.GetWorkflowError())
	var p BackfillProgress
	require.NoError(t, env.GetWorkflowResult(&p))
	assert.Equal(t, "budget_reached", p.Phase)
	assert.Equal(t, 5, p.Started)
	assert.Equal(t, 60, p.TotalSteps)
}

func TestBackfillWorkflow_PauseResumeCancel(t *testing.T) {
	env := newEnv(t)
	env.RegisterWorkflow(BackfillWorkflow)
	steps := 0
	env.OnActivity(act.DiscoverSource, anyCtx, anyCtx).Return(func(_ context.Context, in DiscoverInput) (*DiscoverOutput, error) {
		steps++
		return &DiscoverOutput{}, nil
	})
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Pause arrives while the first step is in flight; that step finishes, then nothing more runs.
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(SignalPause, nil) }, 0)
	env.RegisterDelayedCallback(func() {
		v, err := env.QueryWorkflow(QueryProgress)
		require.NoError(t, err)
		var p BackfillProgress
		require.NoError(t, v.Get(&p))
		assert.True(t, p.Paused)
		assert.Equal(t, "paused", p.Phase)
		env.SignalWorkflow(SignalCancel, nil)
	}, time.Hour)
	env.ExecuteWorkflow(BackfillWorkflow, BackfillInput{Slugs: []string{"a"}, From: from, To: from.AddDate(0, 0, 3)})
	require.NoError(t, env.GetWorkflowError())
	var p BackfillProgress
	require.NoError(t, env.GetWorkflowResult(&p))
	assert.Equal(t, "cancelled", p.Phase)
	assert.LessOrEqual(t, steps, 1, "no step starts while paused")
}

func TestBackfillWorkflow_ContinuesAsNewWithCheckpoint(t *testing.T) {
	env := newEnv(t)
	env.RegisterWorkflow(BackfillWorkflow)
	env.OnActivity(act.DiscoverSource, anyCtx, anyCtx).Return(&DiscoverOutput{}, nil)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	env.ExecuteWorkflow(BackfillWorkflow, BackfillInput{Slugs: []string{"a"}, From: from, To: from.AddDate(0, 0, backfillStepsPerRun+5)})
	err := env.GetWorkflowError()
	var can *workflow.ContinueAsNewError
	require.Error(t, err)
	require.True(t, errors.As(err, &can) || err.Error() != "", "expected Continue-As-New, got %v", err)
	assert.Contains(t, err.Error(), "continue as new")
}

func TestReprocessArticleWorkflow_SupersedesThenResolves(t *testing.T) {
	env := newEnv(t)
	aid, oldEx, newEx, iid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	env.OnActivity(act.PreviousExtraction, anyCtx, aid).Return(&ClassifyResult{ExtractionID: oldEx, Verdict: "confirmed", Incidents: 1}, nil)
	env.OnActivity(act.Classify, anyCtx, anyCtx).Return(&ClassifyResult{ExtractionID: newEx, Verdict: "confirmed", Incidents: 1}, nil)
	env.OnActivity(act.CompareExtractions, anyCtx, oldEx, newEx).Return(&ExtractionDiff{HadPrevious: true, Changed: true, Details: []string{"fatalities ? -> 1"}}, nil)
	env.OnActivity(act.SupersedeExtraction, anyCtx, oldEx).Return([]uuid.UUID{iid}, nil)
	env.OnActivity(act.Geocode, anyCtx, anyCtx).Return(GeoPoint{}, nil)
	env.OnActivity(act.ResolveIncident, anyCtx, anyCtx).Return(&ResolveOutput{IncidentID: iid, Outcome: "created"}, nil)
	env.OnActivity(act.NotifyIncident, anyCtx, anyCtx).Return(nil)

	env.ExecuteWorkflow(ReprocessArticleWorkflow, ReprocessArticleInput{ArticleID: aid})
	require.NoError(t, env.GetWorkflowError())
	var r ReprocessArticleResult
	require.NoError(t, env.GetWorkflowResult(&r))
	assert.True(t, r.Diff.Changed)
	assert.Equal(t, []uuid.UUID{iid}, r.Superseded)
	env.AssertExpectations(t)
}
