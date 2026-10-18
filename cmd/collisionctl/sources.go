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
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/sourcefile"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
)

func newSourcesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sources",
		Short: "Inspect, import and export discovery sources and queries",
		Long: `Sources and queries live in the database and are managed in the admin UI. These commands
are for scripting around that: import a YAML file (for example docs/examples/sources.yaml on a
new install), export what is configured, and check a file before importing it.`,
	}
	fileFlag := func(c *cobra.Command, usage string) {
		c.Flags().String("file", "", usage)
	}
	dbFlag := func(c *cobra.Command) {
		c.Flags().String("database-url", "", "PostgreSQL connection string (COLLISION_DATABASE_URL)")
	}

	validate := &cobra.Command{
		Use:   "validate --file FILE",
		Short: "Check a sources/queries file without touching a database",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, _ := cmd.Flags().GetString("file")
			f, err := loadSourceFile(file)
			if err != nil {
				return err
			}
			reg, closeReg, err := a.discoveryRegistry()
			if err != nil {
				return err
			}
			defer closeReg()
			if errs := sourcefile.Validate(reg, f); len(errs) > 0 {
				for _, e := range errs {
					fmt.Fprintln(a.err, "invalid:", e)
				}
				return fmt.Errorf("%d problem(s) in %s", len(errs), file)
			}
			fmt.Fprintf(a.out, "OK: %d sources, %d queries, source types: %s\n", len(f.Sources), len(f.Queries), strings.Join(reg.Types(), ", "))
			return nil
		},
	}
	fileFlag(validate, "sources/queries YAML to check (required)")

	importCmd := &cobra.Command{
		Use:   "import --file FILE",
		Short: "Validate a sources/queries file and write it to the database",
		Long: `Writes the sources and queries in FILE to the database. By default an entry replaces the
configured one with the same slug (sources) or name (queries); with --only-new, entries that
already exist are left exactly as they are. Temporal Schedules follow on the next
` + "`collisionctl schedules sync`" + ` or when a source is saved in the admin UI.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, _ := cmd.Flags().GetString("file")
			onlyNew, _ := cmd.Flags().GetBool("only-new")
			if a.cfg.DatabaseURL == "" {
				return errors.New("a database is required: --database-url or COLLISION_DATABASE_URL")
			}
			f, err := loadSourceFile(file)
			if err != nil {
				return err
			}
			reg, closeReg, err := a.discoveryRegistry()
			if err != nil {
				return err
			}
			defer closeReg()
			if errs := sourcefile.Validate(reg, f); len(errs) > 0 {
				for _, e := range errs {
					fmt.Fprintln(a.err, "invalid:", e)
				}
				return fmt.Errorf("refusing to import: %d problem(s)", len(errs))
			}
			st, err := storage.Open(cmd.Context(), a.cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer st.Close()
			ns, nq, err := pgrepo.New(st).Import(cmd.Context(), f, !onlyNew)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "wrote %d sources and %d queries\n", ns, nq)
			return nil
		},
	}
	fileFlag(importCmd, "sources/queries YAML to import (required)")
	importCmd.Flags().Bool("only-new", false, "only add what is missing; never overwrite an existing source or query")
	dbFlag(importCmd)

	export := &cobra.Command{
		Use:   "export",
		Short: "Write the configured sources and queries as YAML (to stdout)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.cfg.DatabaseURL == "" {
				return errors.New("a database is required: --database-url or COLLISION_DATABASE_URL")
			}
			st, err := storage.Open(cmd.Context(), a.cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer st.Close()
			repo := pgrepo.New(st)
			srcs, err := repo.Sources(cmd.Context(), false)
			if err != nil {
				return err
			}
			qs, err := repo.AllQueries(cmd.Context())
			if err != nil {
				return err
			}
			var f sourcefile.File
			for _, s := range srcs {
				f.Sources = append(f.Sources, sourcefile.FromSource(s))
			}
			for _, q := range qs {
				f.Queries = append(f.Queries, sourcefile.QueryFromSpec(q))
			}
			b, err := sourcefile.Marshal(sourcefile.Sorted(f))
			if err != nil {
				return err
			}
			_, err = a.out.Write(b)
			return err
		},
	}
	dbFlag(export)

	list := &cobra.Command{
		Use:   "list",
		Short: "List sources with their status (from the database, or from --file without one)",
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
			if len(srcs) == 0 {
				fmt.Fprintln(a.out, "no sources configured; add one in the admin UI or run `collisionctl sources import --file docs/examples/sources.yaml`")
				return nil
			}
			now := time.Now()
			fmt.Fprintf(a.out, "%-18s %-13s %-9s %-9s %-11s %s\n", "SLUG", "TYPE", "ENABLED", "INTERVAL", "HEALTH", "LAST SUCCESS")
			for _, s := range srcs {
				last := "never"
				if s.LastSuccessAt != nil {
					last = s.LastSuccessAt.Local().Format("01-02 15:04")
				}
				h := discovery.AssessHealth(s, false, now)
				fmt.Fprintf(a.out, "%-18s %-13s %-9v %-9s %-11s %s\n", s.Slug, s.Type, s.Enabled, s.PollInterval, h.State, last)
			}
			return nil
		},
	}
	fileFlag(list, "read sources from this file instead of the database")
	list.Flags().Bool("json", false, "machine-readable output")
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
	stats.Flags().Bool("json", false, "machine-readable output")
	dbFlag(stats)

	cmd.AddCommand(validate, importCmd, export, list, stats)
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
