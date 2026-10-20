package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/pipeline"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
)

func newArticlesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "articles", Short: "Maintain stored articles"}
	fp := &cobra.Command{
		Use:   "fingerprint",
		Short: "Compute duplicate-detection fingerprints for articles stored before they existed",
		Long: `Fills in the canonical-URL key and SimHash of articles that still hold their text, so new
articles can be matched against them. It does not mark existing articles as duplicates of each other:
only articles stored after the fingerprints exist are compared. Safe to re-run.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.cfg.DatabaseURL == "" {
				return errors.New("a database is required: --database-url or COLLISION_DATABASE_URL")
			}
			st, err := storage.Open(cmd.Context(), a.cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer st.Close()
			n, err := pipeline.FingerprintArticles(cmd.Context(), st.Queries(), 200)
			fmt.Fprintf(a.out, "fingerprinted %d articles\n", n)
			return err
		},
	}
	fp.Flags().String("database-url", "", "PostgreSQL DSN (COLLISION_DATABASE_URL)")
	cmd.AddCommand(fp)
	return cmd
}
