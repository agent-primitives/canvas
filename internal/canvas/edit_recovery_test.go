package canvas

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
)

func TestEditAbandonedLateOutput(t *testing.T) {
	c, _ := workflowFixture(t, false, func(map[string]any) string { return changeEnvelope("prd", validPRD) })
	q := editRequest(t, c, "", "abandoned-publication")
	r, e := c.RequestEdit(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	// Actual durable Git publication, with its acknowledgement cut from both
	// Canvas records: no synthesized proposal or invalid fixture is involved.
	c.mu.Lock()
	c.editLocked(r.ID).Proposal = ""
	c.editLocked(r.ID).State = "uncertain"
	c.findLocked(r.Attempt).Proposal = ""
	e = c.saveLocked()
	c.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.AbandonEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "abandon-lost-publication", ExpectedHead: q.BaseRevision, Confirmed: true}); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Review(r.Proposal, "accept", "GM"); e == nil {
		t.Fatal("abandoned output bypassed through generic review")
	}
	if mustHead(t, c) != q.BaseRevision {
		t.Fatal("abandoned output changed accepted bytes")
	}
}
func TestEditRestartAndUnknownDispatch(t *testing.T) {
	c, calls := workflowFixture(t, false, func(map[string]any) string { return changeEnvelope("prd", validPRD) })
	c.sem <- struct{}{}
	q := editRequest(t, c, "", "restart-edit-request")
	r, e := c.RequestEdit(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	raw := retirementLedger(t, c)
	c.Close()
	<-c.sem
	if e = os.WriteFile(filepath.Join(c.root, "attempts.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	reopened, e := OpenFlowRuntime(c.root, c.space, c.client, c.address, c.definition, contract.LocalBinder{}, FlowRuntimeOptions{MaxAgentRuns: 12})
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	recovered, e := reopened.GetEdit(context.Background(), r.ID)
	if e != nil || recovered.State != "uncertain" || calls.Load() != 0 {
		t.Fatal("reopen inferred execution or dispatched", recovered, e)
	}
	again, e := reopened.RequestEdit(context.Background(), q)
	if e != nil || again.ID != r.ID || calls.Load() != 0 {
		t.Fatal("exact key minted replacement", again, e)
	}
	other := q
	other.RequestKey = "competing-restart-key"
	if _, e = reopened.RequestEdit(context.Background(), other); e == nil {
		t.Fatal("uncertain slot released")
	}
	if _, e = reopened.AbandonEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "explicit-abandon-key", ExpectedHead: q.BaseRevision, Confirmed: true}); e != nil {
		t.Fatal(e)
	}
	if reopened.data.EditSlot != "" || calls.Load() != 0 || mustHead(t, reopened) != q.BaseRevision {
		t.Fatal("abandon changed bytes/dispatched or held edit slot")
	}
}
func TestEditFrozenReadInputs(t *testing.T) {
	c, _ := workflowFixture(t, false, func(input map[string]any) string {
		docs := input["documents"].(map[string]any)
		if _, ok := docs["brief"]; !ok {
			t.Error("private read scope was changed through exported fields")
		}
		return changeEnvelope("prd", validPRD)
	})
	reads := c.definition.editReads("writer")
	reads[0] = "not-a-document"
	a := c.definition.Agents["writer"]
	a.Read = []string{"prd"}
	a.Propose = []string{"review"}
	c.definition.Agents["writer"] = a
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, "", "frozen-edit-reads"))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	run, ok := c.GetRun(r.Attempt)
	if !ok || run.QuestionsSupported {
		t.Fatal("edit session exposed generic questions", run)
	}
	if view := c.InspectResume(context.Background(), r.Attempt, ""); view.Available {
		t.Fatal("edit session exposed generic Resume")
	}
}
func TestEditReplacementSchemaPrecision(t *testing.T) {
	for _, number := range []string{"1.0000000000000001", "2"} {
		t.Run(number, func(t *testing.T) {
			filename := genericFixture(t)
			genericConfigEdit(t, filename, "canvas.local/v1alpha2", "canvas.local/v1alpha3")
			genericConfigEdit(t, filename, "path: prd.json\n", "path: prd.json\n      editAgent: writer\n")
			root := filepath.Dir(filename)
			if e := os.WriteFile(filepath.Join(root, "schemas", "prd.json"), []byte(`{"type":"object","required":["count"],"properties":{"count":{"type":"integer"}},"additionalProperties":false}`), 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(root, "seeds", "prd.json"), []byte(`{"count":1}`), 0600); e != nil {
				t.Fatal(e)
			}
			c, _, calls := definedFixtureAt(t, filename, func(map[string]any) string { return changeEnvelope("prd", `{"count":`+number+`}`) })
			q := editRequest(t, c, "", "precision-"+strings.ReplaceAll(number, ".", "-"))
			p := "/count"
			q.Section = &EditSection{Kind: "json-pointer", Pointer: &p}
			r, e := c.RequestEdit(context.Background(), q)
			if e != nil {
				t.Fatal(e)
			}
			want := "failed"
			if number == "2" {
				want = "proposal"
			}
			waitEdit(t, c, r.ID, want)
			if calls.Load() != 1 || mustHead(t, c) != q.BaseRevision {
				t.Fatal("numeric validation dispatched twice or mutated accepted")
			}
			if e := validateDocument(c.definition.documents["prd"], `{"count":2}`); e != nil {
				t.Fatal("ordinary validator changed", e)
			}
			var original map[string]any
			if json.Unmarshal([]byte(c.definition.Seed["prd.json"]), &original) != nil || original["count"] != float64(1) {
				t.Fatal("seed validation or bytes changed")
			}
		})
	}
}
