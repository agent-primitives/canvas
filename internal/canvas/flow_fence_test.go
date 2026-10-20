package canvas

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFlowFenceAuthRefusedContinueCanBeRetriedByHuman(t *testing.T) {
	c, f := readyFlowAction(t)
	v, e := c.InspectAnnouncement(context.Background(), f.Action)
	if e != nil {
		t.Fatal(e)
	}
	v.Confirmation.Confirmed = true
	if e = c.DecideAnnouncement(context.Background(), f.Action, "reviewer", true, v.Confirmation); e != nil {
		t.Fatal(e)
	}
	original := c.client
	cl, e := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if m == v1.Sessions_Resume_FullMethodName {
			return status.Error(codes.PermissionDenied, "human lacks exact Resume grant")
		}
		return next(ctx, m, req, reply, cc, opts...)
	})))
	if e != nil {
		t.Fatal(e)
	}
	defer cl.Close()
	c.client = cl
	v, e = c.InspectAnnouncement(context.Background(), f.Action)
	if e != nil {
		t.Fatal(e)
	}
	v.Confirmation.Confirmed = true
	if e = c.ContinueAnnouncement(context.Background(), f.Action, "reviewer", v.Confirmation); status.Code(e) != codes.PermissionDenied {
		t.Fatal("fixture did not refuse before mutation", e)
	}
	op, e := c.announcementOperation(f.Action)
	if e != nil || op.EffectReserved || op.Stage != "idle" {
		t.Fatal("known auth refusal left an unknown effect reservation", op, e)
	}
	c.client = original
	v, e = c.InspectAnnouncement(context.Background(), f.Action)
	if e != nil {
		t.Fatal(e)
	}
	if v.Concluded || v.Receipt != nil {
		t.Fatal("refused human produced a native effect")
	}
	v.Confirmation.Confirmed = true
	if e = c.ContinueAnnouncement(context.Background(), f.Action, "reviewer", v.Confirmation); e != nil {
		t.Fatal("original human permanently wedged", e)
	}
}
func TestFlowFenceCancelBeforeCreateReplyDistinguishesPersistedIntent(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	cl, e := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if m == v1.Sessions_CreateSession_FullMethodName {
			once.Do(func() { close(entered) })
			<-release
		}
		return next(ctx, m, req, reply, cc, opts...)
	})))
	if e != nil {
		t.Fatal(e)
	}
	defer cl.Close()
	c.client = cl
	var released sync.Once
	t.Cleanup(func() { released.Do(func() { close(release) }) })
	f := startWorkflow(t, c, "cancel-create", 12)
	<-entered
	out, e := c.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "GM"})
	released.Do(func() { close(release) })
	if !errors.Is(e, ErrFlowCancelPending) || out.State != "cancelling" {
		t.Fatal("saved cancel was indistinguishable from a refused action", out, e)
	}
	c.wg.Wait()
	out, e = c.GetFlow(context.Background(), f.ID)
	if e != nil || out.State != "cancelled" || calls.Load() != 0 {
		t.Fatal("worker dispatched despite cancel before execution intent", out, e, calls.Load())
	}
}

func TestFlowFenceNativeCancelPinIsUnimplemented(t *testing.T) {
	c, _ := workflowFixture(t, false, nil)
	f := startReviewFlow(t, c, false)
	c.mu.Lock()
	a := *c.findLocked(f.Attempt)
	c.mu.Unlock()
	n, e := c.readFlowExecution(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.client.Sessions().Cancel(context.Background(), &v1.CancelRequest{Session: a.Session, ExecutionId: n.Execution, Reason: "qualification fixture"})
	if status.Code(e) != codes.Unimplemented {
		t.Fatalf("native Cancel capability changed; qualify semantics before changing parked-action policy: %v", e)
	}
}
func TestFlowFenceRunningCancelSurvivesRestartAndSettlesLateOutput(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c, calls := workflowFixture(t, false, func(map[string]any) string { once.Do(func() { close(entered) }); <-release; return "" })
	t.Cleanup(func() {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		default:
			close(release)
		}
	})
	f := startWorkflow(t, c, "cancel-running", 12)
	<-entered
	head, _ := c.space.Head()
	out, e := c.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "GM"})
	if status.Code(e) != codes.Unimplemented || out.State != "cancelling" {
		close(release)
		t.Fatal("native refusal did not retain cancel fence", out, e)
	}
	c.mu.Lock()
	r := cloneFlowRecord(*c.flowRecordLocked(f.ID))
	c.mu.Unlock()
	if !r.CancelRequested || r.Revocation == 0 || r.CancelledBy.Role != "GM" {
		close(release)
		t.Fatal("cancel flags not persisted", r)
	}
	beforeRepeat, _ := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if _, e := c.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "owner"}); !errors.Is(e, ErrFlowCancelPending) {
		t.Error("repeat cancellation lost its pending outcome", e)
	}
	afterRepeat, _ := os.ReadFile(filepath.Join(c.root, "attempts.json"))
	if string(beforeRepeat) != string(afterRepeat) {
		t.Error("repeat cancellation overwrote the original actor/revocation")
	}
	close(release)
	c.wg.Wait()
	c = reopenTestFlow(t, c)
	if _, e = c.StartFlow(context.Background(), FlowStart{Flow: "release", RequestKey: "competing", Role: "GM", Inputs: r.Inputs, AgentRuns: 12}); e == nil {
		t.Fatal("cancel intent released slot on restart")
	}
	out, e = c.ReconcileFlow(context.Background(), FlowMaintenance{Run: f.ID, Role: "GM"})
	if e != nil || out.State != "cancelled" || out.Proposal != "" || calls.Load() != 1 {
		t.Fatal("late completed output revived canceled generation", out, e)
	}
	if now, _ := c.space.Head(); now != head {
		t.Fatal("cancel changed accepted documents")
	}
	if _, e = c.Review(f.Proposal, "accept", "reviewer"); e == nil {
		t.Fatal("canceled output accepted")
	}
}
func TestFlowFenceParkedActionCancelLeavesLedgerUnchanged(t *testing.T) {
	c, f := readyFlowAction(t)
	for _, approved := range []bool{false, true} {
		if approved {
			v, e := c.InspectAnnouncement(context.Background(), f.Action)
			if e != nil {
				t.Fatal(e)
			}
			v.Confirmation.Confirmed = true
			if e = c.DecideAnnouncement(context.Background(), f.Action, "reviewer", true, v.Confirmation); e != nil {
				t.Fatal(e)
			}
		}
		before, _ := os.ReadFile(filepath.Join(c.root, "attempts.json"))
		if _, e := c.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "owner"}); !errors.Is(e, ErrFlowConflict) {
			t.Fatal("parked action cancellation enabled without qualification", e)
		}
		after, _ := os.ReadFile(filepath.Join(c.root, "attempts.json"))
		if string(before) != string(after) {
			t.Fatal("refused cancellation wrote revocation")
		}
	}
}
func TestFlowFenceEffectIntentPrecedesHumanResumeAndSettles(t *testing.T) {
	c, f := readyFlowAction(t)
	v, e := c.InspectAnnouncement(context.Background(), f.Action)
	if e != nil {
		t.Fatal(e)
	}
	v.Confirmation.Confirmed = true
	if e = c.DecideAnnouncement(context.Background(), f.Action, "reviewer", true, v.Confirmation); e != nil {
		t.Fatal(e)
	}
	cl, e := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if m == v1.Sessions_Resume_FullMethodName {
			saved := readPublishedLedger(t, c)
			var found bool
			for _, op := range saved.Announcements {
				if op.Snapshot.Operation == f.Action {
					found = op.EffectReserved && op.Stage == "pending" && op.FlowGeneration != 0
				}
			}
			if !found {
				t.Error("effect RPC preceded durable generation reservation")
			}
		}
		return next(ctx, m, req, reply, cc, opts...)
	})))
	if e != nil {
		t.Fatal(e)
	}
	defer cl.Close()
	c.client = cl
	v, e = c.InspectAnnouncement(context.Background(), f.Action)
	if e != nil {
		t.Fatal(e)
	}
	v.Confirmation.Confirmed = true
	if e = c.ContinueAnnouncement(context.Background(), f.Action, "reviewer", v.Confirmation); e != nil {
		t.Fatal(e)
	}
	op, e := c.announcementOperation(f.Action)
	if e != nil || !op.EffectReserved || !op.EffectSettled {
		t.Fatal("native receipt did not settle preserved effect reservation", op, e)
	}
}
func TestFlowFenceGenerationAndTombstoneRefuseContinueAndRecover(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "wrong-generation", true: "tombstone"}[revoke], func(t *testing.T) {
			c, f := readyFlowAction(t)
			v, e := c.InspectAnnouncement(context.Background(), f.Action)
			if e != nil {
				t.Fatal(e)
			}
			v.Confirmation.Confirmed = true
			if e = c.DecideAnnouncement(context.Background(), f.Action, "reviewer", true, v.Confirmation); e != nil {
				t.Fatal(e)
			}
			op, e := c.announcementOperation(f.Action)
			if e != nil {
				t.Fatal(e)
			}
			if revoke {
				op.Revoked = true
			} else {
				op.FlowGeneration++
			}
			if e = c.saveAnnouncement(op); e != nil {
				t.Fatal(e)
			}
			v, e = c.InspectAnnouncement(context.Background(), f.Action)
			if e != nil {
				t.Fatal(e)
			}
			v.Confirmation.Confirmed = true
			if e = c.ContinueAnnouncement(context.Background(), f.Action, "reviewer", v.Confirmation); !errors.Is(e, ErrFlowConflict) {
				t.Fatal("invalid generation/tombstone resumed", e)
			}
			if e = c.RecoverAnnouncement(context.Background(), f.Action, "reviewer", v.Confirmation); !errors.Is(e, ErrFlowConflict) {
				t.Fatal("invalid generation/tombstone recovered", e)
			}
		})
	}
}
