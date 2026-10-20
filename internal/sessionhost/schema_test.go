package sessionhost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
)

func TestCurrentBindingRequestsRequiredFieldsAndLegacyStillReplays(t *testing.T) {
	b, err := contract.Parse([]byte("apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata: {name: injury}\nspec:\n  instructions: Assess synthetic evidence.\n  model: {name: test-model}\n"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := Start(t.TempDir(), []contract.Bundle{b}, func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
		format := map[string]any{}
		_ = json.Unmarshal([]byte(r.Params["response_format"]), &format)
		if format["type"] == "json_schema" {
			schema := format["json_schema"].(map[string]any)["schema"].(map[string]any)
			if len(schema["required"].([]any)) == 3 {
				return api.ModelResponse{Message: *api.TextMessage("assistant", `{"rank":22,"tier":3,"rationale":"Synthetic recovery overlaps the rookie season."}`)}, nil
			}
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{}`)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	c, err := client.Dial(h.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	binding, _ := (contract.LocalBinder{}).Resolve(b)
	current, err := c.CreateSession(context.Background(), &v1.Session{Harness: binding.Harness})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := c.Exec(context.Background(), client.ExecOptions{Session: current.GetMetadata().GetUid(), Inputs: []string{`{"mode":"injury"}`}})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if json.Unmarshal([]byte(turn.Output), &result) != nil || result["rank"] != float64(22) {
		t.Fatal("current binding permits empty required-field result")
	}
	legacy, err := c.CreateSession(context.Background(), &v1.Session{Harness: "bundle-" + b.Digest})
	if err != nil {
		t.Fatal(err)
	}
	old, err := c.Exec(context.Background(), client.ExecOptions{Session: legacy.GetMetadata().GetUid(), Inputs: []string{`{"mode":"injury"}`}})
	if err != nil || old.Output != "{}" {
		t.Fatal("legacy request behavior changed")
	}
	for _, session := range []*v1.Session{current, legacy} {
		proof, err := sessionclient.Verify(context.Background(), h.Address(), session.GetMetadata().GetUid())
		if err != nil || !proof.Reconstructed || proof.ModelCalls != 0 {
			t.Fatal("binding version lost zero-call reconstruction")
		}
	}
}
