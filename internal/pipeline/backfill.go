package pipeline

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"
)

const (
	backfillStepsPerRun = 40 // Continue-As-New after this many steps keeps history small
	defaultWindow       = 24 * time.Hour
)

// BackfillWorkflow walks a date range one (window, source) step at a time. Each
// step runs discovery and starts article workflows. It can be paused, resumed
// and cancelled with Signals, reports progress through a Query, stops when its
// article budget is spent (a cost bound), and uses Continue-As-New with a
// checkpoint so a multi-year backfill never grows an unbounded history.
func BackfillWorkflow(ctx workflow.Context, in BackfillInput) (*BackfillProgress, error) {
	ctx = WithWorkflowCorrelationID(ctx, in.CorrelationID)
	if in.CorrelationID == "" {
		in.CorrelationID = workflow.GetInfo(ctx).WorkflowExecution.ID
		ctx = WithWorkflowCorrelationID(ctx, in.CorrelationID)
	}
	if in.WindowSize <= 0 {
		in.WindowSize = defaultWindow
	}
	if len(in.Slugs) == 0 || !in.To.After(in.From) {
		return nil, fmt.Errorf("backfill needs at least one source and a non-empty range")
	}
	if in.MaxArticles <= 0 {
		in.MaxArticles = 1000
	}
	windows := int((in.To.Sub(in.From) + in.WindowSize - 1) / in.WindowSize)
	total := windows * len(in.Slugs)

	paused, cancelled := false, false
	p := BackfillProgress{TotalSteps: total, StepsDone: in.Cursor, Started: in.Started, Found: in.Found, MaxArticles: in.MaxArticles, Phase: "running"}
	upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAWorkflowKind, "backfill"))

	if err := workflow.SetQueryHandler(ctx, QueryProgress, func() (BackfillProgress, error) {
		q := p
		q.Paused = paused
		if paused && q.Phase == "running" {
			q.Phase = "paused"
		}
		return q, nil
	}); err != nil {
		return nil, err
	}
	pause, resume, cancel := workflow.GetSignalChannel(ctx, SignalPause), workflow.GetSignalChannel(ctx, SignalResume), workflow.GetSignalChannel(ctx, SignalCancel)
	workflow.Go(ctx, func(ctx workflow.Context) {
		for {
			s := workflow.NewSelector(ctx)
			s.AddReceive(pause, func(c workflow.ReceiveChannel, _ bool) { c.Receive(ctx, nil); paused = true })
			s.AddReceive(resume, func(c workflow.ReceiveChannel, _ bool) { c.Receive(ctx, nil); paused = false })
			s.AddReceive(cancel, func(c workflow.ReceiveChannel, _ bool) { c.Receive(ctx, nil); cancelled = true })
			s.Select(ctx)
		}
	})

	stepsThisRun := 0
	for p.StepsDone < total {
		if err := workflow.Await(ctx, func() bool { return !paused || cancelled }); err != nil {
			return nil, err
		}
		if cancelled {
			p.Phase = "cancelled"
			return &p, nil
		}
		if p.Started >= in.MaxArticles {
			p.Phase = "limit_reached"
			return &p, nil
		}
		if stepsThisRun >= backfillStepsPerRun || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
			in.Cursor, in.Started, in.Found = p.StepsDone, p.Started, p.Found
			return nil, workflow.NewContinueAsNewError(ctx, BackfillWorkflow, in)
		}

		// Slugs are the inner loop so each window finishes across all sources before moving on.
		wi, si := p.StepsDone/len(in.Slugs), p.StepsDone%len(in.Slugs)
		slug := in.Slugs[si]
		since := in.From.Add(time.Duration(wi) * in.WindowSize)
		until := since.Add(in.WindowSize)
		if until.After(in.To) {
			until = in.To
		}
		p.Current = fmt.Sprintf("%s %s..%s", slug, since.Format("2006-01-02"), until.Format("2006-01-02"))

		var out DiscoverOutput
		remaining := in.MaxArticles - p.Started
		err := workflow.ExecuteActivity(discoveryCtx(ctx), act.DiscoverSource,
			DiscoverInput{Slug: slug, Since: since, Until: until, MaxURL: remaining}).Get(ctx, &out)
		if err != nil {
			// One failed step (e.g. a provider that stayed rate-limited) is logged and skipped;
			// the same window can be re-run later and dedupe makes that safe.
			workflow.GetLogger(ctx).Warn("backfill step failed", "step", p.Current, "error", err)
		} else {
			p.Found += out.Found
			for _, ref := range out.ToProcess {
				if p.Started >= in.MaxArticles {
					break
				}
				started, err := startArticle(ctx, ref, slug, in.CorrelationID, in.Options)
				if err != nil {
					return &p, fmt.Errorf("start article workflow: %w", err)
				}
				if started {
					p.Started++
				}
			}
		}
		p.StepsDone++
		stepsThisRun++
	}
	p.Phase, p.Current = "done", ""
	return &p, nil
}
