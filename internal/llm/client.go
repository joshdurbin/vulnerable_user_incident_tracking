// Package llm wraps the official OpenAI Go SDK to talk to any OpenAI-compatible
// gateway (Bifrost, LiteLLM, ...). Provider specifics stay out of callers.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// StructuredMode is how JSON output is requested from the model.
type StructuredMode string

const (
	ModeJSONSchema StructuredMode = "json_schema" // native schema enforcement
	ModeJSONObject StructuredMode = "json_object" // valid JSON, schema in prompt
	ModePrompt     StructuredMode = "prompt"      // schema in prompt only
)

var modeOrder = []StructuredMode{ModeJSONSchema, ModeJSONObject, ModePrompt}

// Config is the gateway configuration.
type Config struct {
	BaseURL         string
	APIKey          string
	Timeout         time.Duration
	MaxRetries      int
	MaxOutputTokens int64
	// StartMode is the first structured-output mode tried per model. Models that
	// reject it are downgraded automatically and remembered.
	StartMode StructuredMode
	Prices    map[string]Price
}

// Request is one structured completion.
type Request struct {
	Model      string
	System     string
	User       string
	SchemaName string
	Schema     map[string]any
}

// Response is the model output plus accounting.
type Response struct {
	Content      string
	Model        string
	Mode         StructuredMode
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
	PriceKnown   bool
	Latency      time.Duration
}

// Completer is the interface the classifier depends on.
type Completer interface {
	Complete(ctx context.Context, req Request) (*Response, error)
}

// Client implements Completer on the OpenAI SDK.
type Client struct {
	cfg    Config
	api    openai.Client
	budget Budget

	mu    sync.Mutex
	modes map[string]StructuredMode // per-model learned capability
}

func NewClient(cfg Config, budget Budget, opts ...option.RequestOption) *Client {
	if cfg.StartMode == "" {
		cfg.StartMode = ModeJSONSchema
	}
	if cfg.MaxOutputTokens == 0 {
		cfg.MaxOutputTokens = 4096
	}
	base := []option.RequestOption{
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(cfg.MaxRetries),
	}
	if cfg.Timeout > 0 {
		base = append(base, option.WithRequestTimeout(cfg.Timeout))
	}
	return &Client{cfg: cfg, api: openai.NewClient(append(base, opts...)...), budget: budget, modes: map[string]StructuredMode{}}
}

func (c *Client) modeFor(model string) StructuredMode {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.modes[model]; ok {
		return m
	}
	return c.cfg.StartMode
}

func (c *Client) setMode(model string, m StructuredMode) {
	c.mu.Lock()
	c.modes[model] = m
	c.mu.Unlock()
}

// Complete sends the request, downgrading the structured-output mode if the
// model/provider rejects it (HTTP 400/422), and records token usage and cost.
func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	if c.budget != nil {
		if err := c.budget.Check(); err != nil {
			return nil, err
		}
	}
	mode := c.modeFor(req.Model)
	for {
		start := time.Now()
		resp, err := c.once(ctx, req, mode)
		if err == nil {
			resp.Latency = time.Since(start)
			resp.Mode = mode
			resp.Model = req.Model
			resp.CostUSD, resp.PriceKnown = Cost(c.cfg.Prices, req.Model, resp.InputTokens, resp.OutputTokens)
			if c.budget != nil {
				c.budget.Record(resp.CostUSD)
			}
			c.setMode(req.Model, mode)
			return resp, nil
		}
		next, ok := downgrade(mode)
		if !ok || !isFormatRejection(err) {
			return nil, fmt.Errorf("llm %s (%s): %w", req.Model, mode, err)
		}
		mode = next
	}
}

func downgrade(m StructuredMode) (StructuredMode, bool) {
	for i, x := range modeOrder {
		if x == m && i+1 < len(modeOrder) {
			return modeOrder[i+1], true
		}
	}
	return "", false
}

func isFormatRejection(err error) bool {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusBadRequest || apiErr.StatusCode == http.StatusUnprocessableEntity
	}
	return false
}

func (c *Client) once(ctx context.Context, req Request, mode StructuredMode) (*Response, error) {
	system := req.System
	if mode != ModeJSONSchema {
		// No native enforcement: put the schema in the prompt instead.
		b, _ := json.Marshal(req.Schema)
		system += "\n\nRespond with a single JSON object (no prose, no code fences) that validates against this JSON Schema:\n" + string(b)
	}
	params := openai.ChatCompletionNewParams{
		Model:               req.Model,
		Messages:            []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(system), openai.UserMessage(req.User)},
		MaxCompletionTokens: openai.Int(c.cfg.MaxOutputTokens),
		Temperature:         openai.Float(0),
	}
	switch mode {
	case ModeJSONSchema:
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
				Name: req.SchemaName, Strict: openai.Bool(true), Schema: req.Schema,
			}},
		}
	case ModeJSONObject:
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{OfJSONObject: &shared.ResponseFormatJSONObjectParam{}}
	}
	out, err := c.api.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, errors.New("no choices in response")
	}
	return &Response{
		Content:      out.Choices[0].Message.Content,
		InputTokens:  out.Usage.PromptTokens,
		OutputTokens: out.Usage.CompletionTokens,
	}, nil
}
