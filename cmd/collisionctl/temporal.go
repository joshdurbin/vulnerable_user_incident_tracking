package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"go.temporal.io/sdk/client"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/classification"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/pgrepo"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/incidents"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/metrics"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/pipeline"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
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
	return pipeline.Options{SearchAttributes: a.cfg.Temporal.SearchAttributes, Render: a.cfg.RenderFallback}
}

// ---- worker ----

func newWorkerCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run the Temporal workers (workflows and activities)",
		Long: `Runs one worker per task queue so slow work cannot starve the rest:
  collision-workflow   workflow tasks
  collision-crawl      page fetches (rate limited)
  collision-llm        classification (rate limited)
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
			promReg := metrics.NewRegistry()
			mh, closeMetrics := metrics.TemporalHandler(promReg)
			defer closeMetrics()
			tc, err := pipeline.Dial(a.cfg.Temporal.Address, a.cfg.Temporal.Namespace, a.log, pipeline.WithMetrics(mh))
			if err != nil {
				return fmt.Errorf("connect to temporal at %s: %w", a.cfg.Temporal.Address, err)
			}
			defer tc.Close()
			go func() {
				if err := metrics.Serve(ctx, a.cfg.MetricsListen, promReg); err != nil {
					a.log.Error().Err(err).Msg("metrics server stopped")
				}
			}()

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
	cmd.Flags().String("metrics-listen", "", "address for /metrics (COLLISION_METRICS_LISTEN, default :9100)")
	addTemporalFlags(cmd)
	addLLMFlags(cmd)
	cmd.Flags().String("database-url", "", "PostgreSQL DSN (COLLISION_DATABASE_URL)")
	cmd.Flags().Bool("resolve-google-news", false, "resolve Google News links (undocumented endpoint)")
	return cmd
}

// ---- schedules ----

func newSchedulesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "schedules", Short: "Deploy-time scheduling setup (the admin UI manages individual sources' schedules)"}
	addTemporalFlags(cmd)

	sync := &cobra.Command{
		Use:   "sync",
		Short: "Make the Schedules match the configured sources (keeps pauses set in the Temporal UI; removes those of deleted sources)",
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
			keep := make(map[string]bool, len(srcs))
			for _, s := range srcs {
				keep[s.Slug] = true
			}
			removed, err := pipeline.PruneSchedules(cmd.Context(), tc, keep)
			if err != nil {
				return err
			}
			if _, err := pipeline.SyncMaintenanceSchedules(cmd.Context(), tc); err != nil {
				return err
			}
			if len(srcs) == 0 {
				fmt.Fprintln(a.out, "no sources are configured: add some in the admin UI or with `collisionctl sources import`")
			}
			fmt.Fprintf(a.out, "schedules: %d created, %d updated, %d removed (plus the retention schedule)\n", c, u, removed)
			return nil
		},
	}
	sync.Flags().String("database-url", "", "PostgreSQL DSN (COLLISION_DATABASE_URL)")
	cmd.AddCommand(sync)
	return cmd
}
