package main

import (
	"io"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/config"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/crawler"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/llm"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/logging"
)

// deps are the process-level collaborators; tests inject fakes.
type deps struct {
	out, err  io.Writer
	fetcher   crawler.Fetcher      // nil = the Python crawler worker, via Temporal
	completer llm.Completer        // nil = build the gateway client from config
	registry  *discovery.Registry  // nil = all real providers
	repo      discovery.Repository // nil = PostgreSQL from config (or --sources-file for --dry-run)
}

// app is resolved once per invocation, after flags and config are bound.
type app struct {
	deps
	v   *viper.Viper
	cfg config.Config
	log zerolog.Logger
}

func newRootCmd(d deps) *cobra.Command {
	v := config.NewViper()
	a := &app{deps: d, v: v}
	var cfgFile string

	root := &cobra.Command{
		Use:           "collisionctl",
		Short:         "Collision tracker admin and debugging CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// Flags override env, which overrides the config file, which overrides defaults.
			for key, flag := range flagKeys {
				if f := cmd.Flags().Lookup(flag); f != nil {
					if err := v.BindPFlag(key, f); err != nil {
						return err
					}
				}
			}
			if err := config.ReadFile(v, cfgFile); err != nil {
				return err
			}
			cfg, err := config.Load(v)
			if err != nil {
				return err
			}
			a.cfg = cfg
			a.log = logging.New(a.err, cfg.LogLevel, cfg.LogFormat)
			return nil
		},
	}
	root.SetOut(d.out)
	root.SetErr(d.err)
	pf := root.PersistentFlags()
	pf.StringVar(&cfgFile, "config", "", "config file (default: ./collisionctl.yaml or ~/.config/collisionctl/)")
	pf.String("log-level", "info", "log level: trace|debug|info|warn|error")
	pf.String("log-format", "json", "log format: json|console")
	pf.Bool("json", false, "machine-readable JSON output")

	root.AddCommand(newFetchCmd(a), newClassifyCmd(a), newEvalCmd(a), newMigrateCmd(a), newDiscoverCmd(a), newSourcesCmd(a), newWorkerCmd(a), newSchedulesCmd(a), newBackfillCmd(a), newReprocessCmd(a), newAPICmd(a), newIncidentsCmd(a))
	return root
}

// flagKeys maps config keys to the flag that overrides each. A flag is bound
// only if the executing command defines it.
var flagKeys = map[string]string{
	"log.level": "log-level", "api.listen": "listen", "metrics.listen": "metrics-listen", "api.admin_token": "admin-token", "database.url": "database-url", "discovery.resolve_google_news": "resolve-google-news", "log.format": "log-format", "temporal.address": "temporal-address", "temporal.namespace": "temporal-namespace", "temporal.search_attributes": "search-attributes",
	"llm.base_url": "llm-url", "llm.model": "model", "llm.fallback_model": "fallback-model",
	"llm.structured_mode": "structured-mode", "llm.timeout": "llm-timeout",
}
