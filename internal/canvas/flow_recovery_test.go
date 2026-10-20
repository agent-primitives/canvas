package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sharedspace"
)

func reopenTestFlow(t *testing.T, c *Canvas) *Canvas {
	t.Helper()
	c.Close()
	n, e := OpenFlowRuntime(c.root, c.space, c.client, c.address, c.definition, c.binder, FlowRuntimeOptions{MaxAgentRuns: 12})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(n.Close)
	return n
}
func TestFlowRecoveryOwnerRotationAndForeignActionCannotBeAdopted(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	f := startReviewFlow(t, c, false)
	c.mu.Lock()
	c.data.ExecutionOwner.Subject = "rotated-owner"
	c.mu.Unlock()
	if _, e := c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"}); e == nil {
		t.Fatal("service-owner rotation was ignored by native recovery")
	}
	action, af := readyFlowAction(t)
	v, e := action.InspectAnnouncement(context.Background(), af.Action)
	if e != nil {
		t.Fatal(e)
	}
	v.Confirmation.Confirmed = true
	if e = action.DecideAnnouncement(context.Background(), af.Action, "reviewer", true, v.Confirmation); e != nil {
		t.Fatal(e)
	}
	v, e = action.InspectAnnouncement(context.Background(), af.Action)
	if e != nil {
		t.Fatal(e)
	}
	v.Confirmation.Confirmed = true
	if e = action.ContinueAnnouncement(context.Background(), af.Action, "reviewer", v.Confirmation); e != nil {
		t.Fatal(e)
	}
	action.mu.Lock()
	foreign := cloneFlowRecord(*action.flowRecordLocked(af.ID))
	action.mu.Unlock()
	foreign.ID = "foreign-flow"
	action.publicationMu.Lock()
	_, e = action.reconcileFlowActionLocked(context.Background(), foreign, sharedspace.ReviewActor{Role: "owner"})
	action.publicationMu.Unlock()
	if e == nil {
		t.Fatal("foreign flow adopted a concluded action")
	}
}

func TestFlowRecoveryTerminalDeniedUnknownCleanupSettles(t *testing.T) {
	c, f := readyFlowAction(t)
	v, e := c.InspectAnnouncement(context.Background(), f.Action)
	if e != nil {
		t.Fatal(e)
	}
	v.Confirmation.Confirmed = true
	if e = c.DecideAnnouncement(context.Background(), f.Action, "reviewer", false, v.Confirmation); e != nil {
		t.Fatal(e)
	}
	v, e = c.InspectAnnouncement(context.Background(), f.Action)
	if e != nil {
		t.Fatal(e)
	}
	v.Confirmation.Confirmed = true
	if e = c.ContinueAnnouncement(context.Background(), f.Action, "reviewer", v.Confirmation); e != nil {
		t.Fatal(e)
	}
	c.mu.Lock()
	for i := range c.data.Announcements {
		if c.data.Announcements[i].Snapshot.Operation == f.Action {
			c.data.Announcements[i].EffectSettled = false
			c.data.Announcements[i].Stage = "uncertain"
		}
	}
	e = c.saveLocked()
	c.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	out, e := c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"})
	c.mu.Lock()
	busy := c.flowEffectBusyLocked(f.ID)
	c.mu.Unlock()
	if e != nil || out.State != "denied" || busy {
		t.Fatal("concluded denial retained an unresolvable cleanup fence", out, e, busy)
	}
}

func TestFlowRecoveryRetrySupersedesExactlyOneGeneration(t *testing.T) {
	runs := 0
	c, calls := workflowFixture(t, false, func(map[string]any) string {
		runs++
		if runs == 1 {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	f := startWorkflow(t, c, "generation-retry", 12)
	f = waitWorkflow(t, c, f.ID, "paused")
	c.mu.Lock()
	old := *c.findLocked(f.Attempt)
	c.mu.Unlock()
	req := FlowRetry{Run: f.ID, Attempt: f.Attempt, RequestKey: "retry-generation", Role: "GM"}
	next, e := c.RetryFlow(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	next = waitWorkflow(t, c, next.ID, "review")
	again, e := c.RetryFlow(context.Background(), req)
	if e != nil || again.Attempt != next.Attempt {
		t.Fatal("retry key reserved duplicate generation", again, e)
	}
	c.mu.Lock()
	r := cloneFlowRecord(*c.flowRecordLocked(f.ID))
	c.mu.Unlock()
	if r.Generation != 2 || len(r.Generations) != 2 || r.Generations[1].Previous != 1 || r.Attempts != 2 || calls.Load() != 2 || c.flowPublishAllowed(old) {
		t.Fatal("supersession changed budget or revived old output", r, calls.Load())
	}
	for range 3 {
		next = waitWorkflow(t, c, f.ID, "review")
		if _, e := c.Review(next.Proposal, "accept", "reviewer"); e != nil {
			t.Fatal(e)
		}
	}
	waitWorkflow(t, c, f.ID, "complete")
	proof, e := c.VerifyFlow(context.Background(), f.ID)
	if e != nil || !proof.Complete {
		t.Fatal("completed validation failure/retry lacked aggregate coverage", proof.Reason, e)
	}
}

func TestFlowRecoveryForeignPrivateBindingsCannotRestoreGate(t *testing.T) {
	for _, field := range []string{"flow", "requester", "definition"} {
		t.Run(field, func(t *testing.T) {
			c, _ := workflowFixture(t, false, nil)
			f := startReviewFlow(t, c, false)
			c = reopenTestFlow(t, c)
			c.mu.Lock()
			r := c.flowRecordLocked(f.ID)
			a := c.findLocked(f.Attempt)
			switch field {
			case "flow":
				a.FlowRun = "foreign-flow"
			case "requester":
				a.RequestedBy.Subject = "foreign-requester"
			case "definition":
				r.Definition = "foreign-definition"
			}
			c.mu.Unlock()
			if out, e := c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"}); e == nil {
				t.Fatal("private/native/proposal bindings disagreed but gate restored", field, out)
			}
		})
	}
}

func TestFlowRecoveryRefusedReviewCanBeRetriedAndReplayed(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	f := startReviewFlow(t, c, false)
	base := c.space
	c.space = reviewFaultSpace{SharedSpace: base, refuse: sharedspace.ErrStale}
	if _, e := c.Review(f.Proposal, "accept", "reviewer"); !errors.Is(e, sharedspace.ErrStale) {
		t.Fatal(e)
	}
	c.space = base
	if _, e := c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"}); e != nil {
		t.Fatal(e)
	}
	for range 3 {
		f = waitWorkflow(t, c, f.ID, "review")
		if _, e := c.Review(f.Proposal, "accept", "reviewer"); e != nil {
			t.Fatal(e)
		}
	}
	waitWorkflow(t, c, f.ID, "complete")
	proof, e := c.VerifyFlow(context.Background(), f.ID)
	if e != nil || !proof.Complete {
		t.Fatal("refused review intent poisoned a valid completed history", proof.Reason, e)
	}
}

func TestFlowAdvanceFailedKeyDoesNotSilentlySucceed(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	f := startReviewFlow(t, c, false)
	c.space = reviewFaultSpace{SharedSpace: c.space, after: true}
	if _, e := c.Review(f.Proposal, "accept", "reviewer"); e == nil {
		t.Fatal("fault hidden")
	}
	if _, e := c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"}); e != nil {
		t.Fatal(e)
	}
	c.mu.Lock()
	originalOwner := c.data.ExecutionOwner
	c.data.ExecutionOwner.Subject = "rotated"
	c.mu.Unlock()
	q := FlowAdvance{Run: f.ID, Role: "GM", RequestKey: "failed-advance"}
	if _, e := c.AdvanceFlow(context.Background(), q); e == nil {
		t.Fatal("owner rotation accepted")
	}
	c.mu.Lock()
	c.data.ExecutionOwner = originalOwner
	c.mu.Unlock()
	if _, e := c.AdvanceFlow(context.Background(), q); e == nil {
		t.Fatal("failed advance key silently succeeded without a reserved step")
	}
	if calls.Load() != 1 {
		t.Fatal("failed advance retried native execution")
	}
}

func TestFlowRecoveryPendingGateAndGeneration(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	f := startReviewFlow(t, c, false)
	c.mu.Lock()
	g := c.flowRecordLocked(f.ID).Generation
	a := *c.findLocked(f.Attempt)
	c.mu.Unlock()
	if g == 0 || a.FlowGeneration != g {
		t.Fatal("generation not reserved before native dispatch", g, a)
	}
	c = reopenTestFlow(t, c)
	before := calls.Load()
	out, e := c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"})
	if e != nil || out.State != "review" || out.Proposal != f.Proposal || calls.Load() != before {
		t.Fatal("known pending gate not restored without dispatch", out, e)
	}
	c.mu.Lock()
	a.FlowGeneration++
	c.mu.Unlock()
	if c.flowPublishAllowed(a) {
		t.Fatal("wrong generation can publish")
	}
}
func TestFlowRecoveryCommittedAcceptRequiresExplicitAdvance(t *testing.T) {
	for _, owner := range []bool{false, true} {
		t.Run(map[bool]string{false: "requester", true: "owner"}[owner], func(t *testing.T) {
			c, calls := workflowFixture(t, false, nil)
			ctx := configureReviewIdentity(t, c)
			f := startReviewFlow(t, c, true)
			c.space = reviewFaultSpace{SharedSpace: c.space, after: true}
			if _, e := c.ReviewContext(ctx, f.Proposal, "accept", "reviewer"); !errors.Is(e, sharedspace.ErrCommitted) {
				t.Fatal(e)
			}
			c = reopenTestFlow(t, c)
			configureTestService(t, c, testServiceIdentity)
			policy, e := NewFlowReviewerPolicy("reviewer", []sessionauth.Identity{{Issuer: testIssuer, Subject: "acceptor"}})
			if e != nil {
				t.Fatal(e)
			}
			c.EnableAuthentication(policy)
			ownerPolicy, e := NewFlowReviewerPolicy("owner", []sessionauth.Identity{{Issuer: testIssuer, Subject: "canvas-owner"}})
			if e != nil {
				t.Fatal(e)
			}
			c.flowOptions.OwnerAuthorizer = ownerPolicy
			who, role := "requester", "GM"
			if owner {
				who, role = "canvas-owner", "owner"
			}
			actorCtx := humanContext(t, who)
			before := calls.Load()
			for range 2 {
				out, e := c.ReconcileFlow(actorCtx, FlowMaintenance{Run: f.ID, Role: role})
				if e != nil || out.State != "ready" || out.Position != 1 || out.Attempts != 1 || calls.Load() != before {
					t.Fatal("reconciliation repeated review/dispatch", out, e)
				}
			}
			request := FlowAdvance{Run: f.ID, Role: role, RequestKey: "advance-1"}
			out, e := c.AdvanceFlow(actorCtx, request)
			if e != nil {
				t.Fatal(e)
			}
			waitWorkflow(t, c, f.ID, "review")
			again, e := c.AdvanceFlow(actorCtx, request)
			if e != nil || again.Attempt != out.Attempt {
				t.Fatal("advance key not idempotent", again, e)
			}
			c.mu.Lock()
			a := *c.findLocked(out.Attempt)
			g := c.flowRecordLocked(f.ID).Generation
			c.mu.Unlock()
			if a.RequestedBy.Subject != "requester" || a.TriggeredBy.Subject != who || a.TriggeringReview != f.Proposal || g != 2 || a.FlowGeneration != 2 || calls.Load() != before+1 {
				t.Fatal("advance lost actor/generation/budget", a, g, calls.Load())
			}
		})
	}
}
func TestFlowRecoveryUnknownAndStaleRemainFenced(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	f := startReviewFlow(t, c, false)
	c = reopenTestFlow(t, c)
	c.mu.Lock()
	saved := c.findLocked(f.Attempt)
	uid := saved.Session
	saved.Session = ""
	c.mu.Unlock()
	out, e := c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"})
	if e == nil || out.State != "uncertain" {
		t.Fatal("missing UID guessed", out, e)
	}
	c.mu.Lock()
	c.findLocked(f.Attempt).Session = uid
	c.flowRecordLocked(f.ID).Generation = 0
	c.mu.Unlock()
	if _, e = c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"}); e == nil {
		t.Fatal("legacy generation adopted")
	}
	if calls.Load() != 1 {
		t.Fatal("unknown work redispatched")
	}
}
func TestFlowAdvanceOwnerToTerminalActionKeepsOriginalRequester(t *testing.T) {
	filename := flowFixture(t)
	genericConfigEdit(t, filename, "        - {id: critique, agent: reviewer, gate: {accept: [review], role: reviewer}}\n", "")
	genericConfigEdit(t, filename, "        - {id: revise, agent: writer, gate: {accept: [prd, announcement], role: reviewer}}\n", "")
	c, calls := workflowFixtureAt(t, filename, nil)
	ctx := configureReviewIdentity(t, c)
	f, e := c.StartFlow(humanContext(t, "requester"), FlowStart{Flow: "release", RequestKey: "owner-action", Role: "GM", Inputs: json.RawMessage(`{"topic":"x"}`), AgentRuns: 12})
	if e != nil {
		t.Fatal(e)
	}
	f = waitWorkflow(t, c, f.ID, "review")
	c.space = reviewFaultSpace{SharedSpace: c.space, after: true}
	if _, e = c.ReviewContext(ctx, f.Proposal, "accept", "reviewer"); e == nil {
		t.Fatal("fault hidden")
	}
	p, e := NewFlowReviewerPolicy("owner", []sessionauth.Identity{{Issuer: testIssuer, Subject: "canvas-owner"}})
	if e != nil {
		t.Fatal(e)
	}
	c.flowOptions.OwnerAuthorizer = p
	ownerCtx := humanContext(t, "canvas-owner")
	if _, e = c.ReconcileFlow(ownerCtx, FlowMaintenance{Run: f.ID, Role: "owner"}); e != nil {
		t.Fatal(e)
	}
	if _, e = c.AdvanceFlow(ownerCtx, FlowAdvance{Run: f.ID, Role: "owner", RequestKey: "owner-action"}); e != nil {
		t.Fatal(e)
	}
	f = waitWorkflow(t, c, f.ID, "action")
	op, e := c.announcementOperation(f.Action)
	if e != nil {
		t.Fatal(e)
	}
	if op.RequestedBy.Subject != "requester" || op.RequestedBy.Role != "GM" || op.TriggeredBy.Subject != "canvas-owner" || calls.Load() != 1 {
		t.Fatal("owner action impersonated requester", op, calls.Load())
	}
}
