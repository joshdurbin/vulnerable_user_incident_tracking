package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/api"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/metrics"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/pipeline"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
)

func newAPICmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api",
		Short: "Run the REST API (see api/openapi.yaml)",
		Long: `Serves the read API and a few admin operations. POST routes need
"Authorization: Bearer <token>" matching COLLISION_API_ADMIN_TOKEN; with no token
configured they are disabled. Read routes work even if Temporal is unreachable.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.cfg.DatabaseURL == "" {
				return errors.New("a database is required: --database-url or COLLISION_DATABASE_URL")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			st, err := storage.Open(ctx, a.cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer st.Close()
			reg := metrics.NewRegistry()
			mh, closeMetrics := metrics.TemporalHandler(reg)
			defer closeMetrics()
			reg.MustRegister(api.NewPipelineCollector(st))
			tc, err := pipeline.DialLazy(a.cfg.Temporal.Address, a.cfg.Temporal.Namespace, a.log, pipeline.WithMetrics(mh))
			if err != nil {
				return err
			}
			defer tc.Close()

			wf := api.TemporalWorkflows{C: tc, Options: a.pipelineOptions()}
			// Checked on every start; writes only when the database has no sources.
			if err := api.SeedSources(ctx, st, wf, nil, a.cfg.Sources.SeedFile, a.log); err != nil {
				return err
			}
			h, err := api.NewServer(st, wf,
				api.Config{AdminToken: a.cfg.API.AdminToken, Log: a.log, Metrics: api.NewHTTPMetrics(reg)}).Handler()
			if err != nil {
				return err
			}
			srv := &http.Server{
				Addr: a.cfg.API.Listen, Handler: h,
				ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 2 * time.Minute,
			}
			errc := make(chan error, 2)
			go func() { errc <- srv.ListenAndServe() }()
			go func() { errc <- metrics.Serve(ctx, a.cfg.MetricsListen, reg) }()
			a.log.Info().Str("listen", a.cfg.API.Listen).Bool("admin_enabled", a.cfg.API.AdminToken != "").Msg("api listening")
			select {
			case err := <-errc:
				return err
			case <-ctx.Done():
			}
			shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return srv.Shutdown(shutdown)
		},
	}
	cmd.Flags().String("database-url", "", "PostgreSQL DSN (COLLISION_DATABASE_URL)")
	cmd.Flags().String("listen", "", "listen address (COLLISION_API_LISTEN, default :8080)")
	cmd.Flags().String("metrics-listen", "", "address for /metrics (COLLISION_METRICS_LISTEN, default :9100)")
	cmd.Flags().String("seed-file", "", "YAML of sources to load when the database has none (COLLISION_SOURCES_SEED_FILE)")
	cmd.Flags().String("admin-token", "", "bearer token for admin routes (prefer COLLISION_API_ADMIN_TOKEN)")
	addTemporalFlags(cmd)
	return cmd
}
