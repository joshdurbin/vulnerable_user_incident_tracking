package pipeline

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
)

type fakeDiscoverer struct {
	rep  *discovery.Report
	err  error
	last discovery.RunRequest
}

func (f *fakeDiscoverer) Run(_ context.Context, rr discovery.RunRequest) (*discovery.Report, error) {
	f.last = rr
	return f.rep, f.err
}
func (f *fakeDiscoverer) Source(context.Context, string) (discovery.Source, error) {
	return discovery.Source{}, errors.New("not used")
}

func TestTestSourceReportsAndCapsSamples(t *testing.T) {
	pub := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	rep := &discovery.Report{Found: 40, Unique: 30, Unresolved: 2, Requests: 3, Truncated: true, Warnings: []string{"w"}, Duration: 1500 * time.Millisecond}
	for i := 0; i < 30; i++ {
		rep.Candidates = append(rep.Candidates, discovery.Deduped{
			Normalized: fmt.Sprintf("https://x.test/%d", i),
			Primary:    discovery.Candidate{Title: fmt.Sprintf("T%d", i), PublishedAt: &pub},
		})
	}
	d := &fakeDiscoverer{rep: rep}
	a := &Activities{Discovery: d}
	src := discovery.Source{Slug: "draft", Type: "rss", URL: "https://x.test/feed"}
	out, err := a.TestSource(context.Background(), SourceTestInput{Source: src})
	require.NoError(t, err)
	assert.True(t, d.last.DryRun, "a test must never persist anything")
	assert.Equal(t, "draft", d.last.Source.Slug)
	assert.True(t, out.OK)
	assert.Equal(t, 40, out.Found)
	assert.Equal(t, 30, out.Unique)
	assert.Equal(t, 2, out.Unresolved)
	assert.True(t, out.Truncated)
	assert.EqualValues(t, 1500, out.DurationMS)
	assert.Equal(t, []string{"w"}, out.Warnings)
	require.Len(t, out.Samples, MaxTestSamples)
	assert.Equal(t, "https://x.test/0", out.Samples[0].URL)
	assert.Equal(t, "T0", out.Samples[0].Title)
	assert.Equal(t, &pub, out.Samples[0].PublishedAt)
}

func TestTestSourceProviderFailureIsAResultNotAnError(t *testing.T) {
	a := &Activities{Discovery: &fakeDiscoverer{rep: &discovery.Report{Found: 0}, err: errors.New("feed x: http 503")}}
	out, err := a.TestSource(context.Background(), SourceTestInput{Source: discovery.Source{Slug: "d", Type: "rss"}})
	require.NoError(t, err, "the UI shows the failure; Temporal must not retry it")
	assert.False(t, out.OK)
	assert.Equal(t, "feed x: http 503", out.Error)
	assert.NotNil(t, out.Samples)
	assert.NotNil(t, out.Warnings)
}

func TestTestSourceWithNoReportStillAnswers(t *testing.T) {
	a := &Activities{Discovery: &fakeDiscoverer{err: errors.New("unknown type")}}
	out, err := a.TestSource(context.Background(), SourceTestInput{})
	require.NoError(t, err)
	assert.False(t, out.OK)
	assert.Equal(t, "unknown type", out.Error)
}

func TestSourceTestWorkflowReturnsTheActivityResult(t *testing.T) {
	env := newEnv(t)
	env.RegisterWorkflow(SourceTestWorkflow)
	env.OnActivity(act.TestSource, anyCtx, anyCtx).Return(&SourceTestResult{OK: true, Found: 5}, nil).Once()
	env.ExecuteWorkflow(SourceTestWorkflow, SourceTestInput{Source: discovery.Source{Slug: "d", Type: "rss"}})
	require.NoError(t, env.GetWorkflowError())
	var r SourceTestResult
	require.NoError(t, env.GetWorkflowResult(&r))
	assert.True(t, r.OK)
	assert.Equal(t, 5, r.Found)
	env.AssertExpectations(t)
}

// A test run is not retried: the person is waiting and sees the error.
func TestSourceTestWorkflowDoesNotRetry(t *testing.T) {
	env := newEnv(t)
	env.RegisterWorkflow(SourceTestWorkflow)
	env.OnActivity(act.TestSource, anyCtx, anyCtx).Return(nil, temporal.NewApplicationError("db down", "Boom")).Once()
	env.ExecuteWorkflow(SourceTestWorkflow, SourceTestInput{})
	require.Error(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}
