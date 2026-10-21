package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/incidents"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/pipeline"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
)

// Bulk jobs (backfills and reprocess runs) and incident corrections. Like the rest of /admin, the
// bearer-token middleware has already run before these handlers.

func jobToAPI(j pipeline.Job) Job {
	out := Job{Id: j.ID, Kind: JobKind(j.Kind), Status: JobStatus(j.Status), StartedAt: j.StartedAt, ClosedAt: j.ClosedAt}
	if b := j.Backfill; b != nil {
		out.Backfill = &BackfillProgress{
			Phase: BackfillProgressPhase(b.Phase), StepsDone: b.StepsDone, TotalSteps: b.TotalSteps, Found: b.Found,
			ArticlesStarted: b.Started, MaxArticles: b.MaxArticles, Current: nonEmptyPtr(b.Current),
		}
	}
	if r := j.Reprocess; r != nil {
		out.Reprocess = &ReprocessProgress{Examined: r.Examined, Changed: r.Changed, Failed: r.Failed}
	}
	return out
}

func (s *Server) ListJobs(ctx context.Context, req ListJobsRequestObject) (ListJobsResponseObject, error) {
	limit := 20
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	jobs, err := s.wf.ListJobs(ctx, string(req.Params.Kind), limit)
	if err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobToAPI(j))
	}
	return ListJobs200JSONResponse(out), nil
}

func (s *Server) GetJob(ctx context.Context, req GetJobRequestObject) (GetJobResponseObject, error) {
	j, err := s.wf.GetJob(ctx, req.Id)
	if errors.Is(err, pipeline.ErrJobNotFound) {
		return GetJob404JSONResponse{NotFoundJSONResponse{Error: "job not found"}}, nil
	}
	if err != nil {
		return nil, err
	}
	return GetJob200JSONResponse(jobToAPI(*j)), nil
}

func (s *Server) ControlJob(ctx context.Context, req ControlJobRequestObject) (ControlJobResponseObject, error) {
	if req.Body == nil {
		return ControlJob400JSONResponse{BadRequestJSONResponse{Error: "body is required"}}, nil
	}
	err := s.wf.ControlJob(ctx, req.Id, string(req.Body.Action))
	switch {
	case errors.Is(err, pipeline.ErrJobNotFound):
		return ControlJob404JSONResponse{NotFoundJSONResponse{Error: "job not found"}}, nil
	case errors.Is(err, pipeline.ErrJobAction):
		return ControlJob409JSONResponse{Error: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	s.cfg.Log.Info().Str("job", req.Id).Str("action", string(req.Body.Action)).Msg("job controlled")
	return ControlJob204Response{}, nil
}

// maxBackfillRange keeps one request from describing decades of windows.
const maxBackfillRange = 10 * 365 * 24 * time.Hour

func (s *Server) StartBackfill(ctx context.Context, req StartBackfillRequestObject) (StartBackfillResponseObject, error) {
	if req.Body == nil {
		return StartBackfill400JSONResponse{BadRequestJSONResponse{Error: "body is required"}}, nil
	}
	b := req.Body
	var errs []error
	from, to := b.From.Time, b.To.Time
	if !to.After(from) {
		errs = append(errs, errors.New("to must be after from"))
	} else if to.Sub(from) > maxBackfillRange {
		errs = append(errs, errors.New("the range may not exceed 10 years"))
	}
	seen := map[string]bool{}
	for _, slug := range b.Sources {
		if seen[slug] {
			errs = append(errs, fmt.Errorf("source %q is listed twice", slug))
			continue
		}
		seen[slug] = true
		if _, err := s.repo.Source(ctx, slug); errors.Is(err, discovery.ErrNotFound) {
			errs = append(errs, fmt.Errorf("source %q does not exist", slug))
		} else if err != nil {
			return nil, err
		}
	}
	if len(errs) > 0 {
		return StartBackfill400JSONResponse{invalid("backfill", errs)}, nil
	}
	in := pipeline.BackfillInput{Slugs: b.Sources, From: from, To: to, WindowSize: 24 * time.Hour, MaxArticles: 1000}
	if b.WindowHours != nil {
		in.WindowSize = time.Duration(*b.WindowHours) * time.Hour
	}
	if b.MaxArticles != nil {
		in.MaxArticles = *b.MaxArticles
	}
	id, err := s.wf.StartBackfill(ctx, in)
	if err != nil {
		return nil, err
	}
	s.cfg.Log.Info().Str("job", id).Strs("sources", b.Sources).Msg("backfill started")
	return StartBackfill201JSONResponse(jobToAPI(pipeline.Job{
		ID: id, Kind: pipeline.JobBackfill, Status: "running", StartedAt: s.now().UTC(),
	})), nil
}

func (s *Server) StartReprocess(ctx context.Context, req StartReprocessRequestObject) (StartReprocessResponseObject, error) {
	in := pipeline.ReprocessInput{Limit: 100, BatchSize: 20, MaxConcurrent: 4}
	if b := req.Body; b != nil {
		if b.Limit != nil {
			in.Limit = *b.Limit
		}
		if b.BatchSize != nil {
			in.BatchSize = *b.BatchSize
		}
		if b.Concurrency != nil {
			in.MaxConcurrent = *b.Concurrency
		}
	}
	id, err := s.wf.StartReprocess(ctx, in)
	if err != nil {
		return nil, err
	}
	s.cfg.Log.Info().Str("job", id).Int("limit", in.Limit).Msg("reprocess started")
	return StartReprocess201JSONResponse(jobToAPI(pipeline.Job{
		ID: id, Kind: pipeline.JobReprocess, Status: "running", StartedAt: s.now().UTC(),
	})), nil
}

func (s *Server) SplitIncident(ctx context.Context, req SplitIncidentRequestObject) (SplitIncidentResponseObject, error) {
	if req.Body == nil || strings.TrimSpace(req.Body.Actor) == "" {
		return SplitIncident400JSONResponse{BadRequestJSONResponse{Error: "actor is required"}}, nil
	}
	newID, err := s.res.SplitArticle(ctx, req.Id, req.Body.ArticleId, strings.TrimSpace(req.Body.Actor))
	switch {
	case storage.IsNoRows(err):
		return SplitIncident404JSONResponse{NotFoundJSONResponse{Error: "incident not found"}}, nil
	case errors.Is(err, incidents.ErrSplitSingle), errors.Is(err, incidents.ErrNotLinked):
		return SplitIncident409JSONResponse{Error: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	s.cfg.Log.Info().Stringer("incident", req.Id).Stringer("article", req.Body.ArticleId).Stringer("new_incident", newID).
		Str("actor", req.Body.Actor).Msg("article split into a new incident")
	return SplitIncident200JSONResponse{NewIncidentId: newID}, nil
}
