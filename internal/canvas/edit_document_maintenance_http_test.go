package canvas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEditDocumentHTTPMaintenanceRefusalIsUnreserved(t *testing.T) {
	c, calls := workflowFixture(t, false, func(map[string]any) string { return changeEnvelope("prd", validPRD) })
	r, err := c.RequestEdit(context.Background(), editRequest(t, c, "", "document-http-maintenance"))
	if err != nil {
		t.Fatal(err)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	before, disk := editAdmissionSnapshot(t, c), retirementLedger(t, c)
	head, count := mustHead(t, c), calls.Load()
	mux := http.NewServeMux()
	c.registerEditRoutes(mux)
	q := EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "unreserved-document-abandon", ExpectedHead: "not-the-inspected-head", Confirmed: true}
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/edit/abandon", strings.NewReader(string(raw))))
	var response struct {
		Code string `json:"code"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusConflict || response.Code != "EDIT_REFUSED" {
		t.Fatal("unreserved refusal retained as unknown", w.Code, w.Body.String(), err)
	}
	c.mu.Lock()
	record := c.editLocked(r.ID)
	reserved, slot, state := record.Commands["abandon"], c.data.EditSlot, record.State
	c.mu.Unlock()
	if reserved != "" || slot != r.ID || state != "proposal" {
		t.Fatal("refusal reserved or released edit", reserved, slot, state)
	}
	if string(before) != string(editAdmissionSnapshot(t, c)) || string(disk) != string(retirementLedger(t, c)) || head != mustHead(t, c) || count != calls.Load() {
		t.Fatal("refusal changed journals, accepted head or provider calls")
	}
}
