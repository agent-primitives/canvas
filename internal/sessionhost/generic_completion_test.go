package sessionhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/sessions/api"
)

// Valid document JSON does not establish a successful provider completion.
func TestGenericCompletionRefusalBeforeAdapter(t *testing.T) {
	h, err := NewBundleHarness(nativeBundle(t), 4)
	if err != nil {
		t.Fatal(err)
	}
	capture := &genericCaptureSink{}
	if err = h.Run(t.Context(), &api.Start{Inputs: []api.Message{*api.TextMessage("user", `{"mode":"documents"}`)}}, capture); err != nil {
		t.Fatal(err)
	}
	content := `{"changes":[{"document":"prd","operation":"replace","content":"whole draft"}]}`
	encoded, _ := json.Marshal(content)
	for _, tc := range []struct {
		name, finish, refusal, content string
		ok                             bool
	}{
		{"success", `"stop"`, "null", string(encoded), true},
		{"length", `"length"`, "null", string(encoded), false},
		{"content-filter", `"content_filter"`, "null", string(encoded), false},
		{"missing-finish", "null", "null", string(encoded), false},
		{"refusal-with-stop", `"stop"`, `"provider-private-refusal"`, string(encoded), false},
		{"null-content", `"stop"`, "null", "null", false},
		{"empty-content", `"stop"`, "null", `""`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				fmt.Fprintf(w, `{"choices":[{"index":0,"finish_reason":%s,"message":{"role":"assistant","content":%s,"refusal":%s}}]}`, tc.finish, tc.content, tc.refusal)
			}))
			defer srv.Close()
			provider, err := compatible(endpoint{label: "fixture", base: srv.URL, model: capture.request.Model, reasoning: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := provider(t.Context(), capture.request)
			if tc.ok {
				if err != nil || result.Message.Text() != content {
					t.Fatalf("valid completion changed: %v %q", err, result.Message.Text())
				}
			} else if err == nil || strings.Contains(err.Error(), "provider-private") {
				t.Fatalf("invalid completion accepted or refusal leaked: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("model attempts=%d", calls.Load())
			}
		})
	}
}

type completionBody struct {
	io.Reader
	closed bool
}

func (b *completionBody) Close() error { b.closed = true; return nil }

type completionReadFailure struct{}

func (completionReadFailure) Read([]byte) (int, error) {
	return 0, errors.New("provider-private-read-error")
}

func TestGenericCompletionBodyBoundary(t *testing.T) {
	format, err := profile4ResponseFormat([]api.Message{*api.TextMessage("user", `{"mode":"documents"}`)})
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{}"}}]}`
	for _, tc := range []struct {
		name   string
		status int
		reader io.Reader
		ok     bool
	}{
		{"success", 200, strings.NewReader(valid), true},
		{"non200", 202, strings.NewReader(valid), false},
		{"error", 400, strings.NewReader("provider-private-body"), false},
		{"oversized", 200, strings.NewReader(strings.Repeat("x", (4<<20)+1)), false},
		{"read-failure", 200, io.MultiReader(strings.NewReader("{"), completionReadFailure{}), false},
		{"duplicate-finish", 200, strings.NewReader(strings.Replace(valid, `"finish_reason":"stop"`, `"finish_reason":"length","finish_reason":"stop"`, 1)), false},
		{"case-folded-finish", 200, strings.NewReader(strings.Replace(valid, `"finish_reason":"stop"`, `"finish_reason":"length","FINISH_REASON":"stop"`, 1)), false},
		{"case-folded-refusal", 200, strings.NewReader(strings.Replace(valid, `"role":"assistant"`, `"role":"assistant","refusal":"provider-private-refusal","REFUSAL":null`, 1)), false},
		{"unicode-folded-finish", 200, strings.NewReader(strings.Replace(valid, `"finish_reason":"stop"`, `"finish_reason":"length","FINI\u017fH_REASON":"stop"`, 1)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &completionBody{Reader: tc.reader}
			calls := 0
			transport := boundedGenerationTransport{next: probeTransportFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Body: body}, nil
			}), reasoning: true}
			ctx := context.WithValue(context.WithValue(t.Context(), maxTokensKey{}, "16384"), responseFormatKey{}, format)
			req, _ := http.NewRequestWithContext(ctx, "POST", "http://fixture/v1/chat/completions", strings.NewReader(`{"model":"fixture","messages":[]}`))
			resp, err := transport.RoundTrip(req)
			if tc.ok != (err == nil) || !body.closed || calls != 1 {
				t.Fatalf("completion boundary: ok%t err%v closed%t calls%d", tc.ok, err, body.closed, calls)
			}
			if tc.ok {
				b, e := io.ReadAll(resp.Body)
				resp.Body.Close()
				if e != nil || string(b) != valid || resp.ContentLength != int64(len(valid)) {
					t.Fatal("validated response handoff changed")
				}
			} else if strings.Contains(err.Error(), "provider-private") {
				t.Fatal("provider error leaked")
			}
		})
	}
}

func TestGenericCompletionRejectsLossyUnicodeEscapes(t *testing.T) {
	h, err := NewBundleHarness(nativeBundle(t), 4)
	if err != nil {
		t.Fatal(err)
	}
	capture := &genericCaptureSink{}
	if err = h.Run(t.Context(), &api.Start{Inputs: []api.Message{*api.TextMessage("user", `{"mode":"documents"}`)}}, capture); err != nil {
		t.Fatal(err)
	}
	content := `{"changes":[{"document":"announcement","operation":"replace","content":"TEXT"}]}`
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, escape, want string
		ok                 bool
	}{
		{"lone-high", `\ud800`, "", false},
		{"lone-low", `\udfff`, "", false},
		{"high-then-text", `\ud800text`, "", false},
		{"reversed-pair", `\udc00\ud800`, "", false},
		{"valid-pair", `\uD83D\uDE80`, "🚀", true},
		{"literal-backslash", `\\\\ud800`, `\\ud800`, true},
		{"explicit-replacement-escape", `\ufffd`, "�", true},
		{"explicit-replacement-rune", "�", "�", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				fmt.Fprintf(w, `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":%s}}]}`, strings.Replace(string(encoded), "TEXT", tc.escape, 1))
			}))
			defer srv.Close()
			provider, err := compatible(endpoint{label: "fixture", base: srv.URL, model: capture.request.Model, reasoning: true})
			if err != nil {
				t.Fatal(err)
			}
			got, err := provider(t.Context(), capture.request)
			if tc.ok {
				want := strings.Replace(content, "TEXT", tc.want, 1)
				if err != nil || got.Message.Text() != want {
					t.Fatalf("valid text changed: %v %q", err, got.Message.Text())
				}
			} else if err == nil {
				t.Fatalf("unpaired surrogate reached adapter output: %q", got.Message.Text())
			}
			if calls.Load() != 1 {
				t.Fatalf("provider attempts=%d", calls.Load())
			}
		})
	}
}

func TestGenericCompletionFormatPreservesNumbers(t *testing.T) {
	for _, number := range []string{"9007199254740993", "1e400"} {
		t.Run(number, func(t *testing.T) {
			format := fmt.Sprintf(`{"type":"json_schema","json_schema":{"name":"numeric","strict":true,"schema":{"type":"object","additionalProperties":false,"required":["id","ID"],"properties":{"id":{"type":"number","enum":[%s]},"ID":{"type":"string"}}}}}`, number)
			transport := boundedGenerationTransport{next: probeTransportFunc(func(req *http.Request) (*http.Response, error) {
				raw, _ := io.ReadAll(req.Body)
				if !strings.Contains(string(raw), number) {
					t.Errorf("schema number changed: %s", raw)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{}"}}]}`))}, nil
			})}
			ctx := context.WithValue(context.WithValue(t.Context(), maxTokensKey{}, "16384"), responseFormatKey{}, format)
			req, _ := http.NewRequestWithContext(ctx, "POST", "http://fixture/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
		})
	}
}

func TestGenericCompletionRequiresNativeStrictFormat(t *testing.T) {
	for _, format := range []string{"", `{"type":"json_object"}`, `{"type":"json_schema","json_schema":{"name":"x","strict":false,"schema":{"type":"object"}}}`} {
		calls := 0
		transport := boundedGenerationTransport{next: probeTransportFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`))}, nil
		})}
		ctx := context.WithValue(context.WithValue(t.Context(), maxTokensKey{}, "16384"), responseFormatKey{}, format)
		req, _ := http.NewRequestWithContext(ctx, "POST", "http://fixture/v1/chat/completions", strings.NewReader(`{"model":"fixture","messages":[]}`))
		resp, err := transport.RoundTrip(req)
		if resp != nil {
			resp.Body.Close()
		}
		if err == nil || calls != 0 {
			t.Fatalf("native format fell back: %q %v calls%d", format, err, calls)
		}
	}
}

// The upstream adapter must not turn refusals or empty tool finishes into an
// empty legacy success, or expose provider-controlled text through the error.
func TestLegacyCompletionRefusalAndEmptyToolCallsAreSanitized(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"refusal", `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":null,"refusal":"provider-private-refusal"}}]}`},
		{"empty-tool-calls", `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[]}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			provider, err := compatible(endpoint{label: "fixture", base: srv.URL, model: "legacy"})
			if err != nil {
				t.Fatal(err)
			}
			got, err := provider(t.Context(), api.ModelRequest{Model: "legacy", Messages: []api.Message{*api.TextMessage("user", "legacy")}, Params: map[string]string{"max_tokens": "700", "temperature": "0.2"}})
			if err == nil || err.Error() != "model provider failed" || len(got.Message.Parts) != 0 || calls.Load() != 1 {
				t.Fatalf("legacy completion not sanitized: response=%+v err=%v attempts=%d", got, err, calls.Load())
			}
		})
	}
}

// Native parity must not turn legacy profiles into strict-schema-only clients.
func TestGenericCompletionLegacyProviderUnchanged(t *testing.T) {
	for _, reasoning := range []bool{false, true} {
		t.Run(fmt.Sprint(reasoning), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				field := "max_tokens"
				if reasoning {
					field = "max_completion_tokens"
				}
				format, ok := body["response_format"].(map[string]any)
				if body[field] != float64(700) || !ok || format["type"] != "json_object" {
					t.Errorf("legacy wire changed: %+v", body)
				}
				io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"legacy"}}]}`)
			}))
			defer srv.Close()
			provider, err := compatible(endpoint{label: "fixture", base: srv.URL, model: "legacy", reasoning: reasoning})
			if err != nil {
				t.Fatal(err)
			}
			got, err := provider(context.Background(), api.ModelRequest{Model: "legacy", Messages: []api.Message{*api.TextMessage("user", "legacy")}, Params: map[string]string{"max_tokens": "700", "temperature": "0.2"}})
			if err != nil || got.Message.Text() != "legacy" {
				t.Fatalf("legacy completion refused: %v %+v", err, got)
			}
		})
	}
}
