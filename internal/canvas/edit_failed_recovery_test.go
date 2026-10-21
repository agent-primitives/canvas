package canvas

import (
	"context"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
)

func TestEditReconcileSettlesInvalidNativeOutput(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	q := editRequest(t, c, "", "invalid-output-recovery")
	r, e := c.RequestEdit(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "failed")
	c.wg.Wait()
	c.Close()
	c.data.EditSlot = r.ID
	c.editLocked(r.ID).State = "uncertain"
	a := c.findLocked(r.Attempt)
	a.Status = "running"
	a.FlowStage = "exec-intent"
	a.Completed = time.Time{}
	if e = c.saveLocked(); e != nil {
		t.Fatal(e)
	}
	reopened, e := OpenFlowRuntime(c.root, c.space, c.client, c.address, c.definition, contract.LocalBinder{}, FlowRuntimeOptions{MaxAgentRuns: 12})
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	count := calls.Load()
	out, e := reopened.ReconcileEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "reconcile-invalid-output", ExpectedHead: mustHead(t, reopened), Confirmed: true})
	if e != nil || out.State != "failed" || reopened.data.EditSlot != "" || reopened.findLocked(r.Attempt).Status != "failed" || reopened.findLocked(r.Attempt).FlowStage != "completed" || calls.Load() != count {
		t.Fatal("native validation failure did not settle all admission journals", out, e)
	}
}
