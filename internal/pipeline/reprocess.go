package pipeline

import (
	"fmt"

	"github.com/google/uuid"
	"go.temporal.io/sdk/workflow"
)

const reprocessBatchesPerRun = 20

// ReprocessWorkflow re-runs classification for retained articles that lack an
// extraction for the current model, prompt and schema (for example after a
// prompt change). It pages through articles with a cursor, bounds concurrency,
// and Continue-As-New's with a checkpoint.
func ReprocessWorkflow(ctx workflow.Context, in ReprocessInput) (*ReprocessResult, error) {
	ctx = WithWorkflowCorrelationID(ctx, in.CorrelationID)
	if in.CorrelationID == "" {
		in.CorrelationID = workflow.GetInfo(ctx).WorkflowExecution.ID
		ctx = WithWorkflowCorrelationID(ctx, in.CorrelationID)
	}
	if in.BatchSize <= 0 {
		in.BatchSize = 20
	}
	if in.MaxConcurrent <= 0 {
		in.MaxConcurrent = 4
	}
	upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAWorkflowKind, "reprocess"))
	p := ReprocessResult{Examined: in.Done, Changed: in.Changed, Failed: in.Failed}
	if err := workflow.SetQueryHandler(ctx, QueryProgress, func() (ReprocessResult, error) { return p, nil }); err != nil {
		return nil, err
	}

	for batches := 0; ; batches++ {
		if in.Limit > 0 && p.Examined >= in.Limit {
			break
		}
		if batches >= reprocessBatchesPerRun || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
			in.Done, in.Changed, in.Failed = p.Examined, p.Changed, p.Failed
			return nil, workflow.NewContinueAsNewError(ctx, ReprocessWorkflow, in)
		}
		size := in.BatchSize
		if in.Limit > 0 && in.Limit-p.Examined < size {
			size = in.Limit - p.Examined
		}
		var ids []uuid.UUID
		if err := workflow.ExecuteActivity(dbCtx(ctx), act.ListReprocessBatch, ReprocessBatchInput{After: in.After, Limit: size}).Get(ctx, &ids); err != nil {
			return &p, fmt.Errorf("list batch: %w", err)
		}
		if len(ids) == 0 {
			break
		}
		for lo := 0; lo < len(ids); lo += in.MaxConcurrent {
			hi := min(lo+in.MaxConcurrent, len(ids))
			futs := make([]workflow.ChildWorkflowFuture, 0, hi-lo)
			for _, id := range ids[lo:hi] {
				cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
					WorkflowID: "reprocess-" + id.String() + "-" + workflow.GetInfo(ctx).WorkflowExecution.ID[:min(8, len(workflow.GetInfo(ctx).WorkflowExecution.ID))],
					TaskQueue:  TQWorkflow,
				})
				futs = append(futs, workflow.ExecuteChildWorkflow(cctx, ReprocessArticleWorkflow,
					ReprocessArticleInput{ArticleID: id, CorrelationID: in.CorrelationID, Options: in.Options}))
			}
			for _, f := range futs {
				var r ReprocessArticleResult
				p.Examined++
				if err := f.Get(ctx, &r); err != nil {
					p.Failed++
					workflow.GetLogger(ctx).Warn("reprocess article failed", "error", err)
					continue
				}
				if r.Diff.Changed {
					p.Changed++
				}
			}
		}
		in.After = ids[len(ids)-1]
	}
	return &p, nil
}

// ReprocessArticleWorkflow re-classifies one article, compares the result with
// the previous extraction, replaces what the old one contributed, and resolves
// the new candidates. Steps are idempotent, so retries and replays are safe.
func ReprocessArticleWorkflow(ctx workflow.Context, in ReprocessArticleInput) (*ReprocessArticleResult, error) {
	ctx = WithWorkflowCorrelationID(ctx, in.CorrelationID)
	res := &ReprocessArticleResult{ArticleID: in.ArticleID}

	var prev ClassifyResult
	if err := workflow.ExecuteActivity(dbCtx(ctx), act.PreviousExtraction, in.ArticleID).Get(ctx, &prev); err != nil {
		return nil, err
	}
	var cls ClassifyResult
	if err := workflow.ExecuteActivity(llmCtx(ctx), act.Classify, ClassifyInput{ArticleID: in.ArticleID}).Get(ctx, &cls); err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}
	if err := workflow.ExecuteActivity(dbCtx(ctx), act.CompareExtractions, prev.ExtractionID, cls.ExtractionID).Get(ctx, &res.Diff); err != nil {
		return nil, err
	}
	if prev.ExtractionID != uuid.Nil && prev.ExtractionID != cls.ExtractionID {
		if err := workflow.ExecuteActivity(dbCtx(ctx), act.SupersedeExtraction, prev.ExtractionID).Get(ctx, &res.Superseded); err != nil {
			return nil, fmt.Errorf("supersede: %w", err)
		}
	}
	if cls.Incidents > 0 && cls.Verdict != "nonqualifying" && cls.Verdict != "insufficient" {
		ids, err := resolveCandidates(ctx, in.ArticleID, cls, in.Options)
		res.Incidents = ids
		if err != nil {
			return res, err
		}
	}
	return res, nil
}
