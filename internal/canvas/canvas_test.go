package canvas

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
)

func setup(t *testing.T, fail bool) (*Canvas, func()) {
	t.Helper()
	d := nativeData(t)
	return nativeProviderFixture(t, d, func(_ context.Context, request api.ModelRequest) (api.ModelResponse, error) {
		if fail {
			return api.ModelResponse{}, errors.New("provider unavailable")
		}
		input, err := nativeRequestInput(request)
		if err != nil {
			return api.ModelResponse{}, err
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", nativeOutput(d, input))}, nil
	})
}
func completed(t *testing.T, c *Canvas, id string) Run {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		r, ok := c.GetRun(id)
		if ok && !r.Completed.IsZero() && (r.Status == "proposed" || r.Status == "failed" || r.Status == "uncertain") {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not finish")
	return Run{}
}
func TestManualReviewScopeAndRegenerate(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	id, err := c.Manual("writer", "", "Draft fictional requirements.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	run := completed(t, c, id)
	if run.Status != "proposed" || run.Session == "" || run.Seconds <= 0 {
		t.Fatal("manual run lacks proposal/session/timing")
	}
	proof, err := c.Verify(context.Background(), id)
	if err != nil || !proof.Reconstructed || proof.ModelCalls != 0 || !proof.ChainValid {
		t.Fatal("reconstruction proof missing")
	}
	secondID, err := c.Manual("writer", "", "Draft another fictional proposal.", "coach")
	if err != nil {
		t.Fatal(err)
	}
	second := completed(t, c, secondID)
	if _, err := c.Review(run.Proposal, "accept", "GM"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Review(second.Proposal, "accept", "owner"); !errors.Is(err, sharedspace.ErrStale) {
		t.Fatal("stale proposal accepted")
	}
	fresh, err := c.Regenerate(second.Proposal, "owner")
	if err != nil {
		t.Fatal(err)
	}
	r := completed(t, c, fresh)
	if r.Base == second.Base || r.Session == second.Session || r.Mode != "documents" || r.Status != "proposed" || r.PreviousAttempt != second.ID || r.ParentSession != "" {
		t.Fatal("regeneration did not retain lineage on latest base/fresh session")
	}
	state, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range state.Proposals {
		if len(p.Changes) != 1 || p.Changes["prd.json"] == "" {
			t.Fatal("native writer changed undeclared document")
		}
	}
}
func TestProviderFailureIsVisibleAndNeverAutoRetried(t *testing.T) {
	c, close := setup(t, true)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	r := completed(t, c, id)
	if r.Status != "failed" || r.Proposal != "" || r.Session == "" || r.Seconds <= 0 {
		t.Fatal("failed attempt hidden")
	}
	state, _ := c.State()
	if len(state.Proposals) != 0 {
		t.Fatal("failed run created proposal")
	}
}
