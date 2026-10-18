package api

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// The API's road-user filter must list exactly the categories the classifier can produce,
// or the UI would offer filters that match nothing (or hide categories that exist).
func TestVruFilterEnumMatchesClassifierSchema(t *testing.T) {
	raw, err := os.ReadFile("../classification/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Incidents struct {
				Items struct {
					Properties struct {
						VRU struct {
							Items struct {
								Enum []string `json:"enum"`
							} `json:"items"`
						} `json:"vru_categories"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"incidents"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	want := schema.Properties.Incidents.Items.Properties.VRU.Items.Enum
	if len(want) == 0 {
		t.Fatal("could not read the classifier's vru_categories enum")
	}

	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range spec.Paths.Find("/incidents").Get.Parameters {
		if p.Value.Name == "vru" {
			for _, v := range p.Value.Schema.Value.Items.Value.Enum {
				got = append(got, v.(string))
			}
		}
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Fatalf("API vru enum %v differs from classifier schema %v", got, want)
	}
}
