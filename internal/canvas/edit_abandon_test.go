package canvas

import (
	"context"
	"testing"
)

func TestEditAbandonCannotRewindLaterGeneration(t *testing.T) {
	for _, outcome := range []string{"abandoned", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			c, _ := workflowFixture(t, false, func(input map[string]any) string {
				if input["edit"] != nil {
					if outcome == "abandoned" {
						return changeEnvelope("prd", validPRD)
					}
					return ""
				}
				return changeEnvelope("prd", validPRD)
			})
			f := startWorkflow(t, c, "abandon-stale-generation", 12)
			f = waitWorkflow(t, c, f.ID, "paused")
			r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "abandon-replay-edit"))
			if e != nil {
				t.Fatal(e)
			}
			q := EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "abandon-exact-original", ExpectedHead: mustHead(t, c), Confirmed: true}
			if outcome == "failed" {
				r = waitEdit(t, c, r.ID, "failed")
			} else {
				r = waitEdit(t, c, r.ID, "proposal")
				if _, e = c.AbandonEdit(context.Background(), q); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = c.ResumeAfterEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "resume-before-generation", ExpectedHead: q.ExpectedHead, Confirmed: true}); e != nil {
				t.Fatal(e)
			}
			if _, e = c.RetryFlow(context.Background(), FlowRetry{Run: f.ID, Attempt: f.Attempt, RequestKey: "retry-later-generation", Role: "GM"}); e != nil {
				t.Fatal(e)
			}
			before := waitWorkflow(t, c, f.ID, "paused")
			result, e := c.AbandonEdit(context.Background(), q)
			if outcome == "abandoned" && (e != nil || result.State != "abandoned") {
				t.Fatal("exact abandon lost idempotent result", result, e)
			}
			if outcome == "failed" && e == nil {
				t.Fatal("released failed edit admitted abandonment")
			}
			after, e := c.GetFlow(context.Background(), f.ID)
			if e != nil || after.State != before.State || after.Attempt != before.Attempt || after.Attempts != before.Attempts {
				t.Fatal("stale abandon rewound generation", before, after, e)
			}
		})
	}
}
