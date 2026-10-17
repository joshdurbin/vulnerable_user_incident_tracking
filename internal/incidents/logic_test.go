package incidents

import (
	"strings"
	"testing"
	"time"

	"github.com/joshdurbin/fuck_cars/internal/domain"
)

func ip(i int) *int { return &i }

func obs(at string, c domain.IncidentCandidate) Observation {
	t, _ := time.Parse(time.RFC3339, at)
	return Observation{Candidate: c, ObservedAt: t, ArticleKind: domain.KindCurrent}
}

func pasadena(mut func(*domain.IncidentCandidate)) domain.IncidentCandidate {
	c := domain.IncidentCandidate{
		OccurredAt: "2026-10-13", OccurredAtPrecision: "date", City: "Pasadena", State: "CA",
		StreetAddress: "Colorado Boulevard at Lake Avenue", LocationPrecision: "intersection",
		VRUCategories: []string{"wheelchair_user"}, VehicleCategories: []string{"passenger_car"},
		Severity: "serious", HitAndRun: "no", FactsStatus: "confirmed",
	}
	if mut != nil {
		mut(&c)
	}
	return c
}

func stateOf(c domain.IncidentCandidate) State { return stateFrom(Observation{Candidate: c}) }

func TestSameCrashFromTwoOutletsAutoMatches(t *testing.T) {
	a := stateOf(pasadena(nil))
	b := stateOf(pasadena(func(c *domain.IncidentCandidate) {
		c.StreetAddress, c.Severity, c.HitAndRun = "Colorado and Lake", "unknown", "unknown"
	}))
	m := Compare(DefaultThresholds, a, b, -1)
	if m.Decision != DecisionAuto {
		t.Fatalf("want auto, got %+v", m)
	}
}

func TestInjuryThenDeathStillMatches(t *testing.T) {
	a := stateOf(pasadena(nil))
	b := stateOf(pasadena(func(c *domain.IncidentCandidate) { c.Severity, c.FatalityCount = "fatal", ip(1) }))
	if m := Compare(DefaultThresholds, a, b, -1); m.Decision != DecisionAuto {
		t.Fatalf("%+v", m)
	}
}

func TestNeverMergeOnProximityAlone(t *testing.T) {
	// Same intersection, same coordinates, but a different road-user type: distinct.
	a := stateOf(pasadena(nil))
	b := stateOf(pasadena(func(c *domain.IncidentCandidate) { c.VRUCategories = []string{"bicyclist"} }))
	if m := Compare(DefaultThresholds, a, b, 0); m.Decision != DecisionDistinct {
		t.Fatalf("different victims must stay distinct: %+v", m)
	}
	// Same place and victim type, but five days later: distinct.
	c := stateOf(pasadena(func(c *domain.IncidentCandidate) { c.OccurredAt = "2026-10-18" }))
	if m := Compare(DefaultThresholds, a, c, 0); m.Decision != DecisionDistinct {
		t.Fatalf("different days must stay distinct: %+v", m)
	}
	// Same day and victim, but 8 hours apart (both timed): distinct.
	d1 := stateOf(pasadena(func(c *domain.IncidentCandidate) {
		c.OccurredAt, c.OccurredAtPrecision = "2026-10-13T08:00:00", "datetime"
	}))
	d2 := stateOf(pasadena(func(c *domain.IncidentCandidate) {
		c.OccurredAt, c.OccurredAtPrecision = "2026-10-13T16:00:00", "datetime"
	}))
	if m := Compare(DefaultThresholds, d1, d2, 0); m.Decision != DecisionDistinct {
		t.Fatalf("hours apart must stay distinct: %+v", m)
	}
}

func TestDifferentCityOrFarCoordinatesDistinct(t *testing.T) {
	a := stateOf(pasadena(nil))
	if m := Compare(DefaultThresholds, a, stateOf(pasadena(func(c *domain.IncidentCandidate) { c.City = "Altadena" })), -1); m.Decision != DecisionDistinct {
		t.Fatalf("%+v", m)
	}
	if m := Compare(DefaultThresholds, a, a, 5000); m.Decision != DecisionDistinct {
		t.Fatalf("%+v", m)
	}
}

func TestUnknownDateCannotAutoMatch(t *testing.T) {
	a := stateOf(pasadena(nil))
	b := stateOf(pasadena(func(c *domain.IncidentCandidate) { c.OccurredAt, c.OccurredAtPrecision = "", "unknown" }))
	m := Compare(DefaultThresholds, a, b, -1)
	if m.Decision == DecisionAuto {
		t.Fatalf("unknown date must not auto-match: %+v", m)
	}
	if m.Decision != DecisionReview {
		t.Fatalf("expected a review candidate, got %+v", m)
	}
}

func TestCityOnlyLocationNeverStrongEnough(t *testing.T) {
	cityOnly := func(c *domain.IncidentCandidate) { c.StreetAddress, c.LocationPrecision = "", "city" }
	a, b := stateOf(pasadena(cityOnly)), stateOf(pasadena(cityOnly))
	if m := Compare(DefaultThresholds, a, b, -1); m.Decision == DecisionAuto {
		t.Fatalf("two city-level reports must not auto-merge: %+v", m)
	}
}

func TestHitAndRunConflictBlocksAutoMatch(t *testing.T) {
	a := stateOf(pasadena(nil))
	b := stateOf(pasadena(func(c *domain.IncidentCandidate) { c.HitAndRun = "yes" }))
	m := Compare(DefaultThresholds, a, b, -1)
	if m.Decision != DecisionReview || len(m.Notes) == 0 {
		t.Fatalf("%+v", m)
	}
}

func TestStreetSim(t *testing.T) {
	cases := []struct {
		a, b string
		min  float64
		max  float64
	}{
		{"Colorado Boulevard at Lake Avenue", "Colorado and Lake", 1, 1},
		{"Shaw Avenue at Blackstone", "Blackstone & Shaw Ave", 1, 1},
		{"Main Street", "Oak Street", 0, 0},
		{"", "Oak Street", -1, -1},
	}
	for _, c := range cases {
		if got := streetSim(c.a, c.b); got < c.min || got > c.max {
			t.Errorf("streetSim(%q,%q)=%v", c.a, c.b, got)
		}
	}
}

func TestFoldInjuryBecomesFatality(t *testing.T) {
	first := obs("2026-10-14T18:00:00Z", pasadena(func(c *domain.IncidentCandidate) { c.FatalityCount = ip(0) }))
	later := obs("2026-10-16T18:00:00Z", pasadena(func(c *domain.IncidentCandidate) { c.Severity, c.FatalityCount = "fatal", ip(1) }))
	st, conflicts := Fold([]Observation{later, first}) // order of input must not matter
	if *st.Fatal != 1 || st.Severity != "fatal" || len(conflicts) != 0 {
		t.Fatalf("%+v %v", st, conflicts)
	}
	d := Diff(mustFold(t, first), st)
	if d["fatality_count"][1] != 1 || d["severity"][1] != "fatal" {
		t.Fatalf("diff %v", d)
	}
}

func mustFold(t *testing.T, o ...Observation) State {
	st, _ := Fold(o)
	return st
}

func TestFoldLowerLaterCountIsConflictNotOverwrite(t *testing.T) {
	a := obs("2026-10-14T00:00:00Z", pasadena(func(c *domain.IncidentCandidate) { c.InjuredCount = ip(2) }))
	b := obs("2026-10-15T00:00:00Z", pasadena(func(c *domain.IncidentCandidate) { c.InjuredCount = ip(1) }))
	st, conflicts := Fold([]Observation{a, b})
	if *st.Injured != 2 || len(conflicts) != 1 || !strings.Contains(conflicts[0], "injured_count") {
		t.Fatalf("%+v %v", st, conflicts)
	}
}

func TestFoldMergesPrecisionAndCategories(t *testing.T) {
	a := obs("2026-10-14T00:00:00Z", pasadena(func(c *domain.IncidentCandidate) {
		c.StreetAddress, c.LocationPrecision, c.OccurredAt, c.OccurredAtPrecision = "", "city", "", "unknown"
		c.VehicleCategories = []string{"unknown"}
	}))
	b := obs("2026-10-15T00:00:00Z", pasadena(func(c *domain.IncidentCandidate) {
		c.OccurredAt = "2026-10-13T15:30:00"
		c.OccurredAtPrecision = "datetime"
	}))
	st, _ := Fold([]Observation{a, b})
	if st.LocationPrecision != "intersection" || st.StreetAddress == "" || st.OccurredPrecision != "datetime" || st.OccurredAt == nil {
		t.Fatalf("precision not upgraded: %+v", st)
	}
	if len(st.Vehicles) != 1 || st.Vehicles[0] != "passenger_car" {
		t.Fatalf("vehicles %v", st.Vehicles)
	}
}

func TestFoldConflictingDates(t *testing.T) {
	a := obs("2026-10-14T00:00:00Z", pasadena(nil))
	b := obs("2026-10-15T00:00:00Z", pasadena(func(c *domain.IncidentCandidate) { c.OccurredAt = "2026-10-12" }))
	_, conflicts := Fold([]Observation{a, b})
	if len(conflicts) == 0 {
		t.Fatal("expected a date conflict")
	}
}

func TestNewConflictsOnlyReportsDelta(t *testing.T) {
	got := newConflicts([]string{"a", "b"}, []string{"a", "b", "c"})
	if len(got) != 1 || got[0] != "c" {
		t.Fatal(got)
	}
}
