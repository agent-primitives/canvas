package canvas

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-primitives/canvas/internal/sharedspace"
)

type editLostReviewSpace struct{ sharedspace.SharedSpace }

func (s editLostReviewSpace) Accept(id, head, actor string) (sharedspace.Proposal, error) {
	p, e := s.SharedSpace.Accept(id, head, actor)
	if e != nil {
		return p, e
	}
	return p, errors.New("synthetic lost committed review reply")
}
func TestEditReviewCrashAndStaleBase(t *testing.T) {
	c, _ := workflowFixture(t, false, func(map[string]any) string { return changeEnvelope("prd", validPRD) })
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, "", "lost-edit-review"))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	c.wg.Wait()
	original := c.space
	c.space = editLostReviewSpace{original}
	if _, e = c.Review(r.Proposal, "accept", "GM"); e == nil {
		t.Fatal("expected lost review reply")
	}
	if e = c.update(r.Attempt, func(*attempt) {}); e != nil {
		t.Fatal(e)
	}
	pending, e := c.GetEdit(context.Background(), r.ID)
	if e != nil || pending.State != "uncertain" {
		t.Fatal("finishing bookkeeping renewed review authority", pending, e)
	}
	if _, e = c.Review(r.Proposal, "reject", "GM"); e == nil {
		t.Fatal("uncertain decision was repeatable")
	}
	c.space = original
	head := mustHead(t, c)
	out, e := c.ReconcileEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "reconcile-lost-review", ExpectedHead: head, Confirmed: true})
	if e != nil || out.State != "accepted" || mustHead(t, c) != head {
		t.Fatal("exact review reconciliation failed or repeated Git decision", out, e)
	}
}
