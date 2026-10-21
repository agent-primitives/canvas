package canvas

import (
	"context"
	"maps"
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
func (c *Canvas) ResumeAfterEdit(ctx context.Context, q EditMaintenance) (FlowRun, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	c.mu.Lock()
	r := c.editLocked(q.Edit)
	if r == nil || r.Request.FlowRun == "" || r.State != "rejected" && r.State != "failed" && r.State != "abandoned" && r.State != "stale" {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	f := c.flowRecordLocked(r.Request.FlowRun)
	if f == nil || c.data.EditSlot != "" || f.CancelRequested || f.Revocation != 0 || f.Generation != r.Generation {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	if e := c.editCommandLocked(r, q, "resume"); e != nil {
		c.mu.Unlock()
		return FlowRun{}, e
	}
	head, e := c.space.Head()
	if e != nil || head != q.ExpectedHead || head != r.Request.BaseRevision {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	if f.State != "edit-paused" && !r.ResumePending {
		out := cloneFlowRun(f.FlowRun)
		c.mu.Unlock()
		return out, nil
	}
	currentAttempt := c.findLocked(f.Attempt)
	if currentAttempt == nil {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	r.Commands["resume"] = commandDigest(q, rRequestActor(c, r))
	a := *currentAttempt
	previous := r.PreviousState
	r.ResumePending = previous == "running" && a.Session != ""
	f.State = previous
	if previous == "running" && a.Session == "" {
		f.State = "paused"
	}
	e = c.saveLocked()
	fid := f.ID
	c.mu.Unlock()
	if e != nil {
		return FlowRun{}, e
	}
	if previous == "running" && a.Session != "" {
		n, e := c.readFlowExecution(ctx, a)
		if e != nil || !n.Settled {
			return FlowRun{}, ErrEditConflict
		}
		_, invalid := c.definition.ChangesForStep(f.Flow, a.FlowStep, n.Output)
		if !n.Complete || invalid != nil {
			err := c.update(a.ID, func(saved *attempt) {
				saved.Status = "failed"
				saved.FlowStage = "completed"
				saved.Error = "The settled parked output failed step validation."
				if edit := c.editLocked(q.Edit); edit != nil {
					edit.ResumePending = false
				}
			})
			if err != nil {
				return FlowRun{}, err
			}
			return c.GetFlow(ctx, fid)
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
		proposals, err := c.space.Proposals()
		if err != nil {
			return FlowRun{}, err
		}
		changes, err := c.definition.ChangesForStep(f.Flow, a.FlowStep, n.Output)
		if err != nil {
			return FlowRun{}, err
		}
		for _, p := range proposals {
			if p.Attribution.Run == a.ID {
				if p.Status != "pending" || p.Base != a.Base || !maps.Equal(p.Changes, changes) || p.Attribution.FlowRun != fid || p.Attribution.FlowStep != a.FlowStep || p.Attribution.Session != a.Session || p.Attribution.BundleDigest != a.BundleDigest || p.Attribution.ExecutionOwner != a.ExecutionOwner || p.Attribution.RequestedBy != a.RequestedBy || !maps.Equal(p.Attribution.Sources, a.Sources) {
					return FlowRun{}, ErrEditConflict
				}
				if err := c.update(a.ID, func(saved *attempt) { saved.Proposal = p.ID; saved.Status = "proposed" }); err != nil {
					return FlowRun{}, err
				}
				return c.GetFlow(ctx, fid)
			}
		}
		_, e = c.publishOutputLocked(a, &client.TurnResult{Output: n.Output}, s)
		if e != nil {
			return FlowRun{}, e
		}
	}
	return c.GetFlow(ctx, fid)
}
func (c *Canvas) RerunAfterEdit(ctx context.Context, q EditMaintenance) (FlowRun, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	c.mu.Lock()
	saved := c.editLocked(q.Edit)
	if saved == nil || saved.Request.FlowRun == "" || saved.State != "accepted" || !q.Confirmed || q.Role != saved.Request.Role {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	f := c.flowRecordLocked(saved.Request.FlowRun)
	if f == nil || c.data.EditSlot != "" || f.CancelRequested || f.Revocation != 0 {
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
	if (!continuing && f.State != "edited") || f.Generation != saved.Generation || f.Attempts >= f.AgentRuns {
		c.mu.Unlock()
		return FlowRun{}, ErrEditConflict
	}
	r := cloneEdit(*saved)
	ops := cloneEdit(c.data.Announcements)
	c.mu.Unlock()
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
