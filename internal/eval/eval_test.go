package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/joshdurbin/fuck_cars/internal/classification"
)

const casesDir = "../../tests/evaluations/cases"

func load(t *testing.T) []Case {
	cs, err := Load(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// run replays each case's body, optionally rewritten by mutate(caseID, body).
func run(t *testing.T, mutate func(id, body string) string) *Report {
	t.Helper()
	cs := load(t)
	build := func(c Case) (*classification.Classifier, error) {
		body := string(c.SyntheticResponse)
		if mutate != nil {
			body = mutate(c.ID, body)
		}
		return classification.New(Replay{Body: body}, classification.Config{PrimaryModel: "replay"})
	}
	r, err := Run(context.Background(), cs, build)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAllScenariosPresent(t *testing.T) {
	want := []string{"pedestrian_fatality", "injured_bicyclist", "scooter_struck", "motorcyclist_injury", "hit_and_run",
		"nonqualifying_vehicle_only", "nonqualifying_single_bicycle", "historical", "multiple_incidents",
		"multiple_articles_one_collision", "injury_later_fatal", "missing_or_ambiguous_location", "conflicting_source_reports"}
	have := map[string]bool{}
	for _, c := range load(t) {
		have[c.Scenario] = true
	}
	for _, s := range want {
		if !have[s] {
			t.Errorf("missing scenario %s", s)
		}
	}
}

func TestReplayBaselinePassesEverything(t *testing.T) {
	r := run(t, nil)
	if r.Errors != 0 || r.Precision != 1 || r.Recall != 1 || r.UnverifiedQuotes != 0 {
		t.Fatalf("baseline should be perfect (cases and synthetic responses are consistent): %+v", r)
	}
	for _, c := range r.Results {
		if !c.Pass {
			t.Errorf("%s failed: %v", c.ID, c.Diffs)
		}
	}
}

func TestWrongCityLowersLocationAccuracy(t *testing.T) {
	r := run(t, func(id, b string) string {
		if id == "ped_fatality" {
			return strings.Replace(b, `"city": "Oakland"`, `"city": "Berkeley"`, 1)
		}
		return b
	})
	if f := r.Fields["location"]; f.Correct != f.Total-1 {
		t.Fatalf("location %+v", f)
	}
}

func TestWrongDateAndFatalityCount(t *testing.T) {
	r := run(t, func(id, b string) string {
		if id == "update_to_fatal" {
			b = strings.Replace(b, `"occurred_at": "2026-10-13"`, `"occurred_at": "2026-10-16"`, 1)
			b = strings.Replace(b, `"fatality_count": 1`, `"fatality_count": 0`, 1)
		}
		return b
	})
	if f := r.Fields["date"]; f.Correct != f.Total-1 {
		t.Fatalf("date %+v", f)
	}
	if f := r.Fields["fatality_count"]; f.Correct != f.Total-1 {
		t.Fatalf("fatality %+v", f)
	}
}

func TestHallucinatedQuoteCounted(t *testing.T) {
	r := run(t, func(id, b string) string {
		if id == "hit_and_run" {
			return strings.Replace(b, "kept going", "sped away at high speed", 1)
		}
		return b
	})
	if r.UnverifiedQuotes != 1 || r.HallucinationRate <= 0 {
		t.Fatalf("%+v", r)
	}
}

func TestInventedIncidentIsFalsePositive(t *testing.T) {
	r := run(t, func(id, b string) string {
		if id == "single_bike_fall" {
			return `{"verdict":"possible","article_kind":"current_incident","collision_occurred":true,"motor_vehicle_involved":true,"vru_involved":true,"injury_or_death":true,"multiple_incidents":false,"updates_earlier_incident":false,
			"incidents":[{"occurred_at":null,"occurred_at_precision":"unknown","timezone":null,"city":"Berkeley","county":null,"state":"CA","country":"US","street_address":null,"location_precision":"street","vru_categories":["bicyclist"],"vehicle_categories":["unknown"],"injured_count":1,"fatality_count":null,"severity":"unknown","hit_and_run":"unknown","circumstances":null,"facts_status":"unclear","evidence":[]}]}`
		}
		return b
	})
	if r.FP != 1 || r.Precision >= 1 || r.InventedIncidentCases != 1 || r.SpuriousIncidents != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestPairingMatchesByCityNotOrder(t *testing.T) {
	exp := []ExpectedIncident{{City: "Clovis", Date: "2026-10-10"}, {City: "Sanger", Date: "2026-10-11"}}
	cs := load(t)
	var c Case
	for _, x := range cs {
		if x.ID == "multiple_collisions" {
			c = x
		}
	}
	cl, _ := classification.New(Replay{Body: string(c.SyntheticResponse)}, classification.Config{PrimaryModel: "r"})
	res, err := cl.Classify(context.Background(), c.DomainArticle())
	if err != nil {
		t.Fatal(err)
	}
	// Reverse the predicted order; pairing must still match by content.
	g := res.Incidents
	g[0], g[1] = g[1], g[0]
	pairs, spurious := pair(exp, g)
	if spurious != 0 || pairs[0] == nil || pairs[0].City != "Clovis" || pairs[1] == nil || pairs[1].City != "Sanger" {
		t.Fatalf("pairs=%v spurious=%d", pairs, spurious)
	}
	// Remove one prediction: the other expected incident is reported missing.
	pairs, _ = pair(exp, g[:1])
	if (pairs[0] == nil) == (pairs[1] == nil) {
		t.Fatalf("exactly one expected incident should be matched: %v", pairs)
	}
}

func TestClassificationErrorCountsAsMiss(t *testing.T) {
	r := run(t, func(id, b string) string {
		if id == "ped_fatality" {
			return "not json"
		}
		return b
	})
	if r.Errors != 1 || r.FN != 1 || r.Recall >= 1 {
		t.Fatalf("%+v", r)
	}
}
