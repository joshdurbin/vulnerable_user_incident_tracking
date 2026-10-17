package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/joshdurbin/fuck_cars/internal/classification"
	"github.com/joshdurbin/fuck_cars/internal/eval"
)

func evalCmd(ctx context.Context, args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(errw)
	dir := fs.String("cases", "tests/evaluations/cases", "directory of labeled cases")
	replay := fs.Bool("replay", false, "use each case's author-written synthetic_response instead of calling a model (tests the harness only)")
	asJSON := fs.Bool("json", false, "print JSON report")
	lo := registerLLMFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cases, err := eval.Load(*dir)
	if err != nil {
		return err
	}
	var build eval.Builder
	if *replay {
		build = func(c eval.Case) (*classification.Classifier, error) {
			return classification.New(eval.Replay{Body: string(c.SyntheticResponse)}, classification.Config{PrimaryModel: "replay"})
		}
	} else {
		if *lo.model == "" {
			return errors.New("a model is required: --model or LLM_MODEL (or use --replay)")
		}
		comp, err := lo.completer()
		if err != nil {
			return err
		}
		build = func(eval.Case) (*classification.Classifier, error) {
			return classification.New(comp, classification.Config{PrimaryModel: *lo.model, FallbackModel: *lo.fallback})
		}
	}
	rep, err := eval.Run(ctx, cases, build)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	printReport(out, rep, *replay)
	return nil
}

func printReport(w io.Writer, r *eval.Report, replay bool) {
	if replay {
		fmt.Fprintln(w, "REPLAY MODE: scores reflect author-written responses, NOT any model's quality.")
	}
	if r.Synthetic {
		fmt.Fprintln(w, "NOTE: cases are synthetic (invented articles); replace/extend with real labeled articles before trusting scores.")
	}
	fmt.Fprintf(w, "\nCases: %d   errors: %d\n", r.Cases, r.Errors)
	fmt.Fprintf(w, "Classification   precision %.2f   recall %.2f   (tp %d fp %d fn %d tn %d)\n", r.Precision, r.Recall, r.TP, r.FP, r.FN, r.TN)
	fmt.Fprintf(w, "Article kind     %d/%d\n", r.KindAccuracy.Correct, r.KindAccuracy.Total)
	fmt.Fprintf(w, "Incident recall  %d/%d   spurious incidents: %d\n", r.IncidentRecall.Correct, r.IncidentRecall.Total, r.SpuriousIncidents)
	names := make([]string, 0, len(r.Fields))
	for n := range r.Fields {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := r.Fields[n]
		fmt.Fprintf(w, "  %-20s %d/%d (%.0f%%)\n", n, f.Correct, f.Total, 100*f.Value())
	}
	fmt.Fprintf(w, "Hallucination    %d/%d unverified quotes (%.1f%%)   incidents invented for non-incident articles: %d\n",
		r.UnverifiedQuotes, r.EvidenceQuotes, 100*r.HallucinationRate, r.InventedIncidentCases)
	fmt.Fprintf(w, "Tokens in/out    %d / %d   avg latency %.0f ms\n", r.InputTokens, r.OutputTokens, r.AvgLatencyMS)
	fmt.Fprintf(w, "Cost             $%.5f total   $%.5f/article   $%.5f/correct incident\n", r.CostUSD, r.CostPerArticleUSD, r.CostPerCorrectIncidentUSD)
	fmt.Fprintln(w, "\nPer case:")
	for _, c := range r.Results {
		status := "PASS"
		if !c.Pass {
			status = "FAIL"
		}
		fmt.Fprintf(w, "  %s  %-22s %s\n", status, c.ID, c.Scenario)
		if c.Error != "" {
			fmt.Fprintf(w, "        error: %s\n", c.Error)
		}
		for _, d := range c.Diffs {
			fmt.Fprintf(w, "        - %s\n", d)
		}
	}
}
