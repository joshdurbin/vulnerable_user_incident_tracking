package api

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/search"
)

// searchTimeout bounds a call to OpenSearch made while a person waits.
const searchTimeout = 8 * time.Second

// rankedIDs asks the search index for the best matches of q, best first. ok is false, with a message to
// show, when search is not configured or cannot be reached.
func (s *Server) rankedIDs(ctx context.Context, q string) (ids []uuid.UUID, msg string, ok bool) {
	if s.cfg.Search == nil {
		return nil, "search is not configured on this server", false
	}
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	hits, err := s.cfg.Search.Search(ctx, strings.TrimSpace(q), search.MaxHits)
	if err != nil {
		s.cfg.Log.Error().Err(err).Msg("search failed")
		return nil, "search is not available right now", false
	}
	ids = make([]uuid.UUID, 0, len(hits))
	for _, h := range hits {
		if id, err := uuid.Parse(h.ID); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, "", true
}

func (s *Server) GetSearchStatus(ctx context.Context, _ GetSearchStatusRequestObject) (GetSearchStatusResponseObject, error) {
	st, err := s.searchStatus(ctx)
	if err != nil {
		return nil, err
	}
	return GetSearchStatus200JSONResponse(st), nil
}

func (s *Server) ReindexSearch(ctx context.Context, _ ReindexSearchRequestObject) (ReindexSearchResponseObject, error) {
	if s.cfg.Search == nil {
		return ReindexSearch503JSONResponse{Error: "search is not configured on this server"}, nil
	}
	if err := search.Reset(ctx, s.store); err != nil {
		return nil, err
	}
	// Run it now rather than at the next minute. If the schedule is missing, the next "Sync schedules"
	// creates it and the reset watermark makes that first run do the whole job.
	if err := s.wf.TriggerSearchIndex(ctx); err != nil {
		s.cfg.Log.Warn().Err(err).Msg("could not start the search indexer; it will run on its next schedule")
	}
	s.cfg.Log.Info().Msg("search reindex requested")
	st, err := s.searchStatus(ctx)
	if err != nil {
		return nil, err
	}
	return ReindexSearch202JSONResponse(st), nil
}

func (s *Server) searchStatus(ctx context.Context) (SearchStatus, error) {
	out := SearchStatus{Enabled: s.cfg.Search != nil, Index: search.Index}
	if !out.Enabled {
		return out, nil
	}
	st, err := search.StatusOf(ctx, s.store)
	if err != nil {
		return out, err
	}
	out.Pending, out.Watermark, out.IndexedAt = st.Pending, st.Watermark, st.IndexedAt
	cctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	if n, err := s.cfg.Search.Count(cctx); err != nil {
		msg := "OpenSearch cannot be reached"
		out.Error = &msg
		s.cfg.Log.Warn().Err(err).Msg("search status: count failed")
	} else {
		out.Documents = &n
	}
	return out, nil
}
