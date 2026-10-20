package sessionhost

import (
	"context"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/wire"
)

func TestEachCurrentContractHasIndependentGRPCRouting(t *testing.T) {
	scout := bundle(t, "scouting fixture instructions", "scout-model")
	injury := bundle(t, "injury fixture instructions", "injury-model")
	const injuryResult = `{"rank":8,"tier":3,"rationale":"fixture"}`
	provider := func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{Message: *api.TextMessage("assistant", injuryResult)}, nil
	}
	h, err := Start(t.TempDir(), []contract.Bundle{scout, injury}, provider)
	if err != nil {
		t.Fatal("start failed")
	}
	t.Cleanup(func() { h.Close() })
	c := connect(t, h)
	for _, b := range []contract.Bundle{scout, injury} {
		uid := create(t, c, b).GetMetadata().GetUid()
		records := exec(t, c, uid, `{"mode":"injury","report":{"prospect":"fictional"}}`)
		outputs, models := 0, 0
		for _, r := range records {
			switch r.GetEvent().GetKind() {
			case v1.EventKind_EVENT_MODEL_CALL:
				models++
				if r.GetEvent().GetModel().GetModel() != b.Model {
					t.Fatal("bound model routing substituted default")
				}
			case v1.EventKind_EVENT_OUTPUT:
				outputs++
				if wire.EventFromProto(r.GetEvent()).Message.Text() != injuryResult {
					t.Fatal("typed injury output changed")
				}
			}
		}
		if outputs != 1 || models != 1 {
			t.Fatal("injury must have one mediated model and output")
		}
		p, err := sessionclient.Verify(context.Background(), h.Address(), uid)
		if err != nil || !p.Reconstructed || !p.OutputsMatch || len(p.RequestedModels) != 1 || p.RequestedModels[0] != b.Model {
			t.Fatal("injury reconstruction or independent binding failed")
		}
	}
}
