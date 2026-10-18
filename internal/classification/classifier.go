// Package classification turns an article into validated incident candidates
// using an LLM. Model output is untrusted and always validated before use.
package classification

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/domain"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/llm"
)

const (
	PromptVersion = "v1"
	SchemaVersion = "v1"
)

//go:embed schema.json
var schemaJSON []byte

//go:embed prompts/v1.txt
var promptV1 string

// Config selects models and bounds input size.
type Config struct {
	PrimaryModel  string
	FallbackModel string // optional
	MaxInputChars int    // article text is truncated beyond this
}

type Classifier struct {
	llm    llm.Completer
	cfg    Config
	schema *jsonschema.Schema
	raw    map[string]any
	now    func() time.Time
}

func New(c llm.Completer, cfg Config) (*Classifier, error) {
	if cfg.PrimaryModel == "" {
		return nil, errors.New("classification: primary model is required")
	}
	if cfg.MaxInputChars == 0 {
		cfg.MaxInputChars = 12000
	}
	sch, err := compileSchema(schemaJSON)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(schemaJSON, &raw); err != nil {
		return nil, err
	}
	return &Classifier{llm: c, cfg: cfg, schema: sch, raw: raw, now: time.Now}, nil
}

func (c *Classifier) userPrompt(a *domain.Article) (string, string) {
	text := a.Text
	if r := []rune(text); len(r) > c.cfg.MaxInputChars {
		text = string(r[:c.cfg.MaxInputChars])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\nPublisher: %s\n", a.Title, a.Publisher)
	if a.PublishedAt != nil {
		fmt.Fprintf(&b, "Publication date (NOT necessarily the incident date): %s\n", a.PublishedAt.Format("2006-01-02 (Monday)"))
	}
	b.WriteString("\n<article>\n" + escapeDelimiters(text) + "\n</article>")
	return b.String(), text
}

// Classify runs the primary model, falling back to the secondary on invalid
// output or request failure. Token usage from every attempt is summed.
func (c *Classifier) Classify(ctx context.Context, a *domain.Article) (*domain.ExtractionResult, error) {
	if strings.TrimSpace(a.Text) == "" {
		return nil, errors.New("classification: article has no text")
	}
	user, usedText := c.userPrompt(a)
	models := []string{c.cfg.PrimaryModel}
	if c.cfg.FallbackModel != "" && c.cfg.FallbackModel != c.cfg.PrimaryModel {
		models = append(models, c.cfg.FallbackModel)
	}
	var usage domain.Usage
	var errs []error
	start := time.Now()
	for i, model := range models {
		resp, err := c.llm.Complete(ctx, llm.Request{
			Model: model, System: promptV1, User: user, SchemaName: "collision_extraction", Schema: c.raw,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", model, err))
			if errors.Is(err, llm.ErrBudgetExceeded) || ctx.Err() != nil {
				break
			}
			continue
		}
		usage.Calls++
		usage.InputTokens += resp.InputTokens
		usage.OutputTokens += resp.OutputTokens
		usage.CostUSD += resp.CostUSD
		res, verr := validate(c.schema, resp.Content, usedText, c.now())
		if verr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", model, verr))
			continue
		}
		if len(usedText) < len(a.Text) {
			res.Warnings = append(res.Warnings, "article text truncated for the model")
		}
		if !resp.PriceKnown {
			res.Warnings = append(res.Warnings, "no price configured for model "+model+"; cost not tracked")
		}
		res.Meta = domain.ExtractionMeta{
			Model: model, PromptVersion: PromptVersion, SchemaVersion: SchemaVersion,
			StructuredMode: string(resp.Mode), UsedFallback: i > 0, Usage: usage,
			Latency: time.Since(start), ExtractedAt: c.now().UTC(),
		}
		return res, nil
	}
	return nil, fmt.Errorf("classification failed: %w", errors.Join(errs...))
}

var delimiterRE = regexp.MustCompile(`(?i)</?\s*article\s*>`)

// escapeDelimiters stops untrusted text from closing the <article> block early.
func escapeDelimiters(s string) string {
	return delimiterRE.ReplaceAllStringFunc(s, func(m string) string { return strings.NewReplacer("<", "[", ">", "]").Replace(m) })
}

// Versions identifies what a result from this classifier is attributed to, so
// callers can detect an existing extraction before paying for another LLM call.
func (c *Classifier) Versions() (model, promptVersion, schemaVersion string) {
	return c.cfg.PrimaryModel, PromptVersion, SchemaVersion
}
