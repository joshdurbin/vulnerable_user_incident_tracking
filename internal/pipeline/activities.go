package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/dedupe"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/domain"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/incidents"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage/dbgen"
)

// Application error types. Workflows match on these in retry policies.
const (
	ErrTypeInvalidOutput = "InvalidOutput"
	ErrTypeNotFound      = "NotFound"
	ErrTypeNotPending    = "ReviewNotPending"
)

// Narrow interfaces keep activities testable without a network, a database
// full of fixtures, or an LLM.
type (
	Discoverer interface {
		Run(ctx context.Context, rr discovery.RunRequest) (*discovery.Report, error)
		Source(ctx context.Context, slug string) (discovery.Source, error)
	}
	Classifier interface {
		Classify(ctx context.Context, a *domain.Article) (*domain.ExtractionResult, error)
		Versions() (model, promptVersion, schemaVersion string)
	}
	Geocoder interface {
		Geocode(ctx context.Context, c domain.IncidentCandidate) (GeoPoint, error)
	}
	IncidentResolver interface {
		Resolve(ctx context.Context, in incidents.Input) (*incidents.Result, error)
		ApproveReview(ctx context.Context, reviewID uuid.UUID, actor string) (uuid.UUID, error)
		RejectReview(ctx context.Context, reviewID uuid.UUID, actor string) error
		SupersedeExtraction(ctx context.Context, extractionID uuid.UUID, actor string) ([]uuid.UUID, error)
		Refresh(ctx context.Context, id uuid.UUID, actor string) error
	}
)

// NoopGeocoder returns no coordinates. Incidents still resolve on street, city
// and county text; coordinates arrive when a geocoder is configured.
type NoopGeocoder struct{}

func (NoopGeocoder) Geocode(context.Context, domain.IncidentCandidate) (GeoPoint, error) {
	return GeoPoint{}, nil
}

// Activities holds the dependencies of every activity. Register one instance
// per worker; the method names are the activity names.
type Activities struct {
	DB         *storage.Store
	Discovery  Discoverer
	Classifier Classifier
	Geocoder   Geocoder
	Resolver   IncidentResolver
	Temporal   client.Client // for SignalWithStart from NotifyIncident
	Log        zerolog.Logger

	TextRetention  time.Duration // how long article text is kept (0 = 90 days)
	FollowUpSource string        // search source used for follow-up queries (default googlenews-us)
	Now            func() time.Time
}

func (a *Activities) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Activities) logger(ctx context.Context) zerolog.Logger {
	l := a.Log
	if id := CorrelationID(ctx); id != "" {
		l = l.With().Str("correlation_id", id).Logger()
	}
	if activity.IsActivity(ctx) {
		info := activity.GetInfo(ctx)
		l = l.With().Str("workflow_id", info.WorkflowExecution.ID).Str("activity", info.ActivityType.Name).Logger()
	}
	return l
}

// ---- discovery ----

// DiscoverSource runs one provider and persists what it finds. The output is
// small: URL references only, capped at MaxURL so workflow history stays bounded.
func (a *Activities) DiscoverSource(ctx context.Context, in DiscoverInput) (*DiscoverOutput, error) {
	src, err := a.Discovery.Source(ctx, in.Slug)
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeNotFound, err)
	}
	w := discovery.Window{Since: in.Since, Until: in.Until}
	if w.Since.IsZero() || w.Until.IsZero() {
		w = discovery.DefaultWindow(src, a.now())
	}
	rep, err := a.Discovery.Run(ctx, discovery.RunRequest{Source: src, Window: w})
	if err != nil {
		var rl *discovery.RateLimitedError
		if errors.As(err, &rl) {
			// Let Temporal wait out the provider's own back-off rather than retrying early.
			return nil, temporal.NewApplicationErrorWithOptions(err.Error(), "RateLimited",
				temporal.ApplicationErrorOptions{NextRetryDelay: rl.RetryAfter, Cause: err})
		}
		return nil, err
	}
	out := &DiscoverOutput{Found: rep.Found, Unique: rep.Unique, New: len(rep.New), Pending: len(rep.Pending), Warnings: rep.Warnings, Truncated: rep.Truncated}
	for _, group := range [][]discovery.NewURL{rep.New, rep.Pending} {
		for _, n := range group {
			if in.MaxURL > 0 && len(out.ToProcess) >= in.MaxURL {
				out.Truncated = true
				break
			}
			out.ToProcess = append(out.ToProcess, URLRef{ID: n.ID, URL: n.URL, Title: n.Title})
		}
	}
	return out, nil
}

// testSourceTimeout bounds one test run; the workflow's activity timeout is a little longer.
const testSourceTimeout = 40 * time.Second

// testSourceMaxQueries is how many configured queries a test run of a search source uses.
const testSourceMaxQueries = 2

// TestSource runs a source once without saving it or recording anything, and reports what it found.
func (a *Activities) TestSource(ctx context.Context, in SourceTestInput) (*SourceTestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, testSourceTimeout)
	defer cancel()
	rep, err := a.Discovery.Run(ctx, discovery.RunRequest{Source: in.Source, DryRun: true, MaxQueries: testSourceMaxQueries})
	out := &SourceTestResult{OK: err == nil, Warnings: []string{}, Samples: []TestSample{}}
	if rep != nil {
		out.Found, out.Unique, out.Unresolved, out.Requests, out.Truncated = rep.Found, rep.Unique, rep.Unresolved, rep.Requests, rep.Truncated
		out.DurationMS = rep.Duration.Milliseconds()
		if rep.Warnings != nil {
			out.Warnings = rep.Warnings
		}
		for i, c := range rep.Candidates {
			if i >= MaxTestSamples {
				break
			}
			out.Samples = append(out.Samples, TestSample{URL: c.Normalized, Title: c.Primary.Title, PublishedAt: c.Primary.PublishedAt})
		}
	}
	if err != nil {
		out.Error = err.Error()
	}
	return out, nil
}

// ---- article ----

// FindArticle returns the article already stored for a discovered URL, if any.
// The workflow calls it before paying for a fetch, which makes fetching
// idempotent across retries, duplicate discoveries and reprocessing.
func (a *Activities) FindArticle(ctx context.Context, ref URLRef) (*FetchResult, error) {
	existing, err := a.DB.Queries().GetArticleByDiscoveredURL(ctx, &ref.ID)
	if err != nil {
		if storage.IsNoRows(err) {
			return &FetchResult{}, nil
		}
		return nil, err
	}
	return fetchResult(existing, true), nil
}

// fetchResult summarizes a stored article for the workflow.
func fetchResult(a dbgen.Article, reused bool) *FetchResult {
	r := &FetchResult{ArticleID: a.ID, Status: a.Status, Reused: reused}
	if a.DuplicateOf != nil && a.DuplicateKind != nil {
		r.DuplicateOf, r.DuplicateKind = *a.DuplicateOf, *a.DuplicateKind
	}
	return r
}

// StoreArticle persists what the Python crawler returned, keeping the text for
// the retention period, and records the fetch attempt. Idempotent per URL.
func (a *Activities) StoreArticle(ctx context.Context, in StoreInput) (*FetchResult, error) {
	q := a.DB.Queries()
	ref, art := in.Ref, in.Response.ToArticle()
	cid := CorrelationID(ctx)
	status := articleStatus(art.ExtractionStatus)

	retention := a.TextRetention
	if retention == 0 {
		retention = 90 * 24 * time.Hour
	}
	p := dbgen.InsertArticleParams{
		DiscoveredUrlID: &ref.ID, RequestedUrl: nonEmpty(art.RequestedURL, ref.URL), FinalUrl: nonEmpty(art.FinalURL, ref.URL),
		CanonicalUrl: sp(art.CanonicalURL), Title: sp(art.Title), Publisher: sp(art.Publisher), Author: sp(art.Author),
		PublishedAt: art.PublishedAt, Language: sp(art.Language), Description: sp(art.Description),
		Status: status, FetchedAt: nonZero(art.FetchedAt, a.now()),
	}
	if status == "fetched" {
		exp := a.now().Add(retention)
		sum := sha256.Sum256([]byte(art.Text))
		h := hex.EncodeToString(sum[:])
		p.BodyText, p.TextExpiresAt, p.ContentHash = &art.Text, &exp, &h
		if key := dedupe.CanonicalKey(art.CanonicalURL, p.FinalUrl); key != "" {
			p.CanonicalKey = &key
		}
		if fp, ok := dedupe.Fingerprint(art.Text); ok {
			s := int64(fp)
			p.Simhash = &s
		}
	}
	var res *FetchResult
	err := a.DB.InTx(ctx, func(tq *dbgen.Queries) error {
		// Mark a copy of an article we already hold. It is still stored, so its URL is never fetched
		// again, but corroboration counts it with the original.
		if status == "fetched" {
			if err := a.markDuplicate(ctx, tq, &p); err != nil {
				return err
			}
		}
		row, err := tq.InsertArticle(ctx, p)
		if err != nil {
			return err
		}
		hs := int32(art.HTTPStatus)
		if err := tq.InsertFetchAttempt(ctx, dbgen.InsertFetchAttemptParams{
			DiscoveredUrlID: ref.ID, ArticleID: &row.ID, HttpStatus: &hs, Rendered: art.Rendered,
			ExtractionStatus: string(art.ExtractionStatus), ErrorDetail: sp(art.ErrorDetail), CorrelationID: sp(cid),
		}); err != nil {
			return err
		}
		res = fetchResult(row, false)
		res.Detail = art.ErrorDetail
		return nil
	})
	if err != nil {
		// A concurrent attempt may have inserted the same article: return it.
		if existing, qerr := q.GetArticleByDiscoveredURL(ctx, &ref.ID); qerr == nil {
			return fetchResult(existing, true), nil
		}
		return nil, err
	}
	if res.DuplicateKind != "" {
		a.Log.Info().Stringer("article", res.ArticleID).Stringer("duplicate_of", res.DuplicateOf).
			Str("kind", res.DuplicateKind).Msg("article repeats an earlier one")
	}
	return res, nil
}

// markDuplicate sets p's duplicate fields when an earlier article has the same text or canonical URL,
// or a near-identical text within dedupe.Window. A lookup error fails the store (and is retried)
// rather than recording a duplicate as an original.
func (a *Activities) markDuplicate(ctx context.Context, tq *dbgen.Queries, p *dbgen.InsertArticleParams) error {
	match, err := tq.FindDuplicateArticle(ctx, dbgen.FindDuplicateArticleParams{
		ContentHash: deref(p.ContentHash), CanonicalKey: deref(p.CanonicalKey), Simhash: p.Simhash,
		Since: p.FetchedAt.Add(-dedupe.Window), MaxDistance: dedupe.MaxDistance,
	})
	if err != nil {
		if storage.IsNoRows(err) {
			return nil
		}
		return err
	}
	p.DuplicateOf, p.DuplicateKind = &match.RootID, &match.Kind
	return nil
}

func articleStatus(s domain.ExtractionStatus) string {
	switch s {
	case domain.ExtractionOK:
		return "fetched"
	case domain.ExtractionNoContent, domain.ExtractionUnsupported:
		return "no_content"
	case domain.ExtractionBlocked, domain.ExtractionRobotsDisallowed:
		return "blocked"
	default:
		return "failed"
	}
}

// Classify extracts incident candidates from an article. If an extraction for
// the same model, prompt and schema already exists it is returned unchanged, so
// a retried activity (or a replayed workflow) never pays for the model twice.
func (a *Activities) Classify(ctx context.Context, in ClassifyInput) (*ClassifyResult, error) {
	q := a.DB.Queries()
	model, pv, sv := a.Classifier.Versions()
	if ex, err := q.GetExtractionByVersion(ctx, dbgen.GetExtractionByVersionParams{ArticleID: in.ArticleID, Model: model, PromptVersion: pv, SchemaVersion: sv}); err == nil {
		return extractionSummary(ex, true)
	} else if !storage.IsNoRows(err) {
		return nil, err
	}
	row, err := q.GetArticle(ctx, in.ArticleID)
	if err != nil {
		return nil, err
	}
	if row.BodyText == nil || strings.TrimSpace(*row.BodyText) == "" {
		return nil, temporal.NewNonRetryableApplicationError("article has no retained text", ErrTypeNotFound, nil)
	}
	art := &domain.Article{
		RequestedURL: row.RequestedUrl, FinalURL: row.FinalUrl, Title: deref(row.Title), Publisher: deref(row.Publisher),
		PublishedAt: row.PublishedAt, Text: *row.BodyText, Language: deref(row.Language),
	}
	res, err := a.Classifier.Classify(ctx, art)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Invalid output was already retried on the fallback model inside Classify; a
		// bounded Temporal retry covers transient gateway failures.
		return nil, err
	}
	resJSON, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	mode := res.Meta.StructuredMode
	warnings := res.Warnings
	if warnings == nil {
		warnings = []string{} // the column is NOT NULL
	}
	ex, err := q.UpsertExtraction(ctx, dbgen.UpsertExtractionParams{
		ArticleID: in.ArticleID, Model: res.Meta.Model, PromptVersion: res.Meta.PromptVersion, SchemaVersion: res.Meta.SchemaVersion,
		StructuredMode: &mode, Verdict: string(res.Verdict), ArticleKind: string(res.ArticleKind), Result: resJSON,
		Warnings: warnings, LatencyMs: int32(res.Meta.Latency.Milliseconds()), UsedFallback: res.Meta.UsedFallback,
	})
	if err != nil {
		return nil, err
	}
	return &ClassifyResult{
		ExtractionID: ex.ID, Verdict: string(res.Verdict), Incidents: len(res.Incidents),
		Reused: !ex.Inserted, Model: res.Meta.Model,
	}, nil
}

func extractionSummary(ex dbgen.ArticleExtraction, reused bool) (*ClassifyResult, error) {
	var r domain.ExtractionResult
	if err := json.Unmarshal(ex.Result, &r); err != nil {
		return nil, temporal.NewNonRetryableApplicationError("stored extraction is unreadable: "+err.Error(), ErrTypeInvalidOutput, err)
	}
	return &ClassifyResult{ExtractionID: ex.ID, Verdict: ex.Verdict, Incidents: len(r.Incidents), Reused: reused, Model: ex.Model}, nil
}

// Geocode locates one candidate. Failure to geocode is not an error: the
// incident resolves on text and gains coordinates later.
func (a *Activities) Geocode(ctx context.Context, in GeocodeInput) (GeoPoint, error) {
	cand, _, err := a.candidate(ctx, in.ExtractionID, in.Index)
	if err != nil {
		return GeoPoint{}, err
	}
	g, err := a.Geocoder.Geocode(ctx, cand)
	if err != nil {
		lg := a.logger(ctx)
		lg.Warn().Err(err).Msg("geocode failed; resolving without coordinates")
		return GeoPoint{}, nil
	}
	return g, nil
}

func (a *Activities) candidate(ctx context.Context, extractionID uuid.UUID, idx int) (domain.IncidentCandidate, *domain.ExtractionResult, error) {
	ex, err := a.DB.Queries().GetExtraction(ctx, extractionID)
	if err != nil {
		return domain.IncidentCandidate{}, nil, err
	}
	var r domain.ExtractionResult
	if err := json.Unmarshal(ex.Result, &r); err != nil {
		return domain.IncidentCandidate{}, nil, temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeInvalidOutput, err)
	}
	if idx < 0 || idx >= len(r.Incidents) {
		return domain.IncidentCandidate{}, nil, temporal.NewNonRetryableApplicationError(fmt.Sprintf("candidate %d out of range", idx), ErrTypeNotFound, nil)
	}
	return r.Incidents[idx], &r, nil
}

// ResolveIncident turns one extracted candidate into an observation on a
// canonical incident. The resolver serializes per state in PostgreSQL, and is
// idempotent per (extraction, candidate), so retries are safe.
func (a *Activities) ResolveIncident(ctx context.Context, in ResolveInput) (*ResolveOutput, error) {
	cand, res, err := a.candidate(ctx, in.ExtractionID, in.Index)
	if err != nil {
		return nil, err
	}
	art, err := a.DB.Queries().GetArticle(ctx, in.ArticleID)
	if err != nil {
		return nil, err
	}
	obs := incidents.Observation{
		Candidate: cand, ArticleKind: res.ArticleKind, Publisher: deref(art.Publisher), ObservedAt: art.FetchedAt,
		Lat: in.Geo.Lat, Lon: in.Geo.Lon, GeocodeSource: in.Geo.Source, GeocodePrecision: in.Geo.Precision,
	}
	if in.Geo.Lat != nil {
		c := in.Geo.Confidence
		obs.GeocodeConfidence = &c
	}
	r, err := a.Resolver.Resolve(ctx, incidents.Input{ArticleID: in.ArticleID, ExtractionID: in.ExtractionID, Index: in.Index, Obs: obs})
	if err != nil {
		return nil, err
	}
	return &ResolveOutput{IncidentID: r.IncidentID, Outcome: string(r.Outcome), Score: r.Score, ReviewIDs: r.ReviewIDs}, nil
}

// NotifyIncident tells the incident's long-lived workflow that an article was
// linked, starting the workflow if this is the incident's first article.
// SignalWithStart makes the call idempotent and race-free.
func (a *Activities) NotifyIncident(ctx context.Context, in NotifyInput) error {
	if a.Temporal == nil {
		return nil
	}
	id := in.IncidentID.String()
	_, err := a.Temporal.SignalWithStartWorkflow(ctx, IncidentWorkflowID(id), SignalArticleLinked,
		ArticleLinked{ArticleID: in.ArticleID, Outcome: in.Outcome},
		client.StartWorkflowOptions{ID: IncidentWorkflowID(id), TaskQueue: TQWorkflow},
		"IncidentWorkflow", IncidentInput{IncidentID: id, Options: in.Options})
	return err
}

// ---- incident lifecycle ----

// FollowUp looks for later coverage of an incident (typically an injury that
// became a fatality) with a targeted search, and returns any new URLs.
func (a *Activities) FollowUp(ctx context.Context, in FollowUpInput) (*FollowUpOutput, error) {
	id, err := uuid.Parse(in.IncidentID)
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeNotFound, err)
	}
	inc, err := a.DB.Queries().GetIncident(ctx, id)
	if err != nil {
		return nil, err
	}
	if inc.VerificationStatus == "withdrawn" || inc.MergedInto != nil {
		return &FollowUpOutput{Reason: "incident is " + inc.VerificationStatus}, nil
	}
	if inc.FatalityCount != nil && *inc.FatalityCount > 0 {
		return &FollowUpOutput{Reason: "fatality already recorded"}, nil
	}
	if inc.City == nil || *inc.City == "" {
		return &FollowUpOutput{Reason: "no city to search for"}, nil
	}
	slug := a.FollowUpSource
	if slug == "" {
		slug = "googlenews-us"
	}
	src, err := a.Discovery.Source(ctx, slug)
	if err != nil {
		return &FollowUpOutput{Reason: "follow-up source unavailable: " + err.Error()}, nil
	}
	who := "pedestrian OR cyclist"
	if len(inc.VruCategories) > 0 {
		who = strings.Join(inc.VruCategories, " OR ")
	}
	text := fmt.Sprintf(`"%s" %s (died OR dies OR killed OR "succumbed")`, *inc.City, who)
	until := a.now()
	since := until.AddDate(0, 0, -14)
	rep, err := a.Discovery.Run(ctx, discovery.RunRequest{
		Source: src, Window: discovery.Window{Since: since, Until: until},
		AdHocQueries: []discovery.Query{{Family: "follow-up", Name: "follow-up-" + id.String()[:8], Text: text}},
	})
	if err != nil {
		var rl *discovery.RateLimitedError
		if errors.As(err, &rl) {
			return nil, temporal.NewApplicationErrorWithOptions(err.Error(), "RateLimited",
				temporal.ApplicationErrorOptions{NextRetryDelay: rl.RetryAfter, Cause: err})
		}
		return nil, err
	}
	out := &FollowUpOutput{Checked: true}
	for _, group := range [][]discovery.NewURL{rep.New, rep.Pending} {
		for _, n := range group {
			out.ToProcess = append(out.ToProcess, URLRef{ID: n.ID, URL: n.URL, Title: n.Title})
		}
	}
	return out, nil
}

// ApproveReview merges the new incident into the candidate it was queued against.
func (a *Activities) ApproveReview(ctx context.Context, d ReviewDecision) (*ReviewOutcome, error) {
	target, err := a.Resolver.ApproveReview(ctx, d.ReviewID, d.Actor)
	if err != nil {
		return nil, err
	}
	return &ReviewOutcome{ReviewID: d.ReviewID, Approved: true, Target: target}, nil
}

func (a *Activities) RejectReview(ctx context.Context, d ReviewDecision) (*ReviewOutcome, error) {
	if err := a.Resolver.RejectReview(ctx, d.ReviewID, d.Actor); err != nil {
		return nil, err
	}
	return &ReviewOutcome{ReviewID: d.ReviewID}, nil
}

// PendingReviews lists the open reviews raised by an incident.
func (a *Activities) PendingReviews(ctx context.Context, incidentID string) ([]uuid.UUID, error) {
	id, err := uuid.Parse(incidentID)
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeNotFound, err)
	}
	rows, err := a.DB.Queries().ListPendingReviewsForIncident(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out, nil
}

// RefreshIncident recomputes an incident's canonical fields from its observations.
func (a *Activities) RefreshIncident(ctx context.Context, incidentID string) error {
	id, err := uuid.Parse(incidentID)
	if err != nil {
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeNotFound, err)
	}
	return a.Resolver.Refresh(ctx, id, "system:workflow")
}

// ---- retention ----

// ExpireArticleText removes article bodies past their retention date and reports how many.
func (a *Activities) ExpireArticleText(ctx context.Context) (int64, error) {
	return a.DB.Queries().ExpireArticleText(ctx)
}

// RunHistoryDays is how long discovery run history is kept.
const RunHistoryDays = 90

// PruneDiscoveryRuns removes discovery run history older than RunHistoryDays and reports how many.
func (a *Activities) PruneDiscoveryRuns(ctx context.Context) (int64, error) {
	return a.DB.Queries().PruneDiscoveryRuns(ctx, a.now().AddDate(0, 0, -RunHistoryDays))
}

// FingerprintArticles gives articles stored before duplicate detection existed their fingerprints, a
// bounded amount per run, so new articles can be matched against them. It reports how many it did.
func (a *Activities) FingerprintArticles(ctx context.Context) (int, error) {
	return FingerprintArticles(ctx, a.DB.Queries(), FingerprintBatchSize, FingerprintPerRun)
}

// ---- reprocess ----

// ListReprocessBatch returns the next batch of articles that lack an extraction
// for the current model, prompt and schema.
func (a *Activities) ListReprocessBatch(ctx context.Context, in ReprocessBatchInput) ([]uuid.UUID, error) {
	model, pv, sv := a.Classifier.Versions()
	return a.DB.Queries().ListArticlesToReprocess(ctx, dbgen.ListArticlesToReprocessParams{
		After: in.After, Model: model, PromptVersion: pv, SchemaVersion: sv, MaxRows: int32(in.Limit),
	})
}

// PreviousExtraction returns the latest extraction an article had before a
// reprocess, or the zero ClassifyResult if there was none.
func (a *Activities) PreviousExtraction(ctx context.Context, articleID uuid.UUID) (*ClassifyResult, error) {
	ex, err := a.DB.Queries().LatestExtraction(ctx, articleID)
	if err != nil {
		if storage.IsNoRows(err) {
			return &ClassifyResult{}, nil
		}
		return nil, err
	}
	return extractionSummary(ex, true)
}

// SupersedeExtraction withdraws what an older extraction contributed so the new one can replace it.
func (a *Activities) SupersedeExtraction(ctx context.Context, extractionID uuid.UUID) ([]uuid.UUID, error) {
	return a.Resolver.SupersedeExtraction(ctx, extractionID, "system:reprocess")
}

// CompareExtractions summarizes how two extractions of one article differ.
func (a *Activities) CompareExtractions(ctx context.Context, prev, next uuid.UUID) (*ExtractionDiff, error) {
	q := a.DB.Queries()
	var pr, nr domain.ExtractionResult
	d := &ExtractionDiff{}
	if prev != uuid.Nil {
		p, err := q.GetExtraction(ctx, prev)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(p.Result, &pr); err != nil {
			return nil, err
		}
		d.HadPrevious, d.PreviousModel, d.PreviousVerdict, d.PreviousIncidents = true, p.Model, p.Verdict, len(pr.Incidents)
	}
	n, err := q.GetExtraction(ctx, next)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(n.Result, &nr); err != nil {
		return nil, err
	}
	d.NewVerdict, d.NewIncidents = n.Verdict, len(nr.Incidents)
	if !d.HadPrevious {
		d.Changed = true
		d.Details = append(d.Details, "no previous extraction")
		return d, nil
	}
	if d.PreviousVerdict != d.NewVerdict {
		d.Changed = true
		d.Details = append(d.Details, fmt.Sprintf("verdict %s -> %s", d.PreviousVerdict, d.NewVerdict))
	}
	if d.PreviousIncidents != d.NewIncidents {
		d.Changed = true
		d.Details = append(d.Details, fmt.Sprintf("incidents %d -> %d", d.PreviousIncidents, d.NewIncidents))
	}
	for i := 0; i < len(pr.Incidents) && i < len(nr.Incidents); i++ {
		if diffs := candidateDiff(pr.Incidents[i], nr.Incidents[i]); len(diffs) > 0 {
			d.Changed = true
			d.Details = append(d.Details, fmt.Sprintf("incident %d: %s", i+1, strings.Join(diffs, ", ")))
		}
	}
	return d, nil
}

func candidateDiff(a, b domain.IncidentCandidate) []string {
	var out []string
	add := func(name string, x, y any) {
		if fmt.Sprint(x) != fmt.Sprint(y) {
			out = append(out, name)
		}
	}
	add("date", a.OccurredAt, b.OccurredAt)
	add("city", a.City, b.City)
	add("street", a.StreetAddress, b.StreetAddress)
	add("injured", ptrStr(a.InjuredCount), ptrStr(b.InjuredCount))
	add("fatalities", ptrStr(a.FatalityCount), ptrStr(b.FatalityCount))
	add("severity", a.Severity, b.Severity)
	add("vru", a.VRUCategories, b.VRUCategories)
	return out
}

func ptrStr(p *int) string {
	if p == nil {
		return "?"
	}
	return fmt.Sprint(*p)
}

// ---- small helpers ----

func sp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func nonZero(t, d time.Time) time.Time {
	if t.IsZero() {
		return d
	}
	return t
}
