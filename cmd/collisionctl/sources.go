package main

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/pgrepo"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/seed"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
)

func newSourcesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "sources", Short: "Manage discovery sources and queries"}
	seedFlag := func(c *cobra.Command) {
		c.Flags().String("file", "", "extra sources/queries YAML merged over the embedded defaults")
	}
	dbFlag := func(c *cobra.Command) {
		c.Flags().String("database-url", "", "PostgreSQL connection string (COLLISION_DATABASE_URL)")
	}

	validate := &cobra.Command{
		Use:   "validate",
		Short: "Check the seed files (embedded defaults plus --file) without touching a database",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, _ := cmd.Flags().GetString("file")
			f, err := loadSeeds(file)
			if err != nil {
				return err
			}
			reg, closeReg, err := a.discoveryRegistry()
			if err != nil {
				return err
			}
			defer closeReg()
			errs := seed.Validate(f, reg.Types())
			if len(errs) > 0 {
				for _, e := range errs {
					fmt.Fprintln(a.err, "invalid:", e)
				}
				return fmt.Errorf("%d problem(s) in the seed files", len(errs))
			}
			fmt.Fprintf(a.out, "OK: %d sources, %d queries, provider types: %s\n", len(f.Sources), len(f.Queries), strings.Join(reg.Types(), ", "))
			return nil
		},
	}
	seedFlag(validate)

	sync := &cobra.Command{
		Use:   "sync",
		Short: "Validate the seed files and upsert them into the database",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, _ := cmd.Flags().GetString("file")
			if a.cfg.DatabaseURL == "" {
				return errors.New("a database is required: --database-url or COLLISION_DATABASE_URL")
			}
			f, err := loadSeeds(file)
			if err != nil {
				return err
			}
			reg, closeReg, err := a.discoveryRegistry()
			if err != nil {
				return err
			}
			defer closeReg()
			if errs := seed.Validate(f, reg.Types()); len(errs) > 0 {
				for _, e := range errs {
					fmt.Fprintln(a.err, "invalid:", e)
				}
				return fmt.Errorf("refusing to sync: %d problem(s)", len(errs))
			}
			st, err := storage.Open(cmd.Context(), a.cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer st.Close()
			if onlyNew, _ := cmd.Flags().GetBool("only-new"); onlyNew {
				ns, nq, err := pgrepo.New(st).SyncNew(cmd.Context(), f)
				if err != nil {
					return err
				}
				fmt.Fprintf(a.out, "added %d sources and %d queries (existing rows untouched)\n", ns, nq)
				return nil
			}
			ns, nq, err := pgrepo.New(st).Sync(cmd.Context(), f)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "synced %d sources and %d queries\n", ns, nq)
			return nil
		},
	}
	sync.Flags().Bool("only-new", false, "only add what is missing; never overwrite existing rows (safe to run on every deploy)")
	seedFlag(sync)
	dbFlag(sync)

	list := &cobra.Command{
		Use:   "list",
		Short: "List sources with their status (database, or the seed files if no database is configured)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, _ := cmd.Flags().GetString("file")
			asJSON, _ := cmd.Flags().GetBool("json")
			repo, closeRepo, err := a.discoveryRepo(cmd.Context(), file, true)
			if err != nil {
				return err
			}
			defer closeRepo()
			srcs, err := repo.Sources(cmd.Context(), false)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(a.out, srcs)
			}
			fmt.Fprintf(a.out, "%-18s %-13s %-8s %-9s %-20s %s\n", "SLUG", "TYPE", "ENABLED", "INTERVAL", "LAST SUCCESS", "STATE")
			for _, s := range srcs {
				last := "never"
				if s.LastSuccessAt != nil {
					last = s.LastSuccessAt.Local().Format("01-02 15:04")
				}
				state := "ok"
				if s.ConsecutiveFailures > 0 {
					state = fmt.Sprintf("%d failures: %.50s", s.ConsecutiveFailures, s.ErrorState)
				}
				fmt.Fprintf(a.out, "%-18s %-13s %-8v %-9s %-20s %s\n", s.Slug, s.Type, s.Enabled, s.PollInterval, last, state)
			}
			return nil
		},
	}
	seedFlag(list)
	dbFlag(list)

	stats := &cobra.Command{
		Use:   "stats",
		Short: "Show which discovery sources are actually valuable",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			since, _ := cmd.Flags().GetDuration("since")
			asJSON, _ := cmd.Flags().GetBool("json")
			repo, closeRepo, err := a.discoveryRepo(cmd.Context(), "", false)
			if err != nil {
				return err
			}
			defer closeRepo()
			rows, err := repo.Stats(cmd.Context(), time.Now().Add(-since))
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(a.out, rows)
			}
			printStats(a.out, rows)
			return nil
		},
	}
	stats.Flags().Duration("since", 7*24*time.Hour, "look-back window")
	dbFlag(stats)

	cmd.AddCommand(validate, sync, list, stats)
	return cmd
}

func printStats(w io.Writer, rows []discovery.SourceStats) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Incidents > rows[j].Incidents })
	fmt.Fprintf(w, "%-12s %-18s %6s %6s %6s %7s %7s %8s %9s %5s\n", "PROVIDER", "SOURCE", "SEEN", "UNIQ", "ONLY", "DUP%", "FETCH%", "RELEVANT", "DELAY", "INC")
	for _, r := range rows {
		fmt.Fprintf(w, "%-12s %-18.18s %6d %6d %6d %6.0f%% %6.0f%% %7.0f%% %9s %5d\n", r.Provider, r.SourceSlug, r.Discoveries, r.UniqueURLs, r.ExclusiveURLs,
			100*r.DuplicateRate(), 100*r.FetchSuccessRate(), 100*r.RelevantRate(), (time.Duration(r.AvgDelaySeconds) * time.Second).Round(time.Minute), r.Incidents)
	}
}
