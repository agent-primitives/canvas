package canvas

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Flow-associated editing extends the document-only API only for an inspected,
// active flow. Unknown and completed associations remain side-effect-free refusals.
func TestEditRejectsUnknownOrCompletedFlowRun(t *testing.T) {
	for _, state := range []string{"unknown", "completed"} {
		t.Run(state, func(t *testing.T) {
			c, calls := workflowFixture(t, false, nil)
			id := "unknown-flow-run"
			if state == "completed" {
				f := startWorkflow(t, c, "explicit-flow-boundary", 12)
				for range 3 {
					f = waitWorkflow(t, c, f.ID, "review")
					if _, err := c.Review(f.Proposal, "accept", "reviewer"); err != nil {
						t.Fatal(err)
					}
				}
				f = waitWorkflow(t, c, f.ID, "complete")
				id = f.ID
			}
			c.wg.Wait()
			assertEditAdmissionRefused(t, c, calls, editRequest(t, c, id, "explicit-flow-edit-refusal"))
		})
	}
}

func TestEditFlowHTTPContextRejectsUnknownAssociation(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	before, disk := editAdmissionSnapshot(t, c), retirementLedger(t, c)
	head, count := mustHead(t, c), calls.Load()
	mux := http.NewServeMux()
	c.registerEditRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/edit/context?document=prd&flowRun=unknown-flow-run", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("unknown flow-associated context exposed: %d %s", w.Code, w.Body.String())
	}
	if !bytes.Equal(before, editAdmissionSnapshot(t, c)) || !bytes.Equal(disk, retirementLedger(t, c)) || head != mustHead(t, c) || count != calls.Load() {
		t.Fatal("context read changed journals, head or model calls")
	}
}
