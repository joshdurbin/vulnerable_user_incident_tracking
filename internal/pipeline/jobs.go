package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

// Jobs are the long-running bulk workflows an operator starts and watches: backfills and reprocess
// runs. The admin API is a thin layer over these functions, which is why they live here, next to the
// workflows, where they can be tested against a real Temporal server.

const (
	JobBackfill  = "backfill"
	JobReprocess = "reprocess"

	JobPause  = "pause"
	JobResume = "resume"
	JobCancel = "cancel"
)

var (
	// ErrJobNotFound means no backfill or reprocess run has that ID.
	ErrJobNotFound = errors.New("job not found")
	// ErrJobAction means the action does not apply to that job (a reprocess run cannot be paused) or
	// the job already finished.
	ErrJobAction = errors.New("action not possible for this job")
)

// Job is one backfill or reprocess run as the admin UI shows it.
type Job struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Status    string     `json:"status"` // running|completed|failed|cancelled|timed_out|terminated
	StartedAt time.Time  `json:"started_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	// Exactly one of these is set when the run reported progress.
	Backfill  *BackfillProgress `json:"backfill,omitempty"`
	Reprocess *ReprocessResult  `json:"reprocess,omitempty"`
}

// StartBackfill starts a backfill and returns its workflow ID. The caller validates the input.
func StartBackfill(ctx context.Context, c client.Client, in BackfillInput, opts Options) (string, error) {
	id := "backfill-" + time.Now().UTC().Format("20060102-150405") + "-" + uuid.NewString()[:4]
	in.CorrelationID, in.Options = id, opts
	if _, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: TQWorkflow}, BackfillWorkflow, in); err != nil {
		return "", err
	}
	return id, nil
}

// StartReprocess starts a reprocess run and returns its workflow ID.
func StartReprocess(ctx context.Context, c client.Client, in ReprocessInput, opts Options) (string, error) {
	id := "reprocess-" + time.Now().UTC().Format("20060102-150405") + "-" + uuid.NewString()[:4]
	in.CorrelationID, in.Options = id, opts
	if _, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: TQWorkflow}, ReprocessWorkflow, in); err != nil {
		return "", err
	}
	return id, nil
}

var jobTypes = map[string]string{"BackfillWorkflow": JobBackfill, "ReprocessWorkflow": JobReprocess}

// ListJobs returns the most recent runs of one kind, newest first, with their progress. A run that
// continued as new appears once, as its latest run.
func ListJobs(ctx context.Context, c client.Client, kind string, limit int) ([]Job, error) {
	wfType := map[string]string{JobBackfill: "BackfillWorkflow", JobReprocess: "ReprocessWorkflow"}[kind]
	if wfType == "" {
		return nil, fmt.Errorf("unknown job kind %q", kind)
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var out []Job
	seen := map[string]bool{}
	var token []byte
	for len(out) < limit {
		resp, err := c.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
			Query: fmt.Sprintf("WorkflowType = %q", wfType), PageSize: int32(limit), NextPageToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, e := range resp.Executions {
			id := e.GetExecution().GetWorkflowId()
			if seen[id] || len(out) >= limit {
				continue
			}
			seen[id] = true
			out = append(out, jobFromInfo(ctx, c, e))
		}
		if token = resp.NextPageToken; len(token) == 0 {
			break
		}
	}
	return out, nil
}

// GetJob returns one run by ID.
func GetJob(ctx context.Context, c client.Client, id string) (*Job, error) {
	if !strings.HasPrefix(id, JobBackfill+"-") && !strings.HasPrefix(id, JobReprocess+"-") {
		return nil, ErrJobNotFound // never touch workflows that are not jobs
	}
	d, err := c.DescribeWorkflowExecution(ctx, id, "")
	if err != nil {
		var nf *serviceerror.NotFound
		if errors.As(err, &nf) {
			return nil, ErrJobNotFound
		}
		return nil, err
	}
	if _, ok := jobTypes[d.GetWorkflowExecutionInfo().GetType().GetName()]; !ok {
		return nil, ErrJobNotFound
	}
	j := jobFromInfo(ctx, c, d.GetWorkflowExecutionInfo())
	return &j, nil
}

func jobFromInfo(ctx context.Context, c client.Client, e *workflowpb.WorkflowExecutionInfo) Job {
	id := e.GetExecution().GetWorkflowId()
	j := Job{ID: id, Kind: jobTypes[e.GetType().GetName()], Status: jobStatus(e.GetStatus()), StartedAt: e.GetStartTime().AsTime()}
	if e.GetCloseTime() != nil {
		t := e.GetCloseTime().AsTime()
		j.ClosedAt = &t
	}
	// Progress comes from the workflow's query handler, which also answers for closed runs. A failure
	// leaves it empty: the list is still useful without it.
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v, err := c.QueryWorkflow(qctx, id, e.GetExecution().GetRunId(), QueryProgress)
	if err != nil {
		return j
	}
	switch j.Kind {
	case JobBackfill:
		var p BackfillProgress
		if v.Get(&p) == nil {
			j.Backfill = &p
		}
	case JobReprocess:
		var p ReprocessResult
		if v.Get(&p) == nil {
			j.Reprocess = &p
		}
	}
	return j
}

func jobStatus(s enumspb.WorkflowExecutionStatus) string {
	switch s {
	case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		return "running"
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return "completed"
	case enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:
		return "cancelled"
	case enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return "timed_out"
	case enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		return "terminated"
	default:
		return "failed"
	}
}

// ControlJob pauses, resumes or cancels a run. Only backfills can be paused (they have signals for it);
// either kind can be cancelled. A backfill stops itself on cancel; a reprocess run is cancelled through
// Temporal and finishes the article it is on.
func ControlJob(ctx context.Context, c client.Client, id, action string) error {
	j, err := GetJob(ctx, c, id)
	if err != nil {
		return err
	}
	if j.Status != "running" {
		return fmt.Errorf("%w: it is %s", ErrJobAction, j.Status)
	}
	switch {
	case j.Kind == JobBackfill && (action == JobPause || action == JobResume || action == JobCancel):
		return c.SignalWorkflow(ctx, id, "", action, nil)
	case j.Kind == JobReprocess && action == JobCancel:
		return c.CancelWorkflow(ctx, id, "")
	}
	return fmt.Errorf("%w: a %s cannot be %sd", ErrJobAction, j.Kind, action)
}
