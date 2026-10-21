package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/rs/zerolog"
	slogzerolog "github.com/samber/slog-zerolog/v2"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/payloadcodec"
)

// Dial connects to Temporal with the correlation-ID propagator and zerolog logging.
func Dial(hostPort, namespace string, log zerolog.Logger, opts ...ClientOption) (client.Client, error) {
	return dial(hostPort, namespace, log, false, opts...)
}

// DialLazy returns a client that connects on first use, so a process that can serve
// without Temporal (the API's read routes) starts even when it is unreachable.
func DialLazy(hostPort, namespace string, log zerolog.Logger, opts ...ClientOption) (client.Client, error) {
	return dial(hostPort, namespace, log, true, opts...)
}

// ClientOption adjusts the Temporal client options (for example to add a metrics handler).
type ClientOption func(*client.Options)

func WithMetrics(h client.MetricsHandler) ClientOption {
	return func(o *client.Options) { o.MetricsHandler = h }
}

func dial(hostPort, namespace string, log zerolog.Logger, lazy bool, extra ...ClientOption) (client.Client, error) {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	sl := slog.New(slogzerolog.Option{Logger: &log}.NewZerologHandler())
	opts := client.Options{
		HostPort: hostPort, Namespace: namespace, Logger: tlog.NewStructuredLogger(sl),
		ContextPropagators: []workflow.ContextPropagator{Propagator{}},
		DataConverter:      payloadcodec.DataConverter(),
	}
	for _, o := range extra {
		o(&opts)
	}
	if lazy {
		return client.NewLazyClient(opts)
	}
	return client.Dial(opts)
}

// QueueLimits bound one task queue's worker. Zero values use SDK defaults.
type QueueLimits struct {
	MaxConcurrent int     // simultaneous activities
	PerSecond     float64 // queue-wide activity rate limit
}

// WorkerConfig sets per-queue limits. Slow, costly queues get tight limits so
// the LLM and the crawler cannot starve the database queue.
type WorkerConfig struct {
	LLM, Discovery, DB QueueLimits
	// RegisterCrawl lets a caller serve the crawl queue from Go. Production leaves it nil:
	// the Python worker serves that queue. Integration tests register a fake here.
	RegisterCrawl func(worker.Worker)
	// Queues restricts which queues this process serves (empty = all). Lets the
	// crawl queue run in its own deployment later without code changes.
	Queues []string
}

func DefaultWorkerConfig() WorkerConfig {
	return WorkerConfig{
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
		w.RegisterWorkflow(SourceTestWorkflow)
		w.RegisterWorkflow(IncidentWorkflow)
		w.RegisterWorkflow(BackfillWorkflow)
		w.RegisterWorkflow(ReprocessWorkflow)
		w.RegisterWorkflow(ReprocessArticleWorkflow)
		w.RegisterWorkflow(RetentionWorkflow)
		w.RegisterWorkflow(SearchIndexWorkflow)
		w.RegisterWorkflow(CrawlFetchWorkflow)
		w.RegisterWorkflow(CrawlLinksWorkflow)
		ws = append(ws, w)
	}
	for _, q := range []struct {
		name string
		lim  QueueLimits
	}{{TQLLM, cfg.LLM}, {TQDiscovery, cfg.Discovery}, {TQDB, cfg.DB}} {
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
	if cfg.RegisterCrawl != nil && cfg.serves(TQCrawl) {
		w := worker.New(c, TQCrawl, worker.Options{DisableWorkflowWorker: true})
		cfg.RegisterCrawl(w)
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
	for _, s := range sources {
		wasCreated, err := syncOne(ctx, c, s, opts, nil)
		if err != nil {
			return created, updated, err
		}
		if wasCreated {
			created++
		} else {
			updated++
		}
	}
	return created, updated, nil
}

// syncOne creates a source's Schedule, or updates an existing one. A non-nil setPaused also sets the
// pause state, inside the same update as the interval and action. That matters: an update writes the
// whole schedule, including the pause flag read when it started, and a separate Pause or Unpause sent
// right after can be applied first and then overwritten by that stale flag.
func syncOne(ctx context.Context, c client.Client, s discovery.Source, opts Options, setPaused *bool) (created bool, err error) {
	sc := c.ScheduleClient()
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
		return true, nil
	case errors.Is(cerr, temporal.ErrScheduleAlreadyRunning):
		uerr := sc.GetHandle(ctx, id).Update(ctx, client.ScheduleUpdateOptions{
			DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
				in.Description.Schedule.Spec = &spec
				in.Description.Schedule.Action = action
				if setPaused != nil {
					in.Description.Schedule.State.Paused = *setPaused
					if *setPaused {
						in.Description.Schedule.State.Note = "source disabled"
					} else {
						in.Description.Schedule.State.Note = "source enabled"
					}
				}
				return &client.ScheduleUpdate{Schedule: &in.Description.Schedule}, nil
			},
		})
		if uerr != nil {
			return false, fmt.Errorf("update schedule %s: %w", id, uerr)
		}
		return false, nil
	default:
		return false, fmt.Errorf("create schedule %s: %w", id, cerr)
	}
}

// ApplySource creates or updates one source's Schedule after it was saved in the admin UI.
// The interval and action always follow the source. The pause state follows it only when
// enabledChanged: disabling pauses, enabling resumes, and any other edit leaves a pause an
// operator set alone.
func ApplySource(ctx context.Context, c client.Client, src discovery.Source, opts Options, enabledChanged bool) error {
	var setPaused *bool
	if enabledChanged {
		p := !src.Enabled
		setPaused = &p
	}
	_, err := syncOne(ctx, c, src, opts, setPaused)
	return err
}

// DeleteSourceSchedule removes a source's Schedule; a missing one is not an error.
func DeleteSourceSchedule(ctx context.Context, c client.Client, slug string) error {
	err := c.ScheduleClient().GetHandle(ctx, DiscoveryScheduleID(slug)).Delete(ctx)
	var nf *serviceerror.NotFound
	if errors.As(err, &nf) {
		return nil
	}
	return err
}

// PruneSchedules deletes discovery Schedules whose source no longer exists, so a deleted
// source cannot keep starting workflows that fail. keep is the set of existing slugs.
func PruneSchedules(ctx context.Context, c client.Client, keep map[string]bool) (removed int, err error) {
	const prefix = "discover-"
	it, err := c.ScheduleClient().List(ctx, client.ScheduleListOptions{})
	if err != nil {
		return 0, err
	}
	for it.HasNext() {
		e, err := it.Next()
		if err != nil {
			return removed, err
		}
		slug, ok := strings.CutPrefix(e.ID, prefix)
		if !ok || keep[slug] {
			continue
		}
		if err := DeleteSourceSchedule(ctx, c, slug); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// MaintenanceScheduleID is the Schedule that enforces article text retention.
const MaintenanceScheduleID = "maintenance-retention"

// SearchScheduleID is the Schedule that copies changed incidents into the search index. It exists only
// while search is configured.
const SearchScheduleID = "maintenance-search-index"

// SearchIndexInterval is how often changed incidents reach the index. A change is searchable within
// about this long plus the indexer's safety lag.
const SearchIndexInterval = time.Minute

// SyncMaintenanceSchedules makes sure the housekeeping Schedules exist: the daily retention run always,
// and the search-index run when searchEnabled (it is removed when not, so an install without search
// does no pointless work). Existing schedules are left as they are, including a pause an operator set.
// It returns how many it created.
func SyncMaintenanceSchedules(ctx context.Context, c client.Client, searchEnabled bool) (created int, err error) {
	ensure := func(opts client.ScheduleOptions) error {
		_, err := c.ScheduleClient().Create(ctx, opts)
		if errors.Is(err, temporal.ErrScheduleAlreadyRunning) {
			return nil
		}
		if err == nil {
			created++
		}
		return err
	}
	if err := ensure(client.ScheduleOptions{
		ID:   MaintenanceScheduleID,
		Spec: client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: 24 * time.Hour}}, Jitter: time.Hour},
		Action: &client.ScheduleWorkflowAction{
			ID: "retention", Workflow: RetentionWorkflow, TaskQueue: TQWorkflow,
		},
		Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
		Note:    "deletes article text past its retention date; managed by collisionctl",
	}); err != nil {
		return created, err
	}
	if !searchEnabled {
		err := c.ScheduleClient().GetHandle(ctx, SearchScheduleID).Delete(ctx)
		var nf *serviceerror.NotFound
		if err != nil && !errors.As(err, &nf) {
			return created, err
		}
		return created, nil
	}
	err = ensure(client.ScheduleOptions{
		ID:   SearchScheduleID,
		Spec: client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: SearchIndexInterval}}},
		Action: &client.ScheduleWorkflowAction{
			ID: "search-index", Workflow: SearchIndexWorkflow, TaskQueue: TQWorkflow,
		},
		Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
		Note:    "copies changed incidents into the search index; managed by collisionctl",
	})
	return created, err
}

// TriggerSearchIndex runs the indexer now instead of at the next interval.
func TriggerSearchIndex(ctx context.Context, c client.Client) error {
	return c.ScheduleClient().GetHandle(ctx, SearchScheduleID).Trigger(ctx, client.ScheduleTriggerOptions{})
}
