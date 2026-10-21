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
func TestEditCancelCannotAbandonUnknownReview(t *testing.T) {
	c, _ := workflowFixture(t, false, func(input map[string]any) string {
		if input["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	f := startWorkflow(t, c, "unknown-edit-review-flow", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "unknown-edit-review"))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	original := c.space
	c.space = editLostReviewSpace{original}
	if _, e = c.Review(r.Proposal, "accept", "GM"); e == nil {
		t.Fatal("expected lost committed review reply")
	}
	c.space = original
	if _, e = c.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "GM"}); e == nil {
		t.Fatal("cancel abandoned an unknown human decision")
	}
	edit, e := c.GetEdit(context.Background(), r.ID)
	if e != nil || edit.State != "uncertain" || c.data.EditSlot != r.ID {
		t.Fatal("cancel rewrote accepted bytes as abandonment", edit, e)
	}
	c.mu.Lock()
	record := *c.flowRecordLocked(f.ID)
	c.mu.Unlock()
	if record.CancelRequested || record.Revocation != 0 {
		t.Fatal("refused cancel mutated fences", record)
	}
	if _, e = c.ReconcileEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "recover-before-cancel", ExpectedHead: mustHead(t, c), Confirmed: true}); e != nil {
		t.Fatal(e)
	}
}
