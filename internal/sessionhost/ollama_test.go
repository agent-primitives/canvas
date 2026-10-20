package sessionhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/sessions/api"
)

func TestOllamaExplicitEndpointWithoutAuthAndGenerationBounds(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "environment-must-not-be-used")
	var auth, path string
	var body struct {
		Model       string                           `json:"model"`
		MaxTokens   int                              `json:"max_tokens"`
		Temperature float64                          `json:"temperature"`
		Messages    []struct{ Role, Content string } `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		path = r.URL.Path
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("request decode failed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"strategy\":\"fixture\"}"}}]}`))
	}))
	defer srv.Close()
	fn, err := Ollama(srv.URL, "fallback-model")
	if err != nil {
		t.Fatal("provider construction failed")
	}
	req := api.ModelRequest{Model: "bundle-selected-model", Messages: []api.Message{*api.TextMessage("system", "/no_think fixture")}, Params: map[string]string{"max_tokens": "700", "temperature": "0.2"}}
	resp, err := fn(context.Background(), req)
	if err != nil || resp.Message.Text() != `{"strategy":"fixture"}` {
		t.Fatal("compatible response failed")
	}
	if auth != "" || path != "/v1/chat/completions" || body.Model != req.Model || body.MaxTokens != 700 || body.Temperature != 0.2 || len(body.Messages) != 1 || body.Messages[0].Role != "system" {
		t.Fatal("HTTP routing, auth isolation or generation bounds missing")
	}
	fn, err = Ollama(srv.URL, "")
	if err != nil {
		t.Fatal("default model construction failed")
	}
	if _, err = fn(context.Background(), api.ModelRequest{}); err != nil {
		t.Fatal("default model request failed")
	}
	if body.Model != "qwen3:8b" {
		t.Fatal("default model lost")
	}
	// The upstream sample placeholder must not substitute a declared bundle model.
	if _, err = fn(context.Background(), api.ModelRequest{Model: "echo"}); err != nil {
		t.Fatal("explicit placeholder-named model request failed")
	}
	if body.Model != "echo" {
		t.Fatal("explicit request model silently substituted")
	}
}
func TestOllamaErrorsAreSanitizedBeforeJournal(t *testing.T) {
	const sensitive = "sensitive-response-marker"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":{"message":"` + sensitive + `"}}`))
	}))
	defer srv.Close()
	fn, err := Ollama(srv.URL, "fixture-model")
	if err != nil {
		t.Fatal("provider construction failed")
	}
	if _, err = fn(context.Background(), api.ModelRequest{Model: "fixture-model"}); err == nil || strings.Contains(err.Error(), sensitive) {
		t.Fatal("provider body leaked")
	}
	b := bundle(t, "fixture instructions", "fixture-model")
	h, err := Start(t.TempDir(), []contract.Bundle{b}, fn)
	if err != nil {
		t.Fatal("start failed")
	}
	t.Cleanup(func() { h.Close() })
	c := connect(t, h)
	uid := create(t, c, b).GetMetadata().GetUid()
	if _, err = run(context.Background(), c, uid, `{"mode":"injury"}`); err == nil {
		t.Fatal("failed completion accepted")
	}
	records, err := h.store.Session(uid).Read(1)
	if err != nil {
		t.Fatal("read failed")
	}
	found := false
	for _, r := range records {
		if r.Event.Err != nil {
			found = true
			if strings.Contains(r.Event.Err.Description, sensitive) {
				t.Fatal("body leaked into error journal")
			}
		}
	}
	if !found {
		t.Fatal("error evidence missing")
	}
}
func TestOllamaRejectsImplicitEndpoint(t *testing.T) {
	if _, err := Ollama("", ""); err == nil {
		t.Fatal("implicit provider endpoint accepted")
	}
	if _, err := Ollama("http://user:secret"+"@127.0.0.1:11434", ""); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("URL credential accepted or disclosed")
	}
}
