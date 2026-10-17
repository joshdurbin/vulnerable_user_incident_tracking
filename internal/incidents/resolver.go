package incidents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/joshdurbin/fuck_cars/internal/storage"
	"github.com/joshdurbin/fuck_cars/internal/storage/dbgen"
)

// Config tunes candidate retrieval and thresholds.
type Config struct {
	Thresholds Thresholds
	WindowDays int     // candidate date window either side
	RadiusM    float64 // candidate search radius when coordinates exist
}

func DefaultConfig() Config {
	return Config{Thresholds: DefaultThresholds, WindowDays: 3, RadiusM: 2000}
}

type Resolver struct {
	store *storage.Store
	cfg   Config
	log   zerolog.Logger
}

func NewResolver(s *storage.Store, cfg Config, log zerolog.Logger) *Resolver {
	if cfg.WindowDays == 0 || cfg.RadiusM == 0 {
		d := DefaultConfig()
		if cfg.WindowDays == 0 {
			cfg.WindowDays = d.WindowDays
		}
		if cfg.RadiusM == 0 {
			cfg.RadiusM = d.RadiusM
		}
	}
	if cfg.Thresholds.Auto == 0 {
		cfg.Thresholds = DefaultThresholds
	}
	return &Resolver{store: s, cfg: cfg, log: log}
}

// Input is one candidate from one extraction.
type Input struct {
	ArticleID    uuid.UUID
	ExtractionID uuid.UUID
	Index        int // candidate position within the extraction
	Obs          Observation
}

type Result struct {
	IncidentID uuid.UUID
	Outcome    Outcome
	Score      float64
	ReviewIDs  []uuid.UUID
	Considered []Match
}

// ErrNotPending is returned when a review was already decided.
var ErrNotPending = errors.New("review is not pending")

func lockKey(state string) string {
	if state == "" {
		state = "unknown"
	}
	return "incidents:" + strings.ToUpper(state)
}

// Resolve attaches the candidate to an existing incident or creates a new one.
// It is idempotent per (extraction, candidate index) and serialized per state
// by an advisory lock, so concurrent workers cannot create duplicate incidents.
func (r *Resolver) Resolve(ctx context.Context, in Input) (*Result, error) {
	var res *Result
	err := r.store.InTx(ctx, func(q *dbgen.Queries) error {
		if err := q.LockKey(ctx, lockKey(in.Obs.Candidate.State)); err != nil {
			return err
		}
		if prev, err := q.FindResolution(ctx, dbgen.FindResolutionParams{ExtractionID: in.ExtractionID, CandidateIdx: int32(in.Index)}); err == nil {
			res = &Result{IncidentID: prev.IncidentID, Outcome: OutcomeDuplicate}
			return nil
		} else if !isNoRows(err) {
			return err
		}

		n := stateFrom(in.Obs)
		rows, err := q.FindCandidates(ctx, dbgen.FindCandidatesParams{
			Lon: in.Obs.Lon, Lat: in.Obs.Lat, Street: strp(n.StreetAddress), City: strp(n.City), County: strp(n.County),
			State: strp(n.State), OnDate: n.OccurredOn, WindowDays: int32(r.cfg.WindowDays), RadiusM: r.cfg.RadiusM,
		})
		if err != nil {
			return err
		}
		var matches []Match
		for _, row := range rows {
			m := Compare(r.cfg.Thresholds, stateFromRow(row), n, row.DistanceM)
			m.IncidentID = row.ID.String()
			matches = append(matches, m)
		}
		sort.Slice(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })

		var autos, possibles []Match
		for _, m := range matches {
			switch m.Decision {
			case DecisionAuto:
				autos = append(autos, m)
			case DecisionReview:
				possibles = append(possibles, m)
			}
		}
		// Two near-equal automatic matches mean the data cannot say which incident this is.
		if len(autos) > 1 && autos[0].Score-autos[1].Score < 0.05 {
			possibles = append(autos, possibles...)
			autos = nil
		}
		res = &Result{Considered: matches}

		if len(autos) > 0 {
			best := autos[0]
			id := uuid.MustParse(best.IncidentID)
			if err := r.attach(ctx, q, id, in, "confirmed", best.Score); err != nil {
				return err
			}
			res.IncidentID, res.Outcome, res.Score = id, OutcomeMatched, best.Score
			return nil
		}

		id, err := r.create(ctx, q, in)
		if err != nil {
			return err
		}
		res.IncidentID, res.Outcome = id, OutcomeCreated
		for _, p := range possibles {
			cand := uuid.MustParse(p.IncidentID)
			reasons, _ := json.Marshal(p)
			if err := q.InsertMatchReview(ctx, dbgen.InsertMatchReviewParams{
				ExtractionID: in.ExtractionID, CandidateIdx: int32(in.Index), NewIncidentID: id,
				CandidateIncidentID: cand, Score: float32(p.Score), Reasons: reasons,
			}); err != nil {
				return err
			}
			res.Outcome = OutcomeReview
			if p.Score > res.Score {
				res.Score = p.Score
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if res.Outcome != OutcomeDuplicate {
		r.log.Info().Str("incident_id", res.IncidentID.String()).Str("outcome", string(res.Outcome)).
			Float64("score", res.Score).Str("article_id", in.ArticleID.String()).Msg("incident resolved")
	}
	return res, nil
}

func (r *Resolver) create(ctx context.Context, q *dbgen.Queries, in Input) (uuid.UUID, error) {
	st, _ := Fold([]Observation{in.Obs})
	id, err := q.InsertIncident(ctx, insertParams(st))
	if err != nil {
		return uuid.Nil, err
	}
	if err := r.link(ctx, q, id, in, "created", 0); err != nil {
		return uuid.Nil, err
	}
	if err := r.refresh(ctx, q, id, "system", "created", nil); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func (r *Resolver) attach(ctx context.Context, q *dbgen.Queries, id uuid.UUID, in Input, status string, score float64) error {
	_, before, err := r.foldIncident(ctx, q, id)
	if err != nil {
		return err
	}
	if err := r.link(ctx, q, id, in, status, score); err != nil {
		return err
	}
	_, after, err := r.foldIncident(ctx, q, id)
	if err != nil {
		return err
	}
	return r.refresh(ctx, q, id, "system", "updated", newConflicts(before, after))
}

func (r *Resolver) link(ctx context.Context, q *dbgen.Queries, id uuid.UUID, in Input, status string, score float64) error {
	data, err := json.Marshal(in.Obs)
	if err != nil {
		return err
	}
	if err := q.InsertIncidentArticle(ctx, dbgen.InsertIncidentArticleParams{
		IncidentID: id, ArticleID: in.ArticleID, ExtractionID: in.ExtractionID, CandidateIdx: int32(in.Index),
		MatchStatus: status, MatchScore: f32p(score), MatchedBy: "rules",
	}); err != nil {
		return err
	}
	return q.InsertObservation(ctx, dbgen.InsertObservationParams{
		IncidentID: id, ArticleID: in.ArticleID, ExtractionID: in.ExtractionID, CandidateIdx: int32(in.Index),
		ObservedAt: in.Obs.ObservedAt, Data: data,
	})
}

type storedObs struct {
	Observation
	articleID, extractionID uuid.UUID
	idx                     int
}

func (r *Resolver) observations(ctx context.Context, q *dbgen.Queries, id uuid.UUID) ([]storedObs, error) {
	rows, err := q.ListObservations(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]storedObs, 0, len(rows))
	for _, row := range rows {
		var o Observation
		if err := json.Unmarshal(row.Data, &o); err != nil {
			return nil, fmt.Errorf("observation %s: %w", row.ID, err)
		}
		o.ObservedAt = row.ObservedAt
		out = append(out, storedObs{o, row.ArticleID, row.ExtractionID, int(row.CandidateIdx)})
	}
	return out, nil
}

func (r *Resolver) foldIncident(ctx context.Context, q *dbgen.Queries, id uuid.UUID) (State, []string, error) {
	so, err := r.observations(ctx, q, id)
	if err != nil {
		return State{}, nil, err
	}
	obs := make([]Observation, len(so))
	for i := range so {
		obs[i] = so[i].Observation
	}
	st, c := Fold(obs)
	return st, c, nil
}

// refresh recomputes the canonical row from observations, rebuilds participants
// and appends an audit event with the field-level diff and any new conflicts.
func (r *Resolver) refresh(ctx context.Context, q *dbgen.Queries, id uuid.UUID, actor, event string, newConf []string) error {
	cur, err := q.GetIncident(ctx, id)
	if err != nil {
		return err
	}
	st, _, err := r.foldIncident(ctx, q, id)
	if err != nil {
		return err
	}
	n, err := q.CountIncidentArticles(ctx, id)
	if err != nil {
		return err
	}
	verification := cur.VerificationStatus
	if verification != "reviewed" {
		verification = "unverified"
		if n >= 2 {
			verification = "corroborated"
		}
	}
	lat, lon := st.Lat, st.Lon
	if err := q.UpdateIncidentCanonical(ctx, updateParams(id, st, verification, n, lat, lon)); err != nil {
		return err
	}
	if err := q.DeleteParticipants(ctx, id); err != nil {
		return err
	}
	for _, c := range st.VRU {
		if err := q.InsertParticipant(ctx, dbgen.InsertParticipantParams{IncidentID: id, Kind: "vru", Category: c, Outcome: "unknown"}); err != nil {
			return err
		}
	}
	for _, c := range st.Vehicles {
		if err := q.InsertParticipant(ctx, dbgen.InsertParticipantParams{IncidentID: id, Kind: "vehicle", Category: c, Outcome: "unknown"}); err != nil {
			return err
		}
	}
	diff := Diff(stateFromView(cur), st)
	if event == "updated" && len(diff) == 0 && len(newConf) == 0 {
		return nil
	}
	if err := r.event(ctx, q, id, event, actor, map[string]any{"changes": diff, "article_count": n}); err != nil {
		return err
	}
	if len(newConf) > 0 {
		return r.event(ctx, q, id, "conflict", actor, map[string]any{"conflicts": newConf})
	}
	return nil
}

func (r *Resolver) event(ctx context.Context, q *dbgen.Queries, id uuid.UUID, typ, actor string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return q.InsertEvent(ctx, dbgen.InsertEventParams{IncidentID: id, EventType: typ, Actor: actor, Data: b})
}

// ApproveReview merges the provisional incident into the existing candidate.
func (r *Resolver) ApproveReview(ctx context.Context, reviewID uuid.UUID, actor string) (uuid.UUID, error) {
	var target uuid.UUID
	err := r.store.InTx(ctx, func(q *dbgen.Queries) error {
		rev, err := q.GetReview(ctx, reviewID)
		if err != nil {
			return err
		}
		if rev.Status != "pending" {
			return ErrNotPending
		}
		cand, err := q.GetIncident(ctx, rev.CandidateIncidentID)
		if err != nil {
			return err
		}
		if err := q.LockKey(ctx, lockKey(deref(cand.State))); err != nil {
			return err
		}
		if cand.MergedInto != nil {
			return fmt.Errorf("candidate incident %s was itself merged into %s; reject and re-resolve", cand.ID, cand.MergedInto)
		}
		from, to := rev.NewIncidentID, rev.CandidateIncidentID
		if err := q.MoveIncidentArticles(ctx, dbgen.MoveIncidentArticlesParams{ToID: to, FromID: from}); err != nil {
			return err
		}
		if err := q.MoveObservations(ctx, dbgen.MoveObservationsParams{ToID: to, FromID: from}); err != nil {
			return err
		}
		if err := q.SoftMergeIncident(ctx, dbgen.SoftMergeIncidentParams{ToID: &to, FromID: from}); err != nil {
			return err
		}
		if err := q.DeleteParticipants(ctx, from); err != nil {
			return err
		}
		if err := r.event(ctx, q, from, "merged", actor, map[string]any{"into": to, "review_id": reviewID, "score": rev.Score}); err != nil {
			return err
		}
		if err := q.DecideReview(ctx, dbgen.DecideReviewParams{ID: reviewID, Status: "approved", DecidedBy: &actor}); err != nil {
			return err
		}
		if err := q.SupersedeOtherReviews(ctx, dbgen.SupersedeOtherReviewsParams{NewIncidentID: from, KeepID: reviewID}); err != nil {
			return err
		}
		if err := r.reviewedLink(ctx, q, to); err != nil {
			return err
		}
		if err := r.event(ctx, q, to, "review_approved", actor, map[string]any{"merged_from": from, "review_id": reviewID}); err != nil {
			return err
		}
		target = to
		return r.refresh(ctx, q, to, actor, "updated", nil)
	})
	return target, err
}

// reviewedLink marks the incident as human-reviewed.
func (r *Resolver) reviewedLink(ctx context.Context, q *dbgen.Queries, id uuid.UUID) error {
	cur, err := q.GetIncident(ctx, id)
	if err != nil {
		return err
	}
	st := stateFromView(cur)
	return q.UpdateIncidentCanonical(ctx, updateParams(id, st, "reviewed", cur.ArticleCount, cur.Lat, cur.Lon))
}

// RejectReview records that the candidate is a distinct incident.
func (r *Resolver) RejectReview(ctx context.Context, reviewID uuid.UUID, actor string) error {
	return r.store.InTx(ctx, func(q *dbgen.Queries) error {
		rev, err := q.GetReview(ctx, reviewID)
		if err != nil {
			return err
		}
		if rev.Status != "pending" {
			return ErrNotPending
		}
		if err := q.DecideReview(ctx, dbgen.DecideReviewParams{ID: reviewID, Status: "rejected", DecidedBy: &actor}); err != nil {
			return err
		}
		return r.event(ctx, q, rev.NewIncidentID, "review_rejected", actor, map[string]any{"candidate": rev.CandidateIncidentID, "review_id": reviewID})
	})
}

// SplitArticle moves one article's observations out of an incident into a new
// incident (the manual fix for an incorrect merge) and recomputes both.
func (r *Resolver) SplitArticle(ctx context.Context, incidentID, articleID uuid.UUID, actor string) (uuid.UUID, error) {
	var newID uuid.UUID
	err := r.store.InTx(ctx, func(q *dbgen.Queries) error {
		inc, err := q.GetIncident(ctx, incidentID)
		if err != nil {
			return err
		}
		if err := q.LockKey(ctx, lockKey(deref(inc.State))); err != nil {
			return err
		}
		if n, err := q.CountIncidentArticles(ctx, incidentID); err != nil {
			return err
		} else if n < 2 {
			return errors.New("cannot split: incident has fewer than two articles")
		}
		moving, err := q.ListObservationsForArticle(ctx, dbgen.ListObservationsForArticleParams{IncidentID: incidentID, ArticleID: articleID})
		if err != nil {
			return err
		}
		if len(moving) == 0 {
			return errors.New("article is not linked to this incident")
		}
		var o Observation
		if err := json.Unmarshal(moving[0].Data, &o); err != nil {
			return err
		}
		o.ObservedAt = moving[0].ObservedAt
		st, _ := Fold([]Observation{o})
		newID, err = q.InsertIncident(ctx, insertParams(st))
		if err != nil {
			return err
		}
		if err := q.MoveArticleLinks(ctx, dbgen.MoveArticleLinksParams{ToID: newID, FromID: incidentID, ArticleID: articleID}); err != nil {
			return err
		}
		if err := q.MoveArticleObservations(ctx, dbgen.MoveArticleObservationsParams{ToID: newID, FromID: incidentID, ArticleID: articleID}); err != nil {
			return err
		}
		if err := r.event(ctx, q, incidentID, "split", actor, map[string]any{"article_id": articleID, "new_incident": newID}); err != nil {
			return err
		}
		if err := r.event(ctx, q, newID, "split", actor, map[string]any{"article_id": articleID, "from_incident": incidentID}); err != nil {
			return err
		}
		if err := r.refresh(ctx, q, incidentID, actor, "updated", nil); err != nil {
			return err
		}
		return r.refresh(ctx, q, newID, actor, "created", nil)
	})
	return newID, err
}

// Refresh recomputes an incident's canonical fields from its observations.
func (r *Resolver) Refresh(ctx context.Context, id uuid.UUID, actor string) error {
	return r.store.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.GetIncident(ctx, id)
		if err != nil {
			return err
		}
		if err := q.LockKey(ctx, lockKey(deref(cur.State))); err != nil {
			return err
		}
		return r.refresh(ctx, q, id, actor, "updated", nil)
	})
}

// SupersedeExtraction removes everything a previous extraction contributed (links
// and observations), recomputes the incidents it touched, and withdraws any that
// no longer have supporting articles. Used by reprocessing before a new
// extraction is resolved. It returns the affected incident IDs.
func (r *Resolver) SupersedeExtraction(ctx context.Context, extractionID uuid.UUID, actor string) ([]uuid.UUID, error) {
	var affected []uuid.UUID
	err := r.store.InTx(ctx, func(q *dbgen.Queries) error {
		ids, err := q.ListIncidentsByExtraction(ctx, extractionID)
		if err != nil {
			return err
		}
		// Lock every touched state first, in a stable order, to avoid deadlocks between workers.
		states := map[string]bool{}
		for _, id := range ids {
			inc, err := q.GetIncident(ctx, id)
			if err != nil {
				return err
			}
			states[lockKey(deref(inc.State))] = true
		}
		keys := make([]string, 0, len(states))
		for k := range states {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := q.LockKey(ctx, k); err != nil {
				return err
			}
		}
		if err := q.DeleteLinksByExtraction(ctx, extractionID); err != nil {
			return err
		}
		if err := q.DeleteObservationsByExtraction(ctx, extractionID); err != nil {
			return err
		}
		for _, id := range ids {
			n, err := q.CountIncidentArticles(ctx, id)
			if err != nil {
				return err
			}
			if n == 0 {
				if err := q.WithdrawIncident(ctx, id); err != nil {
					return err
				}
				if err := q.RejectReviewsForIncident(ctx, id); err != nil {
					return err
				}
				if err := r.event(ctx, q, id, "withdrawn", actor, map[string]any{"reason": "all supporting extractions superseded", "extraction_id": extractionID}); err != nil {
					return err
				}
			} else {
				if err := r.refresh(ctx, q, id, actor, "updated", nil); err != nil {
					return err
				}
				if err := r.event(ctx, q, id, "superseded", actor, map[string]any{"extraction_id": extractionID}); err != nil {
					return err
				}
			}
			affected = append(affected, id)
		}
		return nil
	})
	return affected, err
}
