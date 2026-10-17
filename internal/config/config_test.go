package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsAndEnv(t *testing.T) {
	t.Setenv("COLLISION_LLM_MODEL", "qwen3")
	t.Setenv("COLLISION_LLM_PRICES", `{"qwen3":{"input_per_m":0.2,"output_per_m":0.6}}`)
	cfg, err := Load(NewViper())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Model != "qwen3" || cfg.LLM.StructuredMode != "json_schema" || cfg.LLM.Timeout != 120*time.Second {
		t.Fatalf("%+v", cfg)
	}
	if cfg.LLM.Prices["qwen3"].OutputPerM != 0.6 {
		t.Fatalf("prices %+v", cfg.LLM.Prices)
	}
}

func TestFileThenEnvOverrides(t *testing.T) {
	f := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(f, []byte("llm:\n  model: from-file\n  fallback_model: fb\n  prices:\n    from-file:\n      input_per_m: 1\n      output_per_m: 2\n"), 0o600)
	t.Setenv("COLLISION_LLM_MODEL", "from-env")
	v := NewViper()
	if err := ReadFile(v, f); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(v)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Model != "from-env" || cfg.LLM.FallbackModel != "fb" || cfg.LLM.Prices["from-file"].InputPerM != 1 {
		t.Fatalf("%+v", cfg.LLM)
	}
}

func TestMissingFileIsFineButBadPricesFail(t *testing.T) {
	v := NewViper()
	if err := ReadFile(v, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COLLISION_LLM_PRICES", "{not json")
	if _, err := Load(NewViper()); err == nil {
		t.Fatal("expected error")
	}
}

func TestTemporalDefaultsAndEnvOverride(t *testing.T) {
	v := NewViper()
	cfg, err := Load(v)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Temporal.Namespace != "collisions" || !strings.Contains(cfg.Temporal.Address, "temporal-frontend") || cfg.Temporal.SearchAttributes {
		t.Fatalf("unexpected defaults: %+v", cfg.Temporal)
	}
	t.Setenv("COLLISION_TEMPORAL_NAMESPACE", "other")
	t.Setenv("COLLISION_TEMPORAL_SEARCH_ATTRIBUTES", "true")
	cfg, err = Load(NewViper())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Temporal.Namespace != "other" || !cfg.Temporal.SearchAttributes {
		t.Fatalf("env not applied: %+v", cfg.Temporal)
	}
}
