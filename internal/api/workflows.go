package api

import (
	"context"
	"errors"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/pipeline"
)

var (
	ErrNoSchedule   = errors.New("schedule not found")
	ErrNotPending   = errors.New("review is not pending")
	ErrTemporalDown = errors.New("temporal is not available")
)

// Workflows is what the API needs from Temporal. Tests substitute a fake.
type Workflows interface {
	// DecideReview delivers an approve/reject decision to the incident's workflow as an
	// Update, starting that workflow first if it is not running (update-with-start).
	DecideReview(ctx context.Context, incidentID uuid.UUID, d pipeline.ReviewDecision) (*pipeline.ReviewOutcome, error)
	// Schedule returns the discovery schedule state for a source, or ErrNoSchedule.
	Schedule(ctx context.Context, slug string) (*ScheduleState, error)
	ControlSchedule(ctx context.Context, slug, action string) error
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
