package sessionhost

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/modelconfig"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
)

func TestModelProfileSwitchPreservesBothArchivedReplayProfilesAndForkModels(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	old := bundle(t, "Synthetic replay instructions", modelconfig.OfflineModel)
	selected, err := contract.ForModel(old, testHostedModel)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Start(root, []contract.Bundle{old}, func(_ context.Context, r api.ModelRequest) (api.ModelResponse, error) {
		if r.Model != modelconfig.OfflineModel {
			t.Error("old execution model changed")
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{"dossier":"Synthetic replay fixture with missing evidence.","rank":4,"tier":1,"rationale":"Synthetic evidence."}`)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c := connect(t, h)
	var sessions []string
	for _, profile := range []int{1, 2} {
		s, err := c.CreateSession(ctx, &v1.CreateSessionRequest{Session: &v1.Session{Harness: contract.HarnessName(old.Digest, profile)}})
		if err != nil {
			t.Fatal(err)
		}
		uid := s.GetMetadata().GetUid()
		exec(t, c, uid, `{"mode":"scout"}`)
		sessions = append(sessions, uid)
	}
	oldPath := filepath.Join(root, "contracts", old.Digest, "agent.yaml")
	before, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	provider, err := ConfiguredProvider(hostedProfile("https://example.invalid/openai/v1", writeKey(t, 0600), "api-key"), "missing-lock", "http://127.0.0.1:1", []contract.Bundle{selected})
	if err != nil {
		t.Fatal(err)
	}
	h, err = Start(root, []contract.Bundle{selected}, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	c = connect(t, h)
	for i, uid := range sessions {
		proof, err := sessionclient.Verify(ctx, h.Address(), uid)
		if err != nil || !proof.ChainValid || !proof.Reconstructed || !proof.OutputsMatch || !proof.LiveUnchanged || proof.ModelCalls != 0 {
			t.Fatal("historical model/profile replay changed or contacted a provider")
		}
		parent, err := c.GetSession(ctx, &v1.GetSessionRequest{Uid: uid})
		if err != nil {
			t.Fatal(err)
		}
		fork, err := c.Fork(ctx, &v1.ForkRequest{Session: uid, AtSeq: parent.GetLastSeq(), Count: 1})
		if err != nil {
			t.Fatal(err)
		}
		child := fork.GetChildren()[0]
		if child.GetHarness() != contract.HarnessName(old.Digest, i+1) {
			t.Fatal("historical fork was rebound to today's model")
		}
		records, runErr := run(ctx, c, child.GetMetadata().GetUid(), `{"mode":"scenario"}`)
		visibleMismatch := false
		for _, record := range records {
			if record.GetEvent().GetKind() == v1.EventKind_EVENT_ERROR && record.GetEvent().GetError().GetDescription() == ErrModelProfileMismatch.Error() {
				visibleMismatch = true
			}
		}
		if runErr == nil || !visibleMismatch {
			t.Fatal("inherited offline fork must explicitly fail, not call the hosted provider with a different model")
		}
	}
	after, err := os.ReadFile(oldPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("profile switch edited an archived contract")
	}
}
