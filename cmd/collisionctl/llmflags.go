package main

import (
	"github.com/spf13/cobra"

	"github.com/joshdurbin/fuck_cars/internal/llm"
)

// addLLMFlags registers the gateway flags shared by commands that call the LLM.
// Defaults come from config, so flags are only set explicitly when overriding.
func addLLMFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("llm-url", "", "OpenAI-compatible gateway base URL (COLLISION_LLM_BASE_URL)")
	f.String("model", "", "primary model (COLLISION_LLM_MODEL)")
	f.String("fallback-model", "", "fallback model (COLLISION_LLM_FALLBACK_MODEL)")
	f.String("structured-mode", "", "json_schema|json_object|prompt (COLLISION_LLM_STRUCTURED_MODE)")
	f.Duration("llm-timeout", 0, "LLM request timeout (COLLISION_LLM_TIMEOUT)")
	f.String("prices", "", `JSON {"model":{"input_per_m":0.2,"output_per_m":0.6}} (COLLISION_LLM_PRICES)`)
	f.Float64("budget-usd", 0, "spend limit for this run, 0 = unlimited (COLLISION_LLM_BUDGET_USD)")
}

// completer returns the injected Completer (tests) or a gateway client from config.
func (a *app) completer() llm.Completer {
	if a.deps.completer != nil {
		return a.deps.completer
	}
	c := a.cfg.LLM
	key := c.APIKey
	if key == "" {
		key = "unused" // Bifrost in-cluster needs no key; the SDK requires a non-empty value
	}
	return llm.NewClient(llm.Config{
		BaseURL: c.BaseURL, APIKey: key, Timeout: c.Timeout, MaxRetries: 2,
		StartMode: llm.StructuredMode(c.StructuredMode), Prices: c.Prices,
	}, llm.NewMemoryBudget(c.BudgetUSD))
}
