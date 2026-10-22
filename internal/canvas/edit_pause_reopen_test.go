package canvas

import (
	"context"
	"github.com/agent-primitives/canvas/internal/contract"
	"testing"
)

func TestEditReleasedPauseSurvivesReopen(t *testing.T) {
	for _, outcome := range []string{"accepted", "rejected", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			c, calls := workflowFixture(t, false, func(input map[string]any) string {
				if input["edit"] != nil && outcome != "failed" {
					return changeEnvelope("prd", validPRD)
				}
				return ""
			})
			f := startWorkflow(t, c, "edit-pause-reopen", 12)
			f = waitWorkflow(t, c, f.ID, "review")
			if _, e := c.Review(f.Proposal, "accept", "reviewer"); e != nil {
				t.Fatal(e)
			}
			f = waitWorkflow(t, c, f.ID, "review")
			r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "pause-reopen-edit"))
			if e != nil {
				t.Fatal(e)
			}
			if outcome == "failed" {
				r = waitEdit(t, c, r.ID, "failed")
			} else {
				r = waitEdit(t, c, r.ID, "proposal")
				action := "reject"
				if outcome == "accepted" {
					action = "accept"
				}
				if _, e = c.Review(r.Proposal, action, "GM"); e != nil {
					t.Fatal(e)
				}
			}
			c.wg.Wait()
			c.Close()
			count := calls.Load()
			next, e := OpenFlowRuntime(c.root, c.space, c.client, c.address, c.definition, contract.LocalBinder{}, FlowRuntimeOptions{MaxAgentRuns: 12})
			if e != nil {
				t.Fatal(e)
			}
			defer next.Close()
			want := "edit-paused"
			if outcome == "accepted" {
				want = "edited"
			}
			got, e := next.GetFlow(context.Background(), f.ID)
			if e != nil || got.State != want || calls.Load() != count {
				t.Fatal("restart lost explicit edit pause", got, e)
			}
			if _, e = next.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"}); e == nil {
				t.Fatal("generic reconcile bypassed released edit pause")
			}
			if _, e = next.RetryFlow(context.Background(), FlowRetry{Run: f.ID, Attempt: got.Attempt, RequestKey: "forbidden-generic-retry", Role: "GM"}); e == nil {
				t.Fatal("generic retry bypassed first-step rerun")
			}
			q := EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "explicit-post-reopen", ExpectedHead: mustHead(t, next), Confirmed: true}
			if outcome == "accepted" {
				if _, e = next.RerunAfterEdit(context.Background(), q); e != nil {
					t.Fatal(e)
				}
				got = waitWorkflow(t, next, f.ID, "review")
				if got.Position != 0 || got.Attempts != f.Attempts+2 || calls.Load() != count+1 {
					t.Fatal("rerun did not restart first step within spent budget", got)
				}
			} else {
				got, e = next.ResumeAfterEdit(context.Background(), q)
				if e != nil || got.State != "review" || got.Position != 1 || calls.Load() != count {
					t.Fatal("explicit resume lost old gate or dispatched", got, e)
				}
			}
		})
	}
}
