package discovery

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRepo struct {
	queries  []Query
	recorded []Deduped
	marks    []error
	recErr   error
	known    map[string]bool
}

func (f *fakeRepo) Source(context.Context, string) (Source, error) {
	return Source{Slug: "s", Type: "stub", Enabled: false}, nil
}
func (f *fakeRepo) Sources(context.Context, bool) ([]Source, error)         { return nil, nil }
func (f *fakeRepo) Queries(context.Context, string) ([]Query, error)        { return f.queries, nil }
func (f *fakeRepo) Stats(context.Context, time.Time) ([]SourceStats, error) { return nil, nil }
func (f *fakeRepo) MarkResult(_ context.Context, _ string, _ time.Time, err error) error {
	f.marks = append(f.marks, err)
	return nil
}
func (f *fakeRepo) Record(_ context.Context, _ Source, items []Deduped) (RecordResult, error) {
	if f.recErr != nil {
		return RecordResult{}, f.recErr
	}
	var out RecordResult
	for _, it := range items {
		if f.known[it.Normalized] {
			out.Known++
			continue
		}
		out.New = append(out.New, NewURL{URL: it.Normalized, Host: it.Host, Title: it.Primary.Title})
	}
	f.recorded = items
	return out, nil
}

type stubProvider struct {
	typ  string
	res  *Result
	err  error
	last Request
}

func (s *stubProvider) Type() string { return s.typ }
func (s *stubProvider) Discover(_ context.Context, r Request) (*Result, error) {
	s.last = r
	return s.res, s.err
}

func svc(p *stubProvider, repo *fakeRepo) *Service {
	reg := NewRegistry()
	reg.Register(p)
	return NewService(reg, repo)
}

func TestRunDedupesRecordsAndMarksSuccess(t *testing.T) {
	p := &stubProvider{typ: "stub", res: &Result{Candidates: []Candidate{
		{URL: "https://n.example.com/a?utm_source=x", Title: "A", Provider: "stub"},
		{URL: "http://n.example.com/a", Title: "A again"},
		{URL: "https://n.example.com/b", NeedsResolution: true},
		{URL: "not a url"},
	}, Requests: 2}}
	repo := &fakeRepo{known: map[string]bool{"https://n.example.com/b": true}}
	rep, err := svc(p, repo).Run(context.Background(), RunRequest{Source: Source{Slug: "s", Type: "stub", PollInterval: 30 * time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Found != 4 || rep.Unique != 2 || len(rep.Rejected) != 1 || len(rep.New) != 1 || rep.Known != 1 || rep.Unresolved != 1 || rep.Requests != 2 {
		t.Fatalf("%+v", rep)
	}
	if len(repo.marks) != 1 || repo.marks[0] != nil {
		t.Fatalf("marks %v", repo.marks)
	}
	if got := p.last.Window.Duration(); got != 2*time.Hour {
		t.Fatalf("default window should be at least 2h with overlap, got %v", got)
	}
}

func TestDryRunDoesNotPersistOrMark(t *testing.T) {
	p := &stubProvider{typ: "stub", res: &Result{Candidates: []Candidate{{URL: "https://n.example.com/a"}}}}
	repo := &fakeRepo{}
	rep, err := svc(p, repo).Run(context.Background(), RunRequest{Source: Source{Slug: "s", Type: "stub"}, DryRun: true})
	if err != nil || rep.Unique != 1 || len(rep.New) != 0 || repo.recorded != nil || len(repo.marks) != 0 {
		t.Fatalf("%+v %v %+v", rep, err, repo)
	}
}

func TestProviderFailureMarksSourceFailed(t *testing.T) {
	boom := errors.New("upstream down")
	repo := &fakeRepo{}
	_, err := svc(&stubProvider{typ: "stub", res: &Result{}, err: boom}, repo).Run(context.Background(), RunRequest{Source: Source{Slug: "s", Type: "stub"}})
	if !errors.Is(err, boom) || len(repo.marks) != 1 || repo.marks[0] == nil {
		t.Fatalf("err=%v marks=%v", err, repo.marks)
	}
}

func TestPartialResultsAreRecordedWithWarning(t *testing.T) {
	p := &stubProvider{typ: "stub", res: &Result{Candidates: []Candidate{{URL: "https://n.example.com/a"}}}, err: errors.New("one query failed")}
	repo := &fakeRepo{}
	rep, err := svc(p, repo).Run(context.Background(), RunRequest{Source: Source{Slug: "s", Type: "stub"}})
	if err != nil || len(rep.New) != 1 || len(rep.Warnings) == 0 {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestRecordFailureIsReportedAndMarked(t *testing.T) {
	repo := &fakeRepo{recErr: errors.New("db down")}
	p := &stubProvider{typ: "stub", res: &Result{Candidates: []Candidate{{URL: "https://n.example.com/a"}}}}
	if _, err := svc(p, repo).Run(context.Background(), RunRequest{Source: Source{Slug: "s", Type: "stub"}}); err == nil || len(repo.marks) != 1 || repo.marks[0] == nil {
		t.Fatalf("err=%v marks=%v", err, repo.marks)
	}
}

func TestSearchProvidersGetQueriesAndNeedThem(t *testing.T) {
	p := &stubProvider{typ: TypeGDELT, res: &Result{}}
	repo := &fakeRepo{queries: []Query{{Name: "q1", Text: "(a OR b)"}}}
	if _, err := svc(p, repo).Run(context.Background(), RunRequest{Source: Source{Slug: "g", Type: TypeGDELT}, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if len(p.last.Queries) != 1 {
		t.Fatalf("queries not passed: %+v", p.last)
	}
	repo.queries = nil
	if _, err := svc(p, repo).Run(context.Background(), RunRequest{Source: Source{Slug: "g", Type: TypeGDELT}, DryRun: true}); err == nil {
		t.Fatal("a search provider with no queries must be an error, not a silent empty run")
	}
}

func TestDisabledSourceCannotBeRunByNameButCanBeDryRun(t *testing.T) {
	p := &stubProvider{typ: "stub", res: &Result{}}
	s := svc(p, &fakeRepo{})
	if _, err := s.RunSlug(context.Background(), "s", Window{}, false); !errors.Is(err, ErrSourceDisabled) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.RunSlug(context.Background(), "s", Window{}, true); err != nil {
		t.Fatal(err)
	}
}

func TestStatsRates(t *testing.T) {
	st := SourceStats{Discoveries: 10, UniqueURLs: 4, ArticlesFetched: 4, FetchOK: 3, RelevantArticles: 1}
	if st.DuplicateRate() != 0.6 || st.FetchSuccessRate() != 0.75 || st.RelevantRate() != 0.25 {
		t.Fatalf("%v %v %v", st.DuplicateRate(), st.FetchSuccessRate(), st.RelevantRate())
	}
	if (SourceStats{}).DuplicateRate() != 0 {
		t.Fatal("zero division")
	}
}

func TestQueryNamesFilterAndUnknownIsError(t *testing.T) {
	p := &stubProvider{typ: TypeGDELT, res: &Result{}}
	repo := &fakeRepo{queries: []Query{{Name: "q1", Text: "(a OR b)"}, {Name: "q2", Text: "(c OR d)"}}}
	s := svc(p, repo)
	if _, err := s.Run(context.Background(), RunRequest{Source: Source{Slug: "g", Type: TypeGDELT}, DryRun: true, QueryNames: []string{"q2"}}); err != nil {
		t.Fatal(err)
	}
	if len(p.last.Queries) != 1 || p.last.Queries[0].Name != "q2" {
		t.Fatalf("%+v", p.last.Queries)
	}
	if _, err := s.Run(context.Background(), RunRequest{Source: Source{Slug: "g", Type: TypeGDELT}, DryRun: true, QueryNames: []string{"nope"}}); err == nil {
		t.Fatal("unknown query name must be an error")
	}
}
