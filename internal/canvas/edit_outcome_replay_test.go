package canvas

import (
	"context"
	"testing"
)

func TestEditFlowCompleteProofRetainsRejectedAndFailedCalls(t *testing.T) {
	for _, outcome := range []string{"failed", "rejected"} {
		t.Run(outcome, func(t *testing.T) {
			c, calls := workflowFixture(t, false, func(input map[string]any) string {
				if input["edit"] != nil && outcome == "rejected" {
					return changeEnvelope("prd", validPRD)
				}
				return ""
			})
			f := startWorkflow(t, c, "edit-outcome-"+outcome, 12)
			f = waitWorkflow(t, c, f.ID, "review")
			r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "edit-outcome-request"))
			if e != nil {
				t.Fatal(e)
			}
			if outcome == "rejected" {
				r = waitEdit(t, c, r.ID, "proposal")
				if _, e = c.Review(r.Proposal, "reject", "GM"); e != nil {
					t.Fatal(e)
				}
			} else {
				r = waitEdit(t, c, r.ID, "failed")
			}
			if _, e = c.ResumeAfterEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "edit-outcome-resume", ExpectedHead: mustHead(t, c), Confirmed: true}); e != nil {
				t.Fatal(e)
			}
			for range 3 {
				f = waitWorkflow(t, c, f.ID, "review")
				if _, e = c.Review(f.Proposal, "accept", "reviewer"); e != nil {
					t.Fatal(e)
				}
			}
			f = waitWorkflow(t, c, f.ID, "complete")
			before := calls.Load()
			proof, e := c.VerifyFlow(context.Background(), f.ID)
			if e != nil || !proof.Complete || f.Attempts != 4 || len(proof.Sessions) != 4 || calls.Load() != before {
				t.Fatal("settled discarded edit call omitted or rerun", f, proof, e)
			}
		})
	}
}
