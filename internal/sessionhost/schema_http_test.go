package sessionhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-primitives/sessions/api"
)

func TestProviderForwardsJournaledResponseSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ResponseFormat struct {
				Type string `json:"type"`
			} `json:"response_format"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("request decoding failed")
		}
		content := "{}"
		if body.ResponseFormat.Type == "json_schema" {
			content = `{"rank":22}`
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}}})
	}))
	defer srv.Close()
	provider, err := Ollama(srv.URL, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	r, err := provider(context.Background(), api.ModelRequest{Model: "test-model", Messages: []api.Message{*api.TextMessage("user", "Assess synthetic evidence.")}, Params: map[string]string{"response_format": `{"type":"json_schema","json_schema":{"name":"test","schema":{"type":"object","required":["rank"],"properties":{"rank":{"type":"integer"}}}}}`}})
	if err != nil || r.Message.Text() != `{"rank":22}` {
		t.Fatal("provider dropped the recorded response schema")
	}
}
