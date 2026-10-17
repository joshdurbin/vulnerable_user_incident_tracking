package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joshdurbin/fuck_cars/internal/domain"
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

func okArticle() *domain.Article {
	return &domain.Article{RequestedURL: "https://x/a", FinalURL: "https://x/a", Title: "T", Publisher: "P",
		Text: "body text", HTTPStatus: 200, FetchedAt: time.Unix(0, 0).UTC(), ExtractionStatus: domain.ExtractionOK}
}

func TestFetchJSON(t *testing.T) {
	f := &fakeFetcher{art: okArticle()}
	var out, errw bytes.Buffer
	if err := fetchCmd(context.Background(), []string{"https://x/a", "--json", "--render", "auto"}, &out, &errw, f); err != nil {
		t.Fatal(err)
	}
	var got domain.Article
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if got.Title != "T" || got.ExtractionStatus != domain.ExtractionOK {
		t.Fatalf("unexpected: %+v", got)
	}
	if f.got.Rendering != domain.RenderAuto || f.got.CorrelationID == "" {
		t.Fatalf("request not populated: %+v", f.got)
	}
}

func TestFetchHuman(t *testing.T) {
	var out, errw bytes.Buffer
	if err := fetchCmd(context.Background(), []string{"https://x/a"}, &out, &errw, &fakeFetcher{art: okArticle()}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Title:      T") {
		t.Fatalf("missing title: %s", out.String())
	}
}

func TestFetchNonOKStatusIsError(t *testing.T) {
	a := okArticle()
	a.ExtractionStatus, a.ErrorDetail = domain.ExtractionBlocked, "non-public address"
	var out, errw bytes.Buffer
	err := fetchCmd(context.Background(), []string{"http://127.0.0.1/"}, &out, &errw, &fakeFetcher{art: a})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("want blocked error, got %v", err)
	}
}

func TestFetchValidation(t *testing.T) {
	var out, errw bytes.Buffer
	if err := fetchCmd(context.Background(), nil, &out, &errw, &fakeFetcher{}); err == nil {
		t.Fatal("expected usage error")
	}
	if err := fetchCmd(context.Background(), []string{"https://x", "--render", "bogus"}, &out, &errw, &fakeFetcher{}); err == nil {
		t.Fatal("expected invalid render error")
	}
	if err := fetchCmd(context.Background(), []string{"https://x"}, &out, &errw, &fakeFetcher{err: errors.New("boom")}); err == nil {
		t.Fatal("expected propagated error")
	}
}
