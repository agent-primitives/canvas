package canvas

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/sessions/client"
	"github.com/kaimahi-agents/canvas/internal/sharedspace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func readyFlowAction(t *testing.T) (*Canvas, FlowRun) {
	t.Helper()
	c, _ := workflowFixture(t, true, nil)
	f := startWorkflow(t, c, "action-failure", 12)
	for range 3 {
		f = waitWorkflow(t, c, f.ID, "review")
		if _, e := c.Review(f.Proposal, "accept", "reviewer"); e != nil {
			t.Fatal(e)
		}
	}
	return c, waitWorkflow(t, c, f.ID, "action")
}
func TestWorkflowRefusedDenialDoesNotEndFlowOrReleaseSlot(t *testing.T) {
	c, f := readyFlowAction(t)
	cl, e := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if strings.HasSuffix(m, "/Approve") {
			saved := readPublishedLedger(t, c)
			if saved.Flows[0].State == "denied" {
				t.Error("denial intent acquired terminal authority before native decision")
			}
			return status.Error(codes.PermissionDenied, "synthetic missing human grant")
		}
		return next(ctx, m, req, reply, cc, opts...)
	})))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cl.Close() })
	c.client = cl
	v, e := c.InspectAnnouncement(context.Background(), f.Action)
	if e != nil {
		t.Fatal(e)
	}
	v.Confirmation.Confirmed = true
	if e = c.DecideAnnouncement(context.Background(), f.Action, "reviewer", false, v.Confirmation); status.Code(e) != codes.PermissionDenied {
		t.Fatal("fixture did not refuse decision", e)
	}
	got, _ := c.GetFlow(context.Background(), f.ID)
	if got.State != "action" {
		t.Fatal("rejected denial ended flow", got)
	}
}

type flowHistoryFault struct {
	sharedspace.SharedSpace
	fail *atomic.Bool
}

func (s flowHistoryFault) History() ([]sharedspace.Checkpoint, error) {
	if s.fail.Load() {
		return nil, errors.New("synthetic post-Continue inspection failure")
	}
	return s.SharedSpace.History()
}
func TestWorkflowUnknownPostContinueInspectionKeepsUncertainSlot(t *testing.T) {
	c, f := readyFlowAction(t)
	v, e := c.InspectAnnouncement(context.Background(), f.Action)
	if e != nil {
		t.Fatal(e)
	}
	v.Confirmation.Confirmed = true
	if e = c.DecideAnnouncement(context.Background(), f.Action, "reviewer", true, v.Confirmation); e != nil {
		t.Fatal(e)
	}
	var failed atomic.Bool
	c.space = flowHistoryFault{c.space, &failed}
	cl, e := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		e := next(ctx, m, req, reply, cc, opts...)
		if e == nil && strings.HasSuffix(m, "/Resume") {
			failed.Store(true)
		}
		return e
	})))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cl.Close() })
	c.client = cl
	v, e = c.InspectAnnouncement(context.Background(), f.Action)
	if e != nil {
		t.Fatal(e)
	}
	v.Confirmation.Confirmed = true
	if e = c.ContinueAnnouncement(context.Background(), f.Action, "reviewer", v.Confirmation); e == nil {
		t.Fatal("unknown receipt inspection claimed success")
	}
	got, _ := c.GetFlow(context.Background(), f.ID)
	if got.State != "uncertain" {
		t.Fatal("effect uncertainty mislabeled approved-not-executed", got)
	}
	if _, e = c.CancelFlow(context.Background(), FlowCancel{Run: f.ID, Role: "owner"}); e == nil {
		t.Fatal("unknown effect slot released")
	}
}
