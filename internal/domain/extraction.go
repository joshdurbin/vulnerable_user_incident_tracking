package domain

import "time"

// Verdict is the article-level classification.
type Verdict string

const (
	VerdictConfirmed     Verdict = "confirmed"
	VerdictPossible      Verdict = "possible"
	VerdictNonqualifying Verdict = "nonqualifying"
	VerdictInsufficient  Verdict = "insufficient"
)

// ArticleKind says what the article is about in time.
type ArticleKind string

const (
	KindCurrent      ArticleKind = "current_incident"
	KindUpdate       ArticleKind = "update"
	KindHistorical   ArticleKind = "historical"
	KindHypothetical ArticleKind = "hypothetical"
	KindUnrelated    ArticleKind = "unrelated"
)

// Evidence ties a field to a quote from the article. Verified is set by our
// validation (the quote was found in the source text), never by the model.
type Evidence struct {
	Field    string `json:"field"`
	Quote    string `json:"quote"`
	Verified bool   `json:"verified"`
}

// IncidentCandidate is one collision described by one article. Pointer fields
// are nil when the article does not say; unknown is never guessed.
type IncidentCandidate struct {
	OccurredAt          string     `json:"occurred_at,omitempty"` // as extracted: date or RFC3339
	OccurredAtPrecision string     `json:"occurred_at_precision"` // datetime|date|unknown
	Timezone            string     `json:"timezone,omitempty"`
	City                string     `json:"city,omitempty"`
	County              string     `json:"county,omitempty"`
	State               string     `json:"state,omitempty"`
	Country             string     `json:"country,omitempty"`
	StreetAddress       string     `json:"street_address,omitempty"`
	LocationPrecision   string     `json:"location_precision"`
	VRUCategories       []string   `json:"vru_categories"`
	VehicleCategories   []string   `json:"vehicle_categories"`
	InjuredCount        *int       `json:"injured_count,omitempty"`
	FatalityCount       *int       `json:"fatality_count,omitempty"`
	Severity            string     `json:"severity"`
	HitAndRun           string     `json:"hit_and_run"` // yes|no|unknown
	Circumstances       string     `json:"circumstances,omitempty"`
	FactsStatus         string     `json:"facts_status"` // confirmed|alleged|unclear
	Evidence            []Evidence `json:"evidence"`
}

// Usage counts the LLM calls behind one result (the primary model, and the fallback if it was needed).
type Usage struct {
	Calls int `json:"calls"`
}

// ExtractionMeta records what produced a result, for reproducibility.
type ExtractionMeta struct {
	Model          string        `json:"model"`
	PromptVersion  string        `json:"prompt_version"`
	SchemaVersion  string        `json:"schema_version"`
	StructuredMode string        `json:"structured_mode"`
	UsedFallback   bool          `json:"used_fallback"`
	Usage          Usage         `json:"usage"`
	Latency        time.Duration `json:"latency_ns"`
	ExtractedAt    time.Time     `json:"extracted_at"`
}

// ExtractionResult is the validated output of classifying one article.
type ExtractionResult struct {
	Verdict                Verdict             `json:"verdict"`
	ArticleKind            ArticleKind         `json:"article_kind"`
	CollisionOccurred      *bool               `json:"collision_occurred"`
	MotorVehicleInvolved   *bool               `json:"motor_vehicle_involved"`
	VRUInvolved            *bool               `json:"vru_involved"`
	InjuryOrDeath          *bool               `json:"injury_or_death"`
	MultipleIncidents      bool                `json:"multiple_incidents"`
	UpdatesEarlierIncident bool                `json:"updates_earlier_incident"`
	Incidents              []IncidentCandidate `json:"incidents"`
	Warnings               []string            `json:"warnings,omitempty"`
	Meta                   ExtractionMeta      `json:"meta"`
}
