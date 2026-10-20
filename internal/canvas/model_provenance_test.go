package canvas

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/agent-primitives/canvas/internal/modelconfig"
	"github.com/agent-primitives/sessions/api"
)

func TestConfiguredExecutionRecordsBothAuthoredAndExecutionIdentities(t *testing.T) {
	d, err := LoadGenericDefinition(filepath.Join("..", "..", "examples", "northwind-prd", "canvas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	authored := d.Bundles
	profile := modelconfig.Profile{Name: "hosted", Model: "example-deployment"}
	selected, err := profile.Contracts(authored)
	if err != nil {
		t.Fatal(err)
	}
	d.Bundles = selected
	c, close := nativeProviderFixture(t, d, func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
		if r.Model != profile.Model {
			t.Error("actual request model differs from configured contract")
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", changeEnvelope("prd", validPRD))}, nil
	})
	defer close()
	id, err := c.Manual("writer", "", "Assess synthetic evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	run := completed(t, c, id)
	if run.Status != "proposed" {
		t.Fatal("configured assessment failed")
	}
	state, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if run.AuthoredBundleDigest != authored[1].Digest || state.Agents[0].AuthoredDigest != authored[1].Digest {
		t.Fatal("authored provenance missing or confused with derived execution")
	}
	session, err := c.client.GetSession(context.Background(), run.Session)
	if err != nil {
		t.Fatal(err)
	}
	if session.GetAnnotations()["authored-bundle-digest"] != authored[1].Digest || session.GetAnnotations()["bundle-digest"] != selected[1].Digest {
		t.Fatal("session annotations must distinguish both identities")
	}
	for _, p := range state.Proposals {
		if p.ID == run.Proposal && (p.Attribution.BundleDigest != selected[1].Digest || p.Attribution.AuthoredBundleDigest != authored[1].Digest) {
			t.Fatal("proposal provenance lost selected identity")
		}
	}
	proof, err := c.Verify(context.Background(), id)
	if err != nil || proof.ModelCalls != 0 || !proof.Reconstructed {
		t.Fatal("configured contract proof did not reconstruct without provider calls")
	}
}
