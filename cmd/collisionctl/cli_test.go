package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshdurbin/fuck_cars/internal/domain"
	"github.com/joshdurbin/fuck_cars/internal/llm"
)

type fakeFetcher struct {
	art *domain.Article
	err error
	got domain.FetchRequest
}

func (f *fakeFetcher) FetchArticle(_ context.Context, r domain.FetchRequest) (*domain.Article, error) {
	f.got = r
	return f.art, f.err
}

type stubLLM struct{ content string }

func (s stubLLM) Complete(_ context.Context, r llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: s.content, Model: r.Model, Mode: llm.ModeJSONSchema, InputTokens: 10, OutputTokens: 5, CostUSD: 0.0001, PriceKnown: true}, nil
}

const nonqual = `{"verdict":"nonqualifying","article_kind":"unrelated","collision_occurred":false,"motor_vehicle_involved":null,
"vru_involved":null,"injury_or_death":null,"multiple_incidents":false,"updates_earlier_incident":false,"incidents":[]}`

func okArticle() *domain.Article {
	return &domain.Article{RequestedURL: "https://x/a", FinalURL: "https://x/a", Title: "T", Publisher: "P",
		Text: "body text", HTTPStatus: 200, FetchedAt: time.Unix(0, 0).UTC(), ExtractionStatus: domain.ExtractionOK}
}

// run executes the root command with injected deps and returns stdout, stderr.
func run(t *testing.T, d deps, args ...string) (string, string, error) {
	t.Helper()
	var out, errw bytes.Buffer
	d.out, d.err = &out, &errw
	cmd := newRootCmd(d)
	cmd.SetArgs(append(args, "--log-level", "debug"))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errw.String(), err
}

func TestFetchJSON(t *testing.T) {
	f := &fakeFetcher{art: okArticle()}
	out, _, err := run(t, deps{fetcher: f}, "fetch", "https://x/a", "--json", "--render", "auto")
	if err != nil {
		t.Fatal(err)
	}
	var got domain.Article
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if got.Title != "T" || f.got.Rendering != domain.RenderAuto || f.got.CorrelationID == "" {
		t.Fatalf("got=%+v req=%+v", got, f.got)
	}
}

func TestFetchHumanAndLogsGoToStderr(t *testing.T) {
	out, errw, err := run(t, deps{fetcher: &fakeFetcher{art: okArticle()}}, "fetch", "https://x/a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Title:      T") || strings.Contains(out, `"level"`) {
		t.Fatalf("stdout polluted or missing: %s", out)
	}
	if !strings.Contains(errw, `"correlation_id"`) {
		t.Fatalf("expected structured debug log on stderr, got %q", errw)
	}
}

func TestFetchNonOKStatusIsError(t *testing.T) {
	a := okArticle()
	a.ExtractionStatus, a.ErrorDetail = domain.ExtractionBlocked, "non-public address"
	_, _, err := run(t, deps{fetcher: &fakeFetcher{art: a}}, "fetch", "http://127.0.0.1/")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("got %v", err)
	}
}

func TestFetchValidation(t *testing.T) {
	if _, _, err := run(t, deps{fetcher: &fakeFetcher{}}, "fetch"); err == nil {
		t.Fatal("expected arg error")
	}
	if _, _, err := run(t, deps{fetcher: &fakeFetcher{}}, "fetch", "https://x", "--render", "bogus"); err == nil {
		t.Fatal("expected invalid render")
	}
	if _, _, err := run(t, deps{fetcher: &fakeFetcher{err: errors.New("boom")}}, "fetch", "https://x"); err == nil {
		t.Fatal("expected propagated error")
	}
}

func TestClassifyFromTextFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(f, []byte("A city council meeting about parking."), 0o600)
	out, _, err := run(t, deps{completer: stubLLM{nonqual}}, "classify", "--file", f, "--model", "qwen", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var r domain.ExtractionResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r.Verdict != domain.VerdictNonqualifying || r.Meta.Model != "qwen" || r.Meta.PromptVersion != "v1" {
		t.Fatalf("%+v", r)
	}
}

func TestClassifyModelFromEnv(t *testing.T) {
	t.Setenv("COLLISION_LLM_MODEL", "from-env")
	f := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(f, []byte("text"), 0o600)
	out, _, err := run(t, deps{completer: stubLLM{nonqual}}, "classify", "--file", f, "--json")
	if err != nil || !strings.Contains(out, `"model": "from-env"`) {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

func TestClassifyFlagBeatsEnv(t *testing.T) {
	t.Setenv("COLLISION_LLM_MODEL", "from-env")
	f := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(f, []byte("text"), 0o600)
	out, _, err := run(t, deps{completer: stubLLM{nonqual}}, "classify", "--file", f, "--model", "from-flag", "--json")
	if err != nil || !strings.Contains(out, `"model": "from-flag"`) {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

func TestClassifyFromURLUsesFetcher(t *testing.T) {
	fe := &fakeFetcher{art: okArticle()}
	out, _, err := run(t, deps{fetcher: fe, completer: stubLLM{nonqual}}, "classify", "https://x/a", "--model", "qwen")
	if err != nil || fe.got.URL != "https://x/a" || !strings.Contains(out, "Verdict:  nonqualifying") {
		t.Fatalf("err=%v out=%s req=%+v", err, out, fe.got)
	}
}

func TestClassifyRejectsUnusableArticle(t *testing.T) {
	a := okArticle()
	a.ExtractionStatus, a.ErrorDetail = domain.ExtractionRobotsDisallowed, "disallowed by robots.txt"
	_, _, err := run(t, deps{fetcher: &fakeFetcher{art: a}, completer: stubLLM{nonqual}}, "classify", "https://x/a", "--model", "qwen")
	if err == nil || !strings.Contains(err.Error(), "robots") {
		t.Fatalf("got %v", err)
	}
}

func TestClassifyRequiresInputAndModel(t *testing.T) {
	if _, _, err := run(t, deps{completer: stubLLM{nonqual}}, "classify", "--model", "m"); err == nil {
		t.Fatal("expected missing input error")
	}
	t.Setenv("COLLISION_LLM_MODEL", "")
	if _, _, err := run(t, deps{fetcher: &fakeFetcher{art: okArticle()}}, "classify", "https://x"); err == nil {
		t.Fatal("expected model-required error")
	}
}

func TestEvalReplay(t *testing.T) {
	out, _, err := run(t, deps{}, "eval", "--replay", "--cases", "../../tests/evaluations/cases", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Cases     int     `json:"cases"`
		Precision float64 `json:"classification_precision"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil || rep.Cases != 15 || rep.Precision != 1 {
		t.Fatalf("err=%v rep=%+v", err, rep)
	}
}
