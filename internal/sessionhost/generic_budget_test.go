package sessionhost

import (
	"bytes"
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
	recorded, err := json.Marshal(sink.request)
	if err != nil {
		t.Fatal(err)
	}
	for _, reasoning := range []bool{false, true} {
		t.Run(map[bool]string{false: "offline", true: "hosted"}[reasoning], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				field := "max_tokens"
				if reasoning {
					field = "max_completion_tokens"
				}
				if string(body[field]) != "4096" {
					t.Errorf("native wire budget must match host ceiling: %s", body[field])
				}
				if reasoning {
					if _, ok := body["max_tokens"]; ok {
						t.Error("reasoning request retained max_tokens")
					}
					if _, ok := body["temperature"]; ok {
						t.Error("reasoning request retained temperature")
					}
				} else if string(body["temperature"]) != "0.2" {
					t.Error("offline wire temperature changed")
				}
				if _, ok := body["tools"]; ok {
					t.Error("native request introduced tools")
				}
				if !bytes.Equal(body["response_format"], []byte(sink.request.Params["response_format"])) {
					t.Error("recorded native response schema changed on the wire")
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "{}"}}}})
			}))
			defer srv.Close()
			provider, err := compatible(endpoint{label: "fixture", base: srv.URL, model: sink.request.Model, reasoning: reasoning})
			if err != nil {
				t.Fatal(err)
			}
			_, callErr := provider(context.Background(), sink.request)
			after, err := json.Marshal(sink.request)
			if err != nil || !bytes.Equal(after, recorded) {
				t.Fatal("provider changed the recorded request or its shared parameters")
			}
			if callErr != nil {
				t.Fatal(callErr)
			}
		})
	}
}
