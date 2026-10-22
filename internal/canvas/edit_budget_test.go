package canvas

import (
	"context"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
)

func TestEditFlowRecoveryAfterCountedEdit(t *testing.T) {
	c, calls := workflowFixture(t, false, func(input map[string]any) string {
		if input["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	f := startWorkflow(t, c, "counted-edit-recovery", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "counted-edit-request"))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	if _, e = c.Review(r.Proposal, "accept", "GM"); e != nil {
		t.Fatal(e)
	}
	if _, e = c.RerunAfterEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "counted-edit-rerun", ExpectedHead: mustHead(t, c), Confirmed: true}); e != nil {
		t.Fatal(e)
	}
	before := waitWorkflow(t, c, f.ID, "review")
	c.wg.Wait()
	c.Close()
	reopened, e := OpenFlowRuntime(c.root, c.space, c.client, c.address, c.definition, contract.LocalBinder{}, FlowRuntimeOptions{MaxAgentRuns: 12})
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	count := calls.Load()
	after, e := reopened.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"})
	if e != nil || after.State != "review" || after.Attempt != before.Attempt || after.Attempts != 3 || calls.Load() != count {
		t.Fatal("recovery after counted edit failed or dispatched", after, e)
	}
}
func TestEditReviewAndRestartBudget(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	f := startWorkflow(t, c, "failed-edit-budget", 2)
	f = waitWorkflow(t, c, f.ID, "review")
	// The ordinary fixture emits two documents; a scoped edit must reject it.
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "failed-budget-edit"))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "failed")
	if _, e = c.ResumeAfterEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "resume-failed-budget", ExpectedHead: mustHead(t, c), Confirmed: true}); e != nil {
		t.Fatal(e)
	}
	after, e := c.GetFlow(context.Background(), f.ID)
	if e != nil || after.Attempts != 2 || after.State != "review" {
		t.Fatal("edit attempt refunded or old gate lost", after, e)
	}
	if _, e = c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "exhausted-next-edit")); e == nil {
		t.Fatal("edit exceeded same-run budget")
	}
	if calls.Load() != 2 {
		t.Fatal("exhausted edit dispatched", calls.Load())
	}
}
func TestEditStrictStaleReview(t *testing.T) {
	c, _ := workflowFixture(t, false, func(map[string]any) string { return changeEnvelope("prd", validPRD) })
	q := editRequest(t, c, "", "stale-edit-review")
	r, e := c.RequestEdit(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	// A direct external checkpoint changes the full base, even though it does
	// not touch the proposed document. No hidden rebase is permitted for edits.
	if _, e = c.space.Ingest("feedback/new.json", `{"team":"Other","feedback":"new premise"}`, "GM"); e != nil {
		t.Fatal(e)
	}
	head := mustHead(t, c)
	if _, e = c.Review(r.Proposal, "accept", "GM"); e == nil {
		t.Fatal("stale edit accepted")
	}
	if mustHead(t, c) != head {
		t.Fatal("stale edit mutated accepted bytes")
	}
}
