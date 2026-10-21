package sourcefile

import (
	"strings"
	"testing"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/registry"
)

func examples(t *testing.T) File {
	t.Helper()
	f, err := Load("../../../docs/examples/sources.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestShippedExamplesAreValid(t *testing.T) {
	f := examples(t)
	if errs := Validate(registry.New(registry.Deps{}), f); len(errs) != 0 {
		t.Fatalf("docs/examples are invalid: %v", errs)
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
		Sources: []SourceSpec{
			{Slug: "Bad Slug", Name: "x", Type: "rss", URL: "https://a/", PollInterval: 60e9},
			{Slug: "no-url", Name: "x", Type: "rss", PollInterval: 60e9},
			{Slug: "bad-type", Name: "x", Type: "carrier-pigeon", PollInterval: 60e9},
			{Slug: "fast", Name: "x", Type: "gdelt", PollInterval: 1e9},
			{Slug: "police", Name: "PD", Type: "police_press", URL: "https://pd.example.gov/press", PollInterval: 60e9},
			{Slug: "search-with-url", Name: "x", Type: "gdelt", URL: "https://x.test/", PollInterval: 60e9},
			{Slug: "dup", Name: "x", Type: "gdelt", PollInterval: 60e9},
			{Slug: "dup", Name: "x", Type: "gdelt", PollInterval: 60e9},
		},
		Queries: []QuerySpec{
			{Family: "p", Name: "bare-or", Text: "pedestrian OR cyclist"},
			{Family: "p", Name: "unbalanced", Text: "(pedestrian OR cyclist"},
			{Family: "p", Name: "ok", Text: `("hit and run" OR "hit-and-run") pedestrian`},
			{Family: "", Name: "nofamily", Text: "x y"},
			{Family: "p", Name: "badprov", Text: "(a OR b)", Providers: []string{"nope"}},
			{Family: "p", Name: "feedprov", Text: "(a OR b)", Providers: []string{"rss"}},
		},
	}
	var joined string
	for _, e := range Validate(registry.New(registry.Deps{}), f) {
		joined += e.Error() + "\n"
	}
	for _, want := range []string{"slug must be", "needs an absolute http(s) url", "unknown type", "poll interval", "allowed domains",
		"does not use a url", "duplicate slug", "OR must be inside parentheses", "unbalanced", "family is required", "unknown provider",
		"does not run search queries"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, `query "ok"`) {
		t.Errorf("valid query flagged: %s", joined)
	}
}

func TestUnknownYAMLKeysRejected(t *testing.T) {
	if _, err := Parse([]byte("sources:\n  - slug: a\n    pol_interval: 5m\n")); err == nil {
		t.Fatal("typo'd key must fail")
	}
}

func TestToSourceDefaultsEnabled(t *testing.T) {
	off := false
	if !(SourceSpec{Slug: "a"}).ToSource().Enabled || (SourceSpec{Slug: "a", Enabled: &off}).ToSource().Enabled {
		t.Fatal("enabled default/override wrong")
	}
}
