package canvas

import (
	"context"
	"testing"
)

func TestEditStrictStaleReview(t *testing.T) {
	c, _ := workflowFixture(t, false, func(map[string]any) string { return changeEnvelope("prd", validPRD) })
	q := editRequest(t, c, "", "stale-edit-review")
	r, e := c.RequestEdit(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	// A direct external checkpoint changes the full base, even though it does
	// not touch the proposed document. No hidden rebase is permitted for edits.
	if _, e = c.space.Ingest("feedback/new.json", `{"team":"Other","feedback":"new premise"}`, "GM"); e != nil {
		t.Fatal(e)
	}
	head := mustHead(t, c)
	if _, e = c.Review(r.Proposal, "accept", "GM"); e == nil {
		t.Fatal("stale edit accepted")
	}
	if mustHead(t, c) != head {
		t.Fatal("stale edit mutated accepted bytes")
	}
}
