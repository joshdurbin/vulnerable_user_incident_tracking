package incidents

import (
	"math"
	"slices"
	"strings"
	"time"
)

// Thresholds and weights for deterministic matching. An automatic merge needs
// BOTH a high total score AND independent agreement on date, place and victim
// type, so proximity alone never merges two incidents.
type Thresholds struct {
	Auto   float64 // total score for an automatic match
	Review float64 // total score for a possible match queued for review
}

var DefaultThresholds = Thresholds{Auto: 0.80, Review: 0.55}

var streetNoise = map[string]bool{
	"boulevard": true, "blvd": true, "avenue": true, "ave": true, "street": true, "st": true, "road": true, "rd": true,
	"drive": true, "dr": true, "lane": true, "ln": true, "highway": true, "hwy": true, "way": true, "court": true, "ct": true,
	"near": true, "at": true, "and": true, "the": true, "of": true, "block": true, "intersection": true, "&": true, "in": true, "on": true,
}

func streetTokens(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if !streetNoise[f] && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// streetSim is the overlap coefficient of the street-name tokens (-1 if either side is empty).
func streetSim(a, b string) float64 {
	ta, tb := streetTokens(a), streetTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return -1
	}
	n := 0
	for _, t := range ta {
		if slices.Contains(tb, t) {
			n++
		}
	}
	return float64(n) / float64(min(len(ta), len(tb)))
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

// Compare scores a new observation against an existing incident's canonical
// state. distM is the distance between precise coordinates, or <0 if unknown.
func Compare(th Thresholds, existing State, n State, distM float64) Match {
	m := Match{Parts: map[string]float64{}}
	reject := func(why string) Match {
		m.Decision, m.Score = DecisionDistinct, 0
		m.Notes = append(m.Notes, why)
		return m
	}

	// --- date ---
	var date float64
	dateKnown := existing.OccurredOn != nil && n.OccurredOn != nil
	if dateKnown {
		days := math.Abs(n.OccurredOn.Sub(*existing.OccurredOn).Hours() / 24)
		switch {
		case days == 0:
			date = 1
		case days <= 1:
			date = 0.6
		case days <= 2:
			date = 0.2
		default:
			return reject("dates more than 2 days apart")
		}
		if existing.OccurredAt != nil && n.OccurredAt != nil {
			if d := n.OccurredAt.Sub(*existing.OccurredAt); d > 3*time.Hour || d < -3*time.Hour {
				return reject("incident times more than 3 hours apart")
			}
		}
	} else {
		m.Notes = append(m.Notes, "date unknown on one side: cannot auto-match")
	}

	// --- victims ---
	var vru float64
	switch {
	case len(existing.VRU) == 0 || len(n.VRU) == 0:
		vru = 0.5
	case overlaps(existing.VRU, n.VRU):
		vru = 1
	default:
		return reject("different road-user types")
	}

	// --- place ---
	var place float64
	coarse := locationRank[existing.LocationPrecision] <= locationRank["city"] && locationRank[n.LocationPrecision] <= locationRank["city"]
	switch {
	case distM >= 0:
		switch {
		case distM <= 75:
			place = 1
		case distM <= 250:
			place = 0.8
		case distM <= 600:
			place = 0.5
		case distM <= 2000:
			place = 0.15
		default:
			return reject("coordinates more than 2 km apart")
		}
	default:
		if existing.City != "" && n.City != "" && !strings.EqualFold(existing.City, n.City) {
			return reject("different cities")
		}
		sim := streetSim(existing.StreetAddress, n.StreetAddress)
		sameCity := existing.City != "" && strings.EqualFold(existing.City, n.City)
		switch {
		case sim >= 0.99 && sameCity:
			place = 0.85
		case sim >= 0.5 && sameCity:
			place = 0.65
		case sim > 0 && sameCity:
			place = 0.4
		case sim == 0:
			place = 0.05 // both name a street and share none
		case sameCity:
			place = 0.25
		case existing.County != "" && strings.EqualFold(existing.County, n.County):
			place = 0.15
		}
	}
	if coarse && place > 0.25 {
		place = 0.25 // city-level knowledge on both sides is never strong evidence
	}

	// --- vehicle ---
	vehicle := 0.5
	if len(existing.Vehicles) > 0 && len(n.Vehicles) > 0 {
		if overlaps(existing.Vehicles, n.Vehicles) {
			vehicle = 1
		} else {
			vehicle = 0.4
		}
	}

	// --- outcome (severity, hit-and-run) ---
	outcome := 0.6
	switch {
	case existing.Severity == n.Severity && existing.Severity != "unknown":
		outcome = 1
	case existing.Severity == "unknown" || n.Severity == "unknown":
		outcome = 0.6
	case absInt(severityRank[existing.Severity]-severityRank[n.Severity]) == 1:
		outcome = 0.7 // e.g. serious then fatal: plausibly an update
	default:
		outcome = 0.3
	}
	factConflict := false
	if existing.HitAndRun != "unknown" && n.HitAndRun != "unknown" && existing.HitAndRun != n.HitAndRun {
		outcome, factConflict = 0, true
		m.Notes = append(m.Notes, "hit-and-run status disagrees")
	}

	m.Parts = map[string]float64{"date": date, "place": place, "vru": vru, "vehicle": vehicle, "outcome": outcome}
	m.Score = 0.30*date + 0.35*place + 0.20*vru + 0.05*vehicle + 0.10*outcome

	switch {
	case m.Score >= th.Auto && dateKnown && place >= 0.6 && vru >= 1 && !factConflict:
		m.Decision = DecisionAuto
	case m.Score >= th.Review:
		m.Decision = DecisionReview
	default:
		m.Decision = DecisionDistinct
	}
	return m
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
