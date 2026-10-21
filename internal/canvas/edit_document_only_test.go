package canvas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
)

// Refusal must preserve private journals too, including the flow's budget,
// generation and proposal binding, rather than merely avoiding a model call.
func editAdmissionSnapshot(t *testing.T, c *Canvas) []byte {
	t.Helper()
	c.mu.Lock()
	raw, err := json.Marshal(c.data)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertEditAdmissionRefused(t *testing.T, c *Canvas, calls *atomic.Int32, q EditRequest) {
	t.Helper()
	before, disk := editAdmissionSnapshot(t, c), retirementLedger(t, c)
	head, count := mustHead(t, c), calls.Load()
	if _, err := c.RequestEdit(context.Background(), q); !errors.Is(err, ErrEditConflict) {
		t.Fatal("document-only boundary admitted a flow edit", err)
	}
	if !bytes.Equal(before, editAdmissionSnapshot(t, c)) || !bytes.Equal(disk, retirementLedger(t, c)) || head != mustHead(t, c) || count != calls.Load() {
		t.Fatal("refused edit changed journals, head or model calls")
	}
}

func TestEditDocumentOnlyFlowAdmissionBothOrders(t *testing.T) {
	t.Run("flow-first", func(t *testing.T) {
		c, calls := workflowFixture(t, false, nil)
		f := startWorkflow(t, c, "flow-before-document-edit", 12)
		waitWorkflow(t, c, f.ID, "review")
		c.wg.Wait()
		assertEditAdmissionRefused(t, c, calls, editRequest(t, c, "", "document-edit-during-flow"))
	})
	t.Run("edit-first", func(t *testing.T) {
		c, calls := workflowFixture(t, false, func(map[string]any) string { return changeEnvelope("prd", validPRD) })
		r, err := c.RequestEdit(context.Background(), editRequest(t, c, "", "document-edit-before-flow"))
		if err != nil {
			t.Fatal(err)
		}
		waitEdit(t, c, r.ID, "proposal")
		c.wg.Wait()
		before, disk := editAdmissionSnapshot(t, c), retirementLedger(t, c)
		head, count := mustHead(t, c), calls.Load()
		if _, err = c.StartFlow(context.Background(), FlowStart{Flow: "release", RequestKey: "flow-during-document-edit", Inputs: []byte(`{"topic":"Fictional pilot"}`), AgentRuns: 12, Role: "GM"}); !errors.Is(err, ErrFlowBusy) {
			t.Fatal("flow bypassed document edit slot", err)
		}
		if !bytes.Equal(before, editAdmissionSnapshot(t, c)) || !bytes.Equal(disk, retirementLedger(t, c)) || head != mustHead(t, c) || count != calls.Load() {
			t.Fatal("refused flow changed journals, head or model calls")
		}
	})
}
