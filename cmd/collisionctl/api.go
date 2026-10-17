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

	"github.com/joshdurbin/fuck_cars/internal/api"
	"github.com/joshdurbin/fuck_cars/internal/pipeline"
	"github.com/joshdurbin/fuck_cars/internal/storage"
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
			tc, err := pipeline.DialLazy(a.cfg.Temporal.Address, a.cfg.Temporal.Namespace, a.log)
			if err != nil {
				return err
			}
			defer tc.Close()

			h, err := api.NewServer(st, api.TemporalWorkflows{C: tc, Options: a.pipelineOptions()},
				api.Config{AdminToken: a.cfg.API.AdminToken, Log: a.log}).Handler()
			if err != nil {
				return err
			}
			srv := &http.Server{
				Addr: a.cfg.API.Listen, Handler: h,
				ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 2 * time.Minute,
			}
			errc := make(chan error, 1)
			go func() { errc <- srv.ListenAndServe() }()
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
	cmd.Flags().String("admin-token", "", "bearer token for admin routes (prefer COLLISION_API_ADMIN_TOKEN)")
	addTemporalFlags(cmd)
	return cmd
}
