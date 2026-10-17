package incidents

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

var locationRank = map[string]int{
	"exact_address": 7, "intersection": 6, "block": 5, "street": 4, "neighborhood": 3, "city": 2, "county": 1, "unknown": 0,
}
var severityRank = map[string]int{"unknown": 0, "minor": 1, "serious": 2, "fatal": 3}
var occurredRank = map[string]int{"unknown": 0, "date": 1, "datetime": 2}

// parseOccurred splits the extracted occurred_at into a date, an optional time and a precision.
func parseOccurred(v string) (on *time.Time, at *time.Time, precision string) {
	if v == "" {
		return nil, nil, "unknown"
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
			return &d, &t, "datetime"
		}
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return &t, nil, "date"
	}
	return nil, nil, "unknown"
}

func ptrInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func stateFrom(o Observation) State {
	c := o.Candidate
	on, at, prec := parseOccurred(c.OccurredAt)
	if c.OccurredAtPrecision == "date" && prec == "datetime" { // trust a stated date-only precision
		at, prec = nil, "date"
	}
	return State{
		OccurredOn: on, OccurredAt: at, OccurredPrecision: prec, Timezone: c.Timezone,
		City: c.City, County: c.County, State: c.State, StreetAddress: c.StreetAddress, LocationPrecision: orUnknown(c.LocationPrecision),
		Lat: o.Lat, Lon: o.Lon, GeocodeSource: o.GeocodeSource, GeocodeConfidence: o.GeocodeConfidence, GeocodePrecision: o.GeocodePrecision,
		VRU: clean(c.VRUCategories), Vehicles: clean(c.VehicleCategories),
		Injured: ptrInt(c.InjuredCount), Fatal: ptrInt(c.FatalityCount),
		Severity: orUnknown(c.Severity), HitAndRun: orUnknown(c.HitAndRun), Circumstances: c.Circumstances, FactsStatus: orDefault(c.FactsStatus, "unclear"),
	}
}

func orUnknown(s string) string { return orDefault(s, "unknown") }
func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// clean drops "unknown" placeholders and duplicates, keeping a stable order.
func clean(in []string) []string {
	out := []string{}
	for _, s := range in {
		if s != "" && s != "unknown" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func union(a, b []string) []string { return clean(append(slices.Clone(a), b...)) }

// Fold derives the canonical state from observations (oldest first). It also
// returns conflicts: facts that disagree between articles and were not silently overwritten.
func Fold(obs []Observation) (State, []string) {
	if len(obs) == 0 {
		return State{OccurredPrecision: "unknown", LocationPrecision: "unknown", Severity: "unknown", HitAndRun: "unknown", FactsStatus: "unclear"}, nil
	}
	sorted := slices.Clone(obs)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ObservedAt.Before(sorted[j].ObservedAt) })
	st := stateFrom(sorted[0])
	var conflicts []string
	for _, o := range sorted[1:] {
		conflicts = append(conflicts, merge(&st, stateFrom(o), o)...)
	}
	return st, conflicts
}

func merge(st *State, n State, o Observation) []string {
	var conflicts []string

	// Time: keep the most precise statement; a different date is a conflict.
	if occurredRank[n.OccurredPrecision] > occurredRank[st.OccurredPrecision] {
		if st.OccurredOn != nil && n.OccurredOn != nil && !st.OccurredOn.Equal(*n.OccurredOn) {
			conflicts = append(conflicts, fmt.Sprintf("occurred_on: %s vs %s", st.OccurredOn.Format("2006-01-02"), n.OccurredOn.Format("2006-01-02")))
		}
		st.OccurredOn, st.OccurredAt, st.OccurredPrecision = n.OccurredOn, n.OccurredAt, n.OccurredPrecision
		if n.Timezone != "" {
			st.Timezone = n.Timezone
		}
	} else if st.OccurredOn != nil && n.OccurredOn != nil && !st.OccurredOn.Equal(*n.OccurredOn) {
		conflicts = append(conflicts, fmt.Sprintf("occurred_on: %s vs %s", st.OccurredOn.Format("2006-01-02"), n.OccurredOn.Format("2006-01-02")))
	}

	// Location: take the more precise description; keep coordinates from the most precise geocode.
	if locationRank[n.LocationPrecision] > locationRank[st.LocationPrecision] {
		st.StreetAddress, st.LocationPrecision = n.StreetAddress, n.LocationPrecision
		if n.Lat != nil && n.Lon != nil {
			st.Lat, st.Lon, st.GeocodeSource, st.GeocodeConfidence, st.GeocodePrecision = n.Lat, n.Lon, n.GeocodeSource, n.GeocodeConfidence, n.GeocodePrecision
		}
	} else if st.Lat == nil && n.Lat != nil {
		st.Lat, st.Lon, st.GeocodeSource, st.GeocodeConfidence, st.GeocodePrecision = n.Lat, n.Lon, n.GeocodeSource, n.GeocodeConfidence, n.GeocodePrecision
	}
	if st.City == "" {
		st.City = n.City
	}
	if st.County == "" {
		st.County = n.County
	}
	if st.State == "" {
		st.State = n.State
	}

	st.VRU, st.Vehicles = union(st.VRU, n.VRU), union(st.Vehicles, n.Vehicles)

	// Counts only go up automatically (e.g. injury later becomes a death). A lower
	// later count is kept as a conflict for review, not applied.
	st.Fatal = mergeCount("fatality_count", st.Fatal, n.Fatal, &conflicts)
	st.Injured = mergeCount("injured_count", st.Injured, n.Injured, &conflicts)

	switch {
	case severityRank[n.Severity] > severityRank[st.Severity]:
		st.Severity = n.Severity
	case severityRank[n.Severity] < severityRank[st.Severity] && n.Severity != "unknown":
		conflicts = append(conflicts, fmt.Sprintf("severity: %s reported after %s", n.Severity, st.Severity))
	}
	// An update article that reports a death implies fatal severity.
	if st.Fatal != nil && *st.Fatal > 0 {
		st.Severity = "fatal"
	}

	switch {
	case st.HitAndRun == "unknown":
		st.HitAndRun = n.HitAndRun
	case n.HitAndRun != "unknown" && n.HitAndRun != st.HitAndRun:
		conflicts = append(conflicts, fmt.Sprintf("hit_and_run: %s vs %s", st.HitAndRun, n.HitAndRun))
	}

	if n.Circumstances != "" && (st.Circumstances == "" || len(n.Circumstances) > len(st.Circumstances)) {
		st.Circumstances = n.Circumstances
	}
	if n.FactsStatus == "confirmed" || (st.FactsStatus == "unclear" && n.FactsStatus != "") {
		st.FactsStatus = n.FactsStatus
	}
	return conflicts
}

func mergeCount(name string, cur, n *int, conflicts *[]string) *int {
	switch {
	case n == nil:
		return cur
	case cur == nil || *n > *cur:
		return ptrInt(n)
	case *n < *cur:
		*conflicts = append(*conflicts, fmt.Sprintf("%s: %d reported after %d", name, *n, *cur))
	}
	return cur
}

// newConflicts returns conflicts present after but not before.
func newConflicts(before, after []string) []string {
	var out []string
	for _, c := range after {
		if !slices.Contains(before, c) {
			out = append(out, c)
		}
	}
	return out
}

// Diff lists human-readable changes between two states for the audit trail.
func Diff(a, b State) map[string][2]any {
	d := map[string][2]any{}
	chg := func(name string, x, y any) {
		if fmt.Sprint(x) != fmt.Sprint(y) {
			d[name] = [2]any{x, y}
		}
	}
	date := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Format("2006-01-02")
	}
	num := func(p *int) any {
		if p == nil {
			return nil
		}
		return *p
	}
	chg("occurred_on", date(a.OccurredOn), date(b.OccurredOn))
	chg("occurred_precision", a.OccurredPrecision, b.OccurredPrecision)
	chg("street_address", a.StreetAddress, b.StreetAddress)
	chg("location_precision", a.LocationPrecision, b.LocationPrecision)
	chg("city", a.City, b.City)
	chg("vru_categories", strings.Join(a.VRU, ","), strings.Join(b.VRU, ","))
	chg("vehicle_categories", strings.Join(a.Vehicles, ","), strings.Join(b.Vehicles, ","))
	chg("injured_count", num(a.Injured), num(b.Injured))
	chg("fatality_count", num(a.Fatal), num(b.Fatal))
	chg("severity", a.Severity, b.Severity)
	chg("hit_and_run", a.HitAndRun, b.HitAndRun)
	chg("facts_status", a.FactsStatus, b.FactsStatus)
	return d
}
