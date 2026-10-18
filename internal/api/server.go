// Package api serves the REST API described in api/openapi.yaml. Handlers are
// generated against that spec (oapi-codegen, strict mode); requests are validated
// against it before they reach a handler.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/oapi-codegen/nethttp-middleware"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/rs/zerolog"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/pipeline"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage/dbgen"
)

// Config for the API process.
type Config struct {
	// AdminToken guards the POST routes. Empty disables them (401), the safe default.
	AdminToken string
	Log        zerolog.Logger
	// Metrics, if set, records request counts and latency.
	Metrics *HTTPMetrics
}

type Server struct {
	store *storage.Store
	wf    Workflows
	cfg   Config
	now   func() time.Time
}

func NewServer(st *storage.Store, wf Workflows, cfg Config) *Server {
	return &Server{store: st, wf: wf, cfg: cfg, now: time.Now}
}

var _ StrictServerInterface = (*Server)(nil)

// Handler returns the complete HTTP handler: request validation, auth, routes, health.
func (s *Server) Handler() (http.Handler, error) {
	spec, err := GetSwagger()
	if err != nil {
		return nil, err
	}
	r := chi.NewRouter()
	if s.cfg.Metrics != nil {
		r.Use(s.cfg.Metrics.Middleware)
	}
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		if err := s.store.Pool.Ping(req.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	strict := NewStrictHandlerWithOptions(s, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeErr(w, http.StatusBadRequest, err.Error())
		},
		ResponseErrorHandlerFunc: s.responseError,
	})
	// The spec lists the server as "/v1". Validate against the paths below it, with the
	// prefix stripped, so the validator does no host or prefix matching of its own.
	spec.Servers = nil
	v1 := chi.NewRouter()
	v1.Use(nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		ErrorHandler: func(w http.ResponseWriter, message string, status int) { writeErr(w, status, message) },
		Options:      openapi3filter.Options{AuthenticationFunc: s.authenticate},
	}))
	HandlerFromMux(strict, v1)
	r.Mount("/v1", http.StripPrefix("/v1", v1))
	return r, nil
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Error{Error: msg})
}

func (s *Server) responseError(w http.ResponseWriter, r *http.Request, err error) {
	var br badRequest
	switch {
	case errors.As(err, &br):
		writeErr(w, http.StatusBadRequest, br.Error())
	case errors.Is(err, ErrTemporalDown):
		writeErr(w, http.StatusServiceUnavailable, "workflow service unavailable")
	default:
		s.cfg.Log.Error().Err(err).Str("path", r.URL.Path).Msg("request failed")
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

// authenticate enforces the spec's bearer scheme with a constant-time compare.
func (s *Server) authenticate(_ context.Context, in *openapi3filter.AuthenticationInput) error {
	if in.SecuritySchemeName != "adminToken" {
		return errors.New("unknown security scheme")
	}
	if s.cfg.AdminToken == "" {
		return errors.New("admin routes are disabled: no token configured")
	}
	h := in.RequestValidationInput.Request.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.AdminToken)) != 1 {
		return errors.New("missing or wrong admin token")
	}
	return nil
}

var _ = openapi3.T{}

// ---- incidents ----

func dateP(d *openapi_types.Date) *time.Time {
	if d == nil {
		return nil
	}
	t := d.Time
	return &t
}

func (s *Server) ListIncidents(ctx context.Context, req ListIncidentsRequestObject) (ListIncidentsResponseObject, error) {
	p := req.Params
	var vru []string
	if p.Vru != nil {
		vru = *p.Vru
	}
	f, err := newFilter(p.Bbox, dateP(p.From), dateP(p.To), p.State, p.City, vru, str(p.Severity), str(p.Verification), str(p.HitAndRun))
	if err != nil {
		return ListIncidents400JSONResponse{BadRequestJSONResponse{Error: err.Error()}}, nil
	}
	limit, offset := 100, 0
	if p.Limit != nil {
		limit = *p.Limit
	}
	if p.Offset != nil {
		offset = *p.Offset
	}
	rows, err := dbgen.New(s.store.Pool).ListIncidentsAPI(ctx, f.list(limit, offset))
	if err != nil {
		return nil, err
	}
	out := IncidentList{Items: make([]Incident, 0, len(rows))}
	for _, r := range rows {
		out.Total = r.Total
		out.Items = append(out.Items, incidentFromRow(r.ID, r.OccurredOn, r.OccurredAt, r.OccurredPrecision, r.City, r.County, r.State, r.StreetAddress,
			r.LocationPrecision, r.Lat, r.Lon, r.VruCategories, r.VehicleCategories, r.InjuredCount, r.FatalityCount, r.Severity, r.HitAndRun,
			r.FactsStatus, r.VerificationStatus, r.ArticleCount, r.UpdatedAt))
	}
	if len(rows) == 0 && offset > 0 {
		// An offset past the end returns no rows, hiding the total; ask again without paging.
		first, err := dbgen.New(s.store.Pool).ListIncidentsAPI(ctx, f.list(1, 0))
		if err != nil {
			return nil, err
		}
		if len(first) > 0 {
			out.Total = first[0].Total
		}
	}
	return ListIncidents200JSONResponse(out), nil
}

func i32p(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func incidentFromRow(id uuid.UUID, on, at *time.Time, prec string, city, county, state, street *string, locPrec string, lat, lon *float64,
	vru, veh []string, inj, fat *int32, sev, hr, facts, ver string, articles int32, updated time.Time) Incident {
	inc := Incident{
		Id: id, OccurredAt: at, OccurredPrecision: IncidentOccurredPrecision(prec), City: city, County: county, State: state,
		StreetAddress: street, LocationPrecision: locPrec, Lat: lat, Lon: lon, VruCategories: nz(vru), VehicleCategories: nz(veh),
		InjuredCount: i32p(inj), FatalityCount: i32p(fat), Severity: IncidentSeverity(sev), HitAndRun: IncidentHitAndRun(hr),
		FactsStatus: IncidentFactsStatus(facts), VerificationStatus: IncidentVerificationStatus(ver), ArticleCount: int(articles), UpdatedAt: updated,
	}
	if on != nil {
		inc.OccurredOn = &openapi_types.Date{Time: *on}
	}
	return inc
}

func incidentFromView(v dbgen.IncidentsV) Incident {
	return incidentFromRow(v.ID, v.OccurredOn, v.OccurredAt, v.OccurredPrecision, v.City, v.County, v.State, v.StreetAddress, v.LocationPrecision,
		v.Lat, v.Lon, v.VruCategories, v.VehicleCategories, v.InjuredCount, v.FatalityCount, v.Severity, v.HitAndRun, v.FactsStatus,
		v.VerificationStatus, v.ArticleCount, v.UpdatedAt)
}

func reasons(b []byte) *map[string]any {
	m := jsonMap(b)
	return &m
}

func jsonMap(b []byte) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

func (s *Server) GetIncident(ctx context.Context, req GetIncidentRequestObject) (GetIncidentResponseObject, error) {
	q := dbgen.New(s.store.Pool)
	v, err := q.GetIncident(ctx, req.Id)
	if err != nil {
		if storage.IsNoRows(err) {
			return GetIncident404JSONResponse{NotFoundJSONResponse{Error: "incident not found"}}, nil
		}
		return nil, err
	}
	arts, err := q.ListIncidentArticlesAPI(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	obs, err := q.ListObservations(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	evs, err := q.ListEvents(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	d := IncidentDetail{
		Incident: incidentFromView(v), Circumstances: v.Circumstances, Timezone: v.Timezone, GeocodeSource: v.GeocodeSource,
		WorkflowId: pipeline.IncidentWorkflowID(req.Id.String()),
		Articles:   make([]IncidentArticle, 0, len(arts)), Observations: make([]Observation, 0, len(obs)), Events: make([]Event, 0, len(evs)),
	}
	if v.GeocodeConfidence != nil {
		c := float64(*v.GeocodeConfidence)
		d.GeocodeConfidence = &c
	}
	if v.MergedInto != nil {
		m := *v.MergedInto
		d.MergedInto = &m
	}
	for _, a := range arts {
		ia := IncidentArticle{ArticleId: a.ArticleID, Title: a.Title, Publisher: a.Publisher, Url: a.FinalUrl, PublishedAt: a.PublishedAt,
			MatchStatus: a.MatchStatus, MatchedBy: a.MatchedBy}
		fa := a.FetchedAt
		ia.FetchedAt = &fa
		if a.MatchScore != nil {
			sc := float64(*a.MatchScore)
			ia.MatchScore = &sc
		}
		d.Articles = append(d.Articles, ia)
	}
	for _, o := range obs {
		d.Observations = append(d.Observations, Observation{ArticleId: o.ArticleID, ObservedAt: o.ObservedAt, Data: jsonMap(o.Data)})
	}
	for _, e := range evs {
		d.Events = append(d.Events, Event{Id: e.ID, Type: e.EventType, Actor: e.Actor, CreatedAt: e.CreatedAt, Data: jsonMap(e.Data)})
	}
	return GetIncident200JSONResponse(d), nil
}

// ---- stats ----

func (s *Server) StatsSummary(ctx context.Context, req StatsSummaryRequestObject) (StatsSummaryResponseObject, error) {
	p := req.Params
	var vru []string
	if p.Vru != nil {
		vru = *p.Vru
	}
	f, err := newFilter(p.Bbox, dateP(p.From), dateP(p.To), p.State, p.City, vru, str(p.Severity), str(p.Verification), str(p.HitAndRun))
	if err != nil {
		return StatsSummary400JSONResponse{BadRequestJSONResponse{Error: err.Error()}}, nil
	}
	r, err := dbgen.New(s.store.Pool).StatsSummaryAPI(ctx, f.summary())
	if err != nil {
		return nil, err
	}
	return StatsSummary200JSONResponse{Incidents: r.Incidents, Fatalities: r.Fatalities, Injured: r.Injured, FatalIncidents: r.FatalIncidents,
		HitAndRun: r.HitAndRun, WithCoordinates: r.WithCoordinates, Corroborated: r.Corroborated, Articles: r.Articles}, nil
}

func (s *Server) StatsTimeseries(ctx context.Context, req StatsTimeseriesRequestObject) (StatsTimeseriesResponseObject, error) {
	p := req.Params
	var vru []string
	if p.Vru != nil {
		vru = *p.Vru
	}
	f, err := newFilter(p.Bbox, dateP(p.From), dateP(p.To), p.State, p.City, vru, str(p.Severity), str(p.Verification), str(p.HitAndRun))
	if err != nil {
		return StatsTimeseries400JSONResponse{BadRequestJSONResponse{Error: err.Error()}}, nil
	}
	bucket := "month"
	if p.Bucket != nil {
		bucket = string(*p.Bucket)
	}
	rows, err := dbgen.New(s.store.Pool).StatsTimeseriesAPI(ctx, f.timeseries(bucket))
	if err != nil {
		return nil, err
	}
	out := make([]TimeseriesPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, TimeseriesPoint{Bucket: openapi_types.Date{Time: r.Bucket.Time}, Incidents: r.Incidents, Fatalities: r.Fatalities, Injured: r.Injured})
	}
	return StatsTimeseries200JSONResponse(out), nil
}

func (s *Server) StatsBreakdown(ctx context.Context, req StatsBreakdownRequestObject) (StatsBreakdownResponseObject, error) {
	p := req.Params
	var vru []string
	if p.Vru != nil {
		vru = *p.Vru
	}
	f, err := newFilter(p.Bbox, dateP(p.From), dateP(p.To), p.State, p.City, vru, str(p.Severity), str(p.Verification), str(p.HitAndRun))
	if err != nil {
		return StatsBreakdown400JSONResponse{BadRequestJSONResponse{Error: err.Error()}}, nil
	}
	limit := 25
	if p.Limit != nil {
		limit = *p.Limit
	}
	rows, err := dbgen.New(s.store.Pool).StatsBreakdownAPI(ctx, f.breakdown(string(p.By), limit))
	if err != nil {
		return nil, err
	}
	out := make([]BreakdownRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, BreakdownRow{Key: r.Key, Incidents: r.Incidents, Fatalities: r.Fatalities, Injured: r.Injured})
	}
	return StatsBreakdown200JSONResponse(out), nil
}

// ---- reviews ----

func (s *Server) ListReviews(ctx context.Context, req ListReviewsRequestObject) (ListReviewsResponseObject, error) {
	status, limit, offset := "pending", 50, 0
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Offset != nil {
		offset = *req.Params.Offset
	}
	q := dbgen.New(s.store.Pool)
	rows, err := q.ListReviewsAPI(ctx, dbgen.ListReviewsAPIParams{Status: &status, MaxRows: int32(limit), SkipRows: int32(offset)})
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, 2*len(rows))
	for _, r := range rows {
		ids = append(ids, r.NewIncidentID, r.CandidateIncidentID)
	}
	views, err := q.ListIncidentsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]dbgen.IncidentsV, len(views))
	for _, v := range views {
		byID[v.ID] = v
	}
	out := make([]Review, 0, len(rows))
	for _, r := range rows {
		rv := Review{
			Id: r.ID, Status: ReviewStatus(r.Status), Score: float64(r.Score), Reasons: reasons(r.Reasons), CreatedAt: r.CreatedAt,
			DecidedBy: r.DecidedBy, DecidedAt: r.DecidedAt,
			NewIncident: incidentFromView(byID[r.NewIncidentID]), CandidateIncident: incidentFromView(byID[r.CandidateIncidentID]),
		}
		out = append(out, rv)
	}
	return ListReviews200JSONResponse(out), nil
}

func (s *Server) DecideReview(ctx context.Context, req DecideReviewRequestObject) (DecideReviewResponseObject, error) {
	if req.Body == nil || strings.TrimSpace(req.Body.Actor) == "" {
		return DecideReview400JSONResponse{BadRequestJSONResponse{Error: "actor is required"}}, nil
	}
	rev, err := dbgen.New(s.store.Pool).GetReviewNoLock(ctx, req.Id)
	if err != nil {
		if storage.IsNoRows(err) {
			return DecideReview404JSONResponse{NotFoundJSONResponse{Error: "review not found"}}, nil
		}
		return nil, err
	}
	if rev.Status != "pending" {
		return DecideReview409JSONResponse{Error: "review already " + rev.Status}, nil
	}
	out, err := s.wf.DecideReview(ctx, rev.NewIncidentID, pipeline.ReviewDecision{ReviewID: req.Id, Approve: req.Body.Approve, Actor: req.Body.Actor})
	if err != nil {
		if errors.Is(err, ErrNotPending) {
			return DecideReview409JSONResponse{Error: "review is no longer pending"}, nil
		}
		return nil, err
	}
	res := ReviewResult{ReviewId: out.ReviewID, Approved: out.Approved}
	if out.Target != uuid.Nil {
		t := out.Target
		res.Target = &t
	}
	return DecideReview200JSONResponse(res), nil
}

// ---- ops ----

func (s *Server) ListSources(ctx context.Context, req ListSourcesRequestObject) (ListSourcesResponseObject, error) {
	days := 30
	if req.Params.Days != nil {
		days = *req.Params.Days
	}
	q := dbgen.New(s.store.Pool)
	srcs, err := q.ListSources(ctx, false)
	if err != nil {
		return nil, err
	}
	stats, err := q.DiscoveryStats(ctx, s.now().AddDate(0, 0, -days))
	if err != nil {
		return nil, err
	}
	// A source can be found by several providers; sum the rows that name it.
	bySlug := map[string]discovery.SourceStats{}
	for _, st := range stats {
		a := bySlug[st.SourceSlug]
		a.Discoveries += st.Discoveries
		a.UniqueURLs += st.UniqueUrls
		a.ExclusiveURLs += st.ExclusiveUrls
		a.ArticlesFetched += st.ArticlesFetched
		a.FetchOK += st.FetchOk
		a.RelevantArticles += st.RelevantArticles
		a.Incidents += st.Incidents
		a.AvgDelaySeconds = st.AvgDelaySeconds
		bySlug[st.SourceSlug] = a
	}
	out := make([]SourceInfo, 0, len(srcs))
	for _, src := range srcs {
		st := bySlug[src.Slug]
		fsr, rr := st.FetchSuccessRate(), st.RelevantRate()
		avg := st.AvgDelaySeconds
		info := SourceInfo{
			Slug: src.Slug, Name: src.Name, Type: src.SourceType, Enabled: src.Enabled, PollIntervalSeconds: int(src.PollIntervalSeconds),
			LastSuccessAt: src.LastSuccessAt, ConsecutiveFailures: int(src.ConsecutiveFailures), ErrorState: src.ErrorState,
			Discoveries: &st.Discoveries, UniqueUrls: &st.UniqueURLs, ExclusiveUrls: &st.ExclusiveURLs, ArticlesFetched: &st.ArticlesFetched,
			Incidents: &st.Incidents, AvgDelaySeconds: &avg, FetchSuccessRate: &fsr, RelevantRate: &rr,
		}
		out = append(out, info)
	}
	// Schedule state is best effort and fetched concurrently under a short deadline, so an
	// unreachable Temporal slows this page by seconds at most, never by one timeout per source.
	if s.wf != nil {
		sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		var wg sync.WaitGroup
		for i := range out {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if sch, err := s.wf.Schedule(sctx, out[i].Slug); err == nil {
					out[i].Schedule = sch
				}
			}()
		}
		wg.Wait()
	}
	return ListSources200JSONResponse(out), nil
}

func (s *Server) ControlSchedule(ctx context.Context, req ControlScheduleRequestObject) (ControlScheduleResponseObject, error) {
	if req.Body == nil {
		return ControlSchedule400JSONResponse{BadRequestJSONResponse{Error: "body is required"}}, nil
	}
	if _, err := dbgen.New(s.store.Pool).GetSourceBySlug(ctx, req.Slug); err != nil {
		if storage.IsNoRows(err) {
			return ControlSchedule404JSONResponse{NotFoundJSONResponse{Error: "source not found"}}, nil
		}
		return nil, err
	}
	if err := s.wf.ControlSchedule(ctx, req.Slug, string(req.Body.Action)); err != nil {
		if errors.Is(err, ErrNoSchedule) {
			return ControlSchedule404JSONResponse{NotFoundJSONResponse{Error: "no schedule for this source yet; run `collisionctl schedules sync`"}}, nil
		}
		return nil, err
	}
	return ControlSchedule204Response{}, nil
}

func (s *Server) PipelineSummary(ctx context.Context, req PipelineSummaryRequestObject) (PipelineSummaryResponseObject, error) {
	days := 30
	if req.Params.Days != nil {
		days = *req.Params.Days
	}
	q := dbgen.New(s.store.Pool)
	c, err := q.PipelineCounts(ctx)
	if err != nil {
		return nil, err
	}
	spend, err := q.LLMSpendByDay(ctx, s.now().AddDate(0, 0, -days))
	if err != nil {
		return nil, err
	}
	out := PipelineSummary{LlmSpend: make([]LlmSpendRow, 0, len(spend))}
	out.Counts.DiscoveredUrls, out.Counts.Articles, out.Counts.FailedArticles = c.DiscoveredUrls, c.Articles, c.FailedArticles
	out.Counts.Extractions, out.Counts.PendingReviews = c.Extractions, c.PendingReviews
	for _, r := range spend {
		out.LlmSpend = append(out.LlmSpend, LlmSpendRow{Day: openapi_types.Date{Time: r.Day.Time}, Model: r.Model, Extractions: r.Extractions,
			InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CostUsd: r.CostUsd})
	}
	return PipelineSummary200JSONResponse(out), nil
}
