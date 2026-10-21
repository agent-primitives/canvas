package canvas

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Explicit flow association is refused in the document-only API. The separate
// admission tests continue to apply when flow-associated edits are introduced.
func TestEditDocumentOnlyRejectsEveryFlowRun(t *testing.T) {
	for _, state := range []string{"known-nonterminal", "unknown", "completed"} {
		t.Run(state, func(t *testing.T) {
			c, calls := workflowFixture(t, false, nil)
			id := "unknown-flow-run"
			if state != "unknown" {
				f := startWorkflow(t, c, "explicit-flow-boundary", 12)
				f = waitWorkflow(t, c, f.ID, "review")
				if state == "completed" {
					for range 3 {
						f = waitWorkflow(t, c, f.ID, "review")
						if _, err := c.Review(f.Proposal, "accept", "reviewer"); err != nil {
							t.Fatal(err)
						}
					}
					f = waitWorkflow(t, c, f.ID, "complete")
				}
				id = f.ID
			}
			c.wg.Wait()
			assertEditAdmissionRefused(t, c, calls, editRequest(t, c, id, "explicit-flow-edit-refusal"))
		})
	}
}

func TestEditDocumentOnlyHTTPContextRefusesKnownFlow(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	f := startWorkflow(t, c, "context-flow-boundary", 12)
	waitWorkflow(t, c, f.ID, "review")
	c.wg.Wait()
	before, disk := editAdmissionSnapshot(t, c), retirementLedger(t, c)
	head, count := mustHead(t, c), calls.Load()
	mux := http.NewServeMux()
	c.registerEditRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/edit/context?document=prd&flowRun="+f.ID, nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("flow-associated context exposed: %d %s", w.Code, w.Body.String())
	}
	if !bytes.Equal(before, editAdmissionSnapshot(t, c)) || !bytes.Equal(disk, retirementLedger(t, c)) || head != mustHead(t, c) || count != calls.Load() {
		t.Fatal("context read changed journals, head or model calls")
	}
}
