package discovery

import (
	"context"
	"errors"
	"testing"
	"time"
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
func (f fake) Describe(t string) Descriptor {
	return Descriptor{Type: t, Label: f.typ}
}
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

func TestAssessHealth(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	base := Source{Enabled: true, PollInterval: 30 * time.Minute, LastAttemptAt: ago(time.Minute), LastSuccessAt: ago(time.Minute)}
	cases := []struct {
		name   string
		mod    func(s *Source)
		paused bool
		want   HealthState
	}{
		{"healthy", func(*Source) {}, false, HealthHealthy},
		{"disabled wins over everything", func(s *Source) { s.Enabled = false; s.ConsecutiveFailures = 9 }, false, HealthDisabled},
		{"paused schedule", func(*Source) {}, true, HealthPaused},
		{"never ran", func(s *Source) { s.LastAttemptAt, s.LastSuccessAt = nil, nil }, false, HealthWaiting},
		{"one failure is degraded", func(s *Source) { s.ConsecutiveFailures = 1; s.ErrorState = "503" }, false, HealthDegraded},
		{"repeated failures", func(s *Source) { s.ConsecutiveFailures = FailingAfter; s.ErrorState = "503" }, false, HealthFailing},
		{"never succeeded", func(s *Source) { s.LastSuccessAt = nil; s.ConsecutiveFailures = 1 }, false, HealthStale},
		{"quiet for under an hour is fine for a 30m source", func(s *Source) { s.LastSuccessAt = ago(50 * time.Minute) }, false, HealthHealthy},
		{"quiet for over three intervals and an hour is stale", func(s *Source) { s.LastSuccessAt = ago(3 * time.Hour) }, false, HealthStale},
		{"a slow source is judged on its own interval", func(s *Source) { s.PollInterval = 24 * time.Hour; s.LastSuccessAt = ago(30 * time.Hour) }, false, HealthHealthy},
	}
	for _, c := range cases {
		s := base
		c.mod(&s)
		if got := AssessHealth(s, c.paused, now); got.State != c.want || got.Reason == "" {
			t.Errorf("%s: got %+v, want %s", c.name, got, c.want)
		}
	}
}

func TestValidateSourceUsesDescriptors(t *testing.T) {
	r := NewRegistry()
	r.Register(fake{"feed"}, "rss")
	good := Source{Slug: "ok-feed", Name: "OK", Type: "rss", PollInterval: time.Hour}
	// fake describes no URL requirement, so a url is rejected for it: the descriptor decides.
	good.URL = "https://x.test/"
	if errs := ValidateSource(r, good); len(errs) != 1 {
		t.Fatalf("want exactly the does-not-use-a-url error, got %v", errs)
	}
	good.URL = ""
	if errs := ValidateSource(r, good); len(errs) != 0 {
		t.Fatalf("%v", errs)
	}
	bad := Source{Slug: "x", Name: " ", Type: "nope", PollInterval: time.Second}
	if errs := ValidateSource(r, bad); len(errs) < 3 {
		t.Fatalf("slug, name and type must all be reported: %v", errs)
	}
	for _, c := range []Source{
		{Slug: "ok-feed", Name: "n", Type: "rss", PollInterval: time.Hour, Crawl: CrawlPolicy{Render: "sometimes"}},
		{Slug: "ok-feed", Name: "n", Type: "rss", PollInterval: time.Hour, Crawl: CrawlPolicy{URLPatterns: []string{"("}}},
		{Slug: "ok-feed", Name: "n", Type: "rss", PollInterval: time.Hour, Crawl: CrawlPolicy{MaxPages: -1}},
	} {
		if errs := ValidateSource(r, c); len(errs) != 1 {
			t.Errorf("%+v: want one error, got %v", c.Crawl, errs)
		}
	}
}
