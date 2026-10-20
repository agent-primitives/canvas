package canvas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/client"
)

var ErrFlowConflict = errors.New("workflow state, binding or request conflicts; inspect before acting")
var ErrFlowBusy = errors.New("a workflow owns this workspace; competing work is refused")

type FlowRuntimeOptions struct {
	MaxAgentRuns    int
	OwnerAuthorizer ReviewAuthorizer
}
type FlowStart struct {
	Flow, RequestKey, Role string
	Inputs                 json.RawMessage
	AgentRuns              int
}
type FlowRetry struct{ Run, Attempt, RequestKey, Role string }
type FlowChanges struct{ Run, Proposal, RequestKey, Role, Feedback string }
type FlowCancel struct{ Run, Role string }
type FlowFeedback struct {
	Proposal, Text string
	Actor          sharedspace.ReviewActor
}
type flowCommand struct {
	Digest string `json:"digest"`
}

// Keep exact input bytes outside RawMessage: indented ledger encoding rewrites
// RawMessage whitespace, but request-key equality and frozen inputs are byte-bound.
type flowRunRecord struct {
	FlowRun
	InputBytes []byte             `json:"inputBytes,omitempty"`
	Reviews    []flowReviewIntent `json:"reviews,omitempty"`
}

// FlowRun is a detached snapshot, never mutable execution authority.
type FlowRun struct {
	ID             string                  `json:"id"`
	Flow           string                  `json:"flow"`
	RequestKey     string                  `json:"requestKey"`
	Inputs         json.RawMessage         `json:"inputs"`
	Definition     string                  `json:"definition"`
	StartBase      string                  `json:"startBase"`
	ExecutionOwner api.IdentityRef         `json:"executionOwner"`
	RequestedBy    sharedspace.ReviewActor `json:"requestedBy,omitzero"`
	Role           string                  `json:"role"`
	AgentRuns      int                     `json:"agentRuns"`
	Attempts       int                     `json:"attempts"`
	Position       int                     `json:"position"`
	Step           string                  `json:"step"`
	Attempt        string                  `json:"attempt,omitempty"`
	Proposal       string                  `json:"proposal,omitempty"`
	State          string                  `json:"state"`
	Reason         string                  `json:"reason,omitempty"`
	Accepted       map[string]string       `json:"accepted"`
	Action         string                  `json:"action,omitempty"`
	Feedback       []FlowFeedback          `json:"feedback,omitempty"`
	Commands       map[string]flowCommand  `json:"commands,omitempty"`
}

func cloneFlowRun(f FlowRun) FlowRun {
	f.Inputs = bytes.Clone(f.Inputs)
	f.Accepted = maps.Clone(f.Accepted)
	f.Commands = maps.Clone(f.Commands)
	f.Feedback = slices.Clone(f.Feedback)
	return f
}
func terminalFlow(s string) bool {
	return s == "complete" || s == "rejected" || s == "cancelled" || s == "denied" || s == "exhausted"
}
func (c *Canvas) flowLocked(id string) *FlowRun {
	for i := range c.data.Flows {
		if c.data.Flows[i].ID == id {
			return &c.data.Flows[i].FlowRun
		}
	}
	return nil
}
func (c *Canvas) activeFlowLocked() bool {
	for _, f := range c.data.Flows {
		if !terminalFlow(f.State) {
			return true
		}
	}
	return false
}

// flowAdmissionBusyLocked shares the reservation mutex with all native work.
func (c *Canvas) flowAdmissionBusyLocked() bool {
	if c.activeFlowLocked() || c.ctx.Err() != nil {
		return true
	}
	for _, a := range c.data.Attempts {
		if a.Status == "queued" || a.Status == "running" || a.Status == "uncertain" {
			return true
		}
	}
	for _, r := range c.data.Recoveries {
		if r.Status == "pending" || r.Status == "uncertain" {
			return true
		}
	}
	for _, q := range c.conversation.Entries {
		if q.Status == "queued" || q.Status == "running" || q.Status == "uncertain" {
			return true
		}
	}
	return false
}
func (c *Canvas) competingFlow() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeFlowLocked() {
		return ErrFlowBusy
	}
	return nil
}
func (c *Canvas) flowStep(f *FlowRun) (FlowStep, bool) {
	d, ok := c.definition.flows[f.Flow]
	if !ok || f.Position < 0 || f.Position >= len(d.definition.Steps) {
		return FlowStep{}, false
	}
	return d.definition.Steps[f.Position], true
}
func (c *Canvas) flowRole(role string) bool {
	if validRole(role) {
		return true
	}
	if c.flowOptions == nil {
		return false
	}
	for _, f := range c.definition.flows {
		for _, s := range f.definition.Steps {
			if s.Gate != nil && s.Gate.Role == role {
				return true
			}
		}
	}
	return false
}

// OpenFlowRuntime explicitly opts a trusted host into workflows. Ordinary
// command constructors retain their manual-runtime guard.
func OpenFlowRuntime(root string, space sharedspace.SharedSpace, cl *client.Client, address string, d *CompiledDefinition, binder contract.ContractBinder, options FlowRuntimeOptions, archives ...*contract.ArchiveResolver) (*Canvas, error) {
	if d == nil || len(d.flows) == 0 || options.MaxAgentRuns < 1 {
		return nil, ErrFlowConflict
	}
	c, err := openDefined(root, space, cl, address, d, binder, archives...)
	if err != nil {
		return nil, err
	}
	c.flowOptions = &options
	return c, nil
}
func (c *Canvas) GetFlow(ctx context.Context, id string) (FlowRun, error) {
	if err := ctx.Err(); err != nil {
		return FlowRun{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.flowLocked(id)
	if f == nil {
		return FlowRun{}, errNotFound
	}
	return cloneFlowRun(*f), nil
}
func flowKey(s string) bool {
	return len(s) > 0 && len(s) <= 128 && safeText(s, 128) && !strings.ContainsAny(s, "\n\r\t")
}
func commandDigest(v any, actor sharedspace.ReviewActor) string {
	b, _ := json.Marshal(struct {
		Request any
		Actor   sharedspace.ReviewActor
	}{v, actor})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func (c *Canvas) StartFlow(ctx context.Context, r FlowStart) (FlowRun, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	if c.flowOptions == nil || !flowKey(r.RequestKey) || !validRole(r.Role) {
		return FlowRun{}, ErrFlowConflict
	}
	_, who, err := c.requester(ctx, r.Role)
	if err != nil {
		return FlowRun{}, err
	}
	compiled, ok := c.definition.flows[r.Flow]
	if !ok || r.AgentRuns < 1 || r.AgentRuns > compiled.definition.Limits.AgentRuns || r.AgentRuns > c.flowOptions.MaxAgentRuns {
		return FlowRun{}, ErrFlowConflict
	}
	if err := c.definition.ValidateFlowInputs(r.Flow, r.Inputs); err != nil {
		return FlowRun{}, err
	}
	if !safeText(string(r.Inputs), 65536) {
		return FlowRun{}, ErrFlowConflict
	}
	c.mu.Lock()
	for _, f := range c.data.Flows {
		if f.RequestKey == r.RequestKey {
			same := f.Flow == r.Flow && bytes.Equal(f.Inputs, r.Inputs) && f.AgentRuns == r.AgentRuns && f.RequestedBy == who && f.Role == r.Role
			c.mu.Unlock()
			if !same {
				return FlowRun{}, ErrFlowConflict
			}
			return cloneFlowRun(f.FlowRun), nil
		}
	}
	if c.flowAdmissionBusyLocked() {
		c.mu.Unlock()
		return FlowRun{}, ErrFlowBusy
	}
	owner, err := c.executionOwnerLocked()
	c.mu.Unlock()
	if err != nil {
		return FlowRun{}, err
	}
	base, err := c.space.Head()
	if err != nil {
		return FlowRun{}, err
	}
	// Existing parked manual actions cannot coexist with a new flow.
	for _, op := range c.data.Announcements {
		if op.Stage == "abandoned" {
			continue
		}
		v, e := c.inspectAnnouncement(ctx, op)
		if e != nil || !v.Concluded {
			return FlowRun{}, ErrFlowBusy
		}
	}
	f := FlowRun{ID: ID(), Flow: r.Flow, RequestKey: r.RequestKey, Inputs: bytes.Clone(r.Inputs), Definition: c.definition.Digest, StartBase: base, ExecutionOwner: owner, RequestedBy: who, Role: r.Role, AgentRuns: r.AgentRuns, State: "starting", Accepted: map[string]string{}, Commands: map[string]flowCommand{}}
	c.mu.Lock()
	// Native question/recovery claims also reserve under mu. Recheck at the
	// insertion boundary, not only before reading external workspace state.
	if c.flowAdmissionBusyLocked() {
		c.mu.Unlock()
		return FlowRun{}, ErrFlowBusy
	}
	c.data.Flows = append(c.data.Flows, flowRunRecord{FlowRun: f, InputBytes: bytes.Clone(r.Inputs)})
	c.mu.Unlock()
	err = c.dispatchFlowLocked(ctx, f.ID, sharedspace.ReviewActor{}, "")
	result, _ := c.GetFlow(context.Background(), f.ID)
	return result, err
}

// dispatchFlowLocked is publicationMu-owned. It reserves the attempt and budget
// in the same ledger write before starting the existing native worker.
func (c *Canvas) dispatchFlowLocked(ctx context.Context, id string, trigger sharedspace.ReviewActor, review string) error {
	c.mu.Lock()
	f := cloneFlowRun(*c.flowLocked(id))
	s, ok := c.flowStep(&f)
	c.mu.Unlock()
	if !ok {
		return ErrFlowConflict
	}
	if s.Action != "" {
		return c.parkFlowActionLocked(ctx, id, s.Action, trigger)
	}
	if f.Attempts >= f.AgentRuns {
		return c.setFlow(id, "exhausted", "The selected attempt budget is exhausted.")
	}
	request := struct {
		Inputs   json.RawMessage `json:"inputs"`
		Required []string        `json:"requiredOutputs"`
		Feedback []FlowFeedback  `json:"feedback,omitempty"`
	}{f.Inputs, s.Gate.Accept, f.Feedback}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	a, err := c.snapshotRequest(s.Agent, string(raw), f.Role, 128<<10)
	if err != nil {
		_ = c.setFlow(id, "paused", err.Error())
		return err
	}
	a.RequestSummary = "Workflow " + f.Flow + " / " + s.ID
	a.FlowRun, a.FlowStep = id, s.ID
	a.ExecutionOwner = f.ExecutionOwner
	a.RequestedBy = f.RequestedBy
	a.Reviewer = sessionauth.Identity{Issuer: f.RequestedBy.Issuer, Subject: f.RequestedBy.Subject}
	a.TriggeredBy, a.TriggeringReview = trigger, review
	a.FlowStage = "queued"
	c.mu.Lock()
	live := c.flowLocked(id)
	if live == nil || terminalFlow(live.State) || live.State == "uncertain" {
		c.mu.Unlock()
		return ErrFlowConflict
	}
	owner, err := c.executionOwnerLocked()
	if err != nil || owner != f.ExecutionOwner {
		c.mu.Unlock()
		return ErrFlowConflict
	}
	live.Attempts++
	live.Step = s.ID
	live.Attempt = a.ID
	live.Proposal = ""
	live.State = "running"
	live.Reason = ""
	if c.service != nil {
		c.data.ExecutionOwner = owner
	}
	a.Order = uint64(len(c.data.Attempts) + 1)
	c.data.Attempts = append(c.data.Attempts, a)
	if err = c.saveLocked(); err != nil {
		c.persistenceFailureLocked(c.findLocked(a.ID))
		live = c.flowLocked(id)
		live.State = "uncertain"
		live.Reason = "Attempt intent could not be persisted; no dispatch."
		c.mu.Unlock()
		return err
	}
	c.wg.Add(1)
	c.mu.Unlock()
	go c.execute(a.ID)
	return nil
}
func (c *Canvas) setFlow(id, state, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.flowLocked(id)
	if f == nil {
		return ErrFlowConflict
	}
	f.State, f.Reason = state, reason
	if err := c.saveLocked(); err != nil {
		f.State = "uncertain"
		f.Reason = "Workflow bookkeeping could not be persisted."
		c.persistenceFailureLocked(nil)
		return err
	}
	return nil
}

// Called by update with mu held, so publication and flow binding share a save.
func (c *Canvas) observeFlowAttemptLocked(a *attempt) {
	if a.FlowRun == "" {
		return
	}
	f := c.flowLocked(a.FlowRun)
	if f == nil || f.Attempt != a.ID || f.State != "running" {
		return
	}
	if a.Status == "proposed" && a.Proposal != "" {
		f.State = "review"
		f.Proposal = a.Proposal
	} else if a.Status == "failed" || a.Status == "uncertain" {
		f.State = "uncertain"
		if a.FlowStage == "completed" || a.FlowStage == "queued" {
			f.State = "paused"
		}
		f.Reason = a.Error
	}
}
func (c *Canvas) flowPublishAllowed(a attempt) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.flowLocked(a.FlowRun)
	if f == nil || f.State != "running" || f.Attempt != a.ID || f.Step != a.FlowStep {
		return false
	}
	s, ok := c.flowStep(f)
	return ok && s.Agent == a.Agent
}
func (c *Canvas) flowReviewGuard(id, role string) (string, error) {
	ps, err := c.space.Proposals()
	if err != nil {
		return "", err
	}
	var p *sharedspace.Proposal
	for i := range ps {
		if ps[i].ID == id {
			p = &ps[i]
			break
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var a *attempt
	for i := range c.data.Attempts {
		if c.data.Attempts[i].Proposal == id {
			a = &c.data.Attempts[i]
			break
		}
	}
	if a == nil || a.FlowRun == "" {
		if c.activeFlowLocked() || p != nil && p.Attribution.FlowRun != "" {
			return "", ErrFlowBusy
		}
		return "", nil
	}
	f := c.flowLocked(a.FlowRun)
	if f == nil || f.State != "review" || f.Attempt != a.ID || f.Proposal != id || p == nil || p.Status != "pending" {
		return "", ErrFlowConflict
	}
	s, ok := c.flowStep(f)
	if !ok || s.Gate == nil || s.ID != a.FlowStep || s.Agent != a.Agent || s.Gate.Role != role {
		return "", ErrFlowConflict
	}
	head, err := c.space.Head()
	if err != nil || head != a.Base || p.Base != a.Base {
		return "", ErrFlowConflict
	}
	return f.ID, nil
}
func (c *Canvas) beginFlowDecision(id, proposal, action string, who sharedspace.ReviewActor, key, digest, feedback string) error {
	if id == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.flowLocked(id)
	if f == nil || f.State != "review" {
		return ErrFlowConflict
	}
	f.State = "decision"
	f.Reason = action + " intent pending for " + proposal
	c.flowRecordLocked(id).Reviews = append(c.flowRecordLocked(id).Reviews, flowReviewIntent{Definition: f.Definition, Flow: f.Flow, Step: f.Step, Attempt: f.Attempt, Proposal: proposal, Base: c.findLocked(f.Attempt).Base, Action: action, Reviewer: who, RequestKey: key, Digest: digest, Feedback: feedback})
	if key != "" {
		f.Commands[key] = flowCommand{Digest: digest}
	}
	if feedback != "" {
		f.Feedback = append(f.Feedback, FlowFeedback{proposal, feedback, who})
	}
	if err := c.saveLocked(); err != nil {
		f.State = "uncertain"
		return err
	}
	return nil
}
func (c *Canvas) reviewFlowLocked(ctx context.Context, id, action, role string, change *FlowChanges) (sharedspace.Proposal, error) {
	fid, err := c.flowReviewGuard(id, role)
	if err != nil {
		return sharedspace.Proposal{}, err
	}
	_, who, err := c.requester(ctx, role)
	if err != nil {
		return sharedspace.Proposal{}, err
	}
	if who.Role == "" {
		who.Role = role
	}
	key, digest, feedback := "", "", ""
	if change != nil {
		key = change.RequestKey
		digest = commandDigest(*change, who)
		feedback = change.Feedback
	}
	p, err := c.reviewProposalLocked(ctx, id, action, role, func() error { return c.beginFlowDecision(fid, id, action, who, key, digest, feedback) })
	if fid == "" {
		return p, err
	}
	if err != nil {
		c.mu.Lock()
		f := c.flowLocked(fid)
		pending := f != nil && f.State == "decision"
		c.mu.Unlock()
		if pending {
			_ = c.setFlow(fid, "uncertain", "Review may have committed; no automatic dispatch.")
		}
		return p, err
	}
	c.mu.Lock()
	f := c.flowLocked(fid)
	c.markReviewAppliedLocked(fid, p)
	if action == "reject" && change == nil {
		f.State = "rejected"
		f.Reason = "Human rejected the current proposal."
		err = c.saveLocked()
		if err != nil {
			f.State = "uncertain"
		}
		c.mu.Unlock()
		return p, err
	}
	if action == "accept" {
		for name, doc := range c.definition.documents {
			if _, ok := p.Changes[doc.path]; ok {
				f.Accepted[name] = p.ID
			}
		}
		f.Position++
	}
	f.State = "starting"
	f.Proposal = ""
	if err = c.saveLocked(); err != nil {
		f.State = "uncertain"
		f.Reason = "Review committed; next attempt not reserved."
	}
	pos := f.Position
	steps := len(c.definition.flows[f.Flow].definition.Steps)
	c.mu.Unlock()
	if err != nil {
		return p, err
	}
	if pos == steps {
		return p, c.setFlow(fid, "complete", "")
	}
	trigger := p.Reviewer
	if trigger.Role == "" {
		trigger.Role = role
	}
	err = c.dispatchFlowLocked(ctx, fid, trigger, p.ID)
	return p, err
}
func (c *Canvas) RequestFlowChanges(ctx context.Context, r FlowChanges) (FlowRun, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	if !flowKey(r.RequestKey) || strings.TrimSpace(r.Feedback) == "" || !safeText(r.Feedback, 2000) {
		return FlowRun{}, ErrFlowConflict
	}
	_, who, err := c.requester(ctx, r.Role)
	if err != nil {
		return FlowRun{}, err
	}
	if who.Role == "" {
		who.Role = r.Role
	}
	c.mu.Lock()
	f := c.flowLocked(r.Run)
	if f == nil {
		c.mu.Unlock()
		return FlowRun{}, errNotFound
	}
	digest := commandDigest(r, who)
	old, exists := f.Commands[r.RequestKey]
	same := f.Proposal == r.Proposal
	c.mu.Unlock()
	if exists {
		if old.Digest != digest {
			return FlowRun{}, ErrFlowConflict
		}
		return c.GetFlow(ctx, r.Run)
	}
	if !same {
		return FlowRun{}, ErrFlowConflict
	}
	_, err = c.reviewFlowLocked(ctx, r.Proposal, "reject", r.Role, &r)
	out, _ := c.GetFlow(context.Background(), r.Run)
	return out, err
}
func (c *Canvas) RetryFlow(ctx context.Context, r FlowRetry) (FlowRun, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	_, who, err := c.requester(ctx, r.Role)
	if err != nil {
		return FlowRun{}, err
	}
	if who.Role == "" {
		who.Role = r.Role
	}
	if !flowKey(r.RequestKey) {
		return FlowRun{}, ErrFlowConflict
	}
	c.mu.Lock()
	f := c.flowLocked(r.Run)
	if f == nil {
		c.mu.Unlock()
		return FlowRun{}, errNotFound
	}
	digest := commandDigest(r, who)
	if old, ok := f.Commands[r.RequestKey]; ok {
		out := cloneFlowRun(*f)
		c.mu.Unlock()
		if old.Digest != digest {
			return FlowRun{}, ErrFlowConflict
		}
		return out, nil
	}
	a := c.findLocked(f.Attempt)
	if f.State != "paused" || f.Attempt != r.Attempt || a == nil || a.FlowStage != "completed" || !c.flowRequester(*f, who, r.Role) {
		c.mu.Unlock()
		return FlowRun{}, ErrFlowConflict
	}
	f.Commands[r.RequestKey] = flowCommand{Digest: digest}
	f.State = "starting"
	if err = c.saveLocked(); err != nil {
		f.State = "uncertain"
		c.mu.Unlock()
		return FlowRun{}, err
	}
	c.mu.Unlock()
	err = c.dispatchFlowLocked(ctx, r.Run, who, "")
	out, _ := c.GetFlow(context.Background(), r.Run)
	return out, err
}
func (c *Canvas) flowRequester(f FlowRun, who sharedspace.ReviewActor, role string) bool {
	if c.authenticated {
		return who.Issuer != "" && who.Issuer == f.RequestedBy.Issuer && who.Subject == f.RequestedBy.Subject
	}
	return role == f.Role
}
func (c *Canvas) CancelFlow(ctx context.Context, r FlowCancel) (FlowRun, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	_, who, err := c.requester(ctx, r.Role)
	if err != nil {
		return FlowRun{}, err
	}
	c.mu.Lock()
	live := c.flowLocked(r.Run)
	if live == nil {
		c.mu.Unlock()
		return FlowRun{}, errNotFound
	}
	f := cloneFlowRun(*live)
	a := c.findLocked(f.Attempt)
	safe := f.State == "review" || f.State == "paused" && (a != nil && (a.FlowStage == "completed" || a.FlowStage == "queued") || f.Attempts == 0 && f.Attempt == "")
	c.mu.Unlock()
	if !safe {
		return FlowRun{}, ErrFlowConflict
	}
	allowed := c.flowRequester(f, who, r.Role)
	if !allowed && r.Role == "owner" {
		if !c.authenticated {
			allowed = true
		} else if c.flowOptions.OwnerAuthorizer != nil {
			id, _ := sessionauth.Caller(ctx)
			var paths []string
			for _, flow := range c.definition.flows {
				for _, step := range flow.definition.Steps {
					if step.Gate != nil {
						for _, name := range step.Gate.Accept {
							paths = append(paths, c.definition.documents[name].path)
						}
					}
				}
			}
			slices.Sort(paths)
			paths = slices.Compact(paths)
			allowed = c.flowOptions.OwnerAuthorizer.AuthorizeReview(ctx, ReviewAuthorization{Reviewer: id, Role: "owner", Proposal: f.Proposal, Action: "cancel", Paths: paths}) == nil
		}
	}
	if !allowed {
		return FlowRun{}, errDocumentReviewDenied
	}
	err = c.setFlow(r.Run, "cancelled", "Canceled at a quiescent gate; accepted history retained.")
	out, _ := c.GetFlow(context.Background(), r.Run)
	return out, err
}

// Call only with mu held, after all startup attempt recovery. Never dispatch.
func (c *Canvas) reopenFlowsLocked() {
	for i := range c.data.Flows {
		f := &c.data.Flows[i]
		if f.InputBytes != nil {
			f.Inputs = bytes.Clone(f.InputBytes)
		}
		if !terminalFlow(f.State) {
			f.State = "uncertain"
			f.Reason = "Workflow reopened; explicit reconciliation is required before further dispatch."
		}
	}
}
func (c *Canvas) parkFlowActionLocked(ctx context.Context, id, action string, trigger sharedspace.ReviewActor) error {
	c.mu.Lock()
	f := c.flowLocked(id)
	doc := c.definition.actions[action].Document
	proposal := f.Accepted[doc]
	role := trigger.Role
	if role == "" {
		role = f.Role
	}
	c.mu.Unlock()
	if proposal == "" {
		return c.setFlow(id, "uncertain", "No flow-accepted action document is bound.")
	}
	_, err := c.requestAnnouncementLocked(ctx, action, proposal, role, id)
	if err != nil {
		_ = c.setFlow(id, "uncertain", "Terminal action outcome is uncertain; inspect before acting.")
	}
	return err
}
func (c *Canvas) flowActionAllowedLocked(op announcementOperation) bool {
	if op.FlowRun == "" {
		return !c.activeFlowLocked()
	}
	f := c.flowLocked(op.FlowRun)
	return f != nil && f.Action == op.Snapshot.Operation && (f.State == "action" || f.State == "approved-not-executed") && f.Accepted[op.Snapshot.Document] == op.Snapshot.Proposal
}
func (c *Canvas) observeFlowActionLocked(op announcementOperation) {
	if op.FlowRun == "" {
		return
	}
	f := c.flowLocked(op.FlowRun)
	if f == nil || f.Action != op.Snapshot.Operation {
		return
	}
	// A saved decision is only intent until the human RPC succeeds. Keep it
	// nonterminal across that window, including rejection and restart.
	if op.Stage == "pending" {
		return
	}
	if op.Stage == "uncertain" {
		if f.State != "denied" {
			f.State = "uncertain"
		}
		f.Reason = "Terminal action or explicit cleanup has an uncertain native outcome."
		return
	}
	f.State, f.Reason = "action", ""
	if op.Decision != nil {
		if op.Decision.Approved {
			f.State = "approved-not-executed"
		} else {
			f.State = "denied"
			f.Reason = "Human denied the action; cleanup requires explicit human Continue."
		}
	}
}

// A known denial may be concluded explicitly, never recovered or re-executed.
func (c *Canvas) flowDeniedCleanupLocked(op announcementOperation) bool {
	f := c.flowLocked(op.FlowRun)
	return f != nil && f.State == "denied" && f.Action == op.Snapshot.Operation && op.Decision != nil && !op.Decision.Approved && !c.activeFlowLocked()
}

// Used after explicit human Continue, while publicationMu is still held.
func (c *Canvas) finishFlowAction(ctx context.Context, op announcementOperation) error {
	if op.FlowRun == "" {
		return nil
	}
	v, err := c.inspectAnnouncement(ctx, op)
	if err != nil || !v.Concluded {
		if op.Decision != nil && !op.Decision.Approved {
			return err
		}
		if err == nil {
			err = ErrFlowConflict
		}
		return errors.Join(err, c.setFlow(op.FlowRun, "uncertain", "Continue dispatched but its native outcome could not be established."))
	}
	if v.Receipt != nil {
		return c.setFlow(op.FlowRun, "complete", "")
	}
	return nil
}
