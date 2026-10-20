package sessionhost

import (
	"context"
	"encoding/json"
	"github.com/agent-primitives/sessions/api"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostedUsesCompletionTokenLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		if body["temperature"] != nil {
			t.Error("hosted model received unsupported sampling value")
		}
		if body["max_tokens"] != nil || body["max_completion_tokens"] != float64(700) {
			t.Error("hosted reasoning model received unsupported token limit")
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "{}"}}}})
	}))
	defer srv.Close()
	provider, e := compatible(endpoint{label: "hosted", base: srv.URL, model: "example-deployment", reasoning: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = provider(context.Background(), api.ModelRequest{Model: "example-deployment", Messages: []api.Message{*api.TextMessage("user", "Synthetic assessment")}}); e != nil {
		t.Fatal(e)
	}
}
