package sessionhost

import (
	"encoding/json"
	"errors"

	"github.com/agent-primitives/sessions/api"
)

// Profile 4 is immutable and model-only. Document IDs, schemas and scopes are
// checked by Canvas, never read from mutable definition files by this harness.
const genericInstructions = "\n\n/no_think\nReturn only one compact JSON object without markdown or thinking text. The latest current journaled input selects the mode. For documents, return {\"changes\":[{\"document\":\"declared-id\",\"operation\":\"replace\",\"content\":\"whole bytes\"}]}, with 1 to 32 whole-document replacements, document IDs at most 63 characters and content at most 65536 characters. Do not add fields. For question, return only {\"answer\":string}, nonempty and at most 3000 characters: a read-only explanation of recorded evidence, not new document changes. Distinguish accepted content from unaccepted proposals and historical context from current state. Do not invent facts; state uncertainty. Do not call tools or claim tool use."

func profile4ResponseFormat(inputs []api.Message) (string, error) {
	var envelope struct {
		Mode string `json:"mode"`
	}
	if len(inputs) == 0 || json.Unmarshal([]byte(inputs[len(inputs)-1].Text()), &envelope) != nil {
		return "", errors.New("sessionhost: profile 4 requires current documents or question mode")
	}
	switch envelope.Mode {
	case "question":
		return answerResponseFormat(), nil
	case "documents":
		schema := map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"changes"},
			"properties": map[string]any{"changes": map[string]any{
				"type": "array", "minItems": 1, "maxItems": 32,
				"items": map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"document", "operation", "content"},
					"properties": map[string]any{
						"document":  map[string]any{"type": "string", "minLength": 1, "maxLength": 63},
						"operation": map[string]any{"type": "string", "enum": []string{"replace"}},
						"content":   map[string]any{"type": "string", "maxLength": 65536},
					},
				},
			}},
		}
		encoded, _ := json.Marshal(map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "canvas_documents", "strict": true, "schema": schema}})
		return string(encoded), nil
	default:
		return "", errors.New("sessionhost: profile 4 requires current documents or question mode")
	}
}
