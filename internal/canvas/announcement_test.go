package canvas

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/sharedspace"
)

func acceptedAnnouncement(t *testing.T, c *Canvas, content string) sharedspace.Proposal {
	t.Helper()
	head, err := c.space.Head()
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.space.Propose(head, map[string]string{"announcement.md": content}, sharedspace.Attribution{Actor: "GM", Agent: "writer"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = c.Review(p.ID, "accept", "GM")
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func confirmedAnnouncement(t *testing.T, c *Canvas, id string) AnnouncementView {
	t.Helper()
	v, err := c.InspectAnnouncement(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	v.Confirmation.Confirmed = true
	return v
}
func TestAnnouncementExactOldSnapshotAndNoCarryForward(t *testing.T) {
	c, _, calls := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	old := acceptedAnnouncement(t, c, "# Old exact draft\nNothing sent.\n")
	id, err := c.RequestAnnouncement(context.Background(), "announce", old.ID, "GM")
	if err != nil {
		t.Fatal(err)
	}
	original := confirmedAnnouncement(t, c, id)
	newer := acceptedAnnouncement(t, c, "# New draft\nSeparate approval required.\n")
	if err := c.DecideAnnouncement(context.Background(), id, "GM", true, original.Confirmation); err == nil {
		t.Fatal("stale warning-state confirmation accepted")
	}
	view := confirmedAnnouncement(t, c, id)
	if !view.Newer || view.Warning != "A newer version exists; approving publishes this older version" || view.Snapshot.Revision != old.AcceptedCommit {
		t.Fatalf("warning/snapshot: %+v", view)
	}
	unchecked := view.Confirmation
	unchecked.Confirmed = false
	if err := c.DecideAnnouncement(context.Background(), id, "GM", true, unchecked); err == nil {
		t.Fatal("missing explicit confirmation accepted")
	}
	if err := c.DecideAnnouncement(context.Background(), id, "GM", true, view.Confirmation); err != nil {
		t.Fatal(err)
	}
	decided := confirmedAnnouncement(t, c, id)
	if decided.Status != "approved" || decided.Receipt != nil {
		t.Fatalf("decision executed effect: %+v", decided)
	}
	if err := c.ContinueAnnouncement(context.Background(), id, "GM", decided.Confirmation); err != nil {
		t.Fatal(err)
	}
	done := confirmedAnnouncement(t, c, id)
	if done.Status != "simulated" || done.Receipt == nil || done.Receipt.Snapshot.Content != "# Old exact draft\nNothing sent.\n" || done.Receipt.Snapshot.Revision != old.AcceptedCommit {
		t.Fatalf("not exact native receipt: %+v", done)
	}
	next, err := c.RequestAnnouncement(context.Background(), "announce", newer.ID, "GM")
	if err != nil || next == id {
		t.Fatalf("new version reused approval: %s %v", next, err)
	}
	pending := confirmedAnnouncement(t, c, next)
	if pending.Status != "pending-approval" || pending.Receipt != nil {
		t.Fatalf("approval carried forward: %+v", pending)
	}
	if err := c.ContinueAnnouncement(context.Background(), next, "GM", pending.Confirmation); err == nil {
		t.Fatal("unapproved new version continued")
	}
	if calls.Load() != 0 {
		t.Fatalf("action invoked model %d times", calls.Load())
	}
}
func TestAnnouncementRejectsSeedAndDenialNeverPublishes(t *testing.T) {
	c, _, calls := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	if _, err := c.RequestAnnouncement(context.Background(), "announce", "", "GM"); err == nil {
		t.Fatal("seed-only gate")
	}
	p := acceptedAnnouncement(t, c, "# Deny me\n")
	id, err := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if err != nil {
		t.Fatal(err)
	}
	v := confirmedAnnouncement(t, c, id)
	if err := c.DecideAnnouncement(context.Background(), id, "GM", false, v.Confirmation); err != nil {
		t.Fatal(err)
	}
	v = confirmedAnnouncement(t, c, id)
	if v.Status != "denied" {
		t.Fatal(v.Status)
	}
	if err := c.ContinueAnnouncement(context.Background(), id, "GM", v.Confirmation); err != nil {
		proof, proofErr := c.InspectAnnouncement(context.Background(), id)
		t.Fatalf("continue: %v; proof=%+v err=%v", err, proof, proofErr)
	}
	v = confirmedAnnouncement(t, c, id)
	if v.Receipt != nil || v.Status != "denied" {
		t.Fatal("denial became publication")
	}
	if err := c.DecideAnnouncement(context.Background(), id, "GM", false, v.Confirmation); err == nil {
		t.Fatal("concluded denial accepted another decision")
	}
	if calls.Load() != 0 {
		t.Fatal("model used")
	}
}
func TestAnnouncementSameTextRevisionAndUnrelatedInput(t *testing.T) {
	c, _, _ := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Identical\n")
	id, err := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.space.Ingest("feedback/other.json", `{"team":"Northwind","feedback":"unrelated"}`, "GM"); err != nil {
		t.Fatal(err)
	}
	v := confirmedAnnouncement(t, c, id)
	if v.Newer {
		t.Fatal("unrelated input counted as newer")
	}
	acceptedAnnouncement(t, c, "# Intervening\n")
	newest := acceptedAnnouncement(t, c, "# Identical\n")
	v = confirmedAnnouncement(t, c, id)
	if !v.Newer || v.CurrentRevision != newest.AcceptedCommit {
		t.Fatal("same text revision lost version identity")
	}
	if !strings.Contains(v.Snapshot.Content, "Identical") {
		t.Fatal("snapshot replaced")
	}
}
