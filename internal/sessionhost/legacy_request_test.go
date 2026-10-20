package sessionhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
)

// These snapshots deliberately do not use the current request-building helpers:
// profiles 1/2 are immutable, including their unsupported question behavior.
const frozenOutputInstructions = "\n\n/no_think\nReturn only one compact JSON object, without markdown or thinking text. The current journaled input specifies the mode. For scout or scouting use {\"dossier\":string,\"rank\":int,\"tier\":int,\"rationale\":string}; for injury use {\"rank\":int,\"tier\":int,\"rationale\":string}; for scenario use {\"strategy\":string}. Keep the response short."
const frozenScoutSchema = `{"json_schema":{"name":"draft_room_scout","schema":{"additionalProperties":false,"properties":{"dossier":{"maxLength":6000,"minLength":40,"type":"string"},"rank":{"maximum":24,"minimum":1,"type":"integer"},"rationale":{"maxLength":1000,"minLength":8,"type":"string"},"tier":{"maximum":3,"minimum":1,"type":"integer"}},"required":["dossier","rank","tier","rationale"],"type":"object"},"strict":true},"type":"json_schema"}`
const frozenInjurySchema = `{"json_schema":{"name":"draft_room_injury","schema":{"additionalProperties":false,"properties":{"rank":{"maximum":24,"minimum":1,"type":"integer"},"rationale":{"maxLength":1000,"minLength":8,"type":"string"},"tier":{"maximum":3,"minimum":1,"type":"integer"}},"required":["rank","tier","rationale"],"type":"object"},"strict":true},"type":"json_schema"}`
const frozenScenarioSchema = `{"json_schema":{"name":"draft_room_scenario","schema":{"additionalProperties":false,"properties":{"strategy":{"maxLength":6000,"minLength":40,"type":"string"}},"required":["strategy"],"type":"object"},"strict":true},"type":"json_schema"}`

func TestHistoricalProfilesKeepCompleteModelRequestsAndReconstructWithoutCalls(t *testing.T) {
	for _, agent := range []string{"scouting", "injury"} {
		for _, profile := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/profile%d", agent, profile), func(t *testing.T) {
				b := bundle(t, "Synthetic frozen instructions.", "synthetic-frozen-model")
				b, err := contract.Parse([]byte(strings.Replace(string(b.Source), "name: scouting", "name: "+agent, 1)))
				if err != nil {
					t.Fatal(err)
				}
				root := t.TempDir()
				var calls atomic.Int64
				requests := make(chan api.ModelRequest, 1)
				output := `{"strategy":"Synthetic recorded completion."}`
				provider := func(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
					calls.Add(1)
					requests <- req
					return api.ModelResponse{Message: *api.TextMessage("assistant", output)}, nil
				}
				h, err := Start(root, []contract.Bundle{b}, provider)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { h.Close() }()
				c := connect(t, h)
				s, err := c.CreateSession(context.Background(), &v1.CreateSessionRequest{Session: &v1.Session{Harness: contract.HarnessName(b.Digest, profile)}})
				if err != nil {
					t.Fatal(err)
				}
				uid := s.GetMetadata().GetUid()
				want := api.ModelRequest{Model: "synthetic-frozen-model", Messages: []api.Message{*api.TextMessage("system", "Synthetic frozen instructions."+frozenOutputInstructions)}}
				for _, mode := range []string{"scout", "injury", "scenario", "question"} {
					input := `{"mode":"` + mode + `","request":"Synthetic fixture only."}`
					want.Messages = append(want.Messages, *api.TextMessage("user", input))
					want.Params = map[string]string{"max_tokens": "700", "temperature": "0.2"}
					if profile == 2 {
						format := frozenScoutSchema
						if agent == "injury" {
							format = frozenInjurySchema
						}
						if mode == "scenario" {
							format = frozenScenarioSchema
						}
						want.Params["response_format"] = format
					}
					exec(t, c, uid, input)
					got := <-requests
					gotBytes, err := json.Marshal(got)
					if err != nil {
						t.Fatal(err)
					}
					wantBytes, err := json.Marshal(want)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(gotBytes, wantBytes) {
						t.Fatalf("complete historical request changed for %s\ngot %s\nwant %s", mode, gotBytes, wantBytes)
					}
					want.Messages = append(want.Messages, *api.TextMessage("assistant", output))
				}
				if calls.Load() != 4 {
					t.Fatal("historical turn count changed")
				}
				if err := h.Close(); err != nil {
					t.Fatal(err)
				}
				current := bundle(t, "New synthetic instructions.", "new-synthetic-model")
				h, err = Start(root, []contract.Bundle{current}, provider)
				if err != nil {
					t.Fatal(err)
				}
				proof, err := sessionclient.Verify(context.Background(), h.Address(), uid)
				if err != nil || !proof.ChainValid || !proof.Reconstructed || !proof.OutputsMatch || !proof.LiveUnchanged || proof.ModelCalls != 0 || calls.Load() != 4 {
					t.Fatal("historical request hashes no longer reconstruct without model calls")
				}
			})
		}
	}
}
