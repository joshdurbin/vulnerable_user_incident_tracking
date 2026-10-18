package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
)

func newMigrateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending database migrations (idempotent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.cfg.DatabaseURL == "" {
				return errors.New("database url required: --database-url or COLLISION_DATABASE_URL")
			}
			if err := storage.Migrate(cmd.Context(), a.cfg.DatabaseURL); err != nil {
				return err
			}
			a.log.Info().Msg("migrations applied")
			fmt.Fprintln(a.out, "migrations up to date")
			return nil
		},
	}
	cmd.Flags().String("database-url", "", "PostgreSQL connection string (COLLISION_DATABASE_URL)")
	return cmd
}
