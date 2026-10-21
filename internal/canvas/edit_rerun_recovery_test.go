package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
)

func TestEditExactRerunContinuesPublishedPredispatchIntent(t *testing.T) {
	c, calls := workflowFixture(t, false, func(input map[string]any) string {
		if input["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	f := startWorkflow(t, c, "rerun-publication-cut", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "rerun-cut-edit"))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	if _, e = c.Review(r.Proposal, "accept", "GM"); e != nil {
		t.Fatal(e)
	}
	q := EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "rerun-published-key", ExpectedHead: mustHead(t, c), Confirmed: true}
	count := calls.Load()
	c.syncDirectory = func(root string) error {
		raw, e := os.ReadFile(filepath.Join(root, "attempts.json"))
		if e != nil {
			return e
		}
		var saved ledger
		if e = json.Unmarshal(raw, &saved); e != nil {
			return e
		}
		if len(saved.Flows[0].EditCheckpoints) > 0 && saved.Flows[0].Generation == 1 {
			return errors.New("synthetic sync uncertainty after rerun-intent publication")
		}
		return syncCanvasDirectory(root)
	}
	if _, e = c.RerunAfterEdit(context.Background(), q); e == nil {
		t.Fatal("expected visible predispatch publication uncertainty")
	}
	c.Close()
	reopened, e := OpenFlowRuntime(c.root, c.space, c.client, c.address, c.definition, contract.LocalBinder{}, FlowRuntimeOptions{MaxAgentRuns: 12})
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if calls.Load() != count {
		t.Fatal("reopen dispatched")
	}
	if _, e = reopened.RerunAfterEdit(context.Background(), q); e != nil {
		t.Fatal("exact pending rerun refused", e)
	}
	after := waitWorkflow(t, reopened, f.ID, "review")
	if after.Attempts != 3 || calls.Load() != count+1 {
		t.Fatal("exact pending rerun did not reserve exactly one first-step attempt", after, calls.Load(), count)
	}
	if _, e = reopened.RerunAfterEdit(context.Background(), q); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != count+1 {
		t.Fatal("completed exact rerun dispatched again")
	}
}
