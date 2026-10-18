package api

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/pipeline"
)

var (
	ErrNoSchedule   = errors.New("schedule not found")
	ErrNotPending   = errors.New("review is not pending")
	ErrTemporalDown = errors.New("temporal is not available")
	// ErrWorkerUnavailable means no discovery worker picked up a test run in time.
	ErrWorkerUnavailable = errors.New("no discovery worker answered in time")
)

// Workflows is what the API needs from Temporal. Tests substitute a fake.
type Workflows interface {
	// DecideReview delivers an approve/reject decision to the incident's workflow as an
	// Update, starting that workflow first if it is not running (update-with-start).
	DecideReview(ctx context.Context, incidentID uuid.UUID, d pipeline.ReviewDecision) (*pipeline.ReviewOutcome, error)
	// Schedule returns the discovery schedule state for a source, or ErrNoSchedule.
	Schedule(ctx context.Context, slug string) (*ScheduleState, error)
	ControlSchedule(ctx context.Context, slug, action string) error
	// ApplySource creates or updates a source's Schedule after it was saved. The pause state
	// follows the source only when its enabled flag changed.
	ApplySource(ctx context.Context, src discovery.Source, enabledChanged bool) error
	// DeleteSchedule removes a source's Schedule; a missing one is not an error.
	DeleteSchedule(ctx context.Context, slug string) error
	// SyncSchedules makes the Schedules match the sources, and removes those of deleted sources.
	SyncSchedules(ctx context.Context, srcs []discovery.Source) (created, updated, removed int, err error)
	// TestSource runs a source configuration once through the discovery worker, saving nothing.
	TestSource(ctx context.Context, src discovery.Source) (*pipeline.SourceTestResult, error)
}

// TemporalWorkflows implements Workflows with a Temporal client.
type TemporalWorkflows struct {
	C       client.Client
	Options pipeline.Options
}

func (t TemporalWorkflows) DecideReview(ctx context.Context, incidentID uuid.UUID, d pipeline.ReviewDecision) (*pipeline.ReviewOutcome, error) {
	id := incidentID.String()
	op := t.C.NewWithStartWorkflowOperation(client.StartWorkflowOptions{
		ID: pipeline.IncidentWorkflowID(id), TaskQueue: pipeline.TQWorkflow,
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}, "IncidentWorkflow", pipeline.IncidentInput{IncidentID: id, Options: t.Options})
	h, err := t.C.UpdateWithStartWorkflow(ctx, client.UpdateWithStartWorkflowOptions{
		StartWorkflowOperation: op,
		UpdateOptions: client.UpdateWorkflowOptions{
			UpdateID: "review-" + d.ReviewID.String(), UpdateName: pipeline.UpdateDecideReview, Args: []any{d},
			WaitForStage: client.WorkflowUpdateStageCompleted,
		},
	})
	if err != nil {
		return nil, mapTemporalErr(err)
	}
	var out pipeline.ReviewOutcome
	if err := h.Get(ctx, &out); err != nil {
		return nil, mapTemporalErr(err)
	}
	return &out, nil
}

func mapTemporalErr(err error) error {
	var ae *temporal.ApplicationError
	if errors.As(err, &ae) && ae.Type() == pipeline.ErrTypeNotPending {
		return ErrNotPending
	}
	var unavailable *serviceerror.Unavailable
	if errors.As(err, &unavailable) {
		return ErrTemporalDown
	}
	return err
}

func (t TemporalWorkflows) Schedule(ctx context.Context, slug string) (*ScheduleState, error) {
	d, err := t.C.ScheduleClient().GetHandle(ctx, pipeline.DiscoveryScheduleID(slug)).Describe(ctx)
	if err != nil {
		var nf *serviceerror.NotFound
		if errors.As(err, &nf) {
			return nil, ErrNoSchedule
		}
		return nil, mapTemporalErr(err)
	}
	st := &ScheduleState{}
	paused := d.Schedule.State.Paused
	st.Paused = &paused
	if len(d.Info.NextActionTimes) > 0 {
		n := d.Info.NextActionTimes[0]
		st.NextRun = &n
	}
	if n := len(d.Info.RecentActions); n > 0 {
		l := d.Info.RecentActions[n-1].ActualTime
		st.LastRun = &l
	}
	return st, nil
}

func (t TemporalWorkflows) ControlSchedule(ctx context.Context, slug, action string) error {
	h := t.C.ScheduleClient().GetHandle(ctx, pipeline.DiscoveryScheduleID(slug))
	var err error
	switch action {
	case "pause":
		err = h.Pause(ctx, client.SchedulePauseOptions{Note: "paused via API"})
	case "resume":
		err = h.Unpause(ctx, client.ScheduleUnpauseOptions{Note: "resumed via API"})
	case "trigger":
		err = h.Trigger(ctx, client.ScheduleTriggerOptions{})
	default:
		return badRequest("unknown action " + action)
	}
	var nf *serviceerror.NotFound
	if errors.As(err, &nf) {
		return ErrNoSchedule
	}
	return mapTemporalErr(err)
}

func (t TemporalWorkflows) ApplySource(ctx context.Context, src discovery.Source, enabledChanged bool) error {
	return mapTemporalErr(pipeline.ApplySource(ctx, t.C, src, t.Options, enabledChanged))
}

func (t TemporalWorkflows) DeleteSchedule(ctx context.Context, slug string) error {
	return mapTemporalErr(pipeline.DeleteSourceSchedule(ctx, t.C, slug))
}

func (t TemporalWorkflows) SyncSchedules(ctx context.Context, srcs []discovery.Source) (created, updated, removed int, err error) {
	if created, updated, err = pipeline.SyncSchedules(ctx, t.C, srcs, t.Options); err != nil {
		return 0, 0, 0, mapTemporalErr(err)
	}
	keep := make(map[string]bool, len(srcs))
	for _, s := range srcs {
		keep[s.Slug] = true
	}
	if removed, err = pipeline.PruneSchedules(ctx, t.C, keep); err != nil {
		return created, updated, removed, mapTemporalErr(err)
	}
	return created, updated, removed, nil
}

// testRunTimeout bounds one test run end to end; the HTTP server's write timeout is longer.
const testRunTimeout = 55 * time.Second

func (t TemporalWorkflows) TestSource(ctx context.Context, src discovery.Source) (*pipeline.SourceTestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, testRunTimeout)
	defer cancel()
	run, err := t.C.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: "source-test-" + uuid.NewString(), TaskQueue: pipeline.TQWorkflow, WorkflowExecutionTimeout: testRunTimeout + 10*time.Second,
	}, pipeline.SourceTestWorkflow, pipeline.SourceTestInput{Source: src})
	if err != nil {
		return nil, mapTemporalErr(err)
	}
	var out pipeline.SourceTestResult
	if err := run.Get(ctx, &out); err != nil {
		if pipeline.IsScheduleToStartTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
			return nil, ErrWorkerUnavailable
		}
		return nil, mapTemporalErr(err)
	}
	return &out, nil
}
