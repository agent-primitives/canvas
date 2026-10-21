package canvas

import (
	"context"
	"errors"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"testing"
)

type editUncommittedReviewSpace struct{ sharedspace.SharedSpace }

func (s editUncommittedReviewSpace) Accept(string, string, string) (sharedspace.Proposal, error) {
	return sharedspace.Proposal{}, errors.New("synthetic failure before Git decision")
}
func (s editUncommittedReviewSpace) Reject(string, string) (sharedspace.Proposal, error) {
	return sharedspace.Proposal{}, errors.New("synthetic failure before Git decision")
}
func TestEditUncommittedReviewRequiresFreshDecision(t *testing.T) {
	for _, action := range []string{"accept", "reject"} {
		t.Run(action, func(t *testing.T) {
			c, calls := workflowFixture(t, false, func(map[string]any) string { return changeEnvelope("prd", validPRD) })
			q := editRequest(t, c, "", "uncommitted-review-edit")
			r, e := c.RequestEdit(context.Background(), q)
			if e != nil {
				t.Fatal(e)
			}
			r = waitEdit(t, c, r.ID, "proposal")
			c.wg.Wait()
			original := c.space
			c.space = editUncommittedReviewSpace{original}
			if _, e = c.Review(r.Proposal, action, "GM"); e == nil {
				t.Fatal("expected uncommitted review failure")
			}
			c.space = original
			count := calls.Load()
			out, e := c.ReconcileEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "inspect-uncommitted-review", ExpectedHead: mustHead(t, c), Confirmed: true})
			if e != nil || out.State != "proposal" || c.data.EditSlot != r.ID || calls.Load() != count || mustHead(t, c) != q.BaseRevision {
				t.Fatal("uncommitted intent stranded slot or replayed a decision", out, e)
			}
			if _, e = c.Review(r.Proposal, "reject", "GM"); e != nil {
				t.Fatal("fresh whole-proposal decision refused", e)
			}
			if mustHead(t, c) != q.BaseRevision || c.data.EditSlot != "" {
				t.Fatal("fresh rejection changed bytes or retained slot")
			}
		})
	}
}
