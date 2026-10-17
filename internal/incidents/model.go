// Package incidents resolves extracted incident candidates into canonical
// incidents: matching, merging, history and review. Observations (one per
// article per candidate) are the source of truth; an incident's canonical
// fields are always derived by folding its observations.
package incidents

import (
	"time"

	"github.com/joshdurbin/fuck_cars/internal/domain"
)

// Observation is one article's view of an incident, stored verbatim.
type Observation struct {
	Candidate         domain.IncidentCandidate `json:"candidate"`
	ArticleKind       domain.ArticleKind       `json:"article_kind"`
	Publisher         string                   `json:"publisher,omitempty"`
	ObservedAt        time.Time                `json:"observed_at"`
	Lat               *float64                 `json:"lat,omitempty"` // nil unless a geocoder returned a point
	Lon               *float64                 `json:"lon,omitempty"`
	GeocodeSource     string                   `json:"geocode_source,omitempty"`
	GeocodeConfidence *float32                 `json:"geocode_confidence,omitempty"`
	GeocodePrecision  string                   `json:"geocode_precision,omitempty"`
}

// State is the canonical (derived) view of an incident.
type State struct {
	OccurredOn        *time.Time // date only, UTC midnight
	OccurredAt        *time.Time
	OccurredPrecision string
	Timezone          string
	City, County      string
	State             string
	StreetAddress     string
	LocationPrecision string
	Lat, Lon          *float64
	GeocodeSource     string
	GeocodeConfidence *float32
	GeocodePrecision  string
	VRU, Vehicles     []string
	Injured, Fatal    *int
	Severity          string
	HitAndRun         string
	Circumstances     string
	FactsStatus       string
}

// Outcome of resolving one candidate.
type Outcome string

const (
	OutcomeCreated   Outcome = "created"   // no plausible match: new incident
	OutcomeMatched   Outcome = "matched"   // confidently attached to an existing incident
	OutcomeReview    Outcome = "review"    // new incident created, possible duplicates queued for a human
	OutcomeDuplicate Outcome = "duplicate" // this exact (extraction, candidate) was already resolved
)

// Match describes how a candidate compared with one existing incident.
type Match struct {
	IncidentID string             `json:"incident_id"`
	Score      float64            `json:"score"`
	Decision   Decision           `json:"decision"`
	Parts      map[string]float64 `json:"parts"`
	Notes      []string           `json:"notes,omitempty"`
}

type Decision string

const (
	DecisionAuto     Decision = "auto_match"
	DecisionReview   Decision = "possible_match"
	DecisionDistinct Decision = "distinct"
)
