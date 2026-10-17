//go:build integration

package pipeline

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/joshdurbin/fuck_cars/internal/discovery"
	"github.com/joshdurbin/fuck_cars/internal/domain"
	"github.com/joshdurbin/fuck_cars/internal/incidents"
	"github.com/joshdurbin/fuck_cars/internal/storage"
	"github.com/joshdurbin/fuck_cars/internal/storage/dbgen"
	"github.com/joshdurbin/fuck_cars/internal/testutil"
)

var (
	pg    *testutil.Postgres
	store *storage.Store
	tc    client.Client
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	if pg, err = testutil.StartPostgres(ctx); err != nil {
		println("cannot start postgres:", err.Error())
		os.Exit(1)
	}
	if store, err = storage.Open(ctx, pg.DSN); err != nil {
		println(err.Error())
		os.Exit(1)
	}
	tcont, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "temporalio/temporal:latest",
			Cmd:          []string{"server", "start-dev", "--ip", "0.0.0.0", "--headless"},
			ExposedPorts: []string{"7233/tcp"},
			WaitingFor:   wait.ForListeningPort("7233/tcp").WithStartupTimeout(120 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		println("cannot start temporal:", err.Error())
		os.Exit(1)
	}
	host, _ := tcont.Host(ctx)
	port, _ := tcont.MappedPort(ctx, "7233/tcp")
	addr := fmt.Sprintf("%s:%s", host, port.Port())
	deadline := time.Now().Add(60 * time.Second)
	for {
		tc, err = Dial(addr, "default", zerolog.Nop())
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			println("cannot connect to temporal:", err.Error())
			os.Exit(1)
		}
		time.Sleep(time.Second)
	}
	code := m.Run()
	tc.Close()
	store.Close()
	_ = tcont.Terminate(ctx)
	pg.Terminate(ctx)
	os.Exit(code)
}

// ---- fakes: the only things not real in this test are the web and the LLM ----

type fakeFetcher struct {
	mu       sync.Mutex
	attempts map[string]int
	delay    time.Duration
}

func (f *fakeFetcher) FetchArticle(ctx context.Context, req domain.FetchRequest) (*domain.Article, error) {
	f.mu.Lock()
	f.attempts[req.URL]++
	n := f.attempts[req.URL]
	f.mu.Unlock()
	time.Sleep(f.delay)
	k, pub := parseURL(req.URL)
	// Fault injection: every third article fails on its first attempt, like a crashed crawler.
	if k%3 == 0 && n == 1 {
		return nil, fmt.Errorf("injected crawler outage")
	}
	return &domain.Article{
		RequestedURL: req.URL, FinalURL: req.URL, Title: "story " + req.URL, Publisher: "pub" + strconv.Itoa(pub),
		Text: fmt.Sprintf("collision %d reported by pub%d", k, pub), HTTPStatus: 200, FetchedAt: time.Now(),
		ExtractionStatus: domain.ExtractionOK,
	}, nil
}

func parseURL(u string) (collision, pub int) {
	parts := strings.Split(strings.TrimPrefix(u, "https://news.test/"), "/")
	collision, _ = strconv.Atoi(parts[0])
	pub, _ = strconv.Atoi(parts[1])
	return
}

type fakeClassifier struct{ calls atomic.Int64 }

func (c *fakeClassifier) Versions() (string, string, string) { return "fake-model", "v1", "v1" }
func (c *fakeClassifier) Classify(ctx context.Context, a *domain.Article) (*domain.ExtractionResult, error) {
	c.calls.Add(1)
	var k, p int
	fmt.Sscanf(a.Text, "collision %d reported by pub%d", &k, &p)
	inj := 1
	return &domain.ExtractionResult{
		Verdict: domain.VerdictConfirmed, ArticleKind: domain.KindCurrent,
		Incidents: []domain.IncidentCandidate{{
			OccurredAt: fmt.Sprintf("2026-10-%02d", 1+k%25), OccurredAtPrecision: "date",
			City: fmt.Sprintf("Testville%c", 'A'+rune(k)), State: "CA", StreetAddress: fmt.Sprintf("%d Main Street", 100+k),
			LocationPrecision: "street", VRUCategories: []string{"pedestrian"}, VehicleCategories: []string{"passenger_car"},
			InjuredCount: &inj, Severity: "serious", HitAndRun: "no", FactsStatus: "confirmed",
		}},
		Meta: domain.ExtractionMeta{Model: "fake-model", PromptVersion: "v1", SchemaVersion: "v1", StructuredMode: "json_schema", ExtractedAt: time.Now()},
	}, nil
}

func newActivities(f *fakeFetcher, cl *fakeClassifier) *Activities {
	return &Activities{
		DB: store, Fetcher: f, Classifier: cl, Geocoder: NoopGeocoder{},
		Resolver: incidents.NewResolver(store, incidents.DefaultConfig(), zerolog.Nop()),
		Temporal: tc, Log: zerolog.Nop(),
	}
}

func startWorkers(t *testing.T, a *Activities) []worker.Worker {
	t.Helper()
	ws := NewWorkers(tc, a, DefaultWorkerConfig())
	for _, w := range ws {
		require.NoError(t, w.Start())
	}
	return ws
}

func stop(ws []worker.Worker) {
	for _, w := range ws {
		w.Stop()
	}
}

func reset(t *testing.T) {
	pg.Reset(t, store)
}

func urlsFor(t *testing.T, collisions, pubs int) []URLRef {
	var refs []URLRef
	for k := 0; k < collisions; k++ {
		for p := 0; p < pubs; p++ {
			u := fmt.Sprintf("https://news.test/%d/%d", k, p)
			row, err := store.Queries().UpsertDiscoveredURL(context.Background(), dbgen.UpsertDiscoveredURLParams{NormalizedUrl: u, Domain: "news.test"})
			require.NoError(t, err)
			refs = append(refs, URLRef{ID: row.ID, URL: u})
		}
	}
	return refs
}

// Milestone 5 acceptance: workers disappear mid-run and come back (forcing every
// open workflow to replay from history), a crawler outage is injected, and the
// result is still exactly one incident per collision with every article linked
// and no article classified twice.
func TestPipelineSurvivesWorkerRestartAndFaults(t *testing.T) {
	reset(t)
	ctx := context.Background()
	const collisions, pubs = 12, 3
	refs := urlsFor(t, collisions, pubs)
	fetch := &fakeFetcher{attempts: map[string]int{}, delay: 20 * time.Millisecond}
	cl := &fakeClassifier{}

	ws := startWorkers(t, newActivities(fetch, cl))
	var runs []client.WorkflowRun
	for _, ref := range refs {
		run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: ArticleWorkflowID(ref.URL), TaskQueue: TQWorkflow},
			ArticleWorkflow, ArticleInput{URL: ref, SourceSlug: "it", CorrelationID: "chaos-" + ref.ID.String()})
		require.NoError(t, err)
		runs = append(runs, run)
	}

	time.Sleep(700 * time.Millisecond) // let some work finish, leave the rest mid-flight
	stop(ws)                           // every worker gone: nothing can make progress
	time.Sleep(1500 * time.Millisecond)
	ws = startWorkers(t, newActivities(fetch, cl)) // fresh process state: workflows replay from history
	defer stop(ws)

	done := 0
	for _, run := range runs {
		var res ArticleResult
		wctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err := run.Get(wctx, &res)
		cancel()
		require.NoError(t, err, "workflow %s", run.GetID())
		require.Equal(t, "done", res.Status)
		done++
	}
	assert.Equal(t, collisions*pubs, done)

	var nInc, nLinks, nUnmergedWithWrongCount int
	require.NoError(t, store.Pool.QueryRow(ctx, `SELECT count(*) FROM incidents WHERE merged_into IS NULL AND verification_status <> 'withdrawn'`).Scan(&nInc))
	require.NoError(t, store.Pool.QueryRow(ctx, `SELECT count(*) FROM incident_articles`).Scan(&nLinks))
	require.NoError(t, store.Pool.QueryRow(ctx, `SELECT count(*) FROM incidents WHERE article_count <> $1`, pubs).Scan(&nUnmergedWithWrongCount))
	assert.Equal(t, collisions, nInc, "one incident per collision, no duplicates")
	assert.Equal(t, collisions*pubs, nLinks, "every article linked exactly once")
	assert.Zero(t, nUnmergedWithWrongCount)

	var extractions int
	require.NoError(t, store.Pool.QueryRow(ctx, `SELECT count(*) FROM article_extractions`).Scan(&extractions))
	assert.Equal(t, collisions*pubs, extractions, "one extraction per article")
	assert.LessOrEqual(t, int(cl.calls.Load()), collisions*pubs+10, "retries reuse stored extractions instead of paying for the model again")

	faults := 0
	for _, n := range fetch.attempts {
		if n > 1 {
			faults++
		}
	}
	assert.Equal(t, collisions*pubs/3, faults, "injected crawler outages were retried by Temporal")

	// Each incident has its own long-lived workflow, started by SignalWithStart.
	require.Eventually(t, func() bool {
		var ids []uuid.UUID
		rows, err := store.Pool.Query(ctx, `SELECT id FROM incidents WHERE merged_into IS NULL`)
		if err != nil {
			return false
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		for _, id := range ids {
			d, err := tc.DescribeWorkflowExecution(ctx, IncidentWorkflowID(id.String()), "")
			if err != nil || d.WorkflowExecutionInfo.Status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
				return false
			}
		}
		return len(ids) == collisions
	}, 30*time.Second, 500*time.Millisecond)

	// Replay every recorded history against the current workflow code: catches non-determinism.
	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflow(ArticleWorkflow)
	r.RegisterWorkflow(IncidentWorkflow)
	replayed := 0
	for _, ref := range refs[:6] {
		it := tc.GetWorkflowHistory(ctx, ArticleWorkflowID(ref.URL), "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		hist, err := historyFrom(it)
		require.NoError(t, err)
		require.NoError(t, r.ReplayWorkflowHistory(nil, hist))
		replayed++
	}
	assert.Equal(t, 6, replayed)
}

func TestSyncSchedulesCreatesThenUpdatesAndKeepsOperatorPause(t *testing.T) {
	ctx := context.Background()
	src := discovery.Source{Slug: "sched-it", Type: "rss", PollInterval: 30 * time.Minute, Enabled: true}
	c, u, err := SyncSchedules(ctx, tc, []discovery.Source{src}, Options{})
	require.NoError(t, err)
	assert.Equal(t, [2]int{1, 0}, [2]int{c, u})
	h := tc.ScheduleClient().GetHandle(ctx, DiscoveryScheduleID("sched-it"))
	defer h.Delete(ctx)
	require.NoError(t, h.Pause(ctx, client.SchedulePauseOptions{Note: "operator paused it"}))

	src.PollInterval = time.Hour
	c, u, err = SyncSchedules(ctx, tc, []discovery.Source{src}, Options{})
	require.NoError(t, err)
	assert.Equal(t, [2]int{0, 1}, [2]int{c, u})
	d, err := h.Describe(ctx)
	require.NoError(t, err)
	assert.True(t, d.Schedule.State.Paused, "a UI pause survives a config sync")
	assert.Equal(t, time.Hour, d.Schedule.Spec.Intervals[0].Every)
	assert.Equal(t, enumspb.SCHEDULE_OVERLAP_POLICY_SKIP, d.Schedule.Policy.Overlap)
}
