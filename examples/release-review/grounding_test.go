package releasereview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCitationChecksDoNotConfuseExistenceWithTruth(t *testing.T) {
	inputs := []byte(`{"sources":[{"id":"pr-lifetime"}]}`)
	for _, tc := range []struct {
		name, doc, content string
		valid              bool
	}{
		{"supported ID", "notes", `{"changes":[{"text":"New default is 8h.","sourceIds":["pr-lifetime"]}]}`, true},
		{"unknown ID", "notes", `{"changes":[{"text":"New default is 8h.","sourceIds":["made-up"]}]}`, false},
		{"empty citations", "notes", `{"changes":[{"text":"New default is 8h.","sourceIds":[]}]}`, false},
		{"missing citations", "notes", `{"changes":[{"text":"New default is 8h."}]}`, false},
		{"prose needs semantic review", "notes", `{"changes":[{"text":"All existing environments now expire in 8h.","sourceIds":["pr-lifetime"]}]}`, true},
		{"honest clean critique", "review", `{"findings":[]}`, true},
		{"fabricated review ID", "review", `{"findings":[{"claim":"An outage occurred.","sourceIds":["fake-incident"]}]}`, false},
		{"Markdown citations", "announcement", "New default is 8h. [pr-lifetime]", true},
		{"unknown Markdown citation", "announcement", "New default is 8h. [fake]", false},
		{"uncited Markdown", "announcement", "New default is 8h.", false},
		{"unknown document", "other", `{}`, false},
		{"malformed output", "notes", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckCitations(inputs, tc.doc, []byte(tc.content)); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestNegativeEvaluationCases(t *testing.T) {
	inputs, err := os.ReadFile("seeds/inputs.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("evals/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID       string          `json:"id"`
		Agent    string          `json:"agent"`
		Claim    json.RawMessage `json:"candidateClaim"`
		Finding  json.RawMessage `json:"candidateFinding"`
		Expected string          `json:"citationCheck"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		if tc.Expected == "" {
			continue
		}
		doc, candidate := "notes", map[string]any{"changes": []json.RawMessage{tc.Claim}}
		if tc.Agent == "reviewer" {
			doc, candidate = "review", map[string]any{"findings": []json.RawMessage{tc.Finding}}
		}
		raw, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckCitations(inputs, doc, raw); (err == nil) != (tc.Expected == "pass") {
			t.Errorf("%s: %v", tc.ID, err)
		}
	}
}

func TestRecordedLocalOutputsHaveKnownCitations(t *testing.T) {
	inputs, err := os.ReadFile("seeds/inputs.json")
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("evals/recorded/*/*")
	if err != nil || len(files) == 0 {
		t.Fatal("no recorded real-model outputs", err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		doc := filepath.Base(file)
		doc = doc[:len(doc)-len(filepath.Ext(doc))]
		if err := CheckCitations(inputs, doc, raw); err != nil {
			t.Errorf("%s: %v", file, err)
		}
	}
}
