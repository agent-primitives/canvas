package canvas

import (
	"context"
	"testing"
)

func TestEditReconcileKeysAreDurablePerRequest(t *testing.T) {
	c, calls := workflowFixture(t, false, func(map[string]any) string { return changeEnvelope("prd", validPRD) })
	request := editRequest(t, c, "", "multi-reconcile-edit")
	r, e := c.RequestEdit(context.Background(), request)
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	original := c.space
	c.space = editUncommittedReviewSpace{original}
	if _, e = c.Review(r.Proposal, "accept", "GM"); e == nil {
		t.Fatal("expected failed intent")
	}
	c.space = original
	q1 := EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "first-reconcile-key", ExpectedHead: request.BaseRevision, Confirmed: true}
	if _, e = c.ReconcileEdit(context.Background(), q1); e != nil {
		t.Fatal(e)
	}
	c.space = editLostReviewSpace{original}
	if _, e = c.Review(r.Proposal, "accept", "GM"); e == nil {
		t.Fatal("expected lost committed reply")
	}
	c.space = original
	q2 := EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "second-reconcile-key", ExpectedHead: mustHead(t, c), Confirmed: true}
	out, e := c.ReconcileEdit(context.Background(), q2)
	if e != nil || out.State != "accepted" {
		t.Fatal("new reconciliation nonce could not observe new evidence", out, e)
	}
	out, e = c.ReconcileEdit(context.Background(), q1)
	if e != nil || out.State != "accepted" || calls.Load() != 1 {
		t.Fatal("settled old key lost readback idempotency", out, e)
	}
	changed := q1
	changed.ExpectedHead = q2.ExpectedHead
	if _, e = c.ReconcileEdit(context.Background(), changed); e == nil {
		t.Fatal("settled key accepted changed payload")
	}
}
