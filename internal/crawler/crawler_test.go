package crawler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/joshdurbin/fuck_cars/internal/domain"
)

func TestArticleResponseConversion(t *testing.T) {
	pub := "2026-10-13T08:30:00+00:00"
	a := ArticleResponse{
		RequestedURL: "https://x.test/a", FinalURL: "https://x.test/a", Title: "T", Text: "body", PublishedAt: &pub,
		FetchedAt: "2026-10-14T01:02:03Z", HTTPStatus: 200, ExtractionStatus: "OK",
	}.ToArticle()
	require.Equal(t, domain.ExtractionOK, a.ExtractionStatus)
	require.Equal(t, time.Date(2026, 10, 13, 8, 30, 0, 0, time.UTC), a.PublishedAt.UTC())
	require.Equal(t, 2026, a.FetchedAt.Year())
}

// An unrecognised status must never read as success.
func TestUnknownStatusIsAFailure(t *testing.T) {
	require.Equal(t, domain.ExtractionFetchFailed, ArticleResponse{ExtractionStatus: "WAT"}.ToArticle().ExtractionStatus)
	require.Equal(t, domain.ExtractionFetchFailed, ArticleResponse{}.ToArticle().ExtractionStatus)
}

func TestStatusesCoverEveryPythonValue(t *testing.T) {
	for wire, want := range map[string]domain.ExtractionStatus{
		"OK": domain.ExtractionOK, "NO_CONTENT": domain.ExtractionNoContent, "FETCH_FAILED": domain.ExtractionFetchFailed,
		"BLOCKED": domain.ExtractionBlocked, "ROBOTS_DISALLOWED": domain.ExtractionRobotsDisallowed, "UNSUPPORTED": domain.ExtractionUnsupported,
	} {
		require.Equal(t, want, ArticleResponse{ExtractionStatus: wire}.ToArticle().ExtractionStatus, wire)
	}
}

// The JSON field names are the contract with activities.py.
func TestRequestJSONFieldNames(t *testing.T) {
	b, err := json.Marshal(ToFetchRequest(domain.FetchRequest{URL: "u", Timeout: 1500 * time.Millisecond, CorrelationID: "c"}))
	require.NoError(t, err)
	require.JSONEq(t, `{"url":"u","rendering":"http_only","timeout_s":1.5,"correlation_id":"c"}`, string(b))

	b, err = json.Marshal(ToLinksRequest(domain.DiscoverLinksRequest{SeedURL: "s", MaxPages: 3, MaxDepth: 1, MaxDuration: time.Minute}))
	require.NoError(t, err)
	require.JSONEq(t, `{"seed_url":"s","allowed_domains":[],"url_patterns":[],"max_pages":3,"max_depth":1,"max_duration_s":60,"rendering":"http_only"}`, string(b))
}
