package canvas

import (
	"context"
	"maps"
	"slices"

	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/wire"
)

type FlowMaintenance struct{ Run, Role string }
type FlowAdvance struct{ Run, Role, RequestKey string }
type flowGeneration struct {
	Number   uint64 `json:"number"`
	Previous uint64 `json:"previous"`
	Step     string `json:"step"`
	Attempt  string `json:"attempt"`
}
type flowNativeSnapshot struct {
	Session, Execution, Hash, Output string
	Head                             int64
	Complete, Settled                bool
}

func cloneFlowRecord(r flowRunRecord) flowRunRecord {
	r.FlowRun = cloneFlowRun(r.FlowRun)
	r.InputBytes = slices.Clone(r.InputBytes)
	r.Reviews = slices.Clone(r.Reviews)
	r.Generations = slices.Clone(r.Generations)
	return r
}

// Called only inside an update under mu. A saved cancellation wins before the
// next native dispatch intent; an already reserved RPC may still settle later.
func (c *Canvas) claimFlowDispatchStageLocked(a *attempt, stage string) bool {
	if a.FlowRun == "" {
		return true
	}
	f := c.flowLocked(a.FlowRun)
	if !c.flowGenerationCurrentLocked(f, a) {
		a.FlowStage = "cancelled-undispatched"
		if r := c.flowRecordLocked(a.FlowRun); r != nil && r.CancelRequested && f != nil && f.Attempt == a.ID && !c.flowEffectBusyLocked(f.ID) {
			f.State = "cancelled"
			f.Reason = "Cancellation suppressed the next native dispatch."
		}
		return false
	}
	a.FlowStage = stage
	return true
}
func (c *Canvas) flowGenerationCurrentLocked(f *FlowRun, a *attempt) bool {
	if f == nil || a == nil {
		return false
	}
	r := c.flowRecordLocked(f.ID)
	if r == nil || r.Generation == 0 || r.CancelRequested || r.Revocation != 0 || a.FlowGeneration != r.Generation || f.Attempt != a.ID || len(r.Generations) == 0 {
		return false
	}
	g := r.Generations[len(r.Generations)-1]
	return g.Number == r.Generation && g.Attempt == a.ID && g.Step == a.FlowStep
}
func (c *Canvas) authorizeFlowMaintenance(ctx context.Context, f FlowRun, role, action string) (sharedspace.ReviewActor, error) {
	_, who, e := c.requester(ctx, role)
	if e != nil {
		return who, e
	}
	if who.Role == "" {
		who.Role = role
	}
	if c.flowRequester(f, who, role) {
		return who, nil
	}
	if role != "owner" {
		return who, errDocumentReviewDenied
	}
	if !c.authenticated {
		return who, nil
	}
	if c.flowOptions.OwnerAuthorizer == nil {
		return who, errDocumentReviewDenied
	}
	id, ok := sessionauth.Caller(ctx)
	if !ok {
		return who, errDocumentReviewDenied
	}
	var paths []string
	for _, flow := range c.definition.flows {
		for _, s := range flow.definition.Steps {
			if s.Gate != nil {
				for _, name := range s.Gate.Accept {
					paths = append(paths, c.definition.documents[name].path)
				}
			}
		}
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	e = c.flowOptions.OwnerAuthorizer.AuthorizeReview(ctx, ReviewAuthorization{Reviewer: id, Role: role, Proposal: f.Proposal, Action: action, Paths: paths})
	return who, e
}

// readFlowExecution proves the exact document execution using service-owned reads.
// It never resumes an execution or adopts another session after an ambiguous Create.
func (c *Canvas) readFlowExecution(ctx context.Context, a attempt) (flowNativeSnapshot, error) {
	n := flowNativeSnapshot{Session: a.Session}
	c.mu.Lock()
	owned := c.ownedLocked(a.ExecutionOwner)
	c.mu.Unlock()
	if !owned {
		return n, errExecutionOwnerChanged
	}
	if a.Session == "" || a.ExpectedSeq == nil || *a.ExpectedSeq < 0 || a.Harness == "" || a.BundleDigest == "" {
		return n, ErrFlowConflict
	}
	service, sessions, release, e := c.serviceContext(ctx)
	if e != nil {
		return n, e
	}
	defer release()
	s, e := sessions.GetSession(service, a.Session)
	if e != nil {
		return n, e
	}
	if !recordedOwner(s, a.ExecutionOwner) || s.GetMetadata().GetUid() != a.Session || s.GetMetadata().GetProject() != c.project() || s.GetHarness() != a.Harness || s.GetAnnotations()["bundle-digest"] != a.BundleDigest {
		return n, ErrFlowConflict
	}
	records, e := sessionclient.ReadRecovery(service, sessions.Sessions(), a.Session, s.GetLastSeq())
	if e != nil {
		return n, e
	}
	if len(records) == 0 || *a.ExpectedSeq >= int64(len(records)) {
		return n, ErrFlowConflict
	}
	first := records[*a.ExpectedSeq]
	start := first.GetEvent().GetExecutionStart()
	n.Execution = first.GetEvent().GetExecutionId()
	if first.GetSeq() != *a.ExpectedSeq+1 || first.GetEvent().GetKind() != v1.EventKind_EVENT_EXECUTION_START || n.Execution == "" || start == nil || start.GetInputCount() != 1 || start.GetHarness() != a.Harness || start.GetHarnessVersion() != "" || len(start.GetConfig()) != 0 || start.GetResumeFromSeq() != 0 {
		return n, ErrFlowConflict
	}
	inputs, models, outputs, ends := 0, 0, 0, 0
	for _, r := range records[*a.ExpectedSeq+1:] {
		ev := r.GetEvent()
		if ev == nil {
			return n, ErrFlowConflict
		}
		if ev.GetKind() == v1.EventKind_EVENT_LIFECYCLE && ev.GetExecutionId() == "" {
			continue
		}
		if ev.GetExecutionId() != n.Execution || ends != 0 {
			return n, ErrFlowConflict
		}
		switch ev.GetKind() {
		case v1.EventKind_EVENT_INPUT:
			if inputs != 0 || models != 0 || !plainMessage(ev.GetMessage(), "user") || wire.MessageFromProto(ev.GetMessage()).Text() != a.Input {
				return n, ErrFlowConflict
			}
			inputs++
		case v1.EventKind_EVENT_MODEL_CALL:
			if inputs != 1 || models != 0 || ev.GetModel() == nil || ev.GetModel().GetModel() != c.bundles[a.Agent].Model || len(ev.GetModel().GetTools()) != 0 {
				return n, ErrFlowConflict
			}
			models++
		case v1.EventKind_EVENT_OUTPUT:
			if models != 1 || outputs != 0 || !plainMessage(ev.GetMessage(), "assistant") {
				return n, ErrFlowConflict
			}
			outputs++
			n.Output = wire.MessageFromProto(ev.GetMessage()).Text()
		case v1.EventKind_EVENT_USAGE:
			if outputs != 1 || ev.GetUsage() == nil {
				return n, ErrFlowConflict
			}
		case v1.EventKind_EVENT_ERROR:
			if ev.GetError() == nil {
				return n, ErrFlowConflict
			}
		case v1.EventKind_EVENT_END:
			if ev.GetEnd() == nil {
				return n, ErrFlowConflict
			}
			ends++
			n.Settled = true
			n.Complete = ev.GetEnd().GetState() == "COMPLETED" && outputs == 1 && models == 1
		default:
			return n, ErrFlowConflict
		}
	}
	if inputs != 1 {
		return n, ErrFlowConflict
	}
	latest, e := sessions.GetSession(service, a.Session)
	if e != nil {
		return n, e
	}
	if latest.GetLastSeq() != s.GetLastSeq() || latest.GetMetadata().GetUid() != a.Session || latest.GetMetadata().GetProject() != c.project() || latest.GetHarness() != a.Harness || latest.GetAnnotations()["bundle-digest"] != a.BundleDigest || !recordedOwner(latest, a.ExecutionOwner) {
		return n, ErrFlowConflict
	}
	n.Head = s.GetLastSeq()
	n.Hash = records[len(records)-1].GetContentHash()
	return n, nil
}
func (c *Canvas) ReconcileFlow(ctx context.Context, q FlowMaintenance) (FlowRun, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	f, e := c.GetFlow(ctx, q.Run)
	if e != nil {
		return f, e
	}
	who, e := c.authorizeFlowMaintenance(ctx, f, q.Role, "reconcile")
	if e != nil {
		return f, e
	}
	c.mu.Lock()
	r := cloneFlowRecord(*c.flowRecordLocked(f.ID))
	saved := c.findLocked(f.Attempt)
	var a attempt
	if saved != nil {
		a = *saved
	}
	c.mu.Unlock()
	if f.Definition != c.definition.Digest {
		return f, ErrFlowConflict
	}
	if terminalFlow(f.State) {
		c.mu.Lock()
		pendingEffect := c.flowEffectBusyLocked(f.ID)
		c.mu.Unlock()
		if f.Action != "" && pendingEffect {
			return c.reconcileFlowActionLocked(ctx, r, who)
		}
		return f, nil
	}
	if r.Generation == 0 || saved == nil || a.FlowGeneration != r.Generation || a.FlowRun != f.ID || a.FlowStep != f.Step || a.RequestedBy != f.RequestedBy || a.ExecutionOwner != f.ExecutionOwner || len(r.Generations) != r.Attempts || uint64(len(r.Generations)) != r.Generation || r.Generations[len(r.Generations)-1].Attempt != a.ID || r.Generations[len(r.Generations)-1].Step != a.FlowStep {
		return f, ErrFlowConflict
	}
	if f.Action != "" {
		return c.reconcileFlowActionLocked(ctx, r, who)
	}
	n, e := c.readFlowExecution(ctx, a)
	if e != nil {
		return f, e
	}
	if r.CancelRequested || r.Revocation != 0 {
		if !n.Settled {
			return f, ErrFlowConflict
		}
		c.mu.Lock()
		live := c.flowLocked(f.ID)
		live.State = "cancelled"
		live.Reason = "Cancellation settled from bound native execution; no late output eligible."
		settled := c.findLocked(a.ID)
		settled.Status = "failed"
		settled.FlowStage = "completed"
		settled.Error = live.Reason
		e = c.saveLocked()
		if e != nil {
			live.State = "uncertain"
			c.persistenceFailureLocked(nil)
		}
		c.mu.Unlock()
		out, _ := c.GetFlow(context.Background(), f.ID)
		return out, e
	}
	if !n.Complete {
		return f, ErrFlowConflict
	}
	changes, validation := c.definition.ChangesForStep(f.Flow, a.FlowStep, n.Output)
	if validation != nil && a.Proposal == "" {
		c.mu.Lock()
		live := c.flowLocked(f.ID)
		live.State = "paused"
		live.Reason = validation.Error()
		settled := c.findLocked(a.ID)
		settled.FlowStage = "completed"
		settled.Status = "failed"
		settled.Error = validation.Error()
		e = c.saveLocked()
		if e != nil {
			live.State = "uncertain"
			c.persistenceFailureLocked(nil)
		}
		c.mu.Unlock()
		out, _ := c.GetFlow(context.Background(), f.ID)
		return out, e
	}
	if validation != nil {
		return f, ErrFlowConflict
	}
	ev, e := c.inspectFlowReviewLocked(ctx, f.ID, -1)
	if e != nil {
		return f, e
	}
	if ev.Attempt == a.ID && (ev.State == "accepted" || ev.State == "rejected") {
		if len(r.Reviews) == 0 {
			return f, ErrFlowConflict
		}
		intent := r.Reviews[len(r.Reviews)-1]
		if intent.Generation != a.FlowGeneration {
			return f, ErrFlowConflict
		}
		ps, e := c.space.Proposals()
		if e != nil {
			return f, e
		}
		var p *sharedspace.Proposal
		for i := range ps {
			if ps[i].ID == ev.Proposal {
				p = &ps[i]
				break
			}
		}
		if p == nil || !maps.Equal(changes, p.Changes) {
			return f, ErrFlowConflict
		}
		if ev.State == "accepted" && ev.CurrentHead != ev.AcceptedCommit {
			return f, ErrFlowConflict
		}
		c.mu.Lock()
		live := c.flowLocked(f.ID)
		rec := c.flowRecordLocked(f.ID)
		if !intent.Applied {
			if ev.State == "accepted" {
				for name, d := range c.definition.documents {
					if _, ok := p.Changes[d.path]; ok {
						live.Accepted[name] = p.ID
					}
				}
				live.Position++
			}
			c.markReviewAppliedLocked(f.ID, *p)
		}
		if ev.State == "rejected" {
			if intent.RequestKey == "" {
				live.State = "rejected"
			} else {
				live.State = "paused"
			}
		} else if live.Position == len(c.definition.flows[f.Flow].definition.Steps) {
			live.State = "complete"
		} else {
			live.State = "ready"
			rec.ReadyHead = ev.AcceptedCommit
		}
		live.Proposal = ""
		live.Reason = "Reconciled recorded review; no next step dispatched."
		rec.ReconciledBy = who
		c.findLocked(a.ID).Status = "proposed"
		c.findLocked(a.ID).FlowStage = "completed"
		e = c.saveLocked()
		if e != nil {
			live.State = "uncertain"
		}
		c.mu.Unlock()
		out, _ := c.GetFlow(context.Background(), f.ID)
		return out, e
	}
	ps, e := c.space.Proposals()
	if e != nil {
		return f, e
	}
	head, e := c.space.Head()
	if e != nil {
		return f, e
	}
	for _, p := range ps {
		if p.ID != a.Proposal || p.Status != "pending" || p.Base != a.Base || head != a.Base || p.Attribution.Run != a.ID || p.Attribution.FlowRun != f.ID || p.Attribution.FlowStep != a.FlowStep || p.Attribution.Session != a.Session || p.Attribution.BundleDigest != a.BundleDigest || p.Attribution.ExecutionOwner != a.ExecutionOwner || !maps.Equal(p.Changes, changes) {
			continue
		}
		c.mu.Lock()
		live := c.flowLocked(f.ID)
		if !c.flowGenerationCurrentLocked(live, c.findLocked(a.ID)) {
			c.mu.Unlock()
			return f, ErrFlowConflict
		}
		live.State = "review"
		live.Proposal = p.ID
		live.Reason = "Reconciled exact completed generation; awaiting human review."
		c.findLocked(a.ID).FlowStage = "completed"
		c.findLocked(a.ID).Status = "proposed"
		c.flowRecordLocked(f.ID).ReconciledBy = who
		e = c.saveLocked()
		if e != nil {
			live.State = "uncertain"
		}
		c.mu.Unlock()
		out, _ := c.GetFlow(context.Background(), f.ID)
		return out, e
	}
	return f, ErrFlowConflict
}

// Complete the idempotent advance only in the same save as its counted attempt
// or parked-action intent. A failed unreserved key remains visibly pending.
func (c *Canvas) finishAdvanceReservationLocked(f *FlowRun, keys []string) {
	if len(keys) == 1 {
		cmd := f.Commands[keys[0]]
		cmd.Pending = false
		f.Commands[keys[0]] = cmd
	}
}
func (c *Canvas) AdvanceFlow(ctx context.Context, q FlowAdvance) (FlowRun, error) {
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	f, e := c.GetFlow(ctx, q.Run)
	if e != nil {
		return f, e
	}
	who, e := c.authorizeFlowMaintenance(ctx, f, q.Role, "advance")
	if e != nil {
		return f, e
	}
	if !flowKey(q.RequestKey) {
		return f, ErrFlowConflict
	}
	digest := commandDigest(q, who)
	c.mu.Lock()
	r := cloneFlowRecord(*c.flowRecordLocked(f.ID))
	c.mu.Unlock()
	if old, ok := r.Commands[q.RequestKey]; ok {
		if old.Digest != digest || old.Pending {
			return f, ErrFlowConflict
		}
		return f, nil
	}
	if f.State != "ready" || r.Generation == 0 || r.CancelRequested || r.Revocation != 0 || r.ReadyHead == "" {
		return f, ErrFlowConflict
	}
	ev, e := c.inspectFlowReviewLocked(ctx, f.ID, -1)
	if e != nil {
		return f, e
	}
	if ev.State != "accepted" || !ev.Applied || ev.CurrentHead != r.ReadyHead || ev.AcceptedCommit != r.ReadyHead {
		return f, ErrFlowConflict
	}
	c.mu.Lock()
	c.flowLocked(f.ID).Commands[q.RequestKey] = flowCommand{Digest: digest, Pending: true}
	c.mu.Unlock()
	e = c.dispatchFlowLocked(ctx, f.ID, who, ev.Proposal, q.RequestKey)
	out, _ := c.GetFlow(context.Background(), f.ID)
	return out, e
}

// Unsettled intent is a mutation fence even if an older journal lacks the
// explicit reservation bit. Tombstones and settled receipts are never erased.
func (c *Canvas) flowEffectBusyLocked(run string) bool {
	for _, op := range c.data.Announcements {
		if op.FlowRun != "" && (run == "" || op.FlowRun == run) && !op.EffectSettled && (op.EffectReserved || op.Stage == "pending" || op.Stage == "uncertain") {
			return true
		}
	}
	return false
}
func (c *Canvas) reconcileFlowActionLocked(ctx context.Context, r flowRunRecord, who sharedspace.ReviewActor) (FlowRun, error) {
	op, e := c.announcementOperation(r.Action)
	if e != nil {
		return r.FlowRun, e
	}
	if op.FlowRun != r.ID || op.ExecutionOwner != r.ExecutionOwner || op.Snapshot.Definition != r.Definition || r.Accepted[op.Snapshot.Document] != op.Snapshot.Proposal || op.FlowGeneration != r.Generation || r.CancelRequested || r.Revocation != 0 || op.Revoked {
		return r.FlowRun, ErrFlowConflict
	}
	v, e := c.inspectAnnouncement(ctx, op)
	if e != nil {
		return r.FlowRun, e
	}
	if op.EffectReserved || op.Stage == "pending" || op.Stage == "uncertain" {
		if v.Concluded {
			e = c.finishFlowAction(ctx, op)
			out, _ := c.GetFlow(context.Background(), r.ID)
			return out, e
		}
		// Parking and approval cannot execute the tool. Only a durable known
		// phase plus its exact native outcome can lift this stage fence; never
		// infer that an unknown legacy operation or Continue had no effect.
		parked := (op.Phase == "create" || op.Phase == "park") && op.Decision == nil && v.Status == "pending-approval"
		decided := op.Phase == "approve" && op.Decision != nil && ((op.Decision.Approved && v.Status == "approved") || (!op.Decision.Approved && v.Status == "denied"))
		if op.EffectReserved || (!parked && !decided) {
			return r.FlowRun, ErrFlowConflict
		}
		op.Stage = "idle"
		e = c.saveAnnouncement(op)
		out, _ := c.GetFlow(context.Background(), r.ID)
		return out, e
	}
	state := ""
	switch v.Status {
	case "pending-approval":
		state = "action"
	case "approved":
		state = "approved-not-executed"
	case "denied":
		state = "denied"
	default:
		return r.FlowRun, ErrFlowConflict
	}
	e = c.setFlow(r.ID, state, "Reconciled exact parked native action; no decision or Resume dispatched.")
	out, _ := c.GetFlow(context.Background(), r.ID)
	return out, e
}
