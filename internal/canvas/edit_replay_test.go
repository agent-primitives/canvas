package canvas

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// This exercises the real native journal, Git proposal review and action gate;
// counting only ordinary generations must reject this otherwise complete flow.
func TestEditFlowCompleteProofCountsEveryAttempt(t *testing.T) {
	c, calls := workflowFixture(t, true, func(input map[string]any) string {
		if input["edit"] != nil {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	ctx := context.Background()
	f := startWorkflow(t, c, "edit-replay", 12)
	f = waitWorkflow(t, c, f.ID, "review")
	if _, err := c.Review(f.Proposal, "accept", "reviewer"); err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "review")
	e, err := c.RequestEdit(ctx, editRequest(t, c, f.ID, "edit-key"))
	if err != nil {
		t.Fatal(err)
	}
	e = waitEdit(t, c, e.ID, "proposal")
	if _, err = c.Review(e.Proposal, "accept", "GM"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.RerunAfterEdit(ctx, EditMaintenance{Edit: e.ID, Role: "GM", RequestKey: "rerun-key", ExpectedHead: mustHead(t, c), Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		f = waitWorkflow(t, c, f.ID, "review")
		if _, err = c.Review(f.Proposal, "accept", "reviewer"); err != nil {
			t.Fatal(err)
		}
	}
	f = waitWorkflow(t, c, f.ID, "action")
	// Approval and continuation remain distinct explicit human gates.
	v, err := c.InspectAnnouncement(ctx, f.Action)
	if err != nil {
		t.Fatal(err)
	}
	v.Confirmation.Confirmed = true
	if err = c.DecideAnnouncement(ctx, f.Action, "reviewer", true, v.Confirmation); err != nil {
		t.Fatal(err)
	}
	v, err = c.InspectAnnouncement(ctx, f.Action)
	if err != nil {
		t.Fatal(err)
	}
	v.Confirmation.Confirmed = true
	if err = c.ContinueAnnouncement(ctx, f.Action, "reviewer", v.Confirmation); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ReconcileFlow(ctx, FlowMaintenance{Run: f.ID, Role: "GM"}); err != nil {
		t.Fatal(err)
	}
	f = waitWorkflow(t, c, f.ID, "complete")
	c.mu.Lock()
	record := cloneFlowRecord(*c.flowRecordLocked(f.ID))
	c.mu.Unlock()
	if f.Attempts != len(record.Generations)+len(record.EditIDs) || len(record.EditIDs) != 1 {
		t.Fatalf("spent attempts: %+v", record)
	}
	before, err := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if err != nil {
		t.Fatal(err)
	}
	refs := replayRefs(t, c)
	head := mustHead(t, c)
	count := calls.Load()
	p, err := c.VerifyFlow(ctx, f.ID)
	if err != nil || !p.Complete {
		t.Fatalf("edit replay incomplete: %+v %v", p, err)
	}
	edits := 0
	for _, s := range p.Sessions {
		if s.Kind == "edit" {
			edits++
		}
		if s.Coverage != "reconstructed" || !flowReplayCovered(s.Proof, s.Execution, s.Head) {
			t.Fatalf("missing zero-call native proof: %+v", s)
		}
	}
	if edits != 1 || len(p.Sessions) != f.Attempts+1 {
		t.Fatalf("missing counted edit/action sessions: %+v", p)
	}
	after, err := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !bytes.Equal(refs, replayRefs(t, c)) || head != mustHead(t, c) || count != calls.Load() {
		t.Fatal("replay mutated native/Git/private state")
	}
	if len(p.Reviews) != len(record.Reviews) || len(p.Reviews) != 4 {
		t.Fatalf("old review history lost: %+v", p.Reviews)
	}
	// Each corruption starts from the same complete real native/Git history.
	// None may gain complete coverage merely by repairing the arithmetic.
	cases := []struct {
		name   string
		mutate func()
	}{
		{"missing edit pointer", func() { c.flowRecordLocked(f.ID).EditIDs = nil }},
		{"duplicate edit pointer", func() { r := c.flowRecordLocked(f.ID); r.EditIDs = append(r.EditIDs, e.ID); r.Attempts++ }},
		{"foreign edit pointer", func() { c.flowRecordLocked(f.ID).EditIDs[0] = "foreign" }},
		{"wrong association", func() { c.editLocked(e.ID).Request.FlowRun = "foreign" }},
		{"wrong owner", func() { c.findLocked(e.Attempt).ExecutionOwner.Subject = "foreign" }},
		{"wrong requester", func() { c.findLocked(e.Attempt).RequestedBy.Role = "owner" }},
		{"request digest", func() { c.editLocked(e.ID).Digest = "foreign" }},
		{"before bytes", func() { c.editLocked(e.ID).Before += " " }},
		{"frozen scope", func() { c.editLocked(e.ID).Scope.Hash = "foreign" }},
		{"read sources", func() { c.findLocked(e.Attempt).Sources["foreign"] = "foreign" }},
		{"native input", func() { c.findLocked(e.Attempt).Input += " " }},
		{"unknown native", func() { c.findLocked(e.Attempt).ExpectedSeq = nil }},
		{"abandoned edit", func() { c.editLocked(e.ID).State = "abandoned" }},
		{"unsettled edit", func() { c.editLocked(e.ID).State = "uncertain" }},
		{"review action", func() { c.editLocked(e.ID).ReviewAction = "reject" }},
		{"local reviewer", func() { c.editLocked(e.ID).Reviewer.Role = "owner" }},
		{"accepted commit", func() { c.editLocked(e.ID).AcceptedCommit = f.StartBase }},
		{"missing checkpoint", func() { c.flowRecordLocked(f.ID).EditCheckpoints = nil }},
		{"duplicate checkpoint", func() {
			r := c.flowRecordLocked(f.ID)
			r.EditCheckpoints = append(r.EditCheckpoints, r.EditCheckpoints[0])
		}},
		{"wrong previous generation", func() { c.flowRecordLocked(f.ID).EditCheckpoints[0].BeforeGeneration++ }},
		{"wrong restart generation", func() { c.flowRecordLocked(f.ID).EditCheckpoints[0].RestartGeneration++ }},
		{"checkpoint commit", func() { c.flowRecordLocked(f.ID).EditCheckpoints[0].AcceptedCommit = f.StartBase }},
		{"old accepted review removed", func() { r := c.flowRecordLocked(f.ID); r.Reviews = r.Reviews[1:] }},
		{"old revoked native unknown", func() {
			op := cloneEdit(c.data.Announcements[0])
			op.Snapshot.Operation = ID()
			op.FlowGeneration = record.EditCheckpoints[0].BeforeGeneration
			op.Revoked = true
			op.Session = ID() // No native conclusion exists for this historical handle.
			op.Stage = "uncertain"
			c.data.Announcements = append(c.data.Announcements, op)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c.mu.Lock()
			saved := cloneEdit(c.data)
			tc.mutate()
			c.mu.Unlock()
			defer func() { c.mu.Lock(); c.data = saved; c.mu.Unlock() }()
			proof, err := c.VerifyFlow(ctx, f.ID)
			if err != nil || proof.Complete {
				t.Fatalf("corrupt edit history reconstructed: %+v %v", proof, err)
			}
		})
	}
}
