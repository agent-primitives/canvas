package announcementexec

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
)

func nativeController(t *testing.T, log eventlog.Store, executor controller.ToolFunc) *controller.Controller {
	t.Helper()
	c, e := controller.New(log, func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		t.Error("model invoked")
		return api.ModelResponse{}, errors.New("forbidden")
	}, controller.WithSessionUID("session-1"), controller.WithHarness(announcement.HarnessName), controller.WithToolExecutor(executor))
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func parkAndApprove(t *testing.T, log eventlog.Store, c *controller.Controller) {
	t.Helper()
	s, e := announcement.FromArgs(callFor("accepted").Args)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := announcement.Encode(s)
	if e = c.Exec(t.Context(), sessionhost.NewAnnouncementHarness(), []api.Message{*api.TextMessage("user", string(raw))}, 0); !errors.Is(e, api.ErrApprovalParked) {
		t.Fatal(e)
	}
	pending, e := controller.InspectApproval(log, 0)
	if e != nil || pending == nil || pending.Request == nil {
		t.Fatal("missing native approval", e)
	}
	if _, e = controller.Approve(log, api.ApprovalDecision{ExecutionID: pending.ExecutionID, ToolCallID: pending.Call.ID, RequestSeq: pending.Request.Seq, Approved: true}); e != nil {
		t.Fatal(e)
	}
}
func nativeResults(t *testing.T, log eventlog.Store) []*api.ToolResult {
	t.Helper()
	records, e := log.Read(1)
	if e != nil {
		t.Fatal(e)
	}
	var results []*api.ToolResult
	for _, r := range records {
		if r.Event.Result != nil {
			results = append(results, r.Event.Result)
		}
		if r.Event.Kind == api.EventModelCall {
			t.Fatal("model recorded")
		}
	}
	return results
}

func TestNativeCanceledRunReentersOnlyUnansweredApprovedCall(t *testing.T) {
	for _, cut := range []string{"admission", "outcome"} {
		t.Run(cut, func(t *testing.T) {
			root := newRoot(t)
			s, e := Open(root)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { s.Close() }()
			log := eventlog.AsStore(eventlog.New())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cut == "admission" {
				s.afterAdmission = cancel
			}
			executor := func(ctx context.Context, sc controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
				r, e := s.Execute(ctx, sc, call)
				if e == nil && cut == "outcome" {
					cancel()
					return api.ToolResult{}, ctx.Err()
				}
				return r, e
			}
			parkAndApprove(t, log, nativeController(t, log, executor))
			counts(t, s, 0, 0)
			if _, e = nativeController(t, log, executor).Resume(ctx, sessionhost.NewAnnouncementHarness()); e == nil {
				t.Fatal("ended server context succeeded")
			}
			if len(nativeResults(t, log)) != 0 {
				t.Fatal("run cancellation was stamped as terminal failure")
			}
			want := 0
			if cut == "outcome" {
				want = 1
			}
			counts(t, s, 1, want)
			s.Close()
			s, e = Open(root)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = nativeController(t, log, s.Execute).Resume(t.Context(), sessionhost.NewAnnouncementHarness()); e != nil {
				t.Fatal(e)
			}
			counts(t, s, 2, 1)
			results := nativeResults(t, log)
			if len(results) != 1 || results[0].IsError {
				t.Fatalf("wrong native outcome %+v", results)
			}
		})
	}
}

func TestNativeUncertainOutcomeIsNotHeldPending(t *testing.T) {
	s := opened(t)
	log := eventlog.AsStore(eventlog.New())
	s.commitFault = func(phase string, committed bool) error {
		if phase == "outcome" && committed {
			if e := os.Rename(s.path, s.path+".saved"); e != nil {
				t.Fatal(e)
			}
			return errors.New("lost acknowledgement and unavailable reconciliation")
		}
		return nil
	}
	parkAndApprove(t, log, nativeController(t, log, s.Execute))
	counts(t, s, 0, 0)
	if _, e := nativeController(t, log, s.Execute).Resume(t.Context(), sessionhost.NewAnnouncementHarness()); e == nil {
		t.Fatal("uncertain outcome reported success")
	}
	results := nativeResults(t, log)
	if len(results) != 1 || !results[0].IsError || results[0].Code != api.ToolResultCodeExecutorError || results[0].Output != nil {
		t.Fatal("uncertainty misreported as pending/success", results)
	}
	if e := os.Rename(s.path+".saved", s.path); e != nil {
		t.Fatal(e)
	}
	counts(t, s, 1, 1)
	if _, e := s.Execute(t.Context(), scope, callFor("accepted")); !errors.Is(e, ErrUncertain) {
		t.Fatal(e)
	}
}
