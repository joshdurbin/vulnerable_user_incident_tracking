package discovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// NewURL is a URL seen for the first time; the caller schedules article processing for it.
type NewURL struct {
	ID    uuid.UUID
	URL   string // normalized
	Host  string
	Title string
}

// RecordResult summarizes what persisting a run changed.
type RecordResult struct {
	New        []NewURL
	Pending    []NewURL // seen before but never turned into an article (e.g. a crash after recording)
	Known      int      // unique URLs already in the database
	Provenance int      // new (url, provider, source, query) sightings recorded
}

// SourceStats are discovery quality metrics for one provider+source (design §5.7).
type SourceStats struct {
	Provider         string
	SourceSlug       string
	Discoveries      int64
	UniqueURLs       int64
	ExclusiveURLs    int64 // found by this provider and no other
	AvgDelaySeconds  float64
	ArticlesFetched  int64
	FetchOK          int64
	RelevantArticles int64
	Incidents        int64
}

func (s SourceStats) DuplicateRate() float64 {
	if s.Discoveries == 0 {
		return 0
	}
	return 1 - float64(s.UniqueURLs)/float64(s.Discoveries)
}
func (s SourceStats) FetchSuccessRate() float64 { return ratio(s.FetchOK, s.ArticlesFetched) }
func (s SourceStats) RelevantRate() float64     { return ratio(s.RelevantArticles, s.ArticlesFetched) }

func ratio(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// Repository is the persistence the Service needs. The PostgreSQL implementation
// lives in discovery/pgrepo; tests use fakes.
type Repository interface {
	Source(ctx context.Context, slug string) (Source, error)
	Sources(ctx context.Context, enabledOnly bool) ([]Source, error)
	Queries(ctx context.Context, providerType string) ([]Query, error)
	Record(ctx context.Context, src Source, items []Deduped) (RecordResult, error)
	MarkResult(ctx context.Context, slug string, at time.Time, runErr error) error
	Stats(ctx context.Context, since time.Time) ([]SourceStats, error)
}

// Service runs a source end to end: provider -> dedupe -> persist.
type Service struct {
	reg  *Registry
	repo Repository
	now  func() time.Time
}

func NewService(reg *Registry, repo Repository) *Service {
	return &Service{reg: reg, repo: repo, now: time.Now}
}

// RunRequest selects a source and window. A zero Window means "recent, with overlap".
type RunRequest struct {
	Source Source
	Window Window
	DryRun bool // discover and dedupe but do not persist
	// QueryNames restricts a search provider to these configured queries (debugging, targeted backfills).
	QueryNames []string
	// AdHocQueries are used instead of configured queries (targeted follow-ups).
	AdHocQueries []Query
}

// Report is the outcome of one run.
type Report struct {
	Source     Source
	Window     Window
	Found      int // raw candidates from the provider
	Unique     int // after normalization + dedupe within the run
	Rejected   []Candidate
	New        []NewURL // empty on DryRun
	Pending    []NewURL // known URLs that still have no article
	Known      int
	Warnings   []string
	Truncated  bool
	Requests   int
	Unresolved int // candidates still pointing at an intermediary (e.g. Google News)
	Candidates []Deduped
	Duration   time.Duration
}

// DefaultWindow looks back twice the poll interval (at least 2h) so late-indexed
// articles are caught; duplicates across overlapping runs are removed by dedupe.
func DefaultWindow(src Source, now time.Time) Window {
	span := 2 * src.PollInterval
	if span < 2*time.Hour {
		span = 2 * time.Hour
	}
	return Window{Since: now.Add(-span), Until: now}
}

func IsSearchType(t string) bool { return t == TypeGDELT || t == TypeGoogleNews }

func (s *Service) Run(ctx context.Context, rr RunRequest) (*Report, error) {
	start := s.now()
	src := rr.Source
	w := rr.Window
	if w.Since.IsZero() || w.Until.IsZero() {
		w = DefaultWindow(src, start)
	}
	req := Request{Source: src, Window: w}
	if IsSearchType(src.Type) && len(rr.AdHocQueries) > 0 {
		req.Queries = rr.AdHocQueries
	} else if IsSearchType(src.Type) {
		qs, err := s.repo.Queries(ctx, src.Type)
		if err != nil {
			return nil, fmt.Errorf("load queries: %w", err)
		}
		if len(qs) == 0 {
			return nil, fmt.Errorf("no enabled queries for provider %s; run `collisionctl sources sync`", src.Type)
		}
		if len(rr.QueryNames) > 0 {
			var keep []Query
			for _, q := range qs {
				for _, n := range rr.QueryNames {
					if q.Name == n {
						keep = append(keep, q)
					}
				}
			}
			if len(keep) == 0 {
				return nil, fmt.Errorf("none of the requested queries %v exist for provider %s", rr.QueryNames, src.Type)
			}
			qs = keep
		}
		req.Queries = qs
	}

	res, derr := s.reg.Discover(ctx, req)
	rep := &Report{Source: src, Window: w}
	if res != nil {
		rep.Found, rep.Warnings, rep.Truncated, rep.Requests = len(res.Candidates), res.Warnings, res.Truncated, res.Requests
		for _, c := range res.Candidates {
			if c.NeedsResolution {
				rep.Unresolved++
			}
		}
	}
	if derr != nil && (res == nil || len(res.Candidates) == 0) {
		if !rr.DryRun {
			_ = s.repo.MarkResult(ctx, src.Slug, s.now(), derr)
		}
		return rep, derr
	}
	if derr != nil {
		rep.Warnings = append(rep.Warnings, "provider returned partial results: "+derr.Error())
	}

	uniq, rejected := Dedupe(res.Candidates)
	rep.Unique, rep.Rejected, rep.Candidates = len(uniq), rejected, uniq
	if len(rejected) > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d candidates had invalid URLs and were rejected", len(rejected)))
	}
	if !rr.DryRun {
		rec, err := s.repo.Record(ctx, src, uniq)
		if err != nil {
			_ = s.repo.MarkResult(ctx, src.Slug, s.now(), err)
			return rep, fmt.Errorf("record discoveries: %w", err)
		}
		rep.New, rep.Pending, rep.Known = rec.New, rec.Pending, rec.Known
		if err := s.repo.MarkResult(ctx, src.Slug, s.now(), nil); err != nil {
			return rep, err
		}
	}
	rep.Duration = s.now().Sub(start)
	return rep, nil
}

// ErrSourceDisabled is returned when running a disabled source by name.
var ErrSourceDisabled = errors.New("source is disabled")

// RunSlug loads a source by slug and runs it.
func (s *Service) RunSlug(ctx context.Context, slug string, w Window, dryRun bool, queryNames ...string) (*Report, error) {
	src, err := s.repo.Source(ctx, slug)
	if err != nil {
		return nil, err
	}
	if !src.Enabled && !dryRun {
		return nil, fmt.Errorf("%s: %w", slug, ErrSourceDisabled)
	}
	return s.Run(ctx, RunRequest{Source: src, Window: w, DryRun: dryRun, QueryNames: queryNames})
}

// Source loads a source by slug.
func (s *Service) Source(ctx context.Context, slug string) (Source, error) {
	return s.repo.Source(ctx, slug)
}
