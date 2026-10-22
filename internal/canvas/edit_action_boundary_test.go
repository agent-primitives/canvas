package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func announcementEditFixture(t *testing.T) *Canvas {
	t.Helper()
	file := genericFixture(t)
	genericConfigEdit(t, file, "canvas.local/v1alpha2", "canvas.local/v1alpha3")
	genericConfigEdit(t, file, "path: announcement.md\n", "path: announcement.md\n      editAgent: writer\n")
	genericConfigEdit(t, file, "read: [brief, feedback, prd]\n      propose: [prd, announcement]", "read: [brief, feedback, prd, announcement]\n      propose: [prd, announcement]")
	c, _, _ := definedFixtureAt(t, file, func(map[string]any) string { return changeEnvelope("announcement", "# Explicit new announcement\n") })
	return c
}
func announcementEditRequest(t *testing.T, c *Canvas, key string) EditRequest {
	t.Helper()
	head := mustHead(t, c)
	text, e := c.space.Read(head, "announcement.md")
	if e != nil {
		t.Fatal(e)
	}
	return EditRequest{Document: "announcement", BaseRevision: head, BaseDigest: editScopeHash(text), Instruction: "Revise this announcement.", RequestKey: key, Role: "GM"}
}
func TestEditRefusesUnsettledSameDocumentManualAction(t *testing.T) {
	for _, decision := range []string{"pending", "approved", "denied"} {
		t.Run(decision, func(t *testing.T) {
			c := announcementEditFixture(t)
			p := acceptedAnnouncement(t, c, "# Reviewed old announcement\n")
			id, e := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
			if e != nil {
				t.Fatal(e)
			}
			if decision != "pending" {
				v := confirmedAnnouncement(t, c, id)
				if e = c.DecideAnnouncement(context.Background(), id, "GM", decision == "approved", v.Confirmation); e != nil {
					t.Fatal(e)
				}
			}
			before := retirementLedger(t, c)
			head := mustHead(t, c)
			if _, e = c.RequestEdit(context.Background(), announcementEditRequest(t, c, "same-document-refusal")); !errors.Is(e, ErrEditConflict) {
				t.Fatal("unsettled same-document handle admitted edit", e)
			}
			after := retirementLedger(t, c)
			if string(before) != string(after) || head != mustHead(t, c) || len(c.data.Edits) != 0 {
				t.Fatal("refusal changed journals or accepted bytes")
			}
			// Refusal must not strand the native handle: the original human gates work.
			v := confirmedAnnouncement(t, c, id)
			if decision == "pending" {
				if e = c.DecideAnnouncement(context.Background(), id, "GM", false, v.Confirmation); e != nil {
					t.Fatal(e)
				}
				v = confirmedAnnouncement(t, c, id)
			}
			if e = c.ContinueAnnouncement(context.Background(), id, "GM", v.Confirmation); e != nil {
				t.Fatal("refused edit invalidated original handle", e)
			}
		})
	}
}
func TestEditPreservesConcludedSameDocumentManualAction(t *testing.T) {
	c := announcementEditFixture(t)
	p := acceptedAnnouncement(t, c, "# Concluded snapshot\n")
	id, e := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if e != nil {
		t.Fatal(e)
	}
	v := confirmedAnnouncement(t, c, id)
	if e = c.DecideAnnouncement(context.Background(), id, "GM", true, v.Confirmation); e != nil {
		t.Fatal(e)
	}
	v = confirmedAnnouncement(t, c, id)
	if e = c.ContinueAnnouncement(context.Background(), id, "GM", v.Confirmation); e != nil {
		t.Fatal(e)
	}
	before, _ := c.announcementOperation(id)
	raw, _ := json.Marshal(before)
	r, e := c.RequestEdit(context.Background(), announcementEditRequest(t, c, "concluded-document-edit"))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	if _, e = c.Review(r.Proposal, "accept", "GM"); e != nil {
		t.Fatal(e)
	}
	after, _ := c.announcementOperation(id)
	got, _ := json.Marshal(after)
	if string(got) != string(raw) {
		t.Fatal("concluded native record changed", string(raw), string(got))
	}
	view, e := c.InspectAnnouncement(context.Background(), id)
	if e != nil || !view.Concluded || view.Receipt == nil {
		t.Fatal("concluded receipt lost", view, e)
	}
}
func TestEditRevokesOnlyOwnFlowUnexecutedHandles(t *testing.T) {
	c, _ := workflowFixture(t, true, func(input map[string]any) string {
		if input["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	p := acceptedAnnouncement(t, c, "# Earlier concluded manual action\n")
	id, e := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if e != nil {
		t.Fatal(e)
	}
	v := confirmedAnnouncement(t, c, id)
	if e = c.DecideAnnouncement(context.Background(), id, "GM", false, v.Confirmation); e != nil {
		t.Fatal(e)
	}
	v = confirmedAnnouncement(t, c, id)
	if e = c.ContinueAnnouncement(context.Background(), id, "GM", v.Confirmation); e != nil {
		t.Fatal(e)
	}
	old, _ := c.announcementOperation(id)
	before, _ := json.Marshal(old)
	f := startWorkflow(t, c, "own-flow-handle-edit", 12)
	for range 3 {
		f = waitWorkflow(t, c, f.ID, "review")
		if _, e = c.Review(f.Proposal, "accept", "reviewer"); e != nil {
			t.Fatal(e)
		}
	}
	f = waitWorkflow(t, c, f.ID, "action")
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "own-flow-only-edit"))
	if e != nil {
		t.Fatal(e)
	}
	r = waitEdit(t, c, r.ID, "proposal")
	if _, e = c.Review(r.Proposal, "accept", "GM"); e != nil {
		t.Fatal(e)
	}
	current, _ := c.announcementOperation(f.Action)
	if !current.Revoked || current.EffectReserved {
		t.Fatal("own unexecuted handle not invalidated", current)
	}
	old, _ = c.announcementOperation(id)
	after, _ := json.Marshal(old)
	if string(before) != string(after) {
		t.Fatal("other concluded operation was changed")
	}
}
