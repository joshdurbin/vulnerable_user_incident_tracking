//go:build integration

package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/crawler"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/dedupe"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage/dbgen"
)

// wire is an article-length text (about 500 words) built from distinct sentences.
func wire(subject string) string {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "Police said the %s incident number %d happened near the intersection of block %d and was reported by a passerby. ", subject, i, i*7)
	}
	return b.String()
}

func store1(t *testing.T, a *Activities, url, canonical, text string) *FetchResult {
	t.Helper()
	ref := URLRef{URL: url}
	row, err := store.Queries().UpsertDiscoveredURL(context.Background(), dbgen.UpsertDiscoveredURLParams{NormalizedUrl: url, Domain: "news.test"})
	require.NoError(t, err)
	ref.ID = row.ID
	res, err := a.StoreArticle(context.Background(), StoreInput{Ref: ref, Response: crawler.ArticleResponse{
		RequestedURL: url, FinalURL: url, CanonicalURL: canonical, Title: "t", Text: text, HTTPStatus: 200,
		FetchedAt: time.Now().UTC().Format(time.RFC3339), ExtractionStatus: "OK",
	}})
	require.NoError(t, err)
	return res
}

func TestStoreArticleMarksCopiesOfEarlierArticles(t *testing.T) {
	reset(t)
	a := newActivities(&fakeClassifier{})
	story := wire("pedestrian")

	orig := store1(t, a, "https://gazette.test/story", "", story)
	assert.Empty(t, orig.DuplicateKind, "the first sighting is the original")

	t.Run("identical text under another URL", func(t *testing.T) {
		r := store1(t, a, "https://mirror.test/copy", "", story)
		assert.Equal(t, dedupe.KindContentHash, r.DuplicateKind)
		assert.Equal(t, orig.ArticleID, r.DuplicateOf)
	})
	t.Run("same canonical URL, different text", func(t *testing.T) {
		r := store1(t, a, "https://gazette.test/amp/story", "https://gazette.test/story?utm_source=x", "Updated: "+wire("pedestrian")[:300])
		assert.Equal(t, dedupe.KindCanonicalURL, r.DuplicateKind)
		assert.Equal(t, orig.ArticleID, r.DuplicateOf)
	})
	t.Run("republished with small edits", func(t *testing.T) {
		edited := "UPDATE: " + strings.Replace(story, "number 3 ", "number three ", 1) + " Originally published by the Metro Courier."
		r := store1(t, a, "https://courier.test/story", "", edited)
		assert.Equal(t, dedupe.KindNearDuplicate, r.DuplicateKind)
		assert.Equal(t, orig.ArticleID, r.DuplicateOf)
	})
	t.Run("a copy of a copy points at the original", func(t *testing.T) {
		edited := strings.Replace(story, "number 5 ", "number five ", 1)
		first := store1(t, a, "https://a.test/s", "", edited)
		second := store1(t, a, "https://b.test/s", "", edited)
		require.Equal(t, orig.ArticleID, first.DuplicateOf)
		assert.Equal(t, orig.ArticleID, second.DuplicateOf, "not the intermediate copy")
	})
	t.Run("a different story is not a duplicate", func(t *testing.T) {
		r := store1(t, a, "https://other.test/story", "", wire("cyclist"))
		assert.Empty(t, r.DuplicateKind)
	})
	t.Run("short texts are only matched exactly", func(t *testing.T) {
		s1 := store1(t, a, "https://brief.test/1", "", "A pedestrian was hit on Main Street. Police are investigating.")
		s2 := store1(t, a, "https://brief.test/2", "", "A pedestrian was hit on Main Street. Police are investigating it.")
		assert.Empty(t, s1.DuplicateKind)
		assert.Empty(t, s2.DuplicateKind, "no SimHash for short text")
	})
}

func TestStoredCopyIsRememberedOnRefetch(t *testing.T) {
	reset(t)
	a := newActivities(&fakeClassifier{})
	story := wire("scooter")
	orig := store1(t, a, "https://gazette.test/s", "", story)
	copy1 := store1(t, a, "https://mirror.test/s", "", story)
	again, err := a.FindArticle(context.Background(), URLRef{ID: lookupURL(t, "https://mirror.test/s"), URL: "https://mirror.test/s"})
	require.NoError(t, err)
	assert.Equal(t, copy1.ArticleID, again.ArticleID)
	assert.Equal(t, orig.ArticleID, again.DuplicateOf, "a later workflow for the same URL must also skip classification")
	assert.Equal(t, dedupe.KindContentHash, again.DuplicateKind)
}

func lookupURL(t *testing.T, u string) uuid.UUID {
	t.Helper()
	row, err := store.Queries().UpsertDiscoveredURL(context.Background(), dbgen.UpsertDiscoveredURLParams{NormalizedUrl: u, Domain: "news.test"})
	require.NoError(t, err)
	return row.ID
}

func TestReprocessSkipsExactDuplicates(t *testing.T) {
	reset(t)
	a := newActivities(&fakeClassifier{})
	story := wire("motorcyclist")
	orig := store1(t, a, "https://gazette.test/s", "", story)
	store1(t, a, "https://mirror.test/s", "", story)

	ids, err := store.Queries().ListArticlesToReprocess(context.Background(), dbgen.ListArticlesToReprocessParams{
		After: uuid.Nil, Model: "m", PromptVersion: "v", SchemaVersion: "v", MaxRows: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{orig.ArticleID}, ids)
}

func TestFingerprintArticlesFillsInOlderArticlesAndIsRepeatable(t *testing.T) {
	reset(t)
	ctx := context.Background()
	q := store.Queries()
	text := wire("wheelchair")
	// An article stored before fingerprints existed: text, but no key or SimHash.
	old, err := q.InsertArticle(ctx, dbgen.InsertArticleParams{RequestedUrl: "https://old.test/s?utm_source=x", FinalUrl: "https://old.test/s?utm_source=x",
		BodyText: &text, Status: "fetched", FetchedAt: time.Now()})
	require.NoError(t, err)
	require.Nil(t, old.CanonicalKey)

	n, err := FingerprintArticles(ctx, q, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	got, err := q.GetArticle(ctx, old.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CanonicalKey)
	assert.Equal(t, "https://old.test/s", *got.CanonicalKey)
	require.NotNil(t, got.Simhash)

	n, err = FingerprintArticles(ctx, q, 10)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left to do")

	// Now a new copy of it is recognised.
	r := store1(t, newActivities(&fakeClassifier{}), "https://copy.test/s", "", text)
	assert.Equal(t, old.ID, r.DuplicateOf)
}
