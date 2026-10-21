package canvas

import (
	"context"
	"errors"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"testing"
)

type editLostPublicationSpace struct{ sharedspace.SharedSpace }

func (s editLostPublicationSpace) Propose(base string, changes map[string]string, at sharedspace.Attribution) (sharedspace.Proposal, error) {
	p, e := s.SharedSpace.Propose(base, changes, at)
	if e != nil {
		return p, e
	}
	return p, errors.New("synthetic lost published proposal reply")
}
func TestEditLostPublicationRetainsSlotAndReconcilesOneProposal(t *testing.T) {
	c, calls := workflowFixture(t, false, func(map[string]any) string { return changeEnvelope("prd", validPRD) })
	original := c.space
	c.space = editLostPublicationSpace{original}
	q := editRequest(t, c, "", "lost-edit-publication")
	r, e := c.RequestEdit(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	c.wg.Wait()
	c.space = original
	out, e := c.GetEdit(context.Background(), r.ID)
	if e != nil || out.State != "uncertain" || c.data.EditSlot != r.ID {
		t.Fatal("unknown Git publication released edit fence", out, e)
	}
	ps, e := c.space.Proposals()
	if e != nil || len(ps) != 1 {
		t.Fatal("actual published proposal absent", ps, e)
	}
	if _, e = c.Review(ps[0].ID, "accept", "GM"); e == nil {
		t.Fatal("unknown output was reviewable before reconciliation")
	}
	out, e = c.ReconcileEdit(context.Background(), EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "reconcile-published-edit", ExpectedHead: q.BaseRevision, Confirmed: true})
	if e != nil || out.State != "proposal" || out.Proposal != ps[0].ID {
		t.Fatal("publication reconciliation failed", out, e)
	}
	after, e := c.space.Proposals()
	if e != nil || len(after) != 1 || calls.Load() != 1 || mustHead(t, c) != q.BaseRevision {
		t.Fatal("reconciliation repeated producer/publication or accepted bytes", after, e)
	}
}
