package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/pipeline"
)

// The admin routes manage discovery sources and queries. Every one needs the admin token (the
// spec says so per operation); the middleware enforces it before these handlers run.

func strPtr(s string) *string { return &s }

func nonEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func listOrNil(v []string) *[]string {
	if len(v) == 0 {
		return nil
	}
	return &v
}

func derefList(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

func coverageToAPI(c discovery.Coverage) *Coverage {
	return &Coverage{Country: nonEmptyPtr(c.Country), States: listOrNil(c.States), Cities: listOrNil(c.Cities)}
}

func coverageFromAPI(c *Coverage) discovery.Coverage {
	if c == nil {
		return discovery.Coverage{}
	}
	out := discovery.Coverage{States: derefList(c.States), Cities: derefList(c.Cities)}
	if c.Country != nil {
		out.Country = *c.Country
	}
	return out
}

func crawlToAPI(c discovery.CrawlPolicy) *CrawlPolicy {
	out := &CrawlPolicy{AllowedDomains: listOrNil(c.AllowedDomains), UrlPatterns: listOrNil(c.URLPatterns)}
	if c.MaxPages > 0 {
		out.MaxPages = &c.MaxPages
	}
	if c.MaxDepth > 0 {
		out.MaxDepth = &c.MaxDepth
	}
	if c.Render != "" {
		r := CrawlPolicyRender(c.Render)
		out.Render = &r
	}
	return out
}

func crawlFromAPI(c *CrawlPolicy) discovery.CrawlPolicy {
	if c == nil {
		return discovery.CrawlPolicy{}
	}
	out := discovery.CrawlPolicy{AllowedDomains: derefList(c.AllowedDomains), URLPatterns: derefList(c.UrlPatterns)}
	if c.MaxPages != nil {
		out.MaxPages = *c.MaxPages
	}
	if c.MaxDepth != nil {
		out.MaxDepth = *c.MaxDepth
	}
	if c.Render != nil {
		out.Render = string(*c.Render)
	}
	return out
}

func sourceFromInput(in SourceInput) discovery.Source {
	src := discovery.Source{
		Slug: in.Slug, Name: in.Name, Type: in.Type, Enabled: in.Enabled,
		PollInterval: time.Duration(in.PollIntervalSeconds) * time.Second,
		Coverage:     coverageFromAPI(in.Coverage), Crawl: crawlFromAPI(in.Crawl),
	}
	if in.Url != nil {
		src.URL = *in.Url
	}
	return src
}

func problems(errs []error) []string {
	out := make([]string, len(errs))
	for i, e := range errs {
		out[i] = e.Error()
	}
	return out
}

func invalid(what string, errs []error) BadRequestJSONResponse {
	p := problems(errs)
	msg := what + " is invalid"
	if len(p) == 1 {
		msg = p[0]
	}
	return BadRequestJSONResponse{Error: msg, Problems: &p}
}

// ---- source types ----

func (s *Server) ListSourceTypes(_ context.Context, _ ListSourceTypesRequestObject) (ListSourceTypesResponseObject, error) {
	out := make(ListSourceTypes200JSONResponse, 0)
	for _, d := range s.reg.Descriptors() {
		t := SourceType{
			Type: d.Type, Label: d.Label, Description: d.Description, Url: SourceTypeUrl(d.URL), UrlHint: nonEmptyPtr(d.URLHint),
			UsesQueries: d.UsesQueries, UsesCoverage: d.UsesCoverage,
		}
		t.Crawl.AllowedDomains, t.Crawl.RequireDomains, t.Crawl.UrlPatterns = d.Crawl.AllowedDomains, d.Crawl.RequireDomains, d.Crawl.URLPatterns
		t.Crawl.MaxPages, t.Crawl.MaxDepth, t.Crawl.Render = d.Crawl.MaxPages, d.Crawl.MaxDepth, d.Crawl.Render
		out = append(out, t)
	}
	return out, nil
}

// ---- sources ----

// sourceViews is everything besides configuration that a SourceDetail shows.
type sourceViews struct {
	quality   map[string]discovery.SourceStats
	runs      map[string]discovery.RunStats
	schedules map[string]*ScheduleState
}

func (s *Server) loadViews(ctx context.Context, slugs []string, days int) (sourceViews, error) {
	since := s.now().AddDate(0, 0, -days)
	stats, err := s.repo.Stats(ctx, since)
	if err != nil {
		return sourceViews{}, err
	}
	// A source can be found by several providers; sum the rows that name it.
	quality := map[string]discovery.SourceStats{}
	for _, st := range stats {
		a := quality[st.SourceSlug]
		a.Discoveries += st.Discoveries
		a.UniqueURLs += st.UniqueURLs
		a.ExclusiveURLs += st.ExclusiveURLs
		a.ArticlesFetched += st.ArticlesFetched
		a.FetchOK += st.FetchOK
		a.RelevantArticles += st.RelevantArticles
		a.Incidents += st.Incidents
		a.AvgDelaySeconds = st.AvgDelaySeconds
		quality[st.SourceSlug] = a
	}
	runs, err := s.repo.RunStats(ctx, since)
	if err != nil {
		return sourceViews{}, err
	}
	return sourceViews{quality: quality, runs: runs, schedules: s.schedules(ctx, slugs)}, nil
}

// schedules fetches schedule state concurrently under a short deadline, best effort, so an
// unreachable Temporal slows a page by seconds at most, never by one timeout per source.
func (s *Server) schedules(ctx context.Context, slugs []string) map[string]*ScheduleState {
	out := map[string]*ScheduleState{}
	if s.wf == nil {
		return out
	}
	sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, slug := range slugs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if st, err := s.wf.Schedule(sctx, slug); err == nil {
				mu.Lock()
				out[slug] = st
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return out
}

func (s *Server) detail(src discovery.Source, v sourceViews) SourceDetail {
	q, rs := v.quality[src.Slug], v.runs[src.Slug]
	sched := v.schedules[src.Slug]
	paused := sched != nil && sched.Paused != nil && *sched.Paused
	h := discovery.AssessHealth(src, paused, s.now())
	d := SourceDetail{
		Slug: src.Slug, Name: src.Name, Type: src.Type, Url: nonEmptyPtr(src.URL), Enabled: src.Enabled,
		PollIntervalSeconds: int(src.PollInterval / time.Second), Coverage: coverageToAPI(src.Coverage), Crawl: crawlToAPI(src.Crawl),
		Health:        Health{State: HealthState(h.State), Reason: h.Reason},
		LastSuccessAt: src.LastSuccessAt, LastAttemptAt: src.LastAttemptAt, ConsecutiveFailures: src.ConsecutiveFailures,
		ErrorState: nonEmptyPtr(src.ErrorState), Schedule: sched,
		RunStats: RunStats{
			Runs: rs.Runs, Successes: rs.Successes, Partials: rs.Partials, Failures: rs.Failures, SuccessRate: rs.SuccessRate(),
			AvgSeconds: rs.AvgSeconds, P95Seconds: rs.P95Seconds, NewUrls: rs.NewURLs, Requests: rs.Requests,
		},
		Quality: SourceQuality{
			Discoveries: q.Discoveries, UniqueUrls: q.UniqueURLs, ExclusiveUrls: q.ExclusiveURLs, ArticlesFetched: q.ArticlesFetched,
			FetchSuccessRate: q.FetchSuccessRate(), RelevantRate: q.RelevantRate(), Incidents: q.Incidents, AvgDelaySeconds: q.AvgDelaySeconds,
		},
	}
	return d
}

func (s *Server) detailFor(ctx context.Context, src discovery.Source) (SourceDetail, error) {
	v, err := s.loadViews(ctx, []string{src.Slug}, 30)
	if err != nil {
		return SourceDetail{}, err
	}
	return s.detail(src, v), nil
}

func (s *Server) ListAdminSources(ctx context.Context, req ListAdminSourcesRequestObject) (ListAdminSourcesResponseObject, error) {
	days := 30
	if req.Params.Days != nil {
		days = *req.Params.Days
	}
	srcs, err := s.repo.Sources(ctx, false)
	if err != nil {
		return nil, err
	}
	slugs := make([]string, len(srcs))
	for i, src := range srcs {
		slugs[i] = src.Slug
	}
	v, err := s.loadViews(ctx, slugs, days)
	if err != nil {
		return nil, err
	}
	out := make(ListAdminSources200JSONResponse, 0, len(srcs))
	for _, src := range srcs {
		out = append(out, s.detail(src, v))
	}
	return out, nil
}

func (s *Server) GetAdminSource(ctx context.Context, req GetAdminSourceRequestObject) (GetAdminSourceResponseObject, error) {
	src, err := s.repo.Source(ctx, req.Slug)
	if errors.Is(err, discovery.ErrNotFound) {
		return GetAdminSource404JSONResponse{NotFoundJSONResponse{Error: "source not found"}}, nil
	}
	if err != nil {
		return nil, err
	}
	d, err := s.detailFor(ctx, src)
	if err != nil {
		return nil, err
	}
	return GetAdminSource200JSONResponse(d), nil
}

func (s *Server) CreateSource(ctx context.Context, req CreateSourceRequestObject) (CreateSourceResponseObject, error) {
	if req.Body == nil {
		return CreateSource400JSONResponse{BadRequestJSONResponse{Error: "body is required"}}, nil
	}
	src := sourceFromInput(*req.Body)
	if errs := discovery.ValidateSource(s.reg, src); len(errs) > 0 {
		return CreateSource400JSONResponse{invalid("source", errs)}, nil
	}
	created, err := s.repo.CreateSource(ctx, src)
	if errors.Is(err, discovery.ErrAlreadyExists) {
		return CreateSource409JSONResponse{Error: fmt.Sprintf("a source with slug %q already exists", src.Slug)}, nil
	}
	if err != nil {
		return nil, err
	}
	d, err := s.detailFor(ctx, created)
	if err != nil {
		return nil, err
	}
	d.ScheduleError = s.applySchedule(ctx, created, false)
	return CreateSource201JSONResponse(d), nil
}

// applySchedule brings the source's Schedule in line with it. The source is already saved, so a
// failure is reported to the caller rather than undoing the save: the next schedules sync fixes it.
func (s *Server) applySchedule(ctx context.Context, src discovery.Source, enabledChanged bool) *string {
	if s.wf == nil {
		return nil
	}
	if err := s.wf.ApplySource(ctx, src, enabledChanged); err != nil {
		s.cfg.Log.Warn().Err(err).Str("source", src.Slug).Msg("saved source but could not update its schedule")
		return strPtr("saved, but its schedule could not be updated (" + err.Error() + "); use Sync schedules or retry")
	}
	return nil
}

func (s *Server) UpdateSource(ctx context.Context, req UpdateSourceRequestObject) (UpdateSourceResponseObject, error) {
	if req.Body == nil {
		return UpdateSource400JSONResponse{BadRequestJSONResponse{Error: "body is required"}}, nil
	}
	if req.Body.Slug != req.Slug {
		return UpdateSource400JSONResponse{BadRequestJSONResponse{Error: "the slug identifies the source and cannot change"}}, nil
	}
	prev, err := s.repo.Source(ctx, req.Slug)
	if errors.Is(err, discovery.ErrNotFound) {
		return UpdateSource404JSONResponse{NotFoundJSONResponse{Error: "source not found"}}, nil
	}
	if err != nil {
		return nil, err
	}
	src := sourceFromInput(*req.Body)
	if errs := discovery.ValidateSource(s.reg, src); len(errs) > 0 {
		return UpdateSource400JSONResponse{invalid("source", errs)}, nil
	}
	updated, err := s.repo.UpdateSource(ctx, src)
	if errors.Is(err, discovery.ErrNotFound) {
		return UpdateSource404JSONResponse{NotFoundJSONResponse{Error: "source not found"}}, nil
	}
	if err != nil {
		return nil, err
	}
	d, err := s.detailFor(ctx, updated)
	if err != nil {
		return nil, err
	}
	d.ScheduleError = s.applySchedule(ctx, updated, prev.Enabled != updated.Enabled)
	return UpdateSource200JSONResponse(d), nil
}

func (s *Server) DeleteSource(ctx context.Context, req DeleteSourceRequestObject) (DeleteSourceResponseObject, error) {
	if _, err := s.repo.Source(ctx, req.Slug); errors.Is(err, discovery.ErrNotFound) {
		return DeleteSource404JSONResponse{NotFoundJSONResponse{Error: "source not found"}}, nil
	} else if err != nil {
		return nil, err
	}
	// The schedule goes first: if that fails nothing is lost, whereas deleting the source first
	// would leave a schedule starting workflows for a source that no longer exists.
	if s.wf != nil {
		if err := s.wf.DeleteSchedule(ctx, req.Slug); err != nil {
			return nil, err
		}
	}
	if err := s.repo.DeleteSource(ctx, req.Slug); err != nil && !errors.Is(err, discovery.ErrNotFound) {
		return nil, err
	}
	return DeleteSource204Response{}, nil
}

func (s *Server) ListSourceRuns(ctx context.Context, req ListSourceRunsRequestObject) (ListSourceRunsResponseObject, error) {
	limit := 50
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if _, err := s.repo.Source(ctx, req.Slug); errors.Is(err, discovery.ErrNotFound) {
		return ListSourceRuns404JSONResponse{NotFoundJSONResponse{Error: "source not found"}}, nil
	} else if err != nil {
		return nil, err
	}
	runs, err := s.repo.Runs(ctx, req.Slug, limit)
	if err != nil {
		return nil, err
	}
	out := make(ListSourceRuns200JSONResponse, 0, len(runs))
	for _, r := range runs {
		w := r.Warnings
		if w == nil {
			w = []string{}
		}
		run := SourceRun{
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Status: SourceRunStatus(r.Status), Found: r.Found, UniqueUrls: r.Unique,
			NewUrls: r.New, KnownUrls: r.Known, PendingUrls: r.Pending, Unresolved: r.Unresolved, Requests: r.Requests,
			Truncated: r.Truncated, Warnings: w, Error: nonEmptyPtr(r.Error),
		}
		if !r.Window.Since.IsZero() {
			run.WindowSince = &r.Window.Since
		}
		if !r.Window.Until.IsZero() {
			run.WindowUntil = &r.Window.Until
		}
		out = append(out, run)
	}
	return out, nil
}

func (s *Server) TestSource(ctx context.Context, req TestSourceRequestObject) (TestSourceResponseObject, error) {
	if req.Body == nil {
		return TestSource400JSONResponse{BadRequestJSONResponse{Error: "body is required"}}, nil
	}
	src := sourceFromInput(*req.Body)
	if errs := discovery.ValidateSource(s.reg, src); len(errs) > 0 {
		return TestSource400JSONResponse{invalid("source", errs)}, nil
	}
	if s.wf == nil {
		return nil, ErrTemporalDown
	}
	res, err := s.wf.TestSource(ctx, src)
	if err != nil {
		return nil, err
	}
	return TestSource200JSONResponse(testResultToAPI(res)), nil
}

func testResultToAPI(r *pipeline.SourceTestResult) SourceTestResult {
	out := SourceTestResult{
		Ok: r.OK, Error: nonEmptyPtr(r.Error), Found: r.Found, UniqueUrls: r.Unique, Unresolved: r.Unresolved, Requests: r.Requests,
		Truncated: r.Truncated, DurationMs: r.DurationMS, Warnings: r.Warnings,
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	out.Samples = make([]struct {
		PublishedAt *time.Time `json:"published_at,omitempty"`
		Title       *string    `json:"title,omitempty"`
		Url         string     `json:"url"`
	}, 0, len(r.Samples))
	for _, x := range r.Samples {
		out.Samples = append(out.Samples, struct {
			PublishedAt *time.Time `json:"published_at,omitempty"`
			Title       *string    `json:"title,omitempty"`
			Url         string     `json:"url"`
		}{PublishedAt: x.PublishedAt, Title: nonEmptyPtr(x.Title), Url: x.URL})
	}
	return out
}

func (s *Server) SyncSchedules(ctx context.Context, _ SyncSchedulesRequestObject) (SyncSchedulesResponseObject, error) {
	srcs, err := s.repo.Sources(ctx, false)
	if err != nil {
		return nil, err
	}
	if s.wf == nil {
		return nil, ErrTemporalDown
	}
	c, u, r, err := s.wf.SyncSchedules(ctx, srcs)
	if err != nil {
		return nil, err
	}
	return SyncSchedules200JSONResponse{Created: c, Updated: u, Removed: r}, nil
}

// ---- queries ----

func querySpecFromAPI(q Query) discovery.QuerySpec {
	out := discovery.QuerySpec{Family: q.Family, Name: q.Name, Text: q.Text, Providers: derefList(q.Providers), Enabled: q.Enabled}
	if q.Region != nil {
		out.Region = *q.Region
	}
	return out
}

func querySpecToAPI(q discovery.QuerySpec) Query {
	p := q.Providers
	if p == nil {
		p = []string{}
	}
	return Query{Family: q.Family, Name: q.Name, Text: q.Text, Providers: &p, Region: nonEmptyPtr(q.Region), Enabled: q.Enabled}
}

func (s *Server) ListQueries(ctx context.Context, _ ListQueriesRequestObject) (ListQueriesResponseObject, error) {
	qs, err := s.repo.AllQueries(ctx)
	if err != nil {
		return nil, err
	}
	out := make(ListQueries200JSONResponse, 0, len(qs))
	for _, q := range qs {
		out = append(out, querySpecToAPI(q))
	}
	return out, nil
}

func (s *Server) CreateQuery(ctx context.Context, req CreateQueryRequestObject) (CreateQueryResponseObject, error) {
	if req.Body == nil {
		return CreateQuery400JSONResponse{BadRequestJSONResponse{Error: "body is required"}}, nil
	}
	q := querySpecFromAPI(*req.Body)
	if errs := discovery.ValidateQuery(s.reg, q); len(errs) > 0 {
		return CreateQuery400JSONResponse{invalid("query", errs)}, nil
	}
	created, err := s.repo.CreateQuery(ctx, q)
	if errors.Is(err, discovery.ErrAlreadyExists) {
		return CreateQuery409JSONResponse{Error: fmt.Sprintf("a query named %q already exists", q.Name)}, nil
	}
	if err != nil {
		return nil, err
	}
	return CreateQuery201JSONResponse(querySpecToAPI(created)), nil
}

func (s *Server) UpdateQuery(ctx context.Context, req UpdateQueryRequestObject) (UpdateQueryResponseObject, error) {
	if req.Body == nil {
		return UpdateQuery400JSONResponse{BadRequestJSONResponse{Error: "body is required"}}, nil
	}
	if req.Body.Name != req.Name {
		return UpdateQuery400JSONResponse{BadRequestJSONResponse{Error: "the name identifies the query and cannot change"}}, nil
	}
	q := querySpecFromAPI(*req.Body)
	if errs := discovery.ValidateQuery(s.reg, q); len(errs) > 0 {
		return UpdateQuery400JSONResponse{invalid("query", errs)}, nil
	}
	updated, err := s.repo.UpdateQuery(ctx, q)
	if errors.Is(err, discovery.ErrNotFound) {
		return UpdateQuery404JSONResponse{NotFoundJSONResponse{Error: "query not found"}}, nil
	}
	if err != nil {
		return nil, err
	}
	return UpdateQuery200JSONResponse(querySpecToAPI(updated)), nil
}

func (s *Server) DeleteQuery(ctx context.Context, req DeleteQueryRequestObject) (DeleteQueryResponseObject, error) {
	err := s.repo.DeleteQuery(ctx, req.Name)
	if errors.Is(err, discovery.ErrNotFound) {
		return DeleteQuery404JSONResponse{NotFoundJSONResponse{Error: "query not found"}}, nil
	}
	if err != nil {
		return nil, err
	}
	return DeleteQuery204Response{}, nil
}
