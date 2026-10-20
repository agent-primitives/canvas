package sessionhost

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
)

func nativeBundle(t *testing.T) contract.Bundle {
	t.Helper()
	b, err := contract.LoadSuite("writer", contract.SuiteRef{Directory: "../../examples/northwind-prd/suites/writer", Artifact: "../../examples/northwind-prd/layouts/writer", Digest: "sha256:5c2230b6869821a57b2faea4db3f811fdf4be0623b71bbe676d75d7905005375", Member: "writer"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A missing profile4 branch or history-based mode selection changes these requests.
func TestGenericProfileLatestModeAndSingleModelOperation(t *testing.T) {
	b := nativeBundle(t)
	for _, tc := range []struct {
		input, mode string
		ok          bool
	}{
		{`{"mode":"documents"}`, "documents", true},
		{`{"mode":"question"}`, "question", true},
		{`{"mode":"scout"}`, "", false},
		{`{"mode":"documents"`, "", false},
		{`{"mode":null}`, "", false},
		{`{}`, "", false},
		{`null`, "", false},
		{``, "", false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			har, err := NewBundleHarness(b, contract.Profile(b))
			if err != nil {
				t.Fatal(err)
			}
			sink := &genericCaptureSink{}
			start := &api.Start{History: []api.Event{{Kind: api.EventInput, Message: api.TextMessage("user", `{"mode":"question"}`)}}, Inputs: []api.Message{*api.TextMessage("user", `{"mode":"question"}`), *api.TextMessage("user", tc.input)}}
			err = har.Run(context.Background(), start, sink)
			if !tc.ok {
				if err == nil || sink.calls != 0 {
					t.Fatalf("invalid mode reached model: %v, calls=%d", err, sink.calls)
				}
				return
			}
			if err != nil || sink.calls != 1 {
				t.Fatalf("single model operation: %v, calls=%d", err, sink.calls)
			}
			r := sink.request
			if len(r.Tools) != 0 || r.Model != b.Model || r.Params["max_tokens"] != "16384" || r.Params["temperature"] != "0.2" {
				t.Fatalf("unexpected model-only request: %+v", r)
			}
			if !strings.HasPrefix(r.Messages[0].Text(), b.Instructions) || strings.Contains(r.Messages[0].Text(), "dossier") {
				t.Fatal("native instructions not immutable/generic")
			}
			if tc.mode == "question" {
				if r.Params["response_format"] != questionSchemaFixture || !strings.Contains(r.Messages[0].Text(), "read-only") {
					t.Fatal("question not answer-only/read-only")
				}
				return
			}
			var format struct {
				JSONSchema struct {
					Name   string
					Strict bool
					Schema map[string]any
				} `json:"json_schema"`
			}
			if err := json.Unmarshal([]byte(r.Params["response_format"]), &format); err != nil {
				t.Fatal(err)
			}
			schema := format.JSONSchema.Schema
			if !format.JSONSchema.Strict || schema["additionalProperties"] != false || schema["type"] != "object" {
				t.Fatal("not a strict object")
			}
			props := schema["properties"].(map[string]any)
			if len(props) != 1 || strings.Join(asStrings(schema["required"]), ",") != "changes" {
				t.Fatal("envelope not changes-only")
			}
			changes := props["changes"].(map[string]any)
			if changes["type"] != "array" || changes["minItems"] != float64(1) || changes["maxItems"] != float64(32) {
				t.Fatal("unbounded changes")
			}
			item := changes["items"].(map[string]any)
			if item["additionalProperties"] != false || item["type"] != "object" || strings.Join(asStrings(item["required"]), ",") != "document,operation,content" {
				t.Fatal("item not strict")
			}
			fields := item["properties"].(map[string]any)
			if len(fields) != 3 || fields["document"].(map[string]any)["maxLength"] != float64(63) || fields["content"].(map[string]any)["maxLength"] != float64(65536) || strings.Join(asStrings(fields["operation"].(map[string]any)["enum"]), ",") != "replace" {
				t.Fatal("mutable or unbounded fields")
			}
		})
	}
	har, err := NewBundleHarness(b, 4)
	if err != nil {
		t.Fatal(err)
	}
	sink := &genericCaptureSink{}
	if err := har.Run(context.Background(), &api.Start{History: []api.Event{{Kind: api.EventInput, Message: api.TextMessage("user", `{"mode":"documents"}`)}}}, sink); err == nil || sink.calls != 0 {
		t.Fatal("history supplied missing current mode")
	}
}

func asStrings(value any) []string {
	var out []string
	for _, s := range value.([]any) {
		out = append(out, s.(string))
	}
	return out
}

type genericCaptureSink struct {
	api.EventSink
	calls   int
	request api.ModelRequest
}

func (s *genericCaptureSink) Model(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
	s.calls++
	s.request = req
	return api.ModelResponse{}, nil
}

func TestGenericProfilePairsRefused(t *testing.T) {
	b := nativeBundle(t)
	legacy := bundle(t, "Synthetic legacy", "synthetic-model")
	if _, err := NewBundleHarness(b, 0); err == nil {
		t.Fatal("native accepted zero profile")
	}
	for _, p := range []int{1, 2, 3} {
		if _, err := NewBundleHarness(b, p); err == nil {
			t.Fatal("native accepted legacy profile")
		}
		if _, err := NewBundleHarness(legacy, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewBundleHarness(legacy, 4); err == nil {
		t.Fatal("legacy accepted generic profile")
	}
	if _, err := BundleDescriptor(b, 4); err == nil || !strings.Contains(err.Error(), "local-only") {
		t.Fatalf("remote descriptor did not refuse native: %v", err)
	}
}

func TestGenericTamperedArchiveMetadataRefused(t *testing.T) {
	b := nativeBundle(t)
	for _, mutate := range []func(*contract.Bundle){func(b *contract.Bundle) { b.Suite.Member = "tampered" }, func(b *contract.Bundle) { b.AuthoredDigest = strings.Repeat("a", 64) }} {
		changed := nativeBundle(t)
		mutate(&changed)
		if sameBundle(b, changed) {
			t.Fatal("archive metadata omitted from equality")
		}
		if _, err := NewBundleHarness(changed, 4); err == nil {
			t.Fatal("tampered native identity accepted")
		}
		h, err := Start(t.TempDir(), []contract.Bundle{changed}, archiveProvider)
		if h != nil {
			h.Close()
		}
		if err == nil {
			t.Fatal("host accepted tampered metadata")
		}
	}
}

// The scripted provider replaces only external inference; journal/reconstruction stay real.
func TestGenericMixedArchiveRestartAndZeroCallReplay(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	authored := nativeBundle(t)
	native, err := contract.ForModel(authored, "synthetic-selected-model")
	if err != nil {
		t.Fatal(err)
	}
	if native.AuthoredDigest != authored.Digest || native.Suite.Digest != "sha256:5c2230b6869821a57b2faea4db3f811fdf4be0623b71bbe676d75d7905005375" {
		t.Fatal("execution derivation changed authored identity")
	}
	legacy := bundle(t, "Synthetic legacy replay", "synthetic-model")
	var calls atomic.Int64
	requests := make(chan api.ModelRequest, 8)
	provider := func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		requests <- r
		output := `{"changes":[{"document":"prd","operation":"replace","content":"synthetic"}]}`
		if strings.Contains(r.Messages[len(r.Messages)-1].Text(), `"question"`) {
			output = `{"answer":"Synthetic recorded explanation."}`
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", output)}, nil
	}
	h, err := Start(root, []contract.Bundle{native, legacy}, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { h.Close() }()
	c := connect(t, h)
	s, err := c.CreateSession(ctx, &v1.CreateSessionRequest{Session: &v1.Session{}})
	if err != nil || s.GetHarness() != "bundle-v4-"+native.Digest {
		t.Fatalf("native default missing: %v %v", s, err)
	}
	uid := s.GetMetadata().GetUid()
	for _, input := range []string{`{"mode":"documents"}`, `{"mode":"question"}`, `{"mode":"documents"}`} {
		exec(t, c, uid, input)
		r := <-requests
		if len(r.Tools) != 0 {
			t.Fatal("native tools introduced")
		}
		if strings.Contains(input, "documents") && strings.Contains(r.Params["response_format"], "answer") {
			t.Fatal("historical question tainted documents")
		}
	}
	ids := []string{uid}
	for _, p := range []int{1, 2, 3} {
		old, e := c.CreateSession(ctx, &v1.CreateSessionRequest{Session: &v1.Session{Harness: contract.HarnessName(legacy.Digest, p)}})
		if e != nil {
			t.Fatal(e)
		}
		id := old.GetMetadata().GetUid()
		exec(t, c, id, `{"mode":"scout"}`)
		<-requests
		ids = append(ids, id)
	}
	if len(h.harnesses) != 4 {
		t.Fatalf("unsupported profile pairs registered: %d", len(h.harnesses))
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h, err = Start(root, nil, provider)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		proof, e := sessionclient.Verify(ctx, h.Address(), id)
		if e != nil || !proof.ChainValid || !proof.Reconstructed || !proof.OutputsMatch || !proof.LiveUnchanged || proof.ModelCalls != 0 {
			t.Fatalf("replay failed: %+v %v", proof, e)
		}
	}
	if calls.Load() != 6 {
		t.Fatalf("replay called scripted provider: %d", calls.Load())
	}
	fresh, e := connect(t, h).CreateSession(ctx, &v1.CreateSessionRequest{Session: &v1.Session{}})
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := h.harnesses[fresh.GetHarness()]; !ok {
		t.Fatal("archive-only default is unregistered")
	}
}
