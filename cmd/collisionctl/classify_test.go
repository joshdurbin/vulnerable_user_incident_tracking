package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshdurbin/fuck_cars/internal/domain"
	"github.com/joshdurbin/fuck_cars/internal/llm"
)

type stubLLM struct{ content string }

func (s stubLLM) Complete(_ context.Context, r llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: s.content, Model: r.Model, Mode: llm.ModeJSONSchema, InputTokens: 10, OutputTokens: 5, CostUSD: 0.0001, PriceKnown: true}, nil
}

const nonqual = `{"verdict":"nonqualifying","article_kind":"unrelated","collision_occurred":false,"motor_vehicle_involved":null,
"vru_involved":null,"injury_or_death":null,"multiple_incidents":false,"updates_earlier_incident":false,"incidents":[]}`

func TestClassifyFromTextFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(f, []byte("A city council meeting about parking."), 0o600)
	var out, errw bytes.Buffer
	err := classifyCmd(context.Background(), []string{"--file", f, "--model", "qwen", "--json"}, &out, &errw, classifyDeps{completer: stubLLM{nonqual}})
	if err != nil {
		t.Fatal(err)
	}
	var r domain.ExtractionResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if r.Verdict != domain.VerdictNonqualifying || r.Meta.Model != "qwen" || r.Meta.PromptVersion != "v1" {
		t.Fatalf("%+v", r)
	}
}

func TestClassifyFromURLUsesFetcher(t *testing.T) {
	fe := &fakeFetcher{art: okArticle()}
	var out, errw bytes.Buffer
	err := classifyCmd(context.Background(), []string{"https://x/a", "--model", "qwen"}, &out, &errw, classifyDeps{fetcher: fe, completer: stubLLM{nonqual}})
	if err != nil {
		t.Fatal(err)
	}
	if fe.got.URL != "https://x/a" || !strings.Contains(out.String(), "Verdict:  nonqualifying") {
		t.Fatalf("%s / %+v", out.String(), fe.got)
	}
}

func TestClassifyRejectsUnusableArticle(t *testing.T) {
	a := okArticle()
	a.ExtractionStatus, a.ErrorDetail = domain.ExtractionRobotsDisallowed, "disallowed by robots.txt"
	var out, errw bytes.Buffer
	err := classifyCmd(context.Background(), []string{"https://x/a", "--model", "qwen"}, &out, &errw, classifyDeps{fetcher: &fakeFetcher{art: a}, completer: stubLLM{nonqual}})
	if err == nil || !strings.Contains(err.Error(), "robots") {
		t.Fatalf("got %v", err)
	}
}

func TestClassifyRequiresModelAndInput(t *testing.T) {
	var out, errw bytes.Buffer
	if err := classifyCmd(context.Background(), nil, &out, &errw, classifyDeps{}); err == nil {
		t.Fatal("expected usage error")
	}
	t.Setenv("LLM_MODEL", "")
	if err := classifyCmd(context.Background(), []string{"https://x"}, &out, &errw, classifyDeps{fetcher: &fakeFetcher{art: okArticle()}}); err == nil {
		t.Fatal("expected model-required error")
	}
}
