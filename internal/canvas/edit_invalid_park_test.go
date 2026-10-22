package canvas

import (
	"context"
	"sync"
	"testing"
)

func TestEditResumeSettlesInvalidParkedOutput(t *testing.T) {
	for _, kind := range []string{"invalid-step", "native-failed"} {
		t.Run(kind, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			c, calls := workflowFixture(t, false, func(input map[string]any) string {
				if input["edit"] != nil {
					return changeEnvelope("prd", validPRD)
				}
				once.Do(func() { close(entered); <-release })
				if kind == "native-failed" {
					return "invalid native JSON"
				}
				return changeEnvelope("prd", validPRD)
			})
			f := startWorkflow(t, c, "invalid-parked-output", 12)
			<-entered
			r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "invalid-parked-edit"))
			if e != nil {
				close(release)
				t.Fatal(e)
			}
			close(release)
			r = waitEdit(t, c, r.ID, "proposal")
			c.wg.Wait()
			if _, e = c.Review(r.Proposal, "reject", "GM"); e != nil {
				t.Fatal(e)
			}
			q := EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "resume-invalid-parked", ExpectedHead: mustHead(t, c), Confirmed: true}
			out, e := c.ResumeAfterEdit(context.Background(), q)
			if e != nil || out.State != "paused" {
				t.Fatal("invalid parked output did not settle as retryable failure", out, e)
			}
			c.mu.Lock()
			pending := c.editLocked(r.ID).ResumePending
			a := *c.findLocked(out.Attempt)
			c.mu.Unlock()
			if pending || a.Status != "failed" || a.FlowStage != "completed" || out.Attempts != 2 || calls.Load() != 2 {
				t.Fatal("admission journals remain pending or budget changed", pending, a, out)
			}
			if _, e = c.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "GM"}); e != nil {
				t.Fatal("settled failed flow could not cancel", e)
			}
			out, e = c.GetFlow(context.Background(), f.ID)
			if e != nil || out.State != "cancelled" {
				t.Fatal(out, e)
			}
		})
	}
}
