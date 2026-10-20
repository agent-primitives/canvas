package sessionhost

import (
	"context"
	"encoding/json"
	"github.com/agent-primitives/sessions/api"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQwenGenerationBoundsRemainUnchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		if body["max_tokens"] != float64(700) || body["temperature"] != 0.2 || body["max_completion_tokens"] != nil {
			t.Error("local generation settings changed")
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "{}"}}}})
	}))
	defer server.Close()
	provider, e := Ollama(server.URL, "qwen3:8b")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = provider(context.Background(), api.ModelRequest{Model: "qwen3:8b", Messages: []api.Message{*api.TextMessage("user", "Synthetic assessment")}}); e != nil {
		t.Fatal(e)
	}
}
