// Package config loads configuration with viper: defaults, an optional config
// file, COLLISION_* environment variables, then flags (highest precedence).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/joshdurbin/fuck_cars/internal/llm"
)

const EnvPrefix = "COLLISION"

type LLM struct {
	BaseURL        string
	APIKey         string
	Model          string
	FallbackModel  string
	StructuredMode string
	Timeout        time.Duration
	Prices         map[string]llm.Price
	BudgetUSD      float64
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
	v.SetDefault("metrics.listen", ":9100")
	v.SetDefault("api.listen", ":8080")
	v.SetDefault("api.admin_token", "")
	v.SetDefault("temporal.address", "temporal-frontend.temporal.svc.cluster.local:7233")
	v.SetDefault("temporal.namespace", "collisions")
	v.SetDefault("temporal.search_attributes", false)
	v.SetDefault("llm.base_url", "http://bifrost.bifrost.svc.cluster.local:8080/v1")
	v.SetDefault("llm.structured_mode", "json_schema")
	v.SetDefault("llm.timeout", 120*time.Second)
	v.SetDefault("llm.budget_usd", 0.0)
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
	prices, err := loadPrices(v)
	if err != nil {
		return Config{}, err
	}
	return Config{
		DatabaseURL:       v.GetString("database.url"),
		LogLevel:          v.GetString("log.level"),
		LogFormat:         v.GetString("log.format"),
		ResolveGoogleNews: v.GetBool("discovery.resolve_google_news"),
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
			Prices:         prices,
			BudgetUSD:      v.GetFloat64("llm.budget_usd"),
		},
	}, nil
}

// loadPrices accepts either a map (config file) or a JSON string (env/flag).
func loadPrices(v *viper.Viper) (map[string]llm.Price, error) {
	out := map[string]llm.Price{}
	raw := v.Get("llm.prices")
	switch t := raw.(type) {
	case nil:
		return out, nil
	case string:
		if strings.TrimSpace(t) == "" {
			return out, nil
		}
		if err := json.Unmarshal([]byte(t), &out); err != nil {
			return nil, fmt.Errorf("invalid llm.prices JSON: %w", err)
		}
	default:
		if err := v.UnmarshalKey("llm.prices", &out); err != nil {
			return nil, fmt.Errorf("invalid llm.prices: %w", err)
		}
	}
	return out, nil
}
