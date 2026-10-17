package discovery

import (
	"context"
	"errors"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want, host string }{
		{"http://News.Example.com/2026/10/story/?utm_source=x&utm_medium=y&id=7#comments", "https://news.example.com/2026/10/story?id=7", "news.example.com"},
		{"https://example.com/a//b/./c/../d?b=2&a=1&fbclid=zzz", "https://example.com/a/b/d?a=1&b=2", "example.com"},
		{"https://example.com:443/story/", "https://example.com/story", "example.com"},
		{"https://example.com/story?", "https://example.com/story", "example.com"},
	}
	for _, c := range cases {
		got, host, err := Normalize(c.in)
		if err != nil || got != c.want || host != c.host {
			t.Errorf("Normalize(%q) = %q,%q,%v want %q,%q", c.in, got, host, err, c.want, c.host)
		}
	}
	for _, bad := range []string{"ftp://example.com/x", "javascript:alert(1)", "/relative/path", "", "http://"} {
		if _, _, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) should fail", bad)
		}
	}
}

func TestSameArticleFromDifferentFeedsDedupes(t *testing.T) {
	a, _, _ := Normalize("https://news.example.com/s/1?utm_campaign=rss")
	b, _, _ := Normalize("http://NEWS.example.com/s/1/#top")
	if a != b {
		t.Fatalf("%q != %q", a, b)
	}
}

func TestDedupePreservesProvenanceAndRejectsBad(t *testing.T) {
	uniq, rejected := Dedupe([]Candidate{
		{URL: "https://n.example.com/a?utm_source=gdelt", Provider: "gdelt"},
		{URL: "http://n.example.com/a", Provider: "rss", SourceSlug: "n-rss"},
		{URL: "https://n.example.com/b", Provider: "rss"},
		{URL: "mailto:x@y", Provider: "rss"},
	})
	if len(uniq) != 2 || len(rejected) != 1 {
		t.Fatalf("uniq=%d rejected=%d", len(uniq), len(rejected))
	}
	if len(uniq[0].Found) != 2 || uniq[0].Found[0].Provider != "gdelt" || uniq[0].Found[1].SourceSlug != "n-rss" {
		t.Fatalf("provenance lost: %+v", uniq[0].Found)
	}
}

type fake struct{ typ string }

func (f fake) Type() string { return f.typ }
func (f fake) Discover(context.Context, Request) (*Result, error) {
	return &Result{Candidates: []Candidate{{URL: "https://x.example.com/1"}}}, nil
}

func TestRegistryDispatchAndDefaults(t *testing.T) {
	r := NewRegistry()
	r.Register(fake{"a"})
	r.Register(fake{"feed"}, "rss", "atom")
	res, err := r.Discover(context.Background(), Request{Source: Source{Slug: "s1", Type: "atom"}})
	if err != nil || len(res.Candidates) != 1 || res.Candidates[0].Provider != "atom" || res.Candidates[0].SourceSlug != "s1" {
		t.Fatalf("%+v %v", res, err)
	}
	if got := r.Types(); len(got) != 3 || got[0] != "a" {
		t.Fatalf("types %v", got)
	}
	if _, err := r.Discover(context.Background(), Request{Source: Source{Type: "nope"}}); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("got %v", err)
	}
}

func TestRegistryRejectsDuplicateType(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	r := NewRegistry()
	r.Register(fake{"a"})
	r.Register(fake{"a"})
}
