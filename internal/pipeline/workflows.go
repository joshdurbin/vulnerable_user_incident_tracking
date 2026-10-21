package pipeline

import (
	"errors"
	"fmt"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/dedupe"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/domain"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/crawler"
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
			NonRetryableErrorTypes: []string{ErrTypeNotFound, "InvalidArgument"},
		},
	})
}

// renderCtx targets the render pool. ScheduleToStart is bounded so that an absent or
// saturated pool degrades to the plain-HTTP result instead of parking articles forever.
func renderCtx(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: TQRender, StartToCloseTimeout: 90 * time.Second, ScheduleToStartTimeout: 3 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 10 * time.Second, BackoffCoefficient: 2, MaximumInterval: time.Minute, MaximumAttempts: 3,
			NonRetryableErrorTypes: []string{"InvalidArgument"},
		},
	})
}

func llmCtx(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: TQLLM, StartToCloseTimeout: 3 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 10 * time.Second, BackoffCoefficient: 2, MaximumInterval: 2 * time.Minute, MaximumAttempts: 4,
			NonRetryableErrorTypes: []string{ErrTypeInvalidOutput, ErrTypeNotFound},
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

// maxKeyword keeps values well under the server's limit for Keyword search attributes; an
// oversized value would make the workflow task fail.
const maxKeyword = 255

func truncateKeyword(v string) string {
	if r := []rune(v); len(r) > maxKeyword {
		return string(r[:maxKeyword])
	}
	return v
}

func kw(name, v string) temporal.SearchAttributeUpdate {
	v = truncateKeyword(v)
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
	if err := workflow.ExecuteActivity(dbCtx(ctx), act.FindArticle, in.URL).Get(ctx, &fetched); err != nil {
		return nil, fmt.Errorf("find article: %w", err)
	}
	if fetched.ArticleID == uuid.Nil {
		// The Python worker serves fetch_article; this call is by name across languages.
		policy := string(domain.RenderHTTPOnly)
		if in.Options.Render {
			policy = string(domain.RenderAuto)
		}
		page, err := fetchWithRendering(ctx, crawler.FetchArticleRequest{
			URL: in.URL.URL, Rendering: policy, SourceID: in.SourceSlug, TimeoutS: 45, CorrelationID: in.CorrelationID,
		})
		if err != nil {
			upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAStage, "fetch_failed"))
			return nil, fmt.Errorf("fetch %s: %w", in.URL.URL, err)
		}
		if err := workflow.ExecuteActivity(dbCtx(ctx), act.StoreArticle, StoreInput{Ref: in.URL, Response: *page}).Get(ctx, &fetched); err != nil {
			return nil, fmt.Errorf("store article: %w", err)
		}
	}
	res.ArticleID = fetched.ArticleID
	if fetched.Status != "fetched" {
		res.Status = "not_fetched"
		upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAStage, "not_fetched"), kw(SAVerdict, fetched.Status))
		return res, nil
	}

	if fetched.DuplicateKind == dedupe.KindContentHash {
		// Same text as an article already classified: the model would say the same thing.
		res.Status = "duplicate"
		upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAStage, "duplicate"))
		return res, nil
	}

	upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAStage, "classify"))
	var cls ClassifyResult
	if err := workflow.ExecuteActivity(llmCtx(ctx), act.Classify, ClassifyInput{ArticleID: fetched.ArticleID}).Get(ctx, &cls); err != nil {
		switch {
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

// SourceTestWorkflow tries one source configuration (typically an unsaved one from the admin
// UI) through the discovery worker, so the API itself never makes outbound requests. It does
// not retry: the person is waiting, and a failure is shown to them.
func SourceTestWorkflow(ctx workflow.Context, in SourceTestInput) (*SourceTestResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: TQDiscovery, StartToCloseTimeout: testSourceTimeout + 10*time.Second,
		ScheduleToStartTimeout: 20 * time.Second, // no worker polling: fail fast
		RetryPolicy:            &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	var out SourceTestResult
	if err := workflow.ExecuteActivity(ctx, act.TestSource, in).Get(ctx, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RetentionWorkflow enforces the article text retention period and trims old discovery run
// history. A daily Schedule runs it. Its result is how many article bodies were removed; a
// failure to prune history or to fingerprint older articles is logged and does not fail the run.
func RetentionWorkflow(ctx workflow.Context) (int64, error) {
	var n int64
	if err := workflow.ExecuteActivity(dbCtx(ctx), act.ExpireArticleText).Get(ctx, &n); err != nil {
		return n, err
	}
	var pruned int64
	if err := workflow.ExecuteActivity(dbCtx(ctx), act.PruneDiscoveryRuns).Get(ctx, &pruned); err != nil {
		workflow.GetLogger(ctx).Warn("pruning discovery run history failed", "error", err)
	}
	var fingerprinted int
	if err := workflow.ExecuteActivity(dbCtx(ctx), act.FingerprintArticles).Get(ctx, &fingerprinted); err != nil {
		workflow.GetLogger(ctx).Warn("fingerprinting older articles failed", "error", err)
	}
	return n, nil
}

// ---- wrappers for callers that are not workflows ----

// CrawlFetchWorkflow runs one fetch_article activity. The CLI and other
// non-workflow callers use it through crawler.Client.
func CrawlFetchWorkflow(ctx workflow.Context, req crawler.FetchArticleRequest) (*crawler.ArticleResponse, error) {
	return fetchWithRendering(ctx, req)
}

// fetchWithRendering fetches an article honouring the rendering policy in req:
//   - http_only: the crawl pool's plain HTTP fetch;
//   - auto: HTTP first, and only when it finds no article text, the render pool. A render
//     that fails or finds nothing falls back to the HTTP result, so rendering can only help;
//   - always: the render pool alone, and its failure is the fetch's failure.
//
// The two steps are separate activities on separate task queues, so each is visible,
// retried and timed out on its own in the Temporal UI.
func fetchWithRendering(ctx workflow.Context, req crawler.FetchArticleRequest) (*crawler.ArticleResponse, error) {
	policy := domain.RenderingPolicy(req.Rendering)
	if policy == domain.RenderAlways {
		var out crawler.ArticleResponse
		if err := workflow.ExecuteActivity(renderCtx(ctx), crawler.ActivityRenderArticle, req).Get(ctx, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	httpReq := req
	httpReq.Rendering = string(domain.RenderHTTPOnly)
	var out crawler.ArticleResponse
	if err := workflow.ExecuteActivity(crawlCtx(ctx), crawler.ActivityFetchArticle, httpReq).Get(ctx, &out); err != nil {
		return nil, err
	}
	if policy != domain.RenderAuto || out.ExtractionStatus != crawler.StatusNoContent {
		return &out, nil
	}
	var rendered crawler.ArticleResponse
	if err := workflow.ExecuteActivity(renderCtx(ctx), crawler.ActivityRenderArticle, req).Get(ctx, &rendered); err != nil {
		workflow.GetLogger(ctx).Warn("render failed; keeping the HTTP result", "url", req.URL, "error", err)
		return &out, nil
	}
	if rendered.ExtractionStatus == crawler.StatusOK {
		return &rendered, nil
	}
	return &out, nil
}

// CrawlLinksWorkflow runs one discover_links activity (used by crawl-backed discovery providers).
func CrawlLinksWorkflow(ctx workflow.Context, req crawler.DiscoverLinksRequest) (*crawler.DiscoverLinksResponse, error) {
	ao := workflow.ActivityOptions{
		TaskQueue: TQCrawl, StartToCloseTimeout: time.Duration(req.MaxDurationS+30) * time.Second,
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 2, NonRetryableErrorTypes: []string{"InvalidArgument"}},
	}
	var out crawler.DiscoverLinksResponse
	if err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, ao), crawler.ActivityDiscoverLinks, req).Get(ctx, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
