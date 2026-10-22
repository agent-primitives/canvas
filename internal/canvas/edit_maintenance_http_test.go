package canvas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEditHTTPMaintenanceDistinguishesUnreservedRefusal(t *testing.T) {
	c, _ := workflowFixture(t, false, func(input map[string]any) string {
		if input["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	f := startWorkflow(t, c, "edit-http-refusal-flow", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "edit-http-refusal-request"))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	if _, e = c.Review(r.Proposal, "accept", "GM"); e != nil {
		t.Fatal(e)
	}
	mux := http.NewServeMux()
	c.registerEditRoutes(mux)
	q := EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "unreserved-rerun-key", ExpectedHead: "not-the-inspected-head", Confirmed: true}
	raw, _ := json.Marshal(q)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/edit/rerun", strings.NewReader(string(raw))))
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"EDIT_REFUSED"`) {
		t.Fatal("unreserved refusal reported as retained unknown intent", w.Code, w.Body.String())
	}
	c.mu.Lock()
	recorded := c.editLocked(r.ID).Commands["rerun"]
	c.mu.Unlock()
	if recorded != "" {
		t.Fatal("refusal reserved command")
	}
}
