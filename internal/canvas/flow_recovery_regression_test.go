package canvas

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Restore the exact durable window before a finished native turn's worker saves
// completion. The native journal and Git store are real and are not rewritten.
func reopenBeforeFlowCompletion(t *testing.T, c *Canvas, f FlowRun) *Canvas {
	t.Helper()
	c.wg.Wait()
	c.mu.Lock()
	a := c.findLocked(f.Attempt)
	a.Status = "running"
	a.FlowStage = "exec-intent"
	e := c.saveLocked()
	c.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	return reopenTestFlow(t, c)
}
func TestFlowRecoverySettledCancellationReleasesAdmissionAfterCrash(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c, calls := workflowFixture(t, false, func(map[string]any) string { once.Do(func() { close(entered) }); <-release; return "" })
	var released sync.Once
	t.Cleanup(func() { released.Do(func() { close(release) }) })
	f := startWorkflow(t, c, "cancel-crash", 12)
	<-entered
	if _, e := c.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "GM"}); status.Code(e) != codes.Unimplemented {
		t.Fatal(e)
	}
	released.Do(func() { close(release) })
	c = reopenBeforeFlowCompletion(t, c, f)
	out, e := c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"})
	if e != nil || out.State != "cancelled" {
		t.Fatal(out, e)
	}
	c.mu.Lock()
	a := *c.findLocked(f.Attempt)
	c.mu.Unlock()
	if a.Status != "failed" || a.FlowStage != "completed" || a.Proposal != "" {
		t.Fatal("settled cancellation left an admission-blocking attempt", a)
	}
	next := startWorkflow(t, c, "after-cancel-crash", 12)
	waitWorkflow(t, c, next.ID, "review")
	if calls.Load() != 2 {
		t.Fatal("reconciliation dispatched work or held admission", calls.Load())
	}
}
func TestFlowRecoveryValidationFailureRestoresAttemptAfterCrash(t *testing.T) {
	var nativeCalls atomic.Int32
	c, _ := workflowFixture(t, false, func(map[string]any) string {
		if nativeCalls.Add(1) == 1 {
			return changeEnvelope("prd", validPRD)
		}
		return ""
	})
	f := startWorkflow(t, c, "validation-crash", 12)
	f = waitWorkflow(t, c, f.ID, "paused")
	c = reopenBeforeFlowCompletion(t, c, f)
	f, e := c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"})
	if e != nil || f.State != "paused" {
		t.Fatal(f, e)
	}
	c.mu.Lock()
	a := *c.findLocked(f.Attempt)
	c.mu.Unlock()
	if a.Status != "failed" || a.FlowStage != "completed" {
		t.Fatal("proven native output failure stayed uncertain", a)
	}
	f, e = c.RetryFlow(context.Background(), FlowRetry{Run: f.ID, Attempt: f.Attempt, RequestKey: "retry-crash", Role: "GM"})
	if e != nil {
		t.Fatal(e)
	}
	for range 3 {
		f = waitWorkflow(t, c, f.ID, "review")
		if _, e = c.Review(f.Proposal, "accept", "reviewer"); e != nil {
			t.Fatal(e)
		}
	}
	waitWorkflow(t, c, f.ID, "complete")
	proof, e := c.VerifyFlow(context.Background(), f.ID)
	if e != nil || !proof.Complete {
		t.Fatal("proven failure/retry not covered", proof.Reason, e)
	}
	next := startWorkflow(t, c, "after-validation-crash", 12)
	waitWorkflow(t, c, next.ID, "review")
}
func TestFlowRecoveryCommittedReviewSettlesAttemptAfterCrash(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	f := startReviewFlow(t, c, false)
	base := c.space
	c.space = reviewFaultSpace{SharedSpace: base, after: true}
	if _, e := c.Review(f.Proposal, "accept", "reviewer"); e == nil {
		t.Fatal("committed review fault hidden")
	}
	c = reopenBeforeFlowCompletion(t, c, f)
	c.space = base
	out, e := c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"})
	if e != nil || out.State != "ready" {
		t.Fatal(out, e)
	}
	c.mu.Lock()
	a := *c.findLocked(f.Attempt)
	c.mu.Unlock()
	if a.Status != "proposed" || a.FlowStage != "completed" {
		t.Fatal("committed review retained an uncertain attempt", a.Status, a.FlowStage)
	}
	if _, e = c.AdvanceFlow(context.Background(), FlowAdvance{Run: f.ID, Role: "GM", RequestKey: "after-review-crash"}); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		out = waitWorkflow(t, c, f.ID, "review")
		if _, e = c.Review(out.Proposal, "accept", "reviewer"); e != nil {
			t.Fatal(e)
		}
	}
	waitWorkflow(t, c, f.ID, "complete")
	next := startWorkflow(t, c, "after-reviewed-crash", 12)
	waitWorkflow(t, c, next.ID, "review")
}

func TestFlowRecoveryLostApproveReplyRestoresHumanContinue(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(map[bool]string{true: "approve", false: "deny"}[approve], func(t *testing.T) {
			c, f := readyFlowAction(t)
			original := c.client
			cl, e := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
				e := next(ctx, m, req, reply, cc, opts...)
				if m == v1.Sessions_Approve_FullMethodName && e == nil {
					return status.Error(codes.Unavailable, "approval reply lost after commit")
				}
				return e
			})))
			if e != nil {
				t.Fatal(e)
			}
			defer cl.Close()
			c.client = cl
			v, e := c.InspectAnnouncement(context.Background(), f.Action)
			if e != nil {
				t.Fatal(e)
			}
			v.Confirmation.Confirmed = true
			if e = c.DecideAnnouncement(context.Background(), f.Action, "reviewer", approve, v.Confirmation); e == nil {
				t.Fatal("lost approval reply was hidden")
			}
			c = reopenTestFlow(t, c)
			c.client = original
			out, e := c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"})
			want := "approved-not-executed"
			if !approve {
				want = "denied"
			}
			if e != nil || out.State != want {
				t.Fatal("proven native decision remained wedged", out, e)
			}
			v, e = c.InspectAnnouncement(context.Background(), f.Action)
			if e != nil || v.Concluded || v.Receipt != nil {
				t.Fatal("reconciliation resumed an action", v, e)
			}
			v.Confirmation.Confirmed = true
			if e = c.ContinueAnnouncement(context.Background(), f.Action, "reviewer", v.Confirmation); e != nil {
				t.Fatal("explicit human Continue unavailable", e)
			}
		})
	}
}
func TestFlowRecoveryKnownParkVersusUnresolvedContinue(t *testing.T) {
	for _, continuing := range []bool{false, true} {
		t.Run(map[bool]string{false: "park", true: "continue"}[continuing], func(t *testing.T) {
			c, f := readyFlowAction(t)
			before, e := c.InspectAnnouncement(context.Background(), f.Action)
			if e != nil {
				t.Fatal(e)
			}
			op, e := c.announcementOperation(f.Action)
			if e != nil {
				t.Fatal(e)
			}
			if op.Phase != "park" {
				t.Fatal("native park lacked a saved phase", op.Phase)
			}
			if continuing {
				op.Phase = "continue"
				op.EffectReserved = true
			}
			op.Stage = "uncertain"
			if e = c.saveAnnouncement(op); e != nil {
				t.Fatal(e)
			}
			out, e := c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"})
			if continuing {
				if !errors.Is(e, ErrFlowConflict) {
					t.Fatal("unresolved Continue reservation cleared", out, e)
				}
				return
			}
			if e != nil || out.State != "action" {
				t.Fatal("known native park remained unusable", out, e)
			}
			after, e := c.InspectAnnouncement(context.Background(), f.Action)
			if e != nil || after.Confirmation.Head != before.Confirmation.Head || after.Concluded || after.Receipt != nil {
				t.Fatal("reconciliation mutated native execution", after, e)
			}
		})
	}
}
func TestFlowRecoveryActionPhaseAloneNeverGrantsAuthority(t *testing.T) {
	for _, kind := range []string{"uncommitted-approval", "mismatched-decision", "unreserved-denial-cleanup"} {
		t.Run(kind, func(t *testing.T) {
			c, f := readyFlowAction(t)
			original := c.client
			if kind == "uncommitted-approval" {
				cl, e := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
					if m == v1.Sessions_Approve_FullMethodName {
						return status.Error(codes.Unavailable, "approval never reached native handler")
					}
					return next(ctx, m, req, reply, cc, opts...)
				})))
				if e != nil {
					t.Fatal(e)
				}
				defer cl.Close()
				c.client = cl
			}
			v, e := c.InspectAnnouncement(context.Background(), f.Action)
			if e != nil {
				t.Fatal(e)
			}
			v.Confirmation.Confirmed = true
			e = c.DecideAnnouncement(context.Background(), f.Action, "reviewer", kind != "unreserved-denial-cleanup", v.Confirmation)
			if kind == "uncommitted-approval" {
				if e == nil {
					t.Fatal("fixture did not refuse approval")
				}
			} else if e != nil {
				t.Fatal(e)
			}
			c.client = original
			op, e := c.announcementOperation(f.Action)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "mismatched-decision" {
				d := *op.Decision
				d.Approved = false
				op.Decision = &d
			}
			if kind == "unreserved-denial-cleanup" {
				op.Phase = "continue"
				op.EffectReserved = false
			}
			op.Stage = "uncertain"
			if e = c.saveAnnouncement(op); e != nil {
				t.Fatal(e)
			}
			before, e := c.InspectAnnouncement(context.Background(), f.Action)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"}); !errors.Is(e, ErrFlowConflict) {
				t.Fatal("phase or intent granted native authority", kind, e)
			}
			after, e := c.InspectAnnouncement(context.Background(), f.Action)
			if e != nil || after.Confirmation.Head != before.Confirmation.Head || after.Concluded || after.Receipt != nil {
				t.Fatal("refused recovery mutated native state", kind, after, e)
			}
		})
	}
}

func TestFlowRecoveryLegacyUnknownActionStageRemainsFenced(t *testing.T) {
	c, f := readyFlowAction(t)
	op, e := c.announcementOperation(f.Action)
	if e != nil {
		t.Fatal(e)
	}
	op.Phase = "" // Existing journals have no durable operation-phase evidence.
	op.Stage = "uncertain"
	if e = c.saveAnnouncement(op); e != nil {
		t.Fatal(e)
	}
	if _, e = c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"}); !errors.Is(e, ErrFlowConflict) {
		t.Fatal("ambiguous legacy operation adopted", e)
	}
}
