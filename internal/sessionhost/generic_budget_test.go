package sessionhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-primitives/sessions/api"
)

func TestGenericJournaledBudgetReachesCompatibleProvider(t *testing.T) {
	har, err := NewBundleHarness(nativeBundle(t), 4)
	if err != nil {
		t.Fatal(err)
	}
	sink := &genericCaptureSink{}
	if err := har.Run(context.Background(), &api.Start{Inputs: []api.Message{*api.TextMessage("user", `{"mode":"documents"}`)}}, sink); err != nil {
		t.Fatal(err)
	}
	if sink.request.Params["max_tokens"] != "16384" {
		t.Fatal("recorded native budget changed")
	}
	for _, reasoning := range []bool{false, true} {
		t.Run(map[bool]string{false: "offline", true: "hosted"}[reasoning], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				field := "max_tokens"
				if reasoning {
					field = "max_completion_tokens"
				}
				if body[field] != float64(4096) {
					t.Errorf("native wire budget must match host ceiling: %v", body[field])
				}
				if _, ok := body["tools"]; ok {
					t.Error("native request introduced tools")
				}
				if body["response_format"].(map[string]any)["type"] != "json_schema" {
					t.Error("native response schema dropped")
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "{}"}}}})
			}))
			defer srv.Close()
			provider, err := compatible(endpoint{label: "fixture", base: srv.URL, model: sink.request.Model, reasoning: reasoning})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider(context.Background(), sink.request); err != nil {
				t.Fatal(err)
			}
		})
	}
}
