package canvas

import (
	"context"
	"testing"
)

func TestEditReconcileIsIdempotentAfterRerun(t *testing.T) {
	c, _ := workflowFixture(t, false, func(input map[string]any) string {
		if input["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	f := startWorkflow(t, c, "edit-idempotency-flow", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "edit-idempotency-request"))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	if _, e = c.Review(r.Proposal, "accept", "GM"); e != nil {
		t.Fatal(e)
	}
	if _, e = c.RerunAfterEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "edit-idempotency-rerun", ExpectedHead: mustHead(t, c), Confirmed: true}); e != nil {
		t.Fatal(e)
	}
	before := waitWorkflow(t, c, f.ID, "review")
	if _, e = c.ReconcileEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "edit-idempotency-reconcile", ExpectedHead: mustHead(t, c), Confirmed: true}); e != nil {
		t.Fatal(e)
	}
	after, e := c.GetFlow(context.Background(), f.ID)
	if e != nil || after.State != before.State || after.Attempt != before.Attempt || after.Attempts != before.Attempts {
		t.Fatal("reconciling an already applied edit rewound the rerun", before, after, e)
	}
}
