//go:build integration

package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/incidents"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/search"
)

type fakeSearcher struct {
	hits     []search.Hit
	err      error
	count    int64
	countErr error
	asked    []string
}

func (f *fakeSearcher) Search(_ context.Context, q string, _ int) ([]search.Hit, error) {
	f.asked = append(f.asked, q)
	return f.hits, f.err
}
func (f *fakeSearcher) Count(context.Context) (int64, error) { return f.count, f.countErr }

func serverWithSearch(t *testing.T, wf *fakeWF, s Searcher) *httptest.Server {
	t.Helper()
	pg.Reset(t, store)
	h, err := NewServer(store, wf, Config{AdminToken: token, Log: zerolog.Nop(), Search: s}).Handler()
	require.NoError(t, err)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func incidentsResolver() *incidents.Resolver {
	return incidents.NewResolver(store, incidents.DefaultConfig(), zerolog.Nop())
}

func hit(id string) search.Hit { return search.Hit{ID: id} }

func TestTextSearchRefusesPlainlyWhenSearchIsNotConfigured(t *testing.T) {
	ts := serverWithSearch(t, &fakeWF{}, nil)
	var e Error
	require.Equal(t, 503, call(t, ts, "GET", "/v1/incidents?q=oakland", "", nil, &e))
	assert.Contains(t, e.Error, "not configured")
	assert.Equal(t, 200, get(t, ts, "/v1/incidents", nil), "everything else keeps working")
	assert.Equal(t, 200, get(t, ts, "/v1/incidents?q=%20%20", nil), "a blank query is no query")
}

func TestTextSearchReturnsIncidentsInRankOrderAndOtherFiltersNarrowThem(t *testing.T) {
	fs := &fakeSearcher{}
	ts := serverWithSearch(t, &fakeWF{}, fs)
	r := incidentsResolver()
	a := seed(t, r, "a.example", cand("2026-10-10", "Oakland", "CA", "100 Broadway", "fatal", []string{"pedestrian"}, nil, ip(1)), f64(37.80), f64(-122.27))
	b := seed(t, r, "b.example", cand("2026-09-02", "Oakland", "CA", "900 Telegraph Ave", "serious", []string{"bicyclist"}, ip(1), nil), f64(37.81), f64(-122.26))
	c := seed(t, r, "c.example", cand("2026-09-20", "Portland", "OR", "55 Burnside St", "fatal", []string{"scooter_rider"}, nil, ip(1)), nil, nil)

	// The search ranks c first, then a. b is not a match, so it is not returned even though it is newer than c.
	fs.hits = []search.Hit{hit(c.IncidentID.String()), hit(a.IncidentID.String())}
	var list IncidentList
	require.Equal(t, 200, get(t, ts, "/v1/incidents?q="+url.QueryEscape("fatal"), &list))
	require.Len(t, list.Items, 2)
	assert.Equal(t, c.IncidentID, list.Items[0].Id, "best match first, not newest first")
	assert.Equal(t, a.IncidentID, list.Items[1].Id)
	assert.EqualValues(t, 2, list.Total)
	assert.Equal(t, []string{"fatal"}, fs.asked)

	// Filters apply on top of the matches.
	require.Equal(t, 200, get(t, ts, "/v1/incidents?q=fatal&state=ca", &list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, a.IncidentID, list.Items[0].Id)
	require.Equal(t, 200, get(t, ts, "/v1/incidents?q=fatal&vru=scooter_rider", &list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, c.IncidentID, list.Items[0].Id)

	// Without q the order is by date again.
	require.Equal(t, 200, get(t, ts, "/v1/incidents", &list))
	require.Len(t, list.Items, 3)
	assert.Equal(t, a.IncidentID, list.Items[0].Id)
	_ = b

	// Paging walks the ranked list.
	fs.hits = []search.Hit{hit(c.IncidentID.String()), hit(a.IncidentID.String()), hit(b.IncidentID.String())}
	require.Equal(t, 200, get(t, ts, "/v1/incidents?q=x&limit=2&offset=2", &list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, b.IncidentID, list.Items[0].Id)
	assert.EqualValues(t, 3, list.Total)
}

func TestTextSearchWithNoMatchesOrAnUnreachableIndex(t *testing.T) {
	fs := &fakeSearcher{}
	ts := serverWithSearch(t, &fakeWF{}, fs)
	seed(t, incidentsResolver(), "a.example", cand("2026-10-10", "Oakland", "CA", "100 Broadway", "fatal", []string{"pedestrian"}, nil, ip(1)), f64(37.80), f64(-122.27))

	var list IncidentList
	require.Equal(t, 200, get(t, ts, "/v1/incidents?q=nothing", &list))
	assert.Empty(t, list.Items)
	assert.Zero(t, list.Total, "no matches is an empty list, not everything")

	// The index knows an incident the database no longer lists (merged away since): it is simply absent.
	fs.hits = []search.Hit{hit("00000000-0000-0000-0000-000000000001"), hit("not-a-uuid")}
	require.Equal(t, 200, get(t, ts, "/v1/incidents?q=ghost", &list))
	assert.Empty(t, list.Items)

	fs.err = errors.New("connection refused")
	var e Error
	require.Equal(t, 503, call(t, ts, "GET", "/v1/incidents?q=oakland", "", nil, &e))
	assert.Equal(t, "search is not available right now", e.Error, "the cause stays in the log, not the response")
}

func TestSearchStatusAndReindex(t *testing.T) {
	var st SearchStatus
	// Not configured.
	ts := serverWithSearch(t, &fakeWF{}, nil)
	st = SearchStatus{}
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/search", token, nil, &st))
	assert.False(t, st.Enabled)
	assert.Equal(t, 503, call(t, ts, "POST", "/v1/admin/search/reindex", token, nil, nil))

	// Configured.
	fs := &fakeSearcher{count: 41}
	wf := &fakeWF{}
	ts = serverWithSearch(t, wf, fs)
	seed(t, incidentsResolver(), "a.example", cand("2026-10-10", "Oakland", "CA", "100 Broadway", "fatal", []string{"pedestrian"}, nil, ip(1)), f64(37.80), f64(-122.27))
	st = SearchStatus{}
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/search", token, nil, &st))
	assert.True(t, st.Enabled)
	assert.Equal(t, search.Index, st.Index)
	assert.EqualValues(t, 41, *st.Documents)
	assert.EqualValues(t, 1, st.Pending, "the seeded incident has not been indexed")
	assert.Nil(t, st.Watermark)

	// The indexer has been through everything.
	_, err := store.Pool.Exec(context.Background(), `INSERT INTO search_index_state (name, updated_at, last_id) SELECT 'incidents', max(updated_at), (array_agg(id ORDER BY updated_at DESC, id DESC))[1] FROM incidents`)
	require.NoError(t, err)
	st = SearchStatus{}
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/search", token, nil, &st))
	assert.Zero(t, st.Pending)
	assert.NotNil(t, st.Watermark)

	// Reindex forgets the watermark and asks for an immediate run.
	st = SearchStatus{}
	require.Equal(t, 202, call(t, ts, "POST", "/v1/admin/search/reindex", token, nil, &st))
	assert.EqualValues(t, 1, st.Pending, "everything is pending again")
	assert.Nil(t, st.Watermark)
	assert.Equal(t, 1, wf.searchTriggers)

	// A missing schedule does not undo the reset: the next sync creates it and the run does the whole job.
	wf.searchErr = ErrNoSchedule
	st = SearchStatus{}
	require.Equal(t, 202, call(t, ts, "POST", "/v1/admin/search/reindex", token, nil, &st))

	// OpenSearch down: the status says so rather than failing.
	fs.countErr = errors.New("boom")
	st = SearchStatus{}
	require.Equal(t, 200, call(t, ts, "GET", "/v1/admin/search", token, nil, &st))
	assert.Nil(t, st.Documents)
	require.NotNil(t, st.Error)
	assert.NotContains(t, *st.Error, "boom")
}
