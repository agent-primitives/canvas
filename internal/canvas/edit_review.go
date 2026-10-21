package canvas

import (
	"context"
	"maps"

	"github.com/agent-primitives/canvas/internal/sharedspace"
)

// publicationMu is held by every caller. Edit proposals never fall through to
// generic review, even after release or abandonment.
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
