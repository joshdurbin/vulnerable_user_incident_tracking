//go:build integration

package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/pgrepo"
)

const seedYAML = `sources:
  - {slug: alpha, name: Alpha, type: rss, url: "https://alpha.example/feed", poll_interval: 30m}
  - {slug: beta, name: Beta, type: rss, url: "https://beta.example/feed", poll_interval: 1h, enabled: false}
  - {slug: search, name: Search, type: gdelt, poll_interval: 1h, coverage: {country: US}}
queries:
  - {family: pedestrian, name: ped, text: "(pedestrian OR walker) (struck OR hit)"}
`

func writeSeed(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sources.yaml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func slugs(t *testing.T) []string {
	t.Helper()
	srcs, err := pgrepo.New(store).Sources(context.Background(), false)
	require.NoError(t, err)
	var out []string
	for _, s := range srcs {
		out = append(out, s.Slug)
	}
	return out
}

func TestSeedFillsAnEmptyDatabaseAndSchedulesTheSources(t *testing.T) {
	pg.Reset(t, store)
	wf := &fakeWF{}
	require.NoError(t, SeedSources(context.Background(), store, wf, nil, writeSeed(t, seedYAML), zerolog.Nop()))
	assert.ElementsMatch(t, []string{"alpha", "beta", "search"}, slugs(t))
	assert.ElementsMatch(t, []string{"alpha", "beta", "search"}, wf.synced, "seeded sources get their schedules")
	qs, err := pgrepo.New(store).Queries(context.Background(), "gdelt")
	require.NoError(t, err)
	assert.Len(t, qs, 1)
	beta, err := pgrepo.New(store).Source(context.Background(), "beta")
	require.NoError(t, err)
	assert.False(t, beta.Enabled, "enabled: false in the file is respected")
}

func TestSeedDoesNothingOnceAnySourceExists(t *testing.T) {
	pg.Reset(t, store)
	ctx := context.Background()
	path := writeSeed(t, seedYAML)
	require.NoError(t, SeedSources(ctx, store, &fakeWF{}, nil, path, zerolog.Nop()))

	// An operator edits and deletes in the admin UI; the next start must not undo it.
	repo := pgrepo.New(store)
	require.NoError(t, repo.DeleteSource(ctx, "alpha"))
	wf := &fakeWF{}
	require.NoError(t, SeedSources(ctx, store, wf, nil, path, zerolog.Nop()))
	assert.ElementsMatch(t, []string{"beta", "search"}, slugs(t), "a deleted source stays deleted while others remain")
	assert.Empty(t, wf.synced, "nothing seeded, so no schedule work")

	// Only a completely empty source table is seeded again.
	require.NoError(t, repo.DeleteSource(ctx, "beta"))
	require.NoError(t, repo.DeleteSource(ctx, "search"))
	require.NoError(t, SeedSources(ctx, store, &fakeWF{}, nil, path, zerolog.Nop()))
	assert.Len(t, slugs(t), 3)
}

func TestSeedWithoutAFileIsANoOp(t *testing.T) {
	pg.Reset(t, store)
	require.NoError(t, SeedSources(context.Background(), store, &fakeWF{}, nil, "", zerolog.Nop()))
	assert.Empty(t, slugs(t))
}

func TestSeedRefusesABadFileAndWritesNothing(t *testing.T) {
	pg.Reset(t, store)
	ctx := context.Background()
	assert.Error(t, SeedSources(ctx, store, &fakeWF{}, nil, filepath.Join(t.TempDir(), "missing.yaml"), zerolog.Nop()), "a configured file that is missing")
	assert.Error(t, SeedSources(ctx, store, &fakeWF{}, nil, writeSeed(t, "sources: [{slug: x, nope: 1}]"), zerolog.Nop()), "an unknown key")
	err := SeedSources(ctx, store, &fakeWF{}, nil, writeSeed(t, `sources:
  - {slug: good, name: Good, type: rss, url: "https://good.example/feed", poll_interval: 30m}
  - {slug: bad, name: Bad, type: rss, poll_interval: 30m}
`), zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "problem")
	assert.Empty(t, slugs(t), "one bad entry means none are written")
}

func TestSeedKeepsTheSourcesWhenTemporalIsDown(t *testing.T) {
	pg.Reset(t, store)
	wf := &fakeWF{syncErr: errors.New("temporal unavailable")}
	require.NoError(t, SeedSources(context.Background(), store, wf, nil, writeSeed(t, seedYAML), zerolog.Nop()),
		"startup must not fail because schedules could not be created")
	assert.Len(t, slugs(t), 3)
}

func TestReplicasStartingTogetherSeedOnce(t *testing.T) {
	pg.Reset(t, store)
	path := writeSeed(t, seedYAML)
	var wg sync.WaitGroup
	var mu sync.Mutex
	seededBy := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wf := &fakeWF{}
			assert.NoError(t, SeedSources(context.Background(), store, wf, nil, path, zerolog.Nop()))
			if len(wf.synced) > 0 {
				mu.Lock()
				seededBy++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Len(t, slugs(t), 3)
	assert.Equal(t, 1, seededBy, "exactly one replica did the seeding")
}

func TestShippedStarterSetSeedsWithTheRealProviders(t *testing.T) {
	pg.Reset(t, store)
	require.NoError(t, SeedSources(context.Background(), store, &fakeWF{}, nil, "../../docs/examples/sources.yaml", zerolog.Nop()))
	assert.NotEmpty(t, slugs(t))
}
