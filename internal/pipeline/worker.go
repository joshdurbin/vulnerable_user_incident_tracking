package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rs/zerolog"
	slogzerolog "github.com/samber/slog-zerolog/v2"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/joshdurbin/fuck_cars/internal/discovery"
)

// Dial connects to Temporal with the correlation-ID propagator and zerolog logging.
func Dial(hostPort, namespace string, log zerolog.Logger) (client.Client, error) {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	sl := slog.New(slogzerolog.Option{Logger: &log}.NewZerologHandler())
	return client.Dial(client.Options{
		HostPort: hostPort, Namespace: namespace, Logger: tlog.NewStructuredLogger(sl),
		ContextPropagators: []workflow.ContextPropagator{Propagator{}},
	})
}

// QueueLimits bound one task queue's worker. Zero values use SDK defaults.
type QueueLimits struct {
	MaxConcurrent int     // simultaneous activities
	PerSecond     float64 // queue-wide activity rate limit
}

// WorkerConfig sets per-queue limits. Slow, costly queues get tight limits so
// the LLM and the crawler cannot starve the database queue.
type WorkerConfig struct {
	Crawl, LLM, Discovery, DB QueueLimits
	// Queues restricts which queues this process serves (empty = all). Lets the
	// crawl queue run in its own deployment later without code changes.
	Queues []string
}

func DefaultWorkerConfig() WorkerConfig {
	return WorkerConfig{
		Crawl:     QueueLimits{MaxConcurrent: 8, PerSecond: 5},
		LLM:       QueueLimits{MaxConcurrent: 4, PerSecond: 2},
		Discovery: QueueLimits{MaxConcurrent: 2},
		DB:        QueueLimits{MaxConcurrent: 16},
	}
}

func (c WorkerConfig) serves(q string) bool {
	if len(c.Queues) == 0 {
		return true
	}
	for _, x := range c.Queues {
		if x == q {
			return true
		}
	}
	return false
}

// NewWorkers builds one worker per task queue and registers workflows and activities.
func NewWorkers(c client.Client, a *Activities, cfg WorkerConfig) []worker.Worker {
	var ws []worker.Worker
	if cfg.serves(TQWorkflow) {
		w := worker.New(c, TQWorkflow, worker.Options{})
		w.RegisterWorkflow(ArticleWorkflow)
		w.RegisterWorkflow(DiscoveryWorkflow)
		w.RegisterWorkflow(IncidentWorkflow)
		w.RegisterWorkflow(BackfillWorkflow)
		w.RegisterWorkflow(ReprocessWorkflow)
		w.RegisterWorkflow(ReprocessArticleWorkflow)
		ws = append(ws, w)
	}
	for _, q := range []struct {
		name string
		lim  QueueLimits
	}{{TQCrawl, cfg.Crawl}, {TQLLM, cfg.LLM}, {TQDiscovery, cfg.Discovery}, {TQDB, cfg.DB}} {
		if !cfg.serves(q.name) {
			continue
		}
		w := worker.New(c, q.name, worker.Options{
			MaxConcurrentActivityExecutionSize: q.lim.MaxConcurrent,
			TaskQueueActivitiesPerSecond:       q.lim.PerSecond,
			// Do not poll for workflow tasks on activity queues.
			DisableWorkflowWorker: true,
		})
		w.RegisterActivity(a)
		ws = append(ws, w)
	}
	return ws
}

// Run starts every worker and blocks until ctx is cancelled or one fails.
func Run(ctx context.Context, ws []worker.Worker) error {
	for i, w := range ws {
		if err := w.Start(); err != nil {
			for _, s := range ws[:i] {
				s.Stop()
			}
			return fmt.Errorf("start worker: %w", err)
		}
	}
	<-ctx.Done()
	for _, w := range ws {
		w.Stop()
	}
	return nil
}

// SyncSchedules makes one Temporal Schedule per source. New schedules start
// paused if the source is disabled; existing schedules keep whatever pause
// state an operator set in the UI, and only their interval and action are updated.
func SyncSchedules(ctx context.Context, c client.Client, sources []discovery.Source, opts Options) (created, updated int, err error) {
	sc := c.ScheduleClient()
	for _, s := range sources {
		interval := s.PollInterval
		if interval <= 0 {
			interval = time.Hour
		}
		action := &client.ScheduleWorkflowAction{
			ID: "discovery-" + s.Slug, Workflow: DiscoveryWorkflow, TaskQueue: TQWorkflow,
			Args: []any{DiscoveryInput{Slug: s.Slug, Options: opts}},
		}
		spec := client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: interval}}, Jitter: interval / 10}
		id := DiscoveryScheduleID(s.Slug)
		_, cerr := sc.Create(ctx, client.ScheduleOptions{
			ID: id, Spec: spec, Action: action,
			Overlap:       enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
			CatchupWindow: 2 * interval,
			Paused:        !s.Enabled,
			Note:          "managed by collisionctl; pause state is yours",
		})
		switch {
		case cerr == nil:
			created++
		case errors.Is(cerr, temporal.ErrScheduleAlreadyRunning):
			uerr := sc.GetHandle(ctx, id).Update(ctx, client.ScheduleUpdateOptions{
				DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
					in.Description.Schedule.Spec = &spec
					in.Description.Schedule.Action = action
					return &client.ScheduleUpdate{Schedule: &in.Description.Schedule}, nil
				},
			})
			if uerr != nil {
				return created, updated, fmt.Errorf("update schedule %s: %w", id, uerr)
			}
			updated++
		default:
			return created, updated, fmt.Errorf("create schedule %s: %w", id, cerr)
		}
	}
	return created, updated, nil
}
