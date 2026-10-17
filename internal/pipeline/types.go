package pipeline

import (
	"time"

	"github.com/google/uuid"
)

// Options toggle cluster-dependent features. They travel in workflow inputs so
// behavior is fixed per execution (workflows must be deterministic on replay).
type Options struct {
	// SearchAttributes enables upserting custom search attributes. Register them on
	// the namespace first, otherwise workflow tasks fail.
	SearchAttributes bool `json:"search_attributes,omitempty"`
}

// URLRef identifies a discovered URL to process.
type URLRef struct {
	ID    uuid.UUID `json:"id"`
	URL   string    `json:"url"`
	Title string    `json:"title,omitempty"`
}

// ---- discovery ----

type DiscoverInput struct {
	Slug   string    `json:"slug"`
	Since  time.Time `json:"since,omitempty"`
	Until  time.Time `json:"until,omitempty"`
	MaxURL int       `json:"max_urls,omitempty"` // cap on URLs returned to the workflow
}

type DiscoverOutput struct {
	ToProcess []URLRef `json:"to_process"` // new URLs plus known URLs that never became articles
	Found     int      `json:"found"`
	Unique    int      `json:"unique"`
	New       int      `json:"new"`
	Pending   int      `json:"pending"`
	Truncated bool     `json:"truncated"`
	Warnings  []string `json:"warnings,omitempty"`
}

type DiscoveryInput struct {
	Slug          string    `json:"slug"`
	Since         time.Time `json:"since,omitempty"`
	Until         time.Time `json:"until,omitempty"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	Options       Options   `json:"options,omitempty"`
	// MaxArticles bounds how many article workflows one run starts (cost control). 0 = default.
	MaxArticles int `json:"max_articles,omitempty"`
}

type DiscoveryResult struct {
	Slug      string `json:"slug"`
	Found     int    `json:"found"`
	New       int    `json:"new"`
	Pending   int    `json:"pending"`
	Started   int    `json:"started"`
	Skipped   int    `json:"skipped"`  // article workflow already running / done
	Deferred  int    `json:"deferred"` // beyond MaxArticles; picked up by the next run as pending
	Truncated bool   `json:"truncated"`
}

// ---- article ----

type ArticleInput struct {
	URL           URLRef  `json:"url"`
	SourceSlug    string  `json:"source_slug,omitempty"`
	CorrelationID string  `json:"correlation_id,omitempty"`
	Options       Options `json:"options,omitempty"`
}

type FetchResult struct {
	ArticleID uuid.UUID `json:"article_id"`
	Status    string    `json:"status"` // fetched|no_content|blocked|failed
	Reused    bool      `json:"reused"`
	Detail    string    `json:"detail,omitempty"`
}

type ClassifyInput struct {
	ArticleID uuid.UUID `json:"article_id"`
}

type ClassifyResult struct {
	ExtractionID uuid.UUID `json:"extraction_id"`
	Verdict      string    `json:"verdict"`
	Incidents    int       `json:"incidents"`
	Reused       bool      `json:"reused"` // an extraction for these versions already existed: no LLM call was made
	CostUSD      float64   `json:"cost_usd"`
	Model        string    `json:"model"`
}

type GeoPoint struct {
	Lat        *float64 `json:"lat,omitempty"`
	Lon        *float64 `json:"lon,omitempty"`
	Source     string   `json:"source,omitempty"`
	Confidence float32  `json:"confidence,omitempty"`
	Precision  string   `json:"precision,omitempty"`
}

type GeocodeInput struct {
	ExtractionID uuid.UUID `json:"extraction_id"`
	Index        int       `json:"index"`
}

type ResolveInput struct {
	ArticleID    uuid.UUID `json:"article_id"`
	ExtractionID uuid.UUID `json:"extraction_id"`
	Index        int       `json:"index"`
	Geo          GeoPoint  `json:"geo"`
}

type ResolveOutput struct {
	IncidentID uuid.UUID   `json:"incident_id"`
	Outcome    string      `json:"outcome"`
	Score      float64     `json:"score"`
	ReviewIDs  []uuid.UUID `json:"review_ids,omitempty"`
}

type NotifyInput struct {
	IncidentID uuid.UUID `json:"incident_id"`
	ArticleID  uuid.UUID `json:"article_id"`
	Outcome    string    `json:"outcome"`
	Options    Options   `json:"options,omitempty"`
}

type ArticleResult struct {
	URL       string      `json:"url"`
	ArticleID uuid.UUID   `json:"article_id,omitempty"`
	Status    string      `json:"status"` // done|not_fetched|nonqualifying|budget_exceeded|invalid_output
	Verdict   string      `json:"verdict,omitempty"`
	Incidents []uuid.UUID `json:"incidents,omitempty"`
	Reused    bool        `json:"reused,omitempty"`
}

// ---- incident ----

type IncidentInput struct {
	IncidentID string  `json:"incident_id"`
	Options    Options `json:"options,omitempty"`
	// Carried across Continue-As-New.
	ArticlesLinked int       `json:"articles_linked,omitempty"`
	FollowUpsDone  int       `json:"follow_ups_done,omitempty"`
	FirstLinkedAt  time.Time `json:"first_linked_at,omitempty"`

	// Tunables; zero means the defaults (follow-ups at 48h and 7d, retire after 30d idle).
	FollowUpDelays []time.Duration `json:"follow_up_delays,omitempty"`
	IdleTimeout    time.Duration   `json:"idle_timeout,omitempty"`
}

type ArticleLinked struct {
	ArticleID uuid.UUID `json:"article_id"`
	Outcome   string    `json:"outcome"`
}

type IncidentStatus struct {
	IncidentID     string `json:"incident_id"`
	ArticlesLinked int    `json:"articles_linked"`
	FollowUpsDone  int    `json:"follow_ups_done"`
	PendingReviews int    `json:"pending_reviews"`
	Phase          string `json:"phase"` // waiting|following_up|idle
	NextFollowUp   string `json:"next_follow_up,omitempty"`
}

type ReviewDecision struct {
	ReviewID uuid.UUID `json:"review_id"`
	Approve  bool      `json:"approve"`
	Actor    string    `json:"actor"`
}

type ReviewOutcome struct {
	ReviewID uuid.UUID `json:"review_id"`
	Approved bool      `json:"approved"`
	Target   uuid.UUID `json:"target,omitempty"`
}

type FollowUpInput struct {
	IncidentID string `json:"incident_id"`
	Attempt    int    `json:"attempt"`
}

type FollowUpOutput struct {
	Checked   bool     `json:"checked"`
	Reason    string   `json:"reason,omitempty"`
	ToProcess []URLRef `json:"to_process,omitempty"`
}

// ---- backfill ----

type BackfillInput struct {
	Slugs         []string      `json:"slugs"`
	From          time.Time     `json:"from"`
	To            time.Time     `json:"to"`
	WindowSize    time.Duration `json:"window_size"`
	MaxArticles   int           `json:"max_articles"` // total article workflows to start across the backfill (cost bound)
	CorrelationID string        `json:"correlation_id,omitempty"`
	Options       Options       `json:"options,omitempty"`

	// Checkpoint, carried across Continue-As-New.
	Cursor      int `json:"cursor,omitempty"`  // index into the flattened (window, slug) list
	Started     int `json:"started,omitempty"` // article workflows started so far
	Found       int `json:"found,omitempty"`
	WindowsDone int `json:"windows_done,omitempty"`
}

type BackfillProgress struct {
	TotalSteps  int    `json:"total_steps"`
	StepsDone   int    `json:"steps_done"`
	Started     int    `json:"articles_started"`
	Found       int    `json:"found"`
	MaxArticles int    `json:"max_articles"`
	Paused      bool   `json:"paused"`
	Phase       string `json:"phase"` // running|paused|cancelled|budget_reached|done
	Current     string `json:"current,omitempty"`
}

// ---- reprocess ----

type ReprocessInput struct {
	Limit         int       `json:"limit"` // max articles in this reprocess run
	BatchSize     int       `json:"batch_size"`
	MaxConcurrent int       `json:"max_concurrent"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	Options       Options   `json:"options,omitempty"`
	After         uuid.UUID `json:"after,omitempty"` // cursor (Continue-As-New)
	Done          int       `json:"done,omitempty"`
	Changed       int       `json:"changed,omitempty"`
	Failed        int       `json:"failed,omitempty"`
}

type ReprocessBatchInput struct {
	After uuid.UUID `json:"after"`
	Limit int       `json:"limit"`
}

type ReprocessArticleInput struct {
	ArticleID     uuid.UUID `json:"article_id"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	Options       Options   `json:"options,omitempty"`
}

type ExtractionDiff struct {
	HadPrevious       bool     `json:"had_previous"`
	PreviousModel     string   `json:"previous_model,omitempty"`
	PreviousVerdict   string   `json:"previous_verdict,omitempty"`
	NewVerdict        string   `json:"new_verdict,omitempty"`
	PreviousIncidents int      `json:"previous_incidents"`
	NewIncidents      int      `json:"new_incidents"`
	Changed           bool     `json:"changed"`
	Details           []string `json:"details,omitempty"`
}

type ReprocessArticleResult struct {
	ArticleID  uuid.UUID      `json:"article_id"`
	Diff       ExtractionDiff `json:"diff"`
	Superseded []uuid.UUID    `json:"superseded_incidents,omitempty"`
	Incidents  []uuid.UUID    `json:"incidents,omitempty"`
}

type ReprocessResult struct {
	Examined int `json:"examined"`
	Changed  int `json:"changed"`
	Failed   int `json:"failed"`
}
