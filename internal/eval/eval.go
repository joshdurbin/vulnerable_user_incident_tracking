// Package eval measures classification/extraction quality against labeled cases.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/classification"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/domain"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/llm"
)

// ExpectedIncident holds only the labels that are certain. Unset fields are not scored.
type ExpectedIncident struct {
	Date              string   `json:"date,omitempty"` // YYYY-MM-DD
	City              string   `json:"city,omitempty"`
	State             string   `json:"state,omitempty"`
	VRUCategories     []string `json:"vru_categories,omitempty"`
	FatalityCount     *int     `json:"fatality_count,omitempty"`
	InjuredCount      *int     `json:"injured_count,omitempty"`
	Severity          string   `json:"severity,omitempty"`
	HitAndRun         string   `json:"hit_and_run,omitempty"`
	LocationPrecision string   `json:"location_precision,omitempty"`
}

type Expected struct {
	Verdict   domain.Verdict     `json:"verdict"`
	Kind      domain.ArticleKind `json:"kind"`
	Incidents []ExpectedIncident `json:"incidents"`
}

type Case struct {
	ID        string `json:"id"`
	Scenario  string `json:"scenario"`
	Synthetic bool   `json:"synthetic"`
	Note      string `json:"note,omitempty"`
	Article   struct {
		Title       string    `json:"title"`
		Publisher   string    `json:"publisher"`
		PublishedAt time.Time `json:"published_at"`
		Text        string    `json:"text"`
	} `json:"article"`
	Expected Expected `json:"expected"`
	// SyntheticResponse is an AUTHOR-WRITTEN stand-in for model output, used only by
	// replay mode to test the harness. It says nothing about any real model.
	SyntheticResponse json.RawMessage `json:"synthetic_response,omitempty"`
}

func (c *Case) DomainArticle() *domain.Article {
	pub := c.Article.PublishedAt
	return &domain.Article{Title: c.Article.Title, Publisher: c.Article.Publisher, PublishedAt: &pub, Text: c.Article.Text}
}

// Load reads every *.json case in dir, sorted by ID.
func Load(dir string) ([]Case, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var cases []Case
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var c Case
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		cases = append(cases, c)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("no cases in %s", dir)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases, nil
}

// Replay returns a Completer that answers with a fixed body (harness testing).
type Replay struct{ Body string }

func (r Replay) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: r.Body, Model: req.Model, Mode: llm.ModeJSONSchema}, nil
}

// Builder returns the classifier to use for a case.
type Builder func(Case) (*classification.Classifier, error)

type Ratio struct {
	Correct int `json:"correct"`
	Total   int `json:"total"`
}

func (r Ratio) Value() float64 {
	if r.Total == 0 {
		return 0
	}
	return float64(r.Correct) / float64(r.Total)
}
func (r *Ratio) add(ok bool) {
	r.Total++
	if ok {
		r.Correct++
	}
}

type CaseResult struct {
	ID       string   `json:"id"`
	Scenario string   `json:"scenario"`
	Pass     bool     `json:"pass"`
	Error    string   `json:"error,omitempty"`
	Diffs    []string `json:"diffs,omitempty"`
}

type Report struct {
	Cases     int  `json:"cases"`
	Errors    int  `json:"errors"` // classification failed outright
	Synthetic bool `json:"uses_synthetic_cases"`

	// Article-level classification (positive = confirmed or possible).
	TP, FP, FN, TN int            `json:"-"`
	Counts         map[string]int `json:"classification_counts"`
	Precision      float64        `json:"classification_precision"`
	Recall         float64        `json:"classification_recall"`
	KindAccuracy   Ratio          `json:"article_kind_accuracy"`

	// Field accuracy over paired incidents.
	Fields            map[string]*Ratio `json:"field_accuracy"`
	IncidentRecall    Ratio             `json:"incident_recall"`
	SpuriousIncidents int               `json:"spurious_incidents"`

	// Hallucination: evidence quotes not found in the article, and incidents
	// invented for articles labeled to have none.
	EvidenceQuotes        int     `json:"evidence_quotes"`
	UnverifiedQuotes      int     `json:"unverified_quotes"`
	HallucinationRate     float64 `json:"hallucination_rate"`
	InventedIncidentCases int     `json:"invented_incident_cases"`

	AvgLatencyMS float64 `json:"avg_latency_ms"`

	Results []CaseResult `json:"results"`
}

func isPositive(v domain.Verdict) bool {
	return v == domain.VerdictConfirmed || v == domain.VerdictPossible
}

// Run classifies every case and scores it against the labels.
func Run(ctx context.Context, cases []Case, build Builder) (*Report, error) {
	r := &Report{Cases: len(cases), Fields: map[string]*Ratio{}, Counts: map[string]int{}}
	var latency time.Duration
	var correctIncidents int
	for _, c := range cases {
		if c.Synthetic {
			r.Synthetic = true
		}
		cr := CaseResult{ID: c.ID, Scenario: c.Scenario, Pass: true}
		cl, err := build(c)
		if err != nil {
			return nil, err
		}
		res, err := cl.Classify(ctx, c.DomainArticle())
		if err != nil {
			r.Errors++
			cr.Pass, cr.Error = false, err.Error()
			r.Results = append(r.Results, cr)
			if isPositive(c.Expected.Verdict) {
				r.FN++
			}
			continue
		}
		latency += res.Meta.Latency

		exp, got := isPositive(c.Expected.Verdict), isPositive(res.Verdict)
		switch {
		case exp && got:
			r.TP++
		case !exp && got:
			r.FP++
			cr.Diffs = append(cr.Diffs, fmt.Sprintf("verdict %s, want %s", res.Verdict, c.Expected.Verdict))
		case exp && !got:
			r.FN++
			cr.Diffs = append(cr.Diffs, fmt.Sprintf("verdict %s, want %s", res.Verdict, c.Expected.Verdict))
		default:
			r.TN++
		}
		if exp && got && res.Verdict != c.Expected.Verdict {
			cr.Diffs = append(cr.Diffs, fmt.Sprintf("verdict %s, want %s", res.Verdict, c.Expected.Verdict))
		}
		r.KindAccuracy.add(res.ArticleKind == c.Expected.Kind)
		if res.ArticleKind != c.Expected.Kind {
			cr.Diffs = append(cr.Diffs, fmt.Sprintf("kind %s, want %s", res.ArticleKind, c.Expected.Kind))
		}

		for _, in := range res.Incidents {
			for _, e := range in.Evidence {
				r.EvidenceQuotes++
				if !e.Verified {
					r.UnverifiedQuotes++
					cr.Diffs = append(cr.Diffs, fmt.Sprintf("unverified quote for %s", e.Field))
				}
			}
		}
		if len(c.Expected.Incidents) == 0 && len(res.Incidents) > 0 {
			r.InventedIncidentCases++
			r.SpuriousIncidents += len(res.Incidents)
		}
		pairs, spurious := pair(c.Expected.Incidents, res.Incidents)
		if len(c.Expected.Incidents) > 0 {
			r.SpuriousIncidents += spurious
		}
		for i, exp := range c.Expected.Incidents {
			r.IncidentRecall.add(pairs[i] != nil)
			if pairs[i] == nil {
				cr.Diffs = append(cr.Diffs, fmt.Sprintf("missed incident %d (%s %s)", i, exp.City, exp.Date))
				continue
			}
			okAll := score(r, exp, *pairs[i], &cr)
			if okAll {
				correctIncidents++
			}
		}
		cr.Pass = len(cr.Diffs) == 0
		r.Results = append(r.Results, cr)
	}
	if r.TP+r.FP > 0 {
		r.Precision = float64(r.TP) / float64(r.TP+r.FP)
	}
	if r.TP+r.FN > 0 {
		r.Recall = float64(r.TP) / float64(r.TP+r.FN)
	}
	r.Counts = map[string]int{"tp": r.TP, "fp": r.FP, "fn": r.FN, "tn": r.TN}
	if r.EvidenceQuotes > 0 {
		r.HallucinationRate = float64(r.UnverifiedQuotes) / float64(r.EvidenceQuotes)
	}
	if n := r.Cases - r.Errors; n > 0 {
		r.AvgLatencyMS = float64(latency.Milliseconds()) / float64(n)
	}
	return r, nil
}

func field(r *Report, name string) *Ratio {
	if r.Fields[name] == nil {
		r.Fields[name] = &Ratio{}
	}
	return r.Fields[name]
}

// score compares one paired incident field by field; returns true if all scored fields match.
func score(r *Report, e ExpectedIncident, g domain.IncidentCandidate, cr *CaseResult) bool {
	all := true
	check := func(name string, scored, ok bool, want, got any) {
		if !scored {
			return
		}
		field(r, name).add(ok)
		if !ok {
			all = false
			cr.Diffs = append(cr.Diffs, fmt.Sprintf("%s: got %v, want %v", name, got, want))
		}
	}
	gotDate := g.OccurredAt
	if len(gotDate) >= 10 {
		gotDate = gotDate[:10]
	}
	check("date", e.Date != "", gotDate == e.Date, e.Date, gotDate)
	check("location", e.City != "" || e.State != "", (e.City == "" || strings.EqualFold(e.City, g.City)) && (e.State == "" || strings.EqualFold(e.State, g.State)),
		e.City+","+e.State, g.City+","+g.State)
	check("location_precision", e.LocationPrecision != "", e.LocationPrecision == g.LocationPrecision, e.LocationPrecision, g.LocationPrecision)
	check("vru_categories", len(e.VRUCategories) > 0, sameSet(e.VRUCategories, g.VRUCategories), e.VRUCategories, g.VRUCategories)
	check("severity", e.Severity != "", e.Severity == g.Severity, e.Severity, g.Severity)
	check("fatality_count", e.FatalityCount != nil, e.FatalityCount != nil && g.FatalityCount != nil && *e.FatalityCount == *g.FatalityCount, ptr(e.FatalityCount), ptr(g.FatalityCount))
	check("injured_count", e.InjuredCount != nil, e.InjuredCount != nil && g.InjuredCount != nil && *e.InjuredCount == *g.InjuredCount, ptr(e.InjuredCount), ptr(g.InjuredCount))
	check("hit_and_run", e.HitAndRun != "", e.HitAndRun == g.HitAndRun, e.HitAndRun, g.HitAndRun)
	return all
}

func ptr(p *int) any {
	if p == nil {
		return "nil"
	}
	return *p
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

// pair greedily matches each expected incident to the best unused predicted one.
// Returns, per expected index, the matched candidate (nil if none) and the count
// of unmatched predictions.
func pair(exp []ExpectedIncident, got []domain.IncidentCandidate) ([]*domain.IncidentCandidate, int) {
	out := make([]*domain.IncidentCandidate, len(exp))
	used := make([]bool, len(got))
	for i, e := range exp {
		best, bestScore := -1, 0
		for j, g := range got {
			if used[j] {
				continue
			}
			s := 0
			if e.City != "" && strings.EqualFold(e.City, g.City) {
				s += 2
			}
			if e.Date != "" && strings.HasPrefix(g.OccurredAt, e.Date) {
				s += 2
			}
			if e.State != "" && strings.EqualFold(e.State, g.State) {
				s++
			}
			if len(e.VRUCategories) > 0 && sameSet(e.VRUCategories, g.VRUCategories) {
				s++
			}
			if s > bestScore {
				best, bestScore = j, s
			}
		}
		// With a single expected and single predicted incident, always pair them.
		if best == -1 && len(exp) == 1 && len(got) == 1 {
			best = 0
		}
		if best >= 0 {
			used[best] = true
			g := got[best]
			out[i] = &g
		}
	}
	spurious := 0
	for _, u := range used {
		if !u {
			spurious++
		}
	}
	return out, spurious
}
