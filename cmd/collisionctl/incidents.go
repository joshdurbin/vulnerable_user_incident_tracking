package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/joshdurbin/fuck_cars/internal/incidents"
	"github.com/joshdurbin/fuck_cars/internal/storage"
)

func newIncidentsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "incidents", Short: "Correct incidents"}
	split := &cobra.Command{
		Use:   "split <incident-id> <article-id>",
		Short: "Move one article's reports out of an incident into a new incident",
		Long: `Use when an article was merged into the wrong incident. The article's observations move to a
new incident, and both incidents are recomputed from the observations they keep. The change is recorded in
both incidents' history under --actor.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			incID, err := uuid.Parse(args[0])
			if err != nil {
				return fmt.Errorf("incident id: %w", err)
			}
			artID, err := uuid.Parse(args[1])
			if err != nil {
				return fmt.Errorf("article id: %w", err)
			}
			actor, _ := cmd.Flags().GetString("actor")
			if strings.TrimSpace(actor) == "" {
				return errors.New("--actor is required (recorded in the audit trail)")
			}
			if a.cfg.DatabaseURL == "" {
				return errors.New("a database is required: --database-url or COLLISION_DATABASE_URL")
			}
			st, err := storage.Open(cmd.Context(), a.cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer st.Close()
			newID, err := incidents.NewResolver(st, incidents.DefaultConfig(), a.log).SplitArticle(cmd.Context(), incID, artID, actor)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "split: article %s is now on new incident %s\n", artID, newID)
			return nil
		},
	}
	split.Flags().String("actor", "", "who is making the change (required)")
	split.Flags().String("database-url", "", "PostgreSQL DSN (COLLISION_DATABASE_URL)")
	cmd.AddCommand(split)
	return cmd
}
