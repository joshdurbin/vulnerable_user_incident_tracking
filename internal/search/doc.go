package search

import (
	"encoding/json"
	"strings"
	"time"
)

// Doc is one incident as indexed. Only fields people search or filter on are here; the API loads the
// full record from PostgreSQL.
type Doc struct {
	ID            string    `json:"id"`
	OccurredOn    *string   `json:"occurred_on,omitempty"` // YYYY-MM-DD
	City          string    `json:"city,omitempty"`
	County        string    `json:"county,omitempty"`
	State         string    `json:"state,omitempty"`
	StreetAddress string    `json:"street_address,omitempty"`
	Circumstances string    `json:"circumstances,omitempty"`
	Severity      string    `json:"severity"`
	HitAndRun     string    `json:"hit_and_run"`
	Verification  string    `json:"verification_status"`
	VRU           []string  `json:"vru_categories"`
	Vehicles      []string  `json:"vehicle_categories"`
	Labels        string    `json:"labels"` // the categories and outcome in plain words, so "fatal cyclist" matches
	Injured       *int      `json:"injured_count,omitempty"`
	Fatalities    *int      `json:"fatality_count,omitempty"`
	Location      *GeoPoint `json:"location,omitempty"`
	ArticleCount  int       `json:"article_count"`
	Titles        []string  `json:"article_titles"`
	Publishers    []string  `json:"publishers"`
	Descriptions  []string  `json:"article_descriptions"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// GeoPoint is an OpenSearch geo_point object.
type GeoPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// Labels turns an incident's categories into the words a person would type.
func Labels(severity, hitAndRun string, vru, vehicles []string) string {
	var w []string
	add := func(s string) {
		if s != "" && s != "unknown" {
			w = append(w, strings.ReplaceAll(s, "_", " "))
		}
	}
	add(severity)
	for _, c := range vru {
		add(c)
	}
	for _, c := range vehicles {
		add(c)
	}
	if hitAndRun == "yes" {
		w = append(w, "hit and run")
	}
	return strings.Join(w, " ")
}

// searchFields are the text fields a query reads, with how much a match in each counts.
var searchFields = []string{
	"street_address^3", "city^2", "county", "state", "labels^2", "circumstances", "article_titles^2", "publishers", "article_descriptions",
}

func queryBody(q string, limit int) map[string]any {
	return map[string]any{
		"size": limit, "_source": false, "track_total_hits": false,
		"query": map[string]any{"bool": map[string]any{
			"should": []any{
				// Exact words, all of them, in any of the fields; the strongest signal.
				map[string]any{"simple_query_string": map[string]any{
					"query": q, "fields": searchFields, "default_operator": "and",
					"flags": "AND|OR|PREFIX|PHRASE|WHITESPACE|ESCAPE", "boost": 3,
				}},
				// Tolerates typos and a missing word.
				map[string]any{"multi_match": map[string]any{
					"query": q, "fields": searchFields, "type": "most_fields",
					"fuzziness": "AUTO", "prefix_length": 1, "minimum_should_match": "75%",
				}},
			},
			"minimum_should_match": 1,
		}},
	}
}

// mapping is the index definition: strict, so a field that is not listed here fails loudly.
func mapping(replicas int) []byte {
	text := func(analyzer string) map[string]any { return map[string]any{"type": "text", "analyzer": analyzer} }
	m := map[string]any{
		"settings": map[string]any{
			"number_of_shards": 1, "number_of_replicas": replicas,
			"analysis": map[string]any{"analyzer": map[string]any{
				// Names and places: split on words, lowercase, and fold accents (so "Cañon" finds "Canon").
				"place": map[string]any{"type": "custom", "tokenizer": "standard", "filter": []string{"lowercase", "asciifolding"}},
			}},
		},
		"mappings": map[string]any{"dynamic": "strict", "properties": map[string]any{
			"id":                   map[string]any{"type": "keyword"},
			"occurred_on":          map[string]any{"type": "date", "format": "yyyy-MM-dd"},
			"city":                 text("place"),
			"county":               text("place"),
			"state":                text("place"),
			"street_address":       text("place"),
			"circumstances":        text("english"),
			"severity":             map[string]any{"type": "keyword"},
			"hit_and_run":          map[string]any{"type": "keyword"},
			"verification_status":  map[string]any{"type": "keyword"},
			"vru_categories":       map[string]any{"type": "keyword"},
			"vehicle_categories":   map[string]any{"type": "keyword"},
			"labels":               text("english"),
			"injured_count":        map[string]any{"type": "integer"},
			"fatality_count":       map[string]any{"type": "integer"},
			"location":             map[string]any{"type": "geo_point"},
			"article_count":        map[string]any{"type": "integer"},
			"article_titles":       text("english"),
			"publishers":           text("place"),
			"article_descriptions": text("english"),
			"updated_at":           map[string]any{"type": "date"},
		}},
	}
	b, _ := json.Marshal(m)
	return b
}
