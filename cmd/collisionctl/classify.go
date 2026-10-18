package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/classification"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/domain"
)

func newClassifyCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "classify [url]",
		Short: "Classify an article and extract incident candidates via the LLM gateway",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")
			asJSON, _ := cmd.Flags().GetBool("json")
			if len(args) == 0 && file == "" {
				return errors.New("provide a <url> or --file")
			}
			if a.cfg.LLM.Model == "" {
				return errors.New("a model is required: --model or COLLISION_LLM_MODEL")
			}
			url := ""
			if len(args) > 0 {
				url = args[0]
			}
			art, err := a.loadArticle(cmd, url, file)
			if err != nil {
				return err
			}
			if art.ExtractionStatus != "" && art.ExtractionStatus != domain.ExtractionOK {
				return fmt.Errorf("article not usable: %s %s", art.ExtractionStatus, art.ErrorDetail)
			}
			cl, err := classification.New(a.completer(), classification.Config{
				PrimaryModel: a.cfg.LLM.Model, FallbackModel: a.cfg.LLM.FallbackModel,
			})
			if err != nil {
				return err
			}
			res, err := cl.Classify(cmd.Context(), art)
			if err != nil {
				return err
			}
			a.log.Info().Str("model", res.Meta.Model).Str("verdict", string(res.Verdict)).
				Int("incidents", len(res.Incidents)).Msg("classified")
			if asJSON {
				return writeJSON(a.out, res)
			}
			printResult(a.out, art, res)
			return nil
		},
	}
	f := cmd.Flags()
	f.String("file", "", "classify a saved article: JSON from `fetch --json`, or plain text")
	addLLMFlags(cmd)
	addTemporalFlags(cmd)
	return cmd
}

func (a *app) loadArticle(cmd *cobra.Command, url, file string) (*domain.Article, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var art domain.Article
		if json.Unmarshal(b, &art) == nil && art.Text != "" {
			return &art, nil
		}
		return &domain.Article{FinalURL: file, Text: string(b)}, nil // plain text
	}
	f, closeF, err := a.articleFetcher()
	if err != nil {
		return nil, err
	}
	defer closeF()
	cid := uuid.NewString()
	return f.FetchArticle(cmd.Context(), domain.FetchRequest{
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
	fmt.Fprintf(w, "\n\nmodel=%s prompt=%s schema=%s mode=%s fallback=%v latency=%s\n",
		m.Model, m.PromptVersion, m.SchemaVersion, m.StructuredMode, m.UsedFallback, m.Latency.Round(time.Millisecond))
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
