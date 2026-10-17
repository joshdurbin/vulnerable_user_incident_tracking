package classification

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/joshdurbin/fuck_cars/internal/domain"
)

// ValidationError means the model output is unusable (not JSON, schema
// violation, or an inconsistent verdict). It triggers the fallback model.
type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return "invalid model output: " + e.Reason }

func compileSchema(raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("schema.json")
}

// stripFences removes a markdown code fence some models add in non-native modes.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}

// wire mirrors the JSON schema; nullable strings use pointers.
type wireIncident struct {
	OccurredAt          *string           `json:"occurred_at"`
	OccurredAtPrecision string            `json:"occurred_at_precision"`
	Timezone            *string           `json:"timezone"`
	City                *string           `json:"city"`
	County              *string           `json:"county"`
	State               *string           `json:"state"`
	Country             *string           `json:"country"`
	StreetAddress       *string           `json:"street_address"`
	LocationPrecision   string            `json:"location_precision"`
	VRUCategories       []string          `json:"vru_categories"`
	VehicleCategories   []string          `json:"vehicle_categories"`
	InjuredCount        *int              `json:"injured_count"`
	FatalityCount       *int              `json:"fatality_count"`
	Severity            string            `json:"severity"`
	HitAndRun           string            `json:"hit_and_run"`
	Circumstances       *string           `json:"circumstances"`
	FactsStatus         string            `json:"facts_status"`
	Evidence            []domain.Evidence `json:"evidence"`
}

type wireResult struct {
	Verdict                domain.Verdict     `json:"verdict"`
	ArticleKind            domain.ArticleKind `json:"article_kind"`
	CollisionOccurred      *bool              `json:"collision_occurred"`
	MotorVehicleInvolved   *bool              `json:"motor_vehicle_involved"`
	VRUInvolved            *bool              `json:"vru_involved"`
	InjuryOrDeath          *bool              `json:"injury_or_death"`
	MultipleIncidents      bool               `json:"multiple_incidents"`
	UpdatesEarlierIncident bool               `json:"updates_earlier_incident"`
	Incidents              []wireIncident     `json:"incidents"`
}

func s(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// normalize collapses whitespace and case so quotes compare robustly.
func normalize(in string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(in) {
		switch {
		case unicode.IsSpace(r):
			space = true
		case r == '’' || r == '‘':
			b.WriteRune('\'')
			space = false
		case r == '“' || r == '”':
			b.WriteRune('"')
			space = false
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parseOccurred(v string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// validate parses and checks model output against the schema and semantic rules.
// Hard failures return *ValidationError. Soft problems become warnings and, where
// they undermine confidence, downgrade the verdict. Nothing is silently dropped.
func validate(sch *jsonschema.Schema, content, articleText string, now time.Time) (*domain.ExtractionResult, error) {
	content = stripFences(content)
	var generic any
	if err := json.Unmarshal([]byte(content), &generic); err != nil {
		return nil, &ValidationError{"not valid JSON: " + err.Error()}
	}
	if err := sch.Validate(generic); err != nil {
		return nil, &ValidationError{"schema violation: " + firstLine(err.Error())}
	}
	var w wireResult
	dec := json.NewDecoder(strings.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return nil, &ValidationError{"decode: " + err.Error()}
	}

	res := &domain.ExtractionResult{
		Verdict: w.Verdict, ArticleKind: w.ArticleKind,
		CollisionOccurred: w.CollisionOccurred, MotorVehicleInvolved: w.MotorVehicleInvolved,
		VRUInvolved: w.VRUInvolved, InjuryOrDeath: w.InjuryOrDeath,
		MultipleIncidents: w.MultipleIncidents, UpdatesEarlierIncident: w.UpdatesEarlierIncident,
		Incidents: []domain.IncidentCandidate{},
	}
	warn := func(f string, a ...any) { res.Warnings = append(res.Warnings, fmt.Sprintf(f, a...)) }

	qualifying := w.Verdict == domain.VerdictConfirmed || w.Verdict == domain.VerdictPossible
	if qualifying && len(w.Incidents) == 0 {
		return nil, &ValidationError{"verdict " + string(w.Verdict) + " but no incidents"}
	}
	if !qualifying && len(w.Incidents) > 0 {
		warn("verdict %s but %d incidents returned; keeping them for review", w.Verdict, len(w.Incidents))
	}
	if len(w.Incidents) > 1 && !w.MultipleIncidents {
		warn("multiple incidents returned but multiple_incidents=false")
		res.MultipleIncidents = true
	}

	haystack := normalize(articleText)
	downgrade := false
	for i, in := range w.Incidents {
		c := domain.IncidentCandidate{
			OccurredAt: s(in.OccurredAt), OccurredAtPrecision: in.OccurredAtPrecision, Timezone: s(in.Timezone),
			City: s(in.City), County: s(in.County), State: s(in.State), Country: s(in.Country),
			StreetAddress: s(in.StreetAddress), LocationPrecision: in.LocationPrecision,
			VRUCategories: in.VRUCategories, VehicleCategories: in.VehicleCategories,
			InjuredCount: in.InjuredCount, FatalityCount: in.FatalityCount, Severity: in.Severity,
			HitAndRun: in.HitAndRun, Circumstances: s(in.Circumstances), FactsStatus: in.FactsStatus,
		}
		if c.InjuredCount != nil && *c.InjuredCount < 0 || c.FatalityCount != nil && *c.FatalityCount < 0 {
			return nil, &ValidationError{fmt.Sprintf("incident %d has a negative count", i)}
		}
		if c.OccurredAt != "" {
			t, ok := parseOccurred(c.OccurredAt)
			switch {
			case !ok:
				warn("incident %d: unparseable occurred_at %q; cleared", i, c.OccurredAt)
				c.OccurredAt, c.OccurredAtPrecision = "", "unknown"
			case t.After(now.Add(48 * time.Hour)):
				warn("incident %d: occurred_at %s is in the future; cleared", i, c.OccurredAt)
				c.OccurredAt, c.OccurredAtPrecision = "", "unknown"
			case t.Year() < 1990:
				warn("incident %d: implausibly old occurred_at %s; cleared", i, c.OccurredAt)
				c.OccurredAt, c.OccurredAtPrecision = "", "unknown"
			}
		}
		if c.OccurredAt == "" {
			c.OccurredAtPrecision = "unknown"
		}
		if c.FatalityCount != nil && *c.FatalityCount > 0 && c.Severity != "fatal" {
			warn("incident %d: fatality_count>0 but severity=%s", i, c.Severity)
		}
		if c.Severity == "fatal" && (c.FatalityCount == nil || *c.FatalityCount == 0) {
			warn("incident %d: severity=fatal without a fatality_count", i)
		}
		verified := 0
		for _, ev := range in.Evidence {
			ev.Verified = ev.Quote != "" && strings.Contains(haystack, normalize(ev.Quote))
			if ev.Verified {
				verified++
			} else {
				warn("incident %d: evidence quote for %q not found in article text", i, ev.Field)
			}
			c.Evidence = append(c.Evidence, ev)
		}
		if c.Evidence == nil {
			c.Evidence = []domain.Evidence{}
		}
		if qualifying && verified == 0 {
			warn("incident %d: no verifiable evidence", i)
			downgrade = true
		}
		res.Incidents = append(res.Incidents, c)
	}
	if downgrade && res.Verdict == domain.VerdictConfirmed {
		warn("verdict downgraded confirmed -> possible: no verifiable evidence")
		res.Verdict = domain.VerdictPossible
	}
	return res, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
