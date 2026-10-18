package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/seed"
)

type stubProv struct {
	typ  string
	last discovery.Request
}

func (s *stubProv) Type() string { return s.typ }
func (s *stubProv) Discover(_ context.Context, r discovery.Request) (*discovery.Result, error) {
	s.last = r
	pub := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	return &discovery.Result{Requests: 1, Candidates: []discovery.Candidate{
		{URL: "https://gazette.example.com/a?utm_source=x", Title: "Pedestrian killed", PublishedAt: &pub},
		{URL: "http://gazette.example.com/a", Title: "dup"},
	}}, nil
}

func testRegistry() (*discovery.Registry, *stubProv, *stubProv) {
	reg := discovery.NewRegistry()
	rss, gd := &stubProv{typ: "rss"}, &stubProv{typ: "gdelt"}
	reg.Register(rss, "rss", "atom", "googlenews", "sitemap", "news_sitemap", "index_page", "police_press")
	reg.Register(gd)
	return reg, rss, gd
}

func TestSourcesValidateDefaults(t *testing.T) {
	reg, _, _ := testRegistry()
	out, _, err := run(t, deps{registry: reg}, "sources", "validate")
	if err != nil || !strings.Contains(out, "OK:") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestSourcesValidateCatchesBadFile(t *testing.T) {
	reg, _, _ := testRegistry()
	f := filepath.Join(t.TempDir(), "s.yaml")
	os.WriteFile(f, []byte("sources:\n  - {slug: bad, name: Bad, type: rss, poll_interval: 30m}\nqueries:\n  - {family: p, name: q, text: 'a OR b'}\n"), 0o600)
	_, errw, err := run(t, deps{registry: reg}, "sources", "validate", "--file", f)
	if err == nil || !strings.Contains(errw, "absolute http(s) url") || !strings.Contains(errw, "OR must be inside parentheses") {
		t.Fatalf("err=%v stderr=%s", err, errw)
	}
}

func TestDiscoverDryRunUsesSeedsAndNoDatabase(t *testing.T) {
	reg, rss, _ := testRegistry()
	out, _, err := run(t, deps{registry: reg}, "discover", "--source", "oaklandside", "--dry-run", "--lookback", "6h")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DRY RUN", "Found:    2 candidates, 1 unique", "https://gazette.example.com/a"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if rss.last.Source.URL != "https://oaklandside.org/feed/" || rss.last.Window.Duration() != 6*time.Hour {
		t.Fatalf("%+v", rss.last)
	}
}

func TestDiscoverSearchProviderGetsSeededQueries(t *testing.T) {
	reg, _, gd := testRegistry()
	if _, _, err := run(t, deps{registry: reg}, "discover", "--provider", "gdelt", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if len(gd.last.Queries) < 10 || gd.last.Source.Slug != "gdelt-us" {
		t.Fatalf("queries=%d source=%s", len(gd.last.Queries), gd.last.Source.Slug)
	}
}

func TestDiscoverProviderAmbiguousAndMissing(t *testing.T) {
	reg, _, _ := testRegistry()
	if _, _, err := run(t, deps{registry: reg}, "discover", "--provider", "rss", "--dry-run"); err == nil || !strings.Contains(err.Error(), "choose one with --source") {
		t.Fatalf("got %v", err)
	}
	if _, _, err := run(t, deps{registry: reg}, "discover", "--provider", "sitemap", "--dry-run"); err == nil || !strings.Contains(err.Error(), "no enabled source") {
		t.Fatalf("got %v", err)
	}
	if _, _, err := run(t, deps{registry: reg}, "discover", "--dry-run"); err == nil {
		t.Fatal("needs --source or --provider")
	}
}

func TestDiscoverWithoutDryRunRequiresDatabase(t *testing.T) {
	reg, _, _ := testRegistry()
	if _, _, err := run(t, deps{registry: reg}, "discover", "--source", "oaklandside"); err == nil || !strings.Contains(err.Error(), "database is required") {
		t.Fatalf("got %v", err)
	}
}

func TestDiscoverJSONAndRecordWithInjectedRepo(t *testing.T) {
	reg, _, _ := testRegistry()
	f, _ := seed.Defaults()
	out, _, err := run(t, deps{registry: reg, repo: seed.MemoryRepo{F: f}}, "discover", "--source", "oaklandside", "--json")
	if err != nil || !strings.Contains(out, `"Normalized": "https://gazette.example.com/a"`) {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestSourcesListFromSeeds(t *testing.T) {
	out, _, err := run(t, deps{}, "sources", "list")
	if err != nil || !strings.Contains(out, "gdelt-us") || !strings.Contains(out, "streetsblog-cal") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestParseWindow(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if w, _ := parseWindow(0, "", "", now); !w.Since.IsZero() {
		t.Fatal("zero means service default")
	}
	if w, _ := parseWindow(3*time.Hour, "", "", now); w.Since != now.Add(-3*time.Hour) {
		t.Fatalf("%v", w)
	}
	w, err := parseWindow(0, "2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z", now)
	if err != nil || w.Duration() != 24*time.Hour {
		t.Fatalf("%v %v", w, err)
	}
	if _, err := parseWindow(0, "garbage", "", now); err == nil {
		t.Fatal("bad --from")
	}
	if _, err := parseWindow(0, "2026-10-02T00:00:00Z", "2026-10-01T00:00:00Z", now); err == nil {
		t.Fatal("--to before --from")
	}
}
