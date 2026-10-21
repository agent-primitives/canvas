package canvas

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

func editRequest(t *testing.T, c *Canvas, flow, key string) EditRequest {
	t.Helper()
	head := mustHead(t, c)
	text, err := c.space.Read(head, "prd.json")
	if err != nil {
		t.Fatal(err)
	}
	return EditRequest{FlowRun: flow, Document: "prd", BaseRevision: head, BaseDigest: fmt.Sprintf("%x", sha256.Sum256([]byte(text))), Instruction: "Revise only the requested document.", RequestKey: key, Role: "GM"}
}
func waitEdit(t *testing.T, c *Canvas, id, state string) EditRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r, e := c.GetEdit(context.Background(), id)
		if e != nil {
			t.Fatal(e)
		}
		if r.State == state {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	r, _ := c.GetEdit(context.Background(), id)
	t.Fatalf("edit not %s: %+v", state, r)
	return r
}
func TestEditCreatesOnlyScopedProposal(t *testing.T) {
	c, calls := workflowFixture(t, false, func(input map[string]any) string { return changeEnvelope("prd", validPRD) })
	q := editRequest(t, c, "", "edit-one")
	before := mustHead(t, c)
	r, e := c.RequestEdit(context.Background(), q)
	if e != nil {
		t.Fatal("valid edit refused", e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.mu.Lock()
	producer := cloneEdit(*c.findLocked(r.Attempt))
	c.mu.Unlock()
	if producer.EditID != r.ID || producer.FlowRun != "" || producer.FlowStep != "" || producer.FlowGeneration != 0 {
		t.Fatal("document editor acquired a synthetic flow binding", producer)
	}
	if mustHead(t, c) != before || calls.Load() != 1 || r.Proposal == "" {
		t.Fatal("edit mutated accepted bytes or lacked exactly one producer", r)
	}
	again, e := c.RequestEdit(context.Background(), q)
	if e != nil || again.ID != r.ID || calls.Load() != 1 {
		t.Fatal("exact request repeated", again, e)
	}
	q.Instruction += " changed"
	if _, e := c.RequestEdit(context.Background(), q); e == nil {
		t.Fatal("key payload changed")
	}
	if _, e := c.Review(r.Proposal, "accept", "GM"); e != nil {
		t.Fatal(e)
	}
	if mustHead(t, c) == before {
		t.Fatal("explicit acceptance did not update head")
	}
}
func TestEditRejectsScopeEscape(t *testing.T) {
	c, _ := workflowFixture(t, false, func(input map[string]any) string { return changeEnvelope("prd", validPRD) })
	q := editRequest(t, c, "", "scope-escape")
	p := "/title"
	q.Section = &EditSection{Kind: "json-pointer", Pointer: &p}
	r, e := c.RequestEdit(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	waitEdit(t, c, r.ID, "failed")
	if mustHead(t, c) != q.BaseRevision {
		t.Fatal("scope escape changed accepted")
	}
	ps, e := c.space.Proposals()
	if e != nil || len(ps) != 0 {
		t.Fatal("invalid output published", ps, e)
	}
}
func TestEditStaleAdmissionAndFrozenReads(t *testing.T) {
	c, _ := workflowFixture(t, false, func(input map[string]any) string { return changeEnvelope("prd", validPRD) })
	q := editRequest(t, c, "", "stale")
	q.BaseDigest = strings.Repeat("0", 64)
	if _, e := c.RequestEdit(context.Background(), q); e == nil {
		t.Fatal("stale hash admitted")
	}
	if c.data.EditSlot != "" || len(c.data.Edits) != 0 {
		t.Fatal("invalid request reserved")
	}
}
