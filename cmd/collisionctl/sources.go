package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/pgrepo"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/sourcefile"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
)

func newSourcesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sources",
		Short: "Import, export and check discovery sources and queries",
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
"Sync schedules" in the admin UI or when a source is saved there.`,
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

	cmd.AddCommand(validate, importCmd, export)
	return cmd
}
