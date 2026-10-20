package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	releasereview "github.com/agent-primitives/canvas/examples/release-review"
	"github.com/agent-primitives/canvas/internal/canvas"
)

// Retained showcase outputs are fake-provider fixtures here, not a new live run
// or verified suite evaluation. The engine still owns schema/scope/acceptance.
func TestReleaseReviewHostedTransportQualification(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	example := filepath.Join(root, "examples", "release-review")
	load := func(member, document string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(example, "evals", "recorded", member, document))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	notes, announcement, review := load("writer", "notes.json"), load("writer", "announcement.md"), load("reviewer", "review.json")
	inputs, err := os.ReadFile(filepath.Join(example, "seeds", "inputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	for document, content := range map[string]string{"notes": notes, "announcement": announcement, "review": review} {
		if err = releasereview.CheckCitations(inputs, document, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	for _, outcome := range []string{"stop", "length", "refusal"} {
		t.Run(outcome, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var request struct {
					Model       string `json:"model"`
					Limit       int    `json:"max_completion_tokens"`
					LegacyLimit *int   `json:"max_tokens"`
					Format      struct {
						Type string `json:"type"`
					} `json:"response_format"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if request.Model != "gpt-5.6-terra" || request.Limit != 4096 || request.LegacyLimit != nil || request.Format.Type != "json_schema" {
					t.Error("showcase model/schema/completion cap changed")
				}
				if r.Header.Get("api-key") != "fixture-key" || len(request.Messages) == 0 {
					t.Error("provider credential/messages missing")
					w.WriteHeader(400)
					return
				}
				var input struct {
					Agent    string            `json:"agent"`
					Evidence map[string]string `json:"evidence"`
				}
				if err := json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &input); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				changes := []map[string]string{{"document": "notes", "operation": "replace", "content": notes}, {"document": "announcement", "operation": "replace", "content": announcement}}
				if input.Agent == "reviewer" {
					if input.Evidence["notes.json"] != notes || input.Evidence["announcement.md"] != announcement {
						t.Error("reviewer did not read exact accepted writer documents")
					}
					changes = []map[string]string{{"document": "review", "operation": "replace", "content": review}}
				} else if input.Agent != "writer" {
					t.Error("unexpected showcase agent")
				}
				text, err := json.Marshal(map[string]any{"changes": changes})
				if err != nil {
					t.Error(err)
					return
				}
				finish := outcome
				var refusal any
				if outcome == "refusal" {
					finish = "stop"
					refusal = "provider-private-refusal"
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": map[string]any{"role": "assistant", "content": string(text), "refusal": refusal}}}})
			}))
			defer srv.Close()
			pool := x509.NewCertPool()
			pool.AddCert(srv.Certificate())
			old := http.DefaultTransport
			transport := old.(*http.Transport).Clone()
			transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = old; transport.CloseIdleConnections() })
			configRoot := t.TempDir()
			key := filepath.Join(configRoot, "key")
			if err := os.WriteFile(key, []byte("fixture-key\n"), 0600); err != nil {
				t.Fatal(err)
			}
			config, err := json.Marshal(map[string]any{"profile": "hosted", "hosted": map[string]string{"endpoint": srv.URL + "/v1", "model": "gpt-5.6-terra", "keyFile": key}})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(configRoot, "model.config.local.json"), config, 0600); err != nil {
				t.Fatal(err)
			}
			d, err := startWithOptions(configRoot, filepath.Join(t.TempDir(), "state"), "127.0.0.1:0", filepath.Join(example, "canvas.yaml"), engineOptions{modelProfile: "hosted"})
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			browser := producerBrowser(t, d.URL)
			run := releaseReviewRun(t, browser, d.URL, "writer")
			if calls.Load() != 1 {
				t.Fatalf("writer dispatched %d times", calls.Load())
			}
			if outcome != "stop" {
				var state canvas.State
				producerRequest(t, browser, d.URL, "GET", "/api/state", nil, &state)
				if run.Status != "failed" || run.Proposal != "" || len(state.Proposals) != 0 {
					t.Fatalf("invalid metadata published valid document content: %+v", run)
				}
				return
			}
			if run.Status != "proposed" || run.Model != "gpt-5.6-terra" || run.Evaluation != "not verified" {
				t.Fatalf("showcase identity/evidence changed: %+v", run)
			}
			if status := producerRequest(t, browser, d.URL, "POST", "/api/review", map[string]string{"proposal": run.Proposal, "action": "accept", "role": "GM"}, nil); status != 200 {
				t.Fatalf("writer acceptance %d", status)
			}
			if calls.Load() != 1 {
				t.Fatal("acceptance automatically invoked reviewer")
			}
			critique := releaseReviewRun(t, browser, d.URL, "reviewer")
			if critique.Status != "proposed" || critique.Evaluation != "not verified" || calls.Load() != 2 {
				t.Fatalf("showcase critique/dispatch changed: %+v calls%d", critique, calls.Load())
			}
		})
	}
}

func releaseReviewRun(t *testing.T, browser *http.Client, base, agent string) canvas.Run {
	t.Helper()
	var created struct {
		Run string `json:"run"`
	}
	if code := producerRequest(t, browser, base, "POST", "/api/run", map[string]string{"agent": agent, "request": "Review the supplied fictional release sources and accepted documents.", "role": "GM"}, &created); code != 202 {
		t.Fatalf("run status %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var state canvas.State
		producerRequest(t, browser, base, "GET", "/api/state", nil, &state)
		for _, run := range state.Runs {
			if run.ID == created.Run {
				switch run.Status {
				case "proposed", "failed", "refused", "uncertain":
					return run
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("showcase run did not terminate")
	return canvas.Run{}
}
