package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/joshdurbin/fuck_cars/internal/discovery"
	"github.com/joshdurbin/fuck_cars/internal/discovery/googlenews"
	"github.com/joshdurbin/fuck_cars/internal/discovery/pgrepo"
	"github.com/joshdurbin/fuck_cars/internal/discovery/registry"
	"github.com/joshdurbin/fuck_cars/internal/discovery/seed"
	"github.com/joshdurbin/fuck_cars/internal/storage"
)

// discoveryRegistry returns the injected registry or all real providers.
func (a *app) discoveryRegistry() (*discovery.Registry, func(), error) {
	if a.registry != nil {
		return a.registry, func() {}, nil
	}
	// Crawl-backed source types (index pages, press pages) reach the Python worker through Temporal.
	// The connection is made lazily so RSS, GDELT and Google News runs need no cluster.
	c := lazyCrawler{a: a}
	search := discovery.NewPlainHTTPClient(discovery.DefaultUserAgent, 45*time.Second)
	deps := registry.Deps{
		HTTP:       discovery.NewHTTPClient(discovery.DefaultUserAgent, 30*time.Second),
		SearchHTTP: search,
		Crawler:    &c,
	}
	if a.cfg.ResolveGoogleNews {
		// Opt-in: relies on an undocumented Google endpoint (see googlenews.BatchExecuteResolver).
		deps.GoogleResolvers = []googlenews.Resolver{googlenews.LegacyDecoder{}, googlenews.NewBatchExecuteResolver(search)}
	}
	return registry.New(deps), c.close, nil
}

// discoveryRepo returns the injected repository, PostgreSQL if configured, or
// (only when memoryOK) the seed files so dry runs need no database.
func (a *app) discoveryRepo(ctx context.Context, seedFile string, memoryOK bool) (discovery.Repository, func(), error) {
	if a.repo != nil {
		return a.repo, func() {}, nil
	}
	if a.cfg.DatabaseURL != "" {
		st, err := storage.Open(ctx, a.cfg.DatabaseURL)
		if err != nil {
			return nil, nil, err
		}
		return pgrepo.New(st), st.Close, nil
	}
	if !memoryOK {
		return nil, nil, errors.New("a database is required: --database-url or COLLISION_DATABASE_URL")
	}
	f, err := loadSeeds(seedFile)
	if err != nil {
		return nil, nil, err
	}
	return seed.MemoryRepo{F: f}, func() {}, nil
}

func loadSeeds(file string) (seed.File, error) {
	f, err := seed.Defaults()
	if err != nil {
		return f, err
	}
	if file != "" {
		over, err := seed.LoadFile(file)
		if err != nil {
			return f, err
		}
		f = seed.Merge(f, over)
	}
	return f, nil
}

func newDiscoverCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Run discovery for a source and record newly found article URLs",
		Long: `Runs one source (by --source slug, or --provider type when exactly one enabled source has it).
With --dry-run nothing is written and no database is needed: sources and queries come from the seed files.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := cmd.Flags()
			slug, _ := f.GetString("source")
			provider, _ := f.GetString("provider")
			dry, _ := f.GetBool("dry-run")
			asJSON, _ := f.GetBool("json")
			lookback, _ := f.GetDuration("lookback")
			fromS, _ := f.GetString("from")
			toS, _ := f.GetString("to")
			seedFile, _ := f.GetString("seed-file")
			limit, _ := f.GetInt("show")
			queryNames, _ := f.GetStringSlice("query")

			win, err := parseWindow(lookback, fromS, toS, time.Now())
			if err != nil {
				return err
			}
			reg, closeReg, err := a.discoveryRegistry()
			if err != nil {
				return err
			}
			defer closeReg()
			repo, closeRepo, err := a.discoveryRepo(cmd.Context(), seedFile, dry)
			if err != nil {
				return err
			}
			defer closeRepo()

			if slug == "" {
				if provider == "" {
					return errors.New("give --source <slug> or --provider <type>")
				}
				if slug, err = pickSource(cmd.Context(), repo, provider); err != nil {
					return err
				}
			}
			a.log.Info().Str("source", slug).Bool("dry_run", dry).Msg("discovering")
			rep, err := discovery.NewService(reg, repo).RunSlug(cmd.Context(), slug, win, dry, queryNames...)
			if rep != nil && asJSON {
				if werr := writeJSON(a.out, rep); werr != nil {
					return werr
				}
			} else if rep != nil {
				printDiscovery(a.out, rep, limit, dry)
			}
			return err
		},
	}
	f := cmd.Flags()
	f.String("source", "", "source slug (see `collisionctl sources list`)")
	f.String("provider", "", "provider type: gdelt|googlenews|rss|... (picks the single enabled source of that type)")
	f.Bool("dry-run", false, "discover and dedupe but do not write to the database")
	f.Duration("lookback", 0, "window length ending now (default: 2x the source poll interval, at least 2h)")
	f.String("from", "", "window start (RFC3339), for backfills")
	f.String("to", "", "window end (RFC3339), default now")
	f.String("seed-file", "", "extra sources/queries YAML merged over the defaults (dry-run without a database)")
	f.Bool("resolve-google-news", false, "resolve Google News links to publisher URLs via an UNDOCUMENTED Google endpoint (COLLISION_DISCOVERY_RESOLVE_GOOGLE_NEWS)")
	f.StringSlice("query", nil, "only run these configured queries (search providers; repeatable)")
	f.Int("show", 10, "candidates to print in human output")
	f.String("database-url", "", "PostgreSQL connection string (COLLISION_DATABASE_URL)")
	return cmd
}

func parseWindow(lookback time.Duration, from, to string, now time.Time) (discovery.Window, error) {
	switch {
	case from != "":
		since, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return discovery.Window{}, fmt.Errorf("--from: %w", err)
		}
		until := now
		if to != "" {
			if until, err = time.Parse(time.RFC3339, to); err != nil {
				return discovery.Window{}, fmt.Errorf("--to: %w", err)
			}
		}
		if !until.After(since) {
			return discovery.Window{}, errors.New("--to must be after --from")
		}
		return discovery.Window{Since: since.UTC(), Until: until.UTC()}, nil
	case lookback > 0:
		return discovery.Window{Since: now.Add(-lookback).UTC(), Until: now.UTC()}, nil
	}
	return discovery.Window{}, nil
}

func pickSource(ctx context.Context, repo discovery.Repository, provider string) (string, error) {
	all, err := repo.Sources(ctx, true)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, s := range all {
		if s.Type == provider {
			matches = append(matches, s.Slug)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no enabled source of type %q; run `collisionctl sources sync` or pass --source", provider)
	case 1:
		return matches[0], nil
	}
	return "", fmt.Errorf("%d enabled sources have type %q (%v); choose one with --source", len(matches), provider, matches)
}

func printDiscovery(w io.Writer, r *discovery.Report, show int, dry bool) {
	mode := "recorded"
	if dry {
		mode = "DRY RUN (nothing written)"
	}
	fmt.Fprintf(w, "Source:   %s (%s)   %s\n", r.Source.Slug, r.Source.Type, mode)
	fmt.Fprintf(w, "Window:   %s .. %s\n", r.Window.Since.Format(time.RFC3339), r.Window.Until.Format(time.RFC3339))
	fmt.Fprintf(w, "Found:    %d candidates, %d unique after normalization (%d invalid), %d upstream requests\n", r.Found, r.Unique, len(r.Rejected), r.Requests)
	if !dry {
		fmt.Fprintf(w, "Result:   %d NEW urls, %d already known\n", len(r.New), r.Known)
	}
	if r.Unresolved > 0 {
		fmt.Fprintf(w, "Unresolved: %d candidates still point at an intermediary URL and will not be crawled\n", r.Unresolved)
	}
	if r.Truncated {
		fmt.Fprintln(w, "TRUNCATED: a provider limit cut results off")
	}
	for _, wn := range r.Warnings {
		fmt.Fprintf(w, "warning:  %s\n", wn)
	}
	for i, c := range r.Candidates {
		if i >= show {
			fmt.Fprintf(w, "  ... and %d more\n", len(r.Candidates)-show)
			break
		}
		when := "-"
		if c.Primary.PublishedAt != nil {
			when = c.Primary.PublishedAt.Format("01-02 15:04")
		}
		flag := ""
		if c.Primary.NeedsResolution {
			flag = " [unresolved]"
		}
		fmt.Fprintf(w, "  %s  %-22.22s %.70s%s\n        %s\n", when, c.Host, c.Primary.Title, flag, c.Normalized)
	}
}
