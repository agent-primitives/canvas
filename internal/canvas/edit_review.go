package canvas

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/client"
)

// publicationMu is held by every caller. Edit proposals never fall through to
// generic review, even after release, abandonment or a new generation.
func (c *Canvas) reviewEditLocked(ctx context.Context, id, action, role string) (bool, sharedspace.Proposal, error) {
	c.mu.Lock()
	var record *editRecord
	for i := range c.data.Edits {
		if c.data.Edits[i].Proposal == id && id != "" {
			copy := cloneEdit(c.data.Edits[i])
			record = &copy
			break
		}
	}
	c.mu.Unlock()
	if record == nil && id != "" {
		// A Git publication can precede its Canvas acknowledgement. Resolve its
		// stored producer binding too, so abandonment cannot fall through.
		proposals, err := c.space.Proposals()
		if err != nil {
			return true, sharedspace.Proposal{}, err
		}
		for _, p := range proposals {
			if p.ID == id {
				c.mu.Lock()
				if a := c.findLocked(p.Attribution.Run); a != nil && a.EditID != "" {
					if e := c.editLocked(a.EditID); e != nil {
						copy := cloneEdit(*e)
						record = &copy
					}
				}
				c.mu.Unlock()
			}
		}
	}
	if record == nil {
		return false, sharedspace.Proposal{}, nil
	}
	r := *record
	if r.ReviewAction != "" || r.Proposal != id || r.State != "proposal" || r.Request.Role != role || action != "accept" && action != "reject" {
		return true, sharedspace.Proposal{}, ErrEditConflict
	}
	c.mu.Lock()
	eligible := c.data.EditSlot == r.ID
	a := *c.findLocked(r.Attempt)
	c.mu.Unlock()
	head, e := c.space.Head()
	if !eligible || e != nil || head != r.Request.BaseRevision {
		return true, sharedspace.Proposal{}, ErrEditConflict
	}
	ps, e := c.space.Proposals()
	if e != nil {
		return true, sharedspace.Proposal{}, e
	}
	var proposed sharedspace.Proposal
	for _, p := range ps {
		if p.ID == id {
			proposed = p
		}
	}
	if proposed.ID == "" || proposed.Status != "pending" || !c.editProposalMatches(r, a, proposed) {
		return true, sharedspace.Proposal{}, ErrEditConflict
	}
	p, e := c.reviewProposalLocked(ctx, id, action, role, func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		live := c.editLocked(r.ID)
		if live == nil || c.data.EditSlot != r.ID || live.State != "proposal" {
			return ErrEditConflict
		}
		live.ReviewAction = action
		live.Reviewer = sharedspace.ReviewActor{Role: role}
		live.State = "uncertain"
		return c.saveLocked()
	})
	if e != nil {
		return true, p, e
	}
	return true, p, c.applyEditReviewLocked(r.ID, p)
}
func (c *Canvas) editProposalMatches(r editRecord, a attempt, p sharedspace.Proposal) bool {
	doc := c.definition.documents[r.Request.Document]
	text, ok := p.Changes[doc.path]
	return ok && len(p.Changes) == 1 && p.Base == r.Request.BaseRevision && p.Base == a.Base && p.Attribution.Run == a.ID && p.Attribution.Session == a.Session && p.Attribution.Agent == a.Agent && p.Attribution.BundleDigest == a.BundleDigest && p.Attribution.AuthoredBundleDigest == a.AuthoredBundleDigest && p.Attribution.ExecutionOwner == a.ExecutionOwner && p.Attribution.RequestedBy == a.RequestedBy && maps.Equal(p.Attribution.Sources, a.Sources) && validateEditDocument(doc, text) == nil && validateEditScope(r.Before, text, r.Scope) == nil
}
func (c *Canvas) applyEditReviewLocked(id string, p sharedspace.Proposal) error {
	c.mu.Lock()
	record := c.editLocked(id)
	if record == nil {
		c.mu.Unlock()
		return ErrEditConflict
	}
	frozen := cloneEdit(*record)
	operations := cloneEdit(c.data.Announcements)
	c.mu.Unlock()
	var revoke []string
	if frozen.ReviewAction == "accept" && !frozen.ReviewApplied && frozen.Request.FlowRun != "" {
		for _, op := range operations {
			if op.FlowRun != frozen.Request.FlowRun || op.EffectReserved || op.EffectSettled {
				continue
			}
			view, err := c.inspectAnnouncement(context.Background(), op)
			if err != nil || view.Confirmation.Unavailable {
				return ErrEditConflict
			}
			if !view.Concluded {
				revoke = append(revoke, op.Snapshot.Operation)
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.editLocked(id)
	if r == nil || p.ID != r.Proposal || r.ReviewAction == "" || (r.Reviewer.Issuer != "" && p.Reviewer != r.Reviewer) || (r.Reviewer.Issuer == "" && p.Reviewer != (sharedspace.ReviewActor{})) {
		return ErrEditConflict
	}
	if r.ReviewApplied {
		if r.State == "uncertain" && c.data.EditSlot == r.ID {
			if r.ReviewAction == "accept" && p.Status == "accepted" && p.AcceptedCommit == r.AcceptedCommit {
				r.State = "accepted"
			} else if r.ReviewAction == "reject" && p.Status == "rejected" {
				r.State = "rejected"
			} else {
				return ErrEditConflict
			}
			c.releaseEditLocked(r)
			return c.saveLocked()
		}
		return nil
	}
	if r.ReviewAction == "accept" && p.Status == "accepted" && p.AcceptedCommit != "" {
		history, err := c.space.History()
		if err != nil {
			return err
		}
		matched := false
		for _, checkpoint := range history {
			if checkpoint.Commit == p.AcceptedCommit && checkpoint.Proposal == p.ID && checkpoint.Actor == r.Reviewer.Role {
				matched = true
			}
		}
		if !matched {
			return ErrEditConflict
		}
		r.State = "accepted"
		r.AcceptedCommit = p.AcceptedCommit
		// Native handles remain tombstoned across restart. Only read-back proving
		// settlement can permit a later rerun; local revocation is not native Cancel.
		for i := range c.data.Announcements {
			op := &c.data.Announcements[i]
			if slices.Contains(revoke, op.Snapshot.Operation) {
				op.Revoked = true
			}
		}
	} else if r.ReviewAction == "reject" && p.Status == "rejected" {
		r.State = "rejected"
	} else {
		return ErrEditConflict
	}
	r.ReviewApplied = true
	c.releaseEditLocked(r)
	if e := c.saveLocked(); e != nil {
		r.State = "uncertain"
		c.data.EditSlot = r.ID
		return e
	}
	return nil
}
func (c *Canvas) reconcileEditReviewLocked(r editRecord, a attempt, output string) (EditRun, error) {
	changes, e := c.editChanges(r, a, output)
	if e != nil {
		return r.EditRun, e
	}
	ps, e := c.space.Proposals()
	if e != nil {
		return r.EditRun, e
	}
	for _, p := range ps {
		if p.ID == r.Proposal && maps.Equal(changes, p.Changes) && c.editProposalMatches(r, a, p) {
			if p.Status == "pending" || p.Status == "stale" {
				history, err := c.space.History()
				if err != nil {
					return r.EditRun, err
				}
				for _, checkpoint := range history {
					if checkpoint.Proposal == p.ID {
						return r.EditRun, ErrEditConflict
					}
				}
				head, err := c.space.Head()
				if err != nil {
					return r.EditRun, err
				}
				c.mu.Lock()
				saved := c.editLocked(r.ID)
				if saved == nil || saved.ReviewAction != r.ReviewAction || saved.Reviewer != r.Reviewer || saved.ReviewApplied || c.data.EditSlot != r.ID {
					c.mu.Unlock()
					return r.EditRun, ErrEditConflict
				}
				// The local Git call has finished under publicationMu. Pending
				// proposal and complete history prove the intent did not commit.
				// Explicit reconciliation restores fresh review, never replays it.
				saved.LastUncommittedReview = &editReviewIntent{Action: saved.ReviewAction, Reviewer: saved.Reviewer, ObservedHead: head}
				saved.ReviewResetCount++
				saved.ReviewAction = ""
				saved.Reviewer = sharedspace.ReviewActor{}
				saved.State = "proposal"
				if p.Status == "stale" || head != saved.Request.BaseRevision {
					saved.State = "stale"
					c.releaseEditLocked(saved)
				}
				err = c.saveLocked()
				out := cloneEdit(saved.EditRun)
				c.mu.Unlock()
				return out, err
			}
			e = c.applyEditReviewLocked(r.ID, p)
			out, _ := c.GetEdit(context.Background(), r.ID)
			return out, e
		}
	}
	return r.EditRun, ErrEditConflict
}

// Only final queued or suppressed attempts qualify as nondispatch. Resume
// checks after its pending-intent save succeeds, so that full attempt snapshot
// is persisted before release; matching disk bytes alone do not stop workers.
func (c *Canvas) editUndispatchedLocked(a attempt) bool {
	if a.Status != "failed" && a.Status != "uncertain" || a.FlowStage != "queued" && a.FlowStage != "edit-suppressed" {
		return false
	}
	return c.editAttemptRecordedLocked(a)
}

// Completed ordinary attempts retain previously qualified settlement: every
// writer requires successful Exec, exact owned END, or proven nondispatch.
// This is rerun-only: Resume must still inspect and publish parked native output.
func (c *Canvas) editRerunSettledLocked(a attempt) bool {
	if a.FlowStage == "completed" && (a.Status == "failed" || a.Status == "proposed") {
		return c.editAttemptRecordedLocked(a)
	}
	return c.editUndispatchedLocked(a)
}

func (c *Canvas) editAttemptRecordedLocked(a attempt) bool {
	raw, err := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	var disk ledger
	if err != nil || json.Unmarshal(raw, &disk) != nil {
		return false
	}
	for _, saved := range disk.Attempts {
		if saved.ID == a.ID {
			return saved.Status == a.Status && saved.FlowStage == a.FlowStage && saved.FlowRun == a.FlowRun && saved.FlowGeneration == a.FlowGeneration && saved.ExecutionOwner == a.ExecutionOwner
		}
	}
	return false
}

func (c *Canvas) ResumeAfterEdit(ctx context.Context, q EditMaintenance) (out FlowRun, err error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	c.mu.Lock()
	r := c.editLocked(q.Edit)
	if r == nil || r.Request.FlowRun == "" || r.State != "rejected" && r.State != "failed" && r.State != "abandoned" && r.State != "stale" || c.editCommandLocked(r, q, "resume") != nil {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	f := c.flowRecordLocked(r.Request.FlowRun)
	if f == nil {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	// A completed exact command is historical readback, not authority to
	// release whatever pause, generation or cancellation exists now.
	if r.Commands["resume"] != "" && !r.ResumePending {
		out := cloneFlowRun(f.FlowRun)
		c.mu.Unlock()
		return out, nil
	}
	if c.data.EditSlot != "" || f.CancelRequested || f.Revocation != 0 || f.Generation != r.Generation || len(f.EditIDs) == 0 || f.EditIDs[len(f.EditIDs)-1] != r.ID {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	head, e := c.space.Head()
	if e != nil || head != q.ExpectedHead || head != r.Request.BaseRevision {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	currentAttempt := c.findLocked(f.Attempt)
	if currentAttempt == nil {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	a := *currentAttempt
	previous, fid := r.PreviousState, f.ID
	r.Commands["resume"] = commandDigest(q, rRequestActor(c, r))
	r.ResumePending = true
	f.State = "edit-paused"
	// Every continuation first persists intent with the effective pause held.
	// Any failed subsequent save/read/publication must keep exact retry live.
	defer func() {
		if err != nil {
			c.mu.Lock()
			c.editLocked(q.Edit).ResumePending = true
			c.flowLocked(fid).State = "edit-paused"
			// Rename may already have exposed the failed release. Republish
			// the conservative intent (including its nondispatch witness).
			// Even another fsync error must not erase the original error.
			_ = c.saveLocked()
			c.mu.Unlock()
		}
	}()
	e = c.saveLocked()
	undispatched := e == nil && c.editUndispatchedLocked(a)
	c.mu.Unlock()
	if e != nil {
		return FlowRun{}, e
	}
	if previous != "running" || undispatched {
		c.mu.Lock()
		f = c.flowRecordLocked(fid)
		f.State = previous
		if undispatched {
			f.State = "paused"
			current := c.findLocked(a.ID)
			current.Status = "failed"
			current.FlowStage = "completed"
		}
		c.editLocked(q.Edit).ResumePending = false
		e = c.saveLocked()
		if e != nil && undispatched {
			// Preserve the old proof so the exact release can retry durability.
			current := c.findLocked(a.ID)
			current.Status, current.FlowStage = a.Status, a.FlowStage
		}
		c.mu.Unlock()
		if e != nil {
			return FlowRun{}, e
		}
		return c.GetFlow(context.Background(), fid)
	}
	n, e := c.readFlowExecution(ctx, a)
	if e != nil || !n.Settled {
		return FlowRun{}, ErrEditConflict
	}
	c.mu.Lock()
	c.flowLocked(fid).State = "running"
	c.mu.Unlock()
	_, invalid := c.definition.ChangesForStep(f.Flow, a.FlowStep, n.Output)
	if !n.Complete || invalid != nil {
		err := c.update(a.ID, func(saved *attempt) {
			saved.Status = "failed"
			saved.FlowStage = "completed"
			saved.Error = "The settled parked output failed step validation."
			c.editLocked(q.Edit).ResumePending = false
		})
		if err != nil {
			return FlowRun{}, err
		}
		return c.GetFlow(context.Background(), fid)
	}
	service, sessions, release, e := c.serviceContext(ctx)
	if e != nil {
		return FlowRun{}, e
	}
	defer release()
	s, e := sessions.GetSession(service, a.Session)
	if e != nil {
		return FlowRun{}, e
	}
	proposals, e := c.space.Proposals()
	if e != nil {
		return FlowRun{}, e
	}
	changes, e := c.definition.ChangesForStep(f.Flow, a.FlowStep, n.Output)
	if e != nil {
		return FlowRun{}, e
	}
	for _, p := range proposals {
		if p.Attribution.Run == a.ID {
			if p.Status != "pending" || p.Base != a.Base || !maps.Equal(p.Changes, changes) || p.Attribution.FlowRun != fid || p.Attribution.FlowStep != a.FlowStep || p.Attribution.Session != a.Session || p.Attribution.BundleDigest != a.BundleDigest || p.Attribution.ExecutionOwner != a.ExecutionOwner || p.Attribution.RequestedBy != a.RequestedBy || !maps.Equal(p.Attribution.Sources, a.Sources) {
				return FlowRun{}, ErrEditConflict
			}
			if err := c.update(a.ID, func(saved *attempt) { saved.Proposal = p.ID; saved.Status = "proposed" }); err != nil {
				return FlowRun{}, err
			}
			return c.GetFlow(context.Background(), fid)
		}
	}
	_, e = c.publishOutputLocked(a, &client.TurnResult{Output: n.Output}, s)
	if e != nil {
		return FlowRun{}, e
	}
	return c.GetFlow(context.Background(), fid)
}
func (c *Canvas) RerunAfterEdit(ctx context.Context, q EditMaintenance) (FlowRun, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	c.mu.Lock()
	saved := c.editLocked(q.Edit)
	if saved == nil || saved.Request.FlowRun == "" || saved.State != "accepted" || c.editCommandLocked(saved, q, "rerun") != nil {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	f := c.flowRecordLocked(saved.Request.FlowRun)
	if f == nil {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	continuing := saved.RerunPending
	if old, ok := saved.Commands["rerun"]; ok {
		if old != commandDigest(q, rRequestActor(c, saved)) {
			c.mu.Unlock()
			return FlowRun{}, ErrEditConflict
		}
		if !continuing {
			out := cloneFlowRun(f.FlowRun)
			c.mu.Unlock()
			return out, nil
		}
	}
	if c.data.EditSlot != "" || f.CancelRequested || f.Revocation != 0 || (!continuing && f.State != "edited") || f.Generation != saved.Generation || f.Attempts >= f.AgentRuns || len(f.EditIDs) == 0 || f.EditIDs[len(f.EditIDs)-1] != saved.ID {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	r := cloneEdit(*saved)
	ops := cloneEdit(c.data.Announcements)
	old := c.findLocked(f.Attempt)
	if old == nil {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	a := *old
	if !c.flowGenerationCurrentLocked(&f.FlowRun, &a) || a.ExecutionOwner != f.ExecutionOwner || a.RequestedBy != f.RequestedBy || !c.ownedLocked(a.ExecutionOwner) {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	settled := c.editRerunSettledLocked(a)
	c.mu.Unlock()
	// No checkpoint, budget or Git mutation may precede exact old-attempt
	// settlement. A session ID or a locally stopped worker proves no END.
	if !settled {
		n, err := c.readFlowExecution(ctx, a)
		if err != nil || !n.Settled {
			return FlowRun{}, ErrEditConflict
		}
	}
	head, e := c.space.Head()
	if e != nil || head != q.ExpectedHead || head != r.AcceptedCommit {
		return FlowRun{}, ErrEditConflict
	}
	for _, op := range ops {
		if op.FlowRun == r.Request.FlowRun {
			view, e := c.inspectAnnouncement(ctx, op)
			if e != nil || !view.Concluded || view.Receipt != nil {
				return FlowRun{}, ErrFlowCancelPending
			}
		}
	}
	c.mu.Lock()
	saved = c.editLocked(r.ID)
	if e := c.editCommandLocked(saved, q, "rerun"); e != nil {
		c.mu.Unlock()
		return FlowRun{}, e
	}
	saved.Commands["rerun"] = commandDigest(q, rRequestActor(c, saved))
	f = c.flowRecordLocked(r.Request.FlowRun)
	saved.RerunPending = true
	// Exact END (or final nondispatch) also retires the old admission
	// journal. Never publish its output or leave uncertainty fencing a later
	// flow after this explicit restart has consumed that settlement proof.
	old = c.findLocked(a.ID)
	if old.Status != "proposed" {
		old.Status = "failed"
		if old.Error == "" {
			old.Error = "Old ordinary attempt settled before explicit edit rerun; no old output published."
		}
	}
	old.FlowStage = "completed"
	if !continuing {
		f.EditCheckpoints = append(f.EditCheckpoints, editCheckpoint{Edit: r.ID, BeforeGeneration: r.Generation, AcceptedCommit: r.AcceptedCommit, RestartGeneration: f.Generation + 1})
	}
	f.Position = 0
	f.Accepted = map[string]string{}
	f.Proposal = ""
	f.Action = ""
	f.State = "ready"
	f.ReadyHead = head
	if !continuing {
		f.Feedback = append(f.Feedback, FlowFeedback{Proposal: r.Proposal, Text: "Scoped edit " + r.ID + " accepted; rerun from the first step using current accepted bytes.", Actor: r.Reviewer})
	}
	e = c.saveLocked()
	fid := f.ID
	c.mu.Unlock()
	if e != nil {
		return FlowRun{}, e
	}
	e = c.dispatchFlowLocked(ctx, fid, r.Reviewer, r.Proposal)
	out, _ := c.GetFlow(ctx, fid)
	return out, e
}
