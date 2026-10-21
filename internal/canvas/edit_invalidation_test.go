package canvas

import (
	"context"
	"testing"
)

func documentEditFixture(t *testing.T) *Canvas {
	t.Helper()
	file := genericFixture(t)
	genericConfigEdit(t, file, "canvas.local/v1alpha2", "canvas.local/v1alpha3")
	genericConfigEdit(t, file, "path: prd.json\n", "path: prd.json\n      editAgent: writer\n")
	genericConfigEdit(t, file, "read: [brief, feedback, prd]\n      propose: [review]", "read: [review]\n      propose: [review]")
	c, _, _ := definedFixtureAt(t, file, func(input map[string]any) string {
		if input["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		if input["agent"] == "reviewer" {
			return changeEnvelope("review", `{"summary":"Independent unrelated review","risks":[],"unsupportedClaims":[],"fictional":true}`)
		}
		return changeEnvelope("announcement", "# Old-base manual draft\n")
	})
	return c
}
func TestEditAcceptanceInvalidatesOldManualProposal(t *testing.T) {
	c := documentEditFixture(t)
	id, e := c.Manual("reviewer", "", "Draft unrelated review.", "GM")
	if e != nil {
		t.Fatal(e)
	}
	old := completed(t, c, id)
	if old.Status != "proposed" || old.Proposal == "" {
		t.Fatalf("unrelated proposal fixture: %+v", old)
	}
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, "", "invalidate-manual-key"))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	if _, e = c.Review(r.Proposal, "accept", "GM"); e != nil {
		t.Fatal(e)
	}
	head := mustHead(t, c)
	if _, e = c.Review(old.Proposal, "accept", "GM"); e == nil {
		t.Fatal("old-base unrelated manual proposal remained acceptable after edit")
	}
	if mustHead(t, c) != head {
		t.Fatal("old proposal changed accepted head")
	}
}
func TestEditAcceptancePreservesUnrelatedManualAction(t *testing.T) {
	c := documentEditFixture(t)
	p := acceptedAnnouncement(t, c, "# Approved old snapshot\n")
	id, e := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if e != nil {
		t.Fatal(e)
	}
	v := confirmedAnnouncement(t, c, id)
	if e = c.DecideAnnouncement(context.Background(), id, "GM", true, v.Confirmation); e != nil {
		t.Fatal(e)
	}
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, "", "invalidate-action-key"))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	if _, e = c.Review(r.Proposal, "accept", "GM"); e != nil {
		t.Fatal(e)
	}
	v = confirmedAnnouncement(t, c, id)
	if e = c.ContinueAnnouncement(context.Background(), id, "GM", v.Confirmation); e != nil {
		t.Fatal("unrelated manual handle was invalidated", e)
	}
}
