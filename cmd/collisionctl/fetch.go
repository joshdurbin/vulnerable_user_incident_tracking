package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/domain"
)

func newFetchCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fetch <url>",
		Short: "Fetch and extract an article through the crawler worker (needs Temporal and the Python worker)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			render, _ := cmd.Flags().GetString("render")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			full, _ := cmd.Flags().GetBool("text")
			asJSON, _ := cmd.Flags().GetBool("json")

			policy := domain.RenderingPolicy(render)
			switch policy {
			case domain.RenderHTTPOnly, domain.RenderAuto, domain.RenderAlways:
			default:
				return fmt.Errorf("invalid --render %q (http_only|auto|always)", render)
			}
			f, closeF, err := a.articleFetcher()
			if err != nil {
				return err
			}
			defer closeF()
			cid := uuid.NewString()
			a.log.Debug().Str("correlation_id", cid).Str("url", args[0]).Msg("fetching")
			art, err := f.FetchArticle(cmd.Context(), domain.FetchRequest{
				URL: args[0], Rendering: policy, Timeout: timeout, CorrelationID: cid, SourceID: "collisionctl",
			})
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(a.out, art)
			}
			printArticle(a.out, art, full)
			if art.ExtractionStatus != domain.ExtractionOK {
				return fmt.Errorf("extraction status %s: %s", art.ExtractionStatus, art.ErrorDetail)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.String("render", "http_only", "rendering policy: http_only|auto|always")
	f.Duration("timeout", 30*time.Second, "fetch timeout")
	f.Bool("text", false, "print the full article text in human output")
	addTemporalFlags(cmd)
	return cmd
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printArticle(w io.Writer, a *domain.Article, full bool) {
	fmt.Fprintf(w, "Status:     %s (http %d)\n", a.ExtractionStatus, a.HTTPStatus)
	fmt.Fprintf(w, "Title:      %s\n", a.Title)
	fmt.Fprintf(w, "Publisher:  %s\n", a.Publisher)
	if a.Author != "" {
		fmt.Fprintf(w, "Author:     %s\n", a.Author)
	}
	if a.PublishedAt != nil {
		fmt.Fprintf(w, "Published:  %s\n", a.PublishedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(w, "Final URL:  %s\n", a.FinalURL)
	if a.CanonicalURL != "" {
		fmt.Fprintf(w, "Canonical:  %s\n", a.CanonicalURL)
	}
	fmt.Fprintf(w, "Language:   %s  Rendered: %v  Chars: %d\n", a.Language, a.Rendered, len(a.Text))
	if a.ErrorDetail != "" {
		fmt.Fprintf(w, "Detail:     %s\n", a.ErrorDetail)
	}
	text := a.Text
	if !full && len(text) > 600 {
		text = text[:600] + "…"
	}
	if text != "" {
		fmt.Fprintf(w, "\n%s\n", text)
	}
}
