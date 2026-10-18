// Package config loads configuration with viper: defaults, an optional config
// file, COLLISION_* environment variables, then flags (highest precedence).
package config

import (
	"errors"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const EnvPrefix = "COLLISION"

type LLM struct {
	BaseURL        string
	APIKey         string
	Model          string
	FallbackModel  string
	StructuredMode string
	Timeout        time.Duration
}

// Temporal locates the workflow service.
type Temporal struct {
	Address          string
	Namespace        string
	SearchAttributes bool // upsert custom search attributes (register them on the namespace first)
}

// API configures the HTTP API process.
type API struct {
	Listen     string
	AdminToken string // guards POST routes; empty disables them
}

type Config struct {
	MetricsListen string // address for /metrics, separate from any public listener
	API           API
	Temporal      Temporal
	DatabaseURL   string
	LogLevel      string
	LogFormat     string
	LLM           LLM
	// RenderFallback lets article workflows escalate pages with no extractable text to the
	// headless-browser pool (collision-render). Needs render workers to be deployed.
	RenderFallback bool
	// ResolveGoogleNews enables the undocumented batchexecute resolver (default off).
	ResolveGoogleNews bool
}

// NewViper returns a viper with defaults and environment binding. Env var names
// are COLLISION_ + the key with dots/dashes as underscores, e.g. COLLISION_LLM_MODEL.
func NewViper() *viper.Viper {
	v := viper.New()
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()
	v.SetDefault("database.url", "")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("discovery.resolve_google_news", false)
	v.SetDefault("crawler.render_fallback", false)
	v.SetDefault("metrics.listen", ":9100")
	v.SetDefault("api.listen", ":8080")
	v.SetDefault("api.admin_token", "")
	v.SetDefault("temporal.address", "temporal-frontend.temporal.svc.cluster.local:7233")
	v.SetDefault("temporal.namespace", "collisions")
	v.SetDefault("temporal.search_attributes", false)
	v.SetDefault("llm.base_url", "http://bifrost.bifrost.svc.cluster.local:8080/v1")
	v.SetDefault("llm.structured_mode", "json_schema")
	v.SetDefault("llm.timeout", 120*time.Second)
	return v
}

// ReadFile loads an explicit file, or searches for collisionctl.{yaml,toml,json}
// in the working directory and ~/.config/collisionctl. A missing file is not an error.
func ReadFile(v *viper.Viper, path string) error {
	if path != "" {
		v.SetConfigFile(path)
		return v.ReadInConfig()
	}
	v.SetConfigName("collisionctl")
	v.AddConfigPath(".")
	v.AddConfigPath("$HOME/.config/collisionctl")
	if err := v.ReadInConfig(); err != nil {
		var nf viper.ConfigFileNotFoundError
		if errors.As(err, &nf) {
			return nil
		}
		return err
	}
	return nil
}

// Load produces the typed config from a configured viper.
func Load(v *viper.Viper) (Config, error) {
	return Config{
		DatabaseURL:       v.GetString("database.url"),
		LogLevel:          v.GetString("log.level"),
		LogFormat:         v.GetString("log.format"),
		ResolveGoogleNews: v.GetBool("discovery.resolve_google_news"),
		RenderFallback:    v.GetBool("crawler.render_fallback"),
		MetricsListen:     v.GetString("metrics.listen"),
		API:               API{Listen: v.GetString("api.listen"), AdminToken: v.GetString("api.admin_token")},
		Temporal: Temporal{
			Address: v.GetString("temporal.address"), Namespace: v.GetString("temporal.namespace"),
			SearchAttributes: v.GetBool("temporal.search_attributes"),
		},
		LLM: LLM{
			BaseURL:        v.GetString("llm.base_url"),
			APIKey:         v.GetString("llm.api_key"),
			Model:          v.GetString("llm.model"),
			FallbackModel:  v.GetString("llm.fallback_model"),
			StructuredMode: v.GetString("llm.structured_mode"),
			Timeout:        v.GetDuration("llm.timeout"),
		},
	}, nil
}
