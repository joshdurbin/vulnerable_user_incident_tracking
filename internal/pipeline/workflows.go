package pipeline

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// act is a typed handle used to reference activities by method.
var act *Activities

const defaultMaxArticles = 200

// Activity option presets, one per task queue.
func crawlCtx(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: TQCrawl, StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 5 * time.Second, BackoffCoefficient: 2, MaximumInterval: 2 * time.Minute, MaximumAttempts: 5,
			NonRetryableErrorTypes: []string{ErrTypeNotFound},
		},
	})
}

func llmCtx(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: TQLLM, StartToCloseTimeout: 3 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 10 * time.Second, BackoffCoefficient: 2, MaximumInterval: 2 * time.Minute, MaximumAttempts: 4,
			NonRetryableErrorTypes: []string{ErrTypeBudget, ErrTypeInvalidOutput, ErrTypeNotFound},
		},
	})
}

func dbCtx(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: TQDB, StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: 30 * time.Second, MaximumAttempts: 6,
			NonRetryableErrorTypes: []string{ErrTypeNotFound, ErrTypeInvalidOutput},
		},
	})
}

func discoveryCtx(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: TQDiscovery, StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 30 * time.Second, BackoffCoefficient: 2, MaximumInterval: 10 * time.Minute, MaximumAttempts: 4,
			NonRetryableErrorTypes: []string{ErrTypeNotFound},
		},
	})
}

func hasErrType(err error, typ string) bool {
	var ae *temporal.ApplicationError
	return errors.As(err, &ae) && ae.Type() == typ
}

func upsertAttrs(ctx workflow.Context, on bool, updates ...temporal.SearchAttributeUpdate) {
	if !on || len(updates) == 0 {
		return
	}
	// Failing to set a search attribute must never fail the work itself.
	_ = workflow.UpsertTypedSearchAttributes(ctx, updates...)
}

func kw(name, v string) temporal.SearchAttributeUpdate {
	return temporal.NewSearchAttributeKeyKeyword(name).ValueSet(v)
}

// startArticle starts an ArticleWorkflow for a URL without waiting for it. The
// workflow ID derives from the URL, so a URL found by several sources or runs
// is processed once; "already started" is success. Children are abandoned so
// the parent can finish (or Continue-As-New) while articles are still running.
func startArticle(ctx workflow.Context, ref URLRef, source, cid string, opts Options) (started bool, err error) {
	cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID:            ArticleWorkflowID(ref.URL),
		TaskQueue:             TQWorkflow,
		ParentClosePolicy:     enumspb.PARENT_CLOSE_POLICY_ABANDON,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	})
	f := workflow.ExecuteChildWorkflow(cctx, ArticleWorkflow, ArticleInput{URL: ref, SourceSlug: source, CorrelationID: cid, Options: opts})
	var exec workflow.Execution
	if err := f.GetChildWorkflowExecution().Get(ctx, &exec); err != nil {
		if temporal.IsWorkflowExecutionAlreadyStartedError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ---- ArticleWorkflow ----

// ArticleWorkflow takes one discovered URL from fetch to resolved incidents.
// History holds IDs and small summaries only; text lives in PostgreSQL.
func ArticleWorkflow(ctx workflow.Context, in ArticleInput) (*ArticleResult, error) {
	ctx = WithWorkflowCorrelationID(ctx, in.CorrelationID)
	res := &ArticleResult{URL: in.URL.URL}
	upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAArticleURL, in.URL.URL), kw(SASource, in.SourceSlug), kw(SAStage, "fetch"), kw(SAWorkflowKind, "article"))

	var fetched FetchResult
	if err := workflow.ExecuteActivity(crawlCtx(ctx), act.FetchArticle, in.URL).Get(ctx, &fetched); err != nil {
		upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAStage, "fetch_failed"))
		return nil, fmt.Errorf("fetch %s: %w", in.URL.URL, err)
	}
	res.ArticleID = fetched.ArticleID
	if fetched.Status != "fetched" {
		res.Status = "not_fetched"
		upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAStage, "not_fetched"), kw(SAVerdict, fetched.Status))
		return res, nil
	}

	upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAStage, "classify"))
	var cls ClassifyResult
	if err := workflow.ExecuteActivity(llmCtx(ctx), act.Classify, ClassifyInput{ArticleID: fetched.ArticleID}).Get(ctx, &cls); err != nil {
		switch {
		case hasErrType(err, ErrTypeBudget):
			res.Status = "budget_exceeded"
			upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAStage, "budget_exceeded"))
			return res, nil // not a failure: reprocess later when budget allows
		case hasErrType(err, ErrTypeInvalidOutput):
			res.Status = "invalid_output"
			upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAStage, "invalid_output"))
			return res, nil
		}
		return nil, fmt.Errorf("classify: %w", err)
	}
	res.Verdict, res.Reused = cls.Verdict, cls.Reused
	upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAVerdict, cls.Verdict), kw(SAModel, cls.Model))
	if cls.Incidents == 0 || cls.Verdict == "nonqualifying" || cls.Verdict == "insufficient" {
		res.Status = "nonqualifying"
		upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAStage, "done"))
		return res, nil
	}

	upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAStage, "resolve"))
	ids, err := resolveCandidates(ctx, fetched.ArticleID, cls, in.Options)
	if err != nil {
		return nil, err
	}
	res.Incidents, res.Status = ids, "done"
	upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAStage, "done"))
	return res, nil
}

// resolveCandidates geocodes, resolves and announces each candidate of an extraction.
func resolveCandidates(ctx workflow.Context, articleID uuid.UUID, cls ClassifyResult, opts Options) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for i := 0; i < cls.Incidents; i++ {
		var geo GeoPoint
		if err := workflow.ExecuteActivity(dbCtx(ctx), act.Geocode, GeocodeInput{ExtractionID: cls.ExtractionID, Index: i}).Get(ctx, &geo); err != nil {
			return out, fmt.Errorf("geocode candidate %d: %w", i, err)
		}
		var r ResolveOutput
		if err := workflow.ExecuteActivity(dbCtx(ctx), act.ResolveIncident,
			ResolveInput{ArticleID: articleID, ExtractionID: cls.ExtractionID, Index: i, Geo: geo}).Get(ctx, &r); err != nil {
			return out, fmt.Errorf("resolve candidate %d: %w", i, err)
		}
		out = append(out, r.IncidentID)
		if err := workflow.ExecuteActivity(dbCtx(ctx), act.NotifyIncident,
			NotifyInput{IncidentID: r.IncidentID, ArticleID: articleID, Outcome: r.Outcome, Options: opts}).Get(ctx, nil); err != nil {
			return out, fmt.Errorf("notify incident %s: %w", r.IncidentID, err)
		}
	}
	return out, nil
}

// ---- DiscoveryWorkflow ----

// DiscoveryWorkflow runs one source once. A Temporal Schedule starts it on the
// source's poll interval; BackfillWorkflow starts it for historical windows.
func DiscoveryWorkflow(ctx workflow.Context, in DiscoveryInput) (*DiscoveryResult, error) {
	ctx = WithWorkflowCorrelationID(ctx, in.CorrelationID)
	if in.CorrelationID == "" {
		in.CorrelationID = workflow.GetInfo(ctx).WorkflowExecution.ID
		ctx = WithWorkflowCorrelationID(ctx, in.CorrelationID)
	}
	upsertAttrs(ctx, in.Options.SearchAttributes, kw(SASource, in.Slug), kw(SAWorkflowKind, "discovery"))
	max := in.MaxArticles
	if max <= 0 {
		max = defaultMaxArticles
	}
	var out DiscoverOutput
	if err := workflow.ExecuteActivity(discoveryCtx(ctx), act.DiscoverSource,
		DiscoverInput{Slug: in.Slug, Since: in.Since, Until: in.Until, MaxURL: max * 2}).Get(ctx, &out); err != nil {
		return nil, fmt.Errorf("discover %s: %w", in.Slug, err)
	}
	res := &DiscoveryResult{Slug: in.Slug, Found: out.Found, New: out.New, Pending: out.Pending, Truncated: out.Truncated}
	for i, ref := range out.ToProcess {
		if res.Started >= max {
			res.Deferred = len(out.ToProcess) - i // not lost: they have no article yet, so the next run lists them as pending
			break
		}
		started, err := startArticle(ctx, ref, in.Slug, in.CorrelationID, in.Options)
		if err != nil {
			return res, fmt.Errorf("start article workflow: %w", err)
		}
		if started {
			res.Started++
		} else {
			res.Skipped++
		}
	}
	return res, nil
}
