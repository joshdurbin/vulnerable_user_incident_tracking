// collisionctl is the administrative and debugging CLI.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/joshdurbin/fuck_cars/internal/crawler"
	"github.com/joshdurbin/fuck_cars/internal/domain"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, errw io.Writer) error {
	if len(args) == 0 {
		usage(errw)
		return errors.New("missing command")
	}
	switch args[0] {
	case "fetch":
		return fetchCmd(ctx, args[1:], out, errw, nil)
	case "help", "-h", "--help":
		usage(out)
		return nil
	default:
		usage(errw)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `collisionctl - collision tracker admin CLI

Commands:
  fetch <url>   Fetch and extract an article through the crawler service

Global flags (per command): --json for machine-readable output.
`)
}

// fetchCmd is split from run so tests can inject a Fetcher.
func fetchCmd(ctx context.Context, args []string, out, errw io.Writer, f crawler.Fetcher) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	fs.SetOutput(errw)
	addr := fs.String("crawler", envOr("COLLISION_CRAWLER_ADDR", "localhost:9090"), "crawler gRPC address")
	render := fs.String("render", "http_only", "rendering policy: http_only|auto|always")
	timeout := fs.Duration("timeout", 30*time.Second, "fetch timeout")
	asJSON := fs.Bool("json", false, "print JSON")
	showText := fs.Bool("text", false, "print full article text in human output")
	// Allow the URL before or after flags.
	var url string
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		url, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if url == "" && fs.NArg() > 0 {
		url = fs.Arg(0)
	}
	if url == "" {
		return errors.New("usage: collisionctl fetch <url> [--json] [--render auto]")
	}
	policy := domain.RenderingPolicy(*render)
	switch policy {
	case domain.RenderHTTPOnly, domain.RenderAuto, domain.RenderAlways:
	default:
		return fmt.Errorf("invalid --render %q", *render)
	}
	if f == nil {
		c, err := crawler.Dial(*addr)
		if err != nil {
			return err
		}
		defer c.Close()
		f = c
	}
	cid := uuid.NewString()
	ctx = crawler.WithCorrelationID(ctx, cid)
	art, err := f.FetchArticle(ctx, domain.FetchRequest{URL: url, Rendering: policy, Timeout: *timeout, CorrelationID: cid, SourceID: "collisionctl"})
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(art)
	}
	printHuman(out, art, *showText)
	if art.ExtractionStatus != domain.ExtractionOK {
		return fmt.Errorf("extraction status %s: %s", art.ExtractionStatus, art.ErrorDetail)
	}
	return nil
}

func printHuman(w io.Writer, a *domain.Article, full bool) {
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

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
