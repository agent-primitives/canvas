package sessionhost

import (
	"encoding/json"

	"github.com/agent-primitives/sessions/api"
)

// Profile 2 is immutable once recorded. Future constraint changes need a new
// binding version, not a reinterpretation of old sessions' model request hashes.
func responseFormat(agent string, inputs []api.Message) string {
	mode := "scout"
	if agent == "injury" {
		mode = "injury"
	}
	if len(inputs) > 0 {
		var envelope struct {
			Mode string `json:"mode"`
		}
		if json.Unmarshal([]byte(inputs[len(inputs)-1].Text()), &envelope) == nil && envelope.Mode == "scenario" {
			mode = "scenario"
		}
	}
	text := func(min, max int) map[string]any {
		return map[string]any{"type": "string", "minLength": min, "maxLength": max}
	}
	properties := map[string]any{"rank": map[string]any{"type": "integer", "minimum": 1, "maximum": 24}, "tier": map[string]any{"type": "integer", "minimum": 1, "maximum": 3}, "rationale": text(8, 1000)}
	required := []string{"rank", "tier", "rationale"}
	if mode == "scout" {
		properties["dossier"] = text(40, 6000)
		required = append([]string{"dossier"}, required...)
	}
	if mode == "scenario" {
		properties = map[string]any{"strategy": text(40, 6000)}
		required = []string{"strategy"}
	}
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	encoded, _ := json.Marshal(map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "draft_room_" + mode, "strict": true, "schema": schema}})
	return string(encoded)
}

// Profile 3 selects the answer schema only from the current journaled input.
// Historical question inputs must not change a later assessment's format.
func profile3ResponseFormat(agent string, inputs []api.Message) string {
	if len(inputs) > 0 {
		var envelope struct {
			Mode string `json:"mode"`
		}
		if json.Unmarshal([]byte(inputs[len(inputs)-1].Text()), &envelope) == nil && envelope.Mode == "question" {
			return answerResponseFormat()
		}
	}
	return responseFormat(agent, inputs)
}

func answerResponseFormat() string {
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"answer": map[string]any{"type": "string", "minLength": 1, "maxLength": 3000}},
		"required":   []string{"answer"},
	}
	encoded, _ := json.Marshal(map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "draft_room_question", "strict": true, "schema": schema}})
	return string(encoded)
}
