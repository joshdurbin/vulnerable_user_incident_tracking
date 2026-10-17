package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshdurbin/fuck_cars/internal/classification"
	"github.com/joshdurbin/fuck_cars/internal/crawler"
	"github.com/joshdurbin/fuck_cars/internal/domain"
	"github.com/joshdurbin/fuck_cars/internal/llm"
)

// classifyDeps lets tests inject a fetcher and an LLM.
type classifyDeps struct {
	fetcher   crawler.Fetcher
	completer llm.Completer
}

func classifyCmd(ctx context.Context, args []string, out, errw io.Writer, deps classifyDeps) error {
	fs := flag.NewFlagSet("classify", flag.ContinueOnError)
	fs.SetOutput(errw)
	addr := fs.String("crawler", envOr("COLLISION_CRAWLER_ADDR", "localhost:9090"), "crawler gRPC address (URL input)")
	file := fs.String("file", "", "classify a saved article: JSON from `fetch --json`, or plain text")
	asJSON := fs.Bool("json", false, "print JSON")
	base := fs.String("llm-url", envOr("LLM_BASE_URL", "http://bifrost.bifrost.svc.cluster.local:8080/v1"), "OpenAI-compatible gateway base URL")
	model := fs.String("model", envOr("LLM_MODEL", ""), "primary model (env LLM_MODEL)")
	fallback := fs.String("fallback-model", envOr("LLM_FALLBACK_MODEL", ""), "fallback model (env LLM_FALLBACK_MODEL)")
	mode := fs.String("structured-mode", envOr("LLM_STRUCTURED_MODE", "json_schema"), "json_schema|json_object|prompt")
	timeout := fs.Duration("timeout", 120*time.Second, "LLM request timeout")
	prices := fs.String("prices", envOr("LLM_PRICES", ""), `JSON {"model":{"input_per_m":0.2,"output_per_m":0.6}}`)
	budget := fs.Float64("budget-usd", envFloat("LLM_BUDGET_USD", 0), "spend limit for this run (0 = unlimited)")

	var url string
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		url, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if url == "" && *file == "" {
		return errors.New("usage: collisionctl classify <url> | --file article.json|txt  [--model M] [--json]")
	}
	if *model == "" {
		return errors.New("a model is required: --model or LLM_MODEL")
	}

	art, err := loadArticle(ctx, url, *file, *addr, deps.fetcher)
	if err != nil {
		return err
	}
	if art.ExtractionStatus != "" && art.ExtractionStatus != domain.ExtractionOK {
		return fmt.Errorf("article not usable: %s %s", art.ExtractionStatus, art.ErrorDetail)
	}

	completer := deps.completer
	if completer == nil {
		pm := map[string]llm.Price{}
		if *prices != "" {
			if err := json.Unmarshal([]byte(*prices), &pm); err != nil {
				return fmt.Errorf("invalid --prices: %w", err)
			}
		}
		completer = llm.NewClient(llm.Config{
			BaseURL: *base, APIKey: envOr("LLM_API_KEY", "unused"), Timeout: *timeout, MaxRetries: 2,
			StartMode: llm.StructuredMode(*mode), Prices: pm,
		}, llm.NewMemoryBudget(*budget))
	}
	cl, err := classification.New(completer, classification.Config{PrimaryModel: *model, FallbackModel: *fallback})
	if err != nil {
		return err
	}
	res, err := cl.Classify(ctx, art)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	printResult(out, art, res)
	return nil
}

func loadArticle(ctx context.Context, url, file, addr string, f crawler.Fetcher) (*domain.Article, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var a domain.Article
		if json.Unmarshal(b, &a) == nil && a.Text != "" {
			return &a, nil
		}
		return &domain.Article{FinalURL: file, Text: string(b)}, nil // plain text
	}
	if f == nil {
		c, err := crawler.Dial(addr)
		if err != nil {
			return nil, err
		}
		f = c
	}
	cid := uuid.NewString()
	return f.FetchArticle(crawler.WithCorrelationID(ctx, cid), domain.FetchRequest{
		URL: url, Rendering: domain.RenderHTTPOnly, Timeout: 30 * time.Second, CorrelationID: cid, SourceID: "collisionctl",
	})
}

func printResult(w io.Writer, a *domain.Article, r *domain.ExtractionResult) {
	fmt.Fprintf(w, "Article:  %s\n", firstNonEmpty(a.Title, a.FinalURL))
	fmt.Fprintf(w, "Verdict:  %s   Kind: %s   Incidents: %d\n", r.Verdict, r.ArticleKind, len(r.Incidents))
	for i, in := range r.Incidents {
		fmt.Fprintf(w, "\n#%d  %s  [%s]\n", i+1, firstNonEmpty(in.OccurredAt, "date unknown"), in.OccurredAtPrecision)
		fmt.Fprintf(w, "    Where:    %s, %s, %s  (%s: %s)\n", in.City, in.County, in.State, in.LocationPrecision, in.StreetAddress)
		fmt.Fprintf(w, "    Who:      %s  vs  %s\n", strings.Join(in.VRUCategories, ","), strings.Join(in.VehicleCategories, ","))
		fmt.Fprintf(w, "    Outcome:  severity=%s injured=%s fatalities=%s hit_and_run=%s facts=%s\n",
			in.Severity, intp(in.InjuredCount), intp(in.FatalityCount), in.HitAndRun, in.FactsStatus)
		for _, e := range in.Evidence {
			mark := "✓"
			if !e.Verified {
				mark = "✗ UNVERIFIED"
			}
			fmt.Fprintf(w, "    %s %s: %q\n", mark, e.Field, e.Quote)
		}
	}
	for _, wn := range r.Warnings {
		fmt.Fprintf(w, "\nwarning: %s", wn)
	}
	m := r.Meta
	fmt.Fprintf(w, "\n\nmodel=%s prompt=%s schema=%s mode=%s fallback=%v tokens=%d/%d cost=$%.5f latency=%s\n",
		m.Model, m.PromptVersion, m.SchemaVersion, m.StructuredMode, m.UsedFallback,
		m.Usage.InputTokens, m.Usage.OutputTokens, m.Usage.CostUSD, m.Latency.Round(time.Millisecond))
}

func intp(p *int) string {
	if p == nil {
		return "?"
	}
	return strconv.Itoa(*p)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return v
	}
	return def
}
