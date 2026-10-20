package canvas

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/agent-primitives/canvas/internal/sharedspace"
)

// flowReviewIntent records observation intent, never authority to repeat a decision.
// One decision per counted attempt bounds this history by the run's attempt budget.
type flowReviewIntent struct {
	Generation     uint64                  `json:"generation,omitempty"`
	Definition     string                  `json:"definition"`
	Flow           string                  `json:"flow"`
	Step           string                  `json:"step"`
	Attempt        string                  `json:"attempt"`
	Proposal       string                  `json:"proposal"`
	Base           string                  `json:"base"`
	Action         string                  `json:"action"`
	Reviewer       sharedspace.ReviewActor `json:"reviewer"`
	RequestKey     string                  `json:"requestKey,omitempty"`
	Digest         string                  `json:"digest,omitempty"`
	Feedback       string                  `json:"feedback,omitempty"`
	Applied        bool                    `json:"applied"`
	AcceptedCommit string                  `json:"acceptedCommit,omitempty"`
}

// FlowReviewEvidence describes a Git review, not native execution verification or
// permission to advance. LocalRole is unverified attribution, never Reviewer.
type FlowReviewEvidence struct {
	Run            string                  `json:"run"`
	Step           string                  `json:"step,omitempty"`
	Attempt        string                  `json:"attempt,omitempty"`
	Proposal       string                  `json:"proposal,omitempty"`
	Action         string                  `json:"action,omitempty"`
	Reviewer       sharedspace.ReviewActor `json:"reviewer,omitzero"`
	LocalRole      string                  `json:"localRole,omitempty"`
	State          string                  `json:"state"`
	AcceptedCommit string                  `json:"acceptedCommit,omitempty"`
	CurrentHead    string                  `json:"currentHead,omitempty"`
	Reason         string                  `json:"reason,omitempty"`
	Applied        bool                    `json:"applied"`
}

func (c *Canvas) flowRecordLocked(id string) *flowRunRecord {
	for i := range c.data.Flows {
		if c.data.Flows[i].ID == id {
			return &c.data.Flows[i]
		}
	}
	return nil
}
func (c *Canvas) markReviewAppliedLocked(id string, p sharedspace.Proposal) {
	r := c.flowRecordLocked(id)
	for i := len(r.Reviews) - 1; i >= 0; i-- {
		if r.Reviews[i].Proposal == p.ID {
			r.Reviews[i].Applied = true
			r.Reviews[i].AcceptedCommit = p.AcceptedCommit
			return
		}
	}
}

// InspectFlowReview reads proposals before history/head because the store may
// reconcile its private review ledger. It never saves Canvas state, moves the
// accepted ref, invokes native execution, or reconstructs a human credential.
func (c *Canvas) InspectFlowReview(ctx context.Context, run string) (FlowReviewEvidence, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	return c.inspectFlowReviewLocked(ctx, run, -1)
}
func (c *Canvas) inspectFlowReviewLocked(ctx context.Context, run string, index int) (FlowReviewEvidence, error) {
	if err := ctx.Err(); err != nil {
		return FlowReviewEvidence{}, err
	}
	c.mu.Lock()
	r := c.flowRecordLocked(run)
	if r == nil {
		c.mu.Unlock()
		return FlowReviewEvidence{}, errNotFound
	}
	f := cloneFlowRun(r.FlowRun)
	v := FlowReviewEvidence{Run: run, State: "unavailable", Reason: "No typed review intent was recorded."}
	if len(r.Reviews) == 0 {
		c.mu.Unlock()
		return v, nil
	}
	if index < 0 {
		index = len(r.Reviews) - 1
	}
	if index >= len(r.Reviews) {
		c.mu.Unlock()
		return v, ErrFlowConflict
	}
	intent := r.Reviews[index]
	saved := c.findLocked(intent.Attempt)
	var a attempt
	if saved != nil {
		a = *saved
	}
	c.mu.Unlock()
	v.Step, v.Attempt, v.Proposal, v.Action, v.Applied = intent.Step, intent.Attempt, intent.Proposal, intent.Action, intent.Applied
	v.State, v.Reason = "uncertain", "Review bindings or durable decision evidence do not match."
	ps, err := c.space.Proposals()
	if err != nil {
		return v, err
	}
	history, err := c.space.History()
	if err != nil {
		return v, err
	}
	v.CurrentHead, err = c.space.Head()
	if err != nil {
		return v, err
	}
	if err = ctx.Err(); err != nil {
		return v, err
	}
	if saved == nil || intent.Generation == 0 || intent.Generation != a.FlowGeneration || f.Definition != c.definition.Digest || intent.Definition != f.Definition || intent.Flow != f.Flow || a.FlowRun != f.ID || a.FlowStep != intent.Step || a.Base != intent.Base || a.Proposal != intent.Proposal || a.Session == "" || a.BundleDigest == "" || a.ExecutionOwner != f.ExecutionOwner {
		return v, nil
	}
	var p *sharedspace.Proposal
	for i := range ps {
		if ps[i].ID == intent.Proposal {
			p = &ps[i]
			break
		}
	}
	if p == nil || p.Base != intent.Base || p.Attribution.Run != a.ID || p.Attribution.FlowRun != f.ID || p.Attribution.FlowStep != intent.Step || p.Attribution.Agent != a.Agent || p.Attribution.BundleDigest != a.BundleDigest || p.Attribution.Session != a.Session || p.Attribution.ExecutionOwner != a.ExecutionOwner || p.Attribution.RequestedBy != a.RequestedBy || !maps.Equal(p.Attribution.Sources, a.Sources) {
		return v, nil
	}
	var role string
	for _, s := range c.definition.flows[f.Flow].definition.Steps {
		if s.ID == intent.Step && s.Agent == a.Agent && s.Gate != nil {
			role = s.Gate.Role
		}
	}
	if role == "" || intent.Reviewer.Role != role || intent.Action != "accept" && intent.Action != "reject" {
		return v, nil
	}
	// Reapply the compiler's whole-proposal scope, schema and required-output rules.
	type change struct {
		Document  string `json:"document"`
		Operation string `json:"operation"`
		Content   string `json:"content"`
	}
	var changes []change
	for path, text := range p.Changes {
		found := false
		for name, doc := range c.definition.documents {
			if doc.path == path {
				changes = append(changes, change{name, "replace", text})
				found = true
				break
			}
		}
		if !found {
			return v, nil
		}
	}
	raw, _ := json.Marshal(struct {
		Changes []change `json:"changes"`
	}{changes})
	if _, e := c.definition.ChangesForStep(f.Flow, intent.Step, string(raw)); e != nil {
		return v, nil
	}
	verified := intent.Reviewer.Issuer != "" && intent.Reviewer.Subject != ""
	reviewerMatches := verified && p.Reviewer == intent.Reviewer
	switch p.Status {
	case "pending":
		if p.Reviewer == (sharedspace.ReviewActor{}) && v.CurrentHead == intent.Base {
			v.State, v.Reason = "pending", "Recorded intent has no committed review."
		}
	case "rejected":
		if intent.Action != "reject" {
			break
		}
		if reviewerMatches {
			v.State, v.Reason, v.Reviewer = "rejected", "", p.Reviewer
		} else if !verified {
			v.Reason = "Local rejection has no publicly attributable reviewer."
		}
	case "accepted":
		if intent.Action != "accept" || p.AcceptedCommit == "" {
			break
		}
		for _, h := range history {
			if h.Proposal != p.ID || h.Commit != p.AcceptedCommit {
				continue
			}
			if reviewerMatches && h.Reviewer == intent.Reviewer {
				v.State, v.Reason, v.Reviewer, v.AcceptedCommit = "accepted", "", p.Reviewer, h.Commit
			} else if !verified && intent.Reviewer.Subject == "" && intent.Reviewer.Issuer == "" && p.Reviewer == (sharedspace.ReviewActor{}) && h.Reviewer == (sharedspace.ReviewActor{}) && h.Actor == role {
				v.State, v.Reason, v.LocalRole, v.AcceptedCommit = "accepted", "Unverified local role attribution.", role, h.Commit
			}
		}
	}
	return v, nil
}
