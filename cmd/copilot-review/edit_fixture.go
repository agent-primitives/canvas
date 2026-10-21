package main

import (
	"encoding/json"
	"errors"
	"github.com/agent-primitives/canvas/internal/canvas"
)

// This deterministic fixture changes only the PRD title. It does not interpret
// instructions or qualify live inference; Canvas still validates every scope.
func fixtureEditChanges(d *canvas.CompiledDefinition, document string, evidence map[string]string) ([]map[string]string, error) {
	agent, ok := d.EditAgent(document)
	if !ok || agent != "writer" || document != "prd" || d.Documents[document].Path != "prd.json" {
		return nil, errors.New("unsupported deterministic edit fixture")
	}
	var value map[string]any
	if json.Unmarshal([]byte(evidence["prd.json"]), &value) != nil || value == nil {
		return nil, errors.New("accepted PRD unavailable")
	}
	title, ok := value["title"].(string)
	if !ok {
		return nil, errors.New("accepted PRD title unavailable")
	}
	value["title"] = title + " — scoped fixture edit"
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return []map[string]string{{"document": document, "operation": "replace", "content": string(raw)}}, nil
}
