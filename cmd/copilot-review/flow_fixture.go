package main

import (
	"encoding/json"
	"errors"

	"github.com/agent-primitives/canvas/internal/canvas"
)

// Deterministic topic-aware output for the local Northwind example, not a model
// or evaluation. Read the actual native request and accepted evidence only.
func flowFixtureChanges(d *canvas.CompiledDefinition, agent, request string, evidence map[string]string) ([]map[string]string, error) {
	var workflow struct {
		Inputs struct {
			Topic string `json:"topic"`
		} `json:"inputs"`
	}
	if json.Unmarshal([]byte(request), &workflow) != nil || workflow.Inputs.Topic == "" {
		return nil, errors.New("Northwind flow fixture requires a topic")
	}
	topic := workflow.Inputs.Topic
	encode := func(document, content string) map[string]string {
		return map[string]string{"document": document, "operation": "replace", "content": content}
	}
	switch agent {
	case "writer":
		var prd, accepted map[string]any
		if json.Unmarshal([]byte(d.Seed["prd.json"]), &prd) != nil || prd == nil || json.Unmarshal([]byte(evidence["prd.json"]), &accepted) != nil || accepted == nil {
			return nil, errors.New("Northwind fixture requires the accepted PRD")
		}
		problem, ok := prd["problem"].(string)
		if !ok {
			return nil, errors.New("Northwind fixture requires a text problem")
		}
		prd["title"] = topic + " — draft"
		if accepted["title"] == topic+" — draft" {
			var review struct {
				Summary string `json:"summary"`
			}
			if json.Unmarshal([]byte(evidence["review.json"]), &review) != nil || review.Summary == "" {
				return nil, errors.New("Northwind revision requires accepted critique")
			}
			prd["title"] = topic + " — revised"
			prd["problem"] = problem + " Accepted critique: " + review.Summary
		}
		raw, err := json.Marshal(prd)
		if err != nil {
			return nil, err
		}
		return []map[string]string{encode("prd", string(raw)), encode("announcement", "# "+topic+"\n\nFictional local preview. Nothing is sent or published.\n")}, nil
	case "reviewer":
		var review map[string]any
		if json.Unmarshal([]byte(d.Seed["review.json"]), &review) != nil || review == nil {
			return nil, errors.New("Northwind fixture review unavailable")
		}
		review["summary"] = "Critique for " + topic + ": clarify the fictional pilot scope. Advice, not sign-off."
		raw, err := json.Marshal(review)
		if err != nil {
			return nil, err
		}
		return []map[string]string{encode("review", string(raw))}, nil
	}
	return nil, errors.New("unsupported Northwind flow fixture agent")
}
