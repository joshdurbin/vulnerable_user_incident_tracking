package seed

import (
	"strings"
	"testing"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
)

var allTypes = []string{"atom", "gdelt", "googlenews", "index_page", "news_sitemap", "police_press", "rss", "sitemap"}

func TestEmbeddedDefaultsAreValid(t *testing.T) {
	f, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if errs := Validate(f, allTypes); len(errs) != 0 {
		t.Fatalf("shipped defaults are invalid: %v", errs)
	}
	if len(f.Sources) < 8 || len(f.Queries) < 10 {
		t.Fatalf("sources=%d queries=%d", len(f.Sources), len(f.Queries))
	}
	fam := map[string]bool{}
	for _, q := range f.Queries {
		fam[q.Family] = true
	}
	for _, want := range []string{"pedestrian", "cyclist", "scooter", "motorcyclist", "hit_and_run", "wheelchair", "skateboard", "traffic_fatality"} {
		if !fam[want] {
			t.Errorf("missing query family %s", want)
		}
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	f := File{
		Sources: []SourceSeed{
			{Slug: "Bad Slug", Name: "x", Type: "rss", URL: "https://a/", PollInterval: 60e9},
			{Slug: "no-url", Name: "x", Type: "rss", PollInterval: 60e9},
			{Slug: "bad-type", Name: "x", Type: "carrier-pigeon", PollInterval: 60e9},
			{Slug: "fast", Name: "x", Type: "gdelt", PollInterval: 1e9},
			{Slug: "police", Name: "PD", Type: "police_press", URL: "https://pd.example.gov/press", PollInterval: 60e9},
			{Slug: "dup", Name: "x", Type: "gdelt", PollInterval: 60e9},
			{Slug: "dup", Name: "x", Type: "gdelt", PollInterval: 60e9},
		},
		Queries: []QuerySeed{
			{Family: "p", Name: "bare-or", Text: "pedestrian OR cyclist"},
			{Family: "p", Name: "unbalanced", Text: "(pedestrian OR cyclist"},
			{Family: "p", Name: "ok", Text: `("hit and run" OR "hit-and-run") pedestrian`},
			{Family: "", Name: "nofamily", Text: "x y"},
			{Family: "p", Name: "badprov", Text: "(a OR b)", Providers: []string{"nope"}},
		},
	}
	errs := Validate(f, allTypes)
	joined := ""
	for _, e := range errs {
		joined += e.Error() + "\n"
	}
	for _, want := range []string{"slug must be", "needs an absolute http(s) url", "unknown type", "poll_interval", "allowed_domains", "duplicate slug",
		"OR must be inside parentheses", "unbalanced", "required", "unknown provider"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "query ok") {
		t.Errorf("valid query flagged: %s", joined)
	}
}

func TestUnknownYAMLKeysRejected(t *testing.T) {
	if _, err := parse([]byte("sources:\n  - slug: a\n    pol_interval: 5m\n")); err == nil {
		t.Fatal("typo'd key must fail")
	}
}

func TestMergeOverridesBySlugAndName(t *testing.T) {
	base := File{Sources: []SourceSeed{{Slug: "a", Name: "old"}}, Queries: []QuerySeed{{Name: "q", Text: "old"}}}
	out := Merge(base, File{Sources: []SourceSeed{{Slug: "a", Name: "new"}, {Slug: "b"}}, Queries: []QuerySeed{{Name: "q", Text: "new"}}})
	if len(out.Sources) != 2 || out.Sources[0].Name != "new" || out.Queries[0].Text != "new" || base.Sources[0].Name != "old" {
		t.Fatalf("%+v", out)
	}
}

func TestToSourceDefaultsEnabled(t *testing.T) {
	off := false
	if !(SourceSeed{Slug: "a"}).ToSource().Enabled || (SourceSeed{Slug: "a", Enabled: &off}).ToSource().Enabled {
		t.Fatal("enabled default/override wrong")
	}
	_ = discovery.TypeRSS
}
