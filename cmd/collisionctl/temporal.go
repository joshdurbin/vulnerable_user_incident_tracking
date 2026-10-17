package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"go.temporal.io/sdk/client"

	"github.com/joshdurbin/fuck_cars/internal/classification"
	"github.com/joshdurbin/fuck_cars/internal/discovery"
	"github.com/joshdurbin/fuck_cars/internal/discovery/pgrepo"
	"github.com/joshdurbin/fuck_cars/internal/incidents"
	"github.com/joshdurbin/fuck_cars/internal/pipeline"
	"github.com/joshdurbin/fuck_cars/internal/storage"
)

func addTemporalFlags(cmd *cobra.Command) {
	f := cmd.PersistentFlags()
	f.String("temporal-address", "", "Temporal frontend host:port (COLLISION_TEMPORAL_ADDRESS)")
	f.String("temporal-namespace", "", "Temporal namespace (COLLISION_TEMPORAL_NAMESPACE)")
	f.Bool("search-attributes", false, "upsert custom search attributes (register them first; COLLISION_TEMPORAL_SEARCH_ATTRIBUTES)")
}

func (a *app) temporal() (client.Client, error) {
	c, err := pipeline.Dial(a.cfg.Temporal.Address, a.cfg.Temporal.Namespace, a.log)
	if err != nil {
		return nil, fmt.Errorf("connect to temporal at %s: %w", a.cfg.Temporal.Address, err)
	}
	return c, nil
}

func (a *app) pipelineOptions() pipeline.Options {
	return pipeline.Options{SearchAttributes: a.cfg.Temporal.SearchAttributes}
}

// ---- worker ----

func newWorkerCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run the Temporal workers (workflows and activities)",
		Long: `Runs one worker per task queue so slow or costly work cannot starve the rest:
  collision-workflow   workflow tasks
  collision-crawl      page fetches (rate limited)
  collision-llm        classification (rate limited, budget aware)
  collision-discovery  provider calls (run a single replica: provider limits are per process)
  collision-db         geocoding, entity resolution, reviews`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.cfg.DatabaseURL == "" {
				return errors.New("a database is required: --database-url or COLLISION_DATABASE_URL")
			}
			if a.cfg.LLM.Model == "" {
				return errors.New("a model is required: --model or COLLISION_LLM_MODEL")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			st, err := storage.Open(ctx, a.cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer st.Close()
			reg, closeReg, err := a.discoveryRegistry()
			if err != nil {
				return err
			}
			defer closeReg()
			cl, err := classification.New(a.completer(), classification.Config{PrimaryModel: a.cfg.LLM.Model, FallbackModel: a.cfg.LLM.FallbackModel})
			if err != nil {
				return err
			}
			tc, err := a.temporal()
			if err != nil {
				return err
			}
			defer tc.Close()

			acts := &pipeline.Activities{
				DB: st, Discovery: discovery.NewService(reg, pgrepo.New(st)), Classifier: cl,
				Geocoder: pipeline.NoopGeocoder{}, Resolver: incidents.NewResolver(st, incidents.DefaultConfig(), a.log),
				Temporal: tc, Log: a.log,
			}
			wc := pipeline.DefaultWorkerConfig()
			if qs, _ := cmd.Flags().GetStringSlice("queues"); len(qs) > 0 {
				wc.Queues = qs
			}
			a.log.Info().Str("temporal", a.cfg.Temporal.Address).Str("namespace", a.cfg.Temporal.Namespace).Strs("queues", wc.Queues).Msg("worker starting")
			return pipeline.Run(ctx, pipeline.NewWorkers(tc, acts, wc))
		},
	}
	cmd.Flags().StringSlice("queues", nil, "serve only these task queues (default all)")
	addTemporalFlags(cmd)
	addLLMFlags(cmd)
	cmd.Flags().String("database-url", "", "PostgreSQL DSN (COLLISION_DATABASE_URL)")
	cmd.Flags().Bool("resolve-google-news", false, "resolve Google News links (undocumented endpoint)")
	return cmd
}

// ---- schedules ----

func newSchedulesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "schedules", Short: "Manage the per-source discovery Schedules in Temporal"}
	addTemporalFlags(cmd)

	sync := &cobra.Command{
		Use:   "sync",
		Short: "Create or update one Schedule per source (keeps pauses set in the Temporal UI)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.cfg.DatabaseURL == "" {
				return errors.New("a database is required: --database-url or COLLISION_DATABASE_URL")
			}
			st, err := storage.Open(cmd.Context(), a.cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer st.Close()
			srcs, err := pgrepo.New(st).Sources(cmd.Context(), false)
			if err != nil {
				return err
			}
			tc, err := a.temporal()
			if err != nil {
				return err
			}
			defer tc.Close()
			c, u, err := pipeline.SyncSchedules(cmd.Context(), tc, srcs, a.pipelineOptions())
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "schedules: %d created, %d updated\n", c, u)
			return nil
		},
	}
	sync.Flags().String("database-url", "", "PostgreSQL DSN (COLLISION_DATABASE_URL)")

	byslug := func(use, short string, do func(ctx context.Context, h client.ScheduleHandle) error) *cobra.Command {
		return &cobra.Command{
			Use: use + " <source-slug>", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				tc, err := a.temporal()
				if err != nil {
					return err
				}
				defer tc.Close()
				return do(cmd.Context(), tc.ScheduleClient().GetHandle(cmd.Context(), pipeline.DiscoveryScheduleID(args[0])))
			},
		}
	}
	list := &cobra.Command{
		Use: "list", Short: "List discovery Schedules with state and next run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			tc, err := a.temporal()
			if err != nil {
				return err
			}
			defer tc.Close()
			it, err := tc.ScheduleClient().List(cmd.Context(), client.ScheduleListOptions{})
			if err != nil {
				return err
			}
			for it.HasNext() {
				e, err := it.Next()
				if err != nil {
					return err
				}
				if !strings.HasPrefix(e.ID, "discover-") {
					continue
				}
				state := "active"
				if e.Paused {
					state = "paused"
				}
				next := "-"
				if e.NextActionTimes != nil && len(e.NextActionTimes) > 0 {
					next = e.NextActionTimes[0].Format(time.RFC3339)
				}
				fmt.Fprintf(a.out, "%-28s %-7s next=%s\n", strings.TrimPrefix(e.ID, "discover-"), state, next)
			}
			return nil
		},
	}
	cmd.AddCommand(sync, list,
		byslug("pause", "Pause a source's Schedule", func(ctx context.Context, h client.ScheduleHandle) error {
			return h.Pause(ctx, client.SchedulePauseOptions{Note: "paused via collisionctl"})
		}),
		byslug("resume", "Resume a source's Schedule", func(ctx context.Context, h client.ScheduleHandle) error {
			return h.Unpause(ctx, client.ScheduleUnpauseOptions{Note: "resumed via collisionctl"})
		}),
		byslug("trigger", "Run a source now", func(ctx context.Context, h client.ScheduleHandle) error {
			return h.Trigger(ctx, client.ScheduleTriggerOptions{})
		}),
	)
	return cmd
}

// ---- backfill ----

func newBackfillCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "backfill", Short: "Start and control historical backfills"}
	addTemporalFlags(cmd)

	start := &cobra.Command{
		Use:   "start",
		Short: "Start a backfill over a date range",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fl := cmd.Flags()
			slugs, _ := fl.GetStringSlice("source")
			fromS, _ := fl.GetString("from")
			toS, _ := fl.GetString("to")
			window, _ := fl.GetDuration("window")
			max, _ := fl.GetInt("max-articles")
			id, _ := fl.GetString("id")
			from, err := time.Parse("2006-01-02", fromS)
			if err != nil {
				return fmt.Errorf("--from must be YYYY-MM-DD: %w", err)
			}
			to, err := time.Parse("2006-01-02", toS)
			if err != nil {
				return fmt.Errorf("--to must be YYYY-MM-DD: %w", err)
			}
			if len(slugs) == 0 {
				return errors.New("--source is required (repeatable)")
			}
			if !to.After(from) {
				return errors.New("--to must be after --from")
			}
			if id == "" {
				id = "backfill-" + time.Now().UTC().Format("20060102-150405")
			}
			tc, err := a.temporal()
			if err != nil {
				return err
			}
			defer tc.Close()
			run, err := tc.ExecuteWorkflow(cmd.Context(), client.StartWorkflowOptions{ID: id, TaskQueue: pipeline.TQWorkflow},
				pipeline.BackfillWorkflow, pipeline.BackfillInput{
					Slugs: slugs, From: from, To: to, WindowSize: window, MaxArticles: max, CorrelationID: id, Options: a.pipelineOptions(),
				})
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "started %s (run %s)\nwatch: collisionctl backfill status %s\n", run.GetID(), run.GetRunID(), run.GetID())
			return nil
		},
	}
	start.Flags().StringSlice("source", nil, "source slug (repeatable)")
	start.Flags().String("from", "", "start date YYYY-MM-DD")
	start.Flags().String("to", "", "end date YYYY-MM-DD (exclusive)")
	start.Flags().Duration("window", 24*time.Hour, "size of each discovery window")
	start.Flags().Int("max-articles", 1000, "stop after starting this many article workflows (cost bound)")
	start.Flags().String("id", "", "workflow ID (default backfill-<timestamp>)")

	status := &cobra.Command{
		Use: "status <workflow-id>", Short: "Show backfill progress", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tc, err := a.temporal()
			if err != nil {
				return err
			}
			defer tc.Close()
			v, err := tc.QueryWorkflow(cmd.Context(), args[0], "", pipeline.QueryProgress)
			if err != nil {
				return err
			}
			var p pipeline.BackfillProgress
			if err := v.Get(&p); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s  steps %d/%d  found=%d  articles started=%d/%d  %s\n", p.Phase, p.StepsDone, p.TotalSteps, p.Found, p.Started, p.MaxArticles, p.Current)
			return nil
		},
	}
	signalCmd := func(name, short, sig string) *cobra.Command {
		return &cobra.Command{
			Use: name + " <workflow-id>", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				tc, err := a.temporal()
				if err != nil {
					return err
				}
				defer tc.Close()
				return tc.SignalWorkflow(cmd.Context(), args[0], "", sig, nil)
			},
		}
	}
	cmd.AddCommand(start, status, signalCmd("pause", "Pause a backfill", pipeline.SignalPause),
		signalCmd("resume", "Resume a backfill", pipeline.SignalResume), signalCmd("cancel", "Cancel a backfill", pipeline.SignalCancel))
	return cmd
}

// ---- reprocess ----

func newReprocessCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "reprocess", Short: "Re-classify retained articles with the current model, prompt and schema"}
	addTemporalFlags(cmd)
	start := &cobra.Command{
		Use: "start", Short: "Start a reprocess run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fl := cmd.Flags()
			limit, _ := fl.GetInt("limit")
			batch, _ := fl.GetInt("batch-size")
			conc, _ := fl.GetInt("concurrency")
			id := "reprocess-" + uuid.NewString()[:8]
			tc, err := a.temporal()
			if err != nil {
				return err
			}
			defer tc.Close()
			run, err := tc.ExecuteWorkflow(cmd.Context(), client.StartWorkflowOptions{ID: id, TaskQueue: pipeline.TQWorkflow},
				pipeline.ReprocessWorkflow, pipeline.ReprocessInput{Limit: limit, BatchSize: batch, MaxConcurrent: conc, CorrelationID: id, Options: a.pipelineOptions()})
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "started %s\n", run.GetID())
			return nil
		},
	}
	start.Flags().Int("limit", 100, "maximum articles to reprocess in this run (cost bound), 0 = all")
	start.Flags().Int("batch-size", 20, "articles per batch")
	start.Flags().Int("concurrency", 4, "articles in flight at once")
	status := &cobra.Command{
		Use: "status <workflow-id>", Short: "Show reprocess progress", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tc, err := a.temporal()
			if err != nil {
				return err
			}
			defer tc.Close()
			v, err := tc.QueryWorkflow(cmd.Context(), args[0], "", pipeline.QueryProgress)
			if err != nil {
				return err
			}
			var p pipeline.ReprocessResult
			if err := v.Get(&p); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "examined=%d changed=%d failed=%d\n", p.Examined, p.Changed, p.Failed)
			return nil
		},
	}
	cmd.AddCommand(start, status)
	return cmd
}
