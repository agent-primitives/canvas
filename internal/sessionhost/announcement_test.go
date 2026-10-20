package sessionhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/local"
	"github.com/agent-primitives/sessions/wire"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func announcementSnapshot() announcement.Snapshot {
	content := "Pinned café\n<not sent>&\n"
	digest := sha256.Sum256([]byte(content))
	return announcement.Snapshot{Operation: "de9a3ee079994e369d76d051f69513a6", Definition: strings.Repeat("a", 64), Action: "announce", Document: "announcement", Path: "documents/announcement.md", Proposal: "proposal_1", Revision: strings.Repeat("b", 40), Digest: hex.EncodeToString(digest[:]), Content: content}
}
func announcementRecords(t *testing.T, h *Host, uid string) []eventlog.Record {
	t.Helper()
	records, err := h.store.Session(uid).Read(1)
	if err != nil {
		t.Fatal(err)
	}
	return records
}
func announcementSession(t *testing.T, c v1.SessionsClient) *v1.Session {
	t.Helper()
	s, err := c.CreateSession(t.Context(), &v1.CreateSessionRequest{Session: &v1.Session{Harness: announcement.HarnessName, Metadata: &v1.ResourceMetadata{Project: "fixture", Name: "local-announcement"}}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Removing the separate native placer, auto-resuming Approve, losing the pinned
// bytes, or calling the model makes this integration fail.
func TestAnnouncementNativeApprovalDecisionThenGuardedResume(t *testing.T) {
	for _, approved := range []bool{true, false} {
		t.Run(map[bool]string{true: "approved", false: "denied"}[approved], func(t *testing.T) {
			root := t.TempDir()
			b := bundle(t, "unchanged", "fixture-model")
			var models atomic.Int32
			provider := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
				models.Add(1)
				return api.ModelResponse{}, errors.New("model forbidden")
			}
			h, err := Start(root, []contract.Bundle{b}, provider)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { h.Close() })
			c := connect(t, h)
			s := announcementSession(t, c)
			uid := s.Metadata.Uid
			snapshot := announcementSnapshot()
			encoded, err := announcement.Encode(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			stream, err := c.Exec(t.Context(), &v1.ExecRequest{Session: uid, ExpectedLastSeq: proto.Int64(s.LastSeq), Inputs: []*v1.Message{wire.MessageToProto(api.TextMessage("user", string(encoded)))}})
			if err != nil {
				t.Fatal(err)
			}
			for {
				_, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			paused, err := c.GetSession(t.Context(), &v1.GetSessionRequest{Uid: uid})
			if err != nil {
				t.Fatal(err)
			}
			if paused.ExecState != v1.ExecState_EXEC_AWAITING || paused.PendingApproval == nil {
				t.Fatalf("not parked: %v", paused)
			}
			ref := paused.PendingApproval
			records := announcementRecords(t, h, uid)
			calls, requests := 0, 0
			for _, r := range records {
				switch r.Event.Kind {
				case api.EventToolCall:
					calls++
					call := r.Event.ToolCall
					got, err := announcement.FromArgs(call.Args)
					if err != nil || got != snapshot || call.ID != snapshot.Operation || call.IdempotencyKey != snapshot.Operation || call.Tool != announcement.ToolName || call.Mediation != api.MediationRequiresApproval {
						t.Fatalf("wrong native call: %v %v", call, err)
					}
				case api.EventApprovalRequest:
					requests++
					if r.Seq != ref.RequestSeq || r.Event.ExecutionID != ref.ExecutionId || r.Event.Approval.ToolCallID != ref.ToolCallId {
						t.Fatal("pending tuple differs from journal")
					}
				case api.EventToolResult, api.EventModelCall, api.EventOutput, api.EventEnd, api.EventError:
					t.Fatalf("unexpected event before decision %s", r.Event.Kind)
				}
			}
			if calls != 1 || requests != 1 {
				t.Fatalf("calls=%d requests=%d", calls, requests)
			}
			// Restart discovery must not dispatch anything or lose the native pending tuple.
			if err := h.Close(); err != nil {
				t.Fatal(err)
			}
			h, err = Start(root, []contract.Bundle{b}, provider)
			if err != nil {
				t.Fatal(err)
			}
			c = connect(t, h)
			reopened, err := c.GetSession(t.Context(), &v1.GetSessionRequest{Uid: uid})
			if err != nil || !proto.Equal(reopened.PendingApproval, ref) || reopened.LastSeq != paused.LastSeq {
				t.Fatalf("reopen: %v %v", reopened, err)
			}
			if !reflect.DeepEqual(records, announcementRecords(t, h, uid)) {
				t.Fatal("restart mutated journal")
			}
			wrong := &v1.ApproveRequest{Session: uid, ExecutionId: ref.ExecutionId, ToolCallId: "other", RequestSeq: ref.RequestSeq, Approved: proto.Bool(approved)}
			if _, err := c.Approve(t.Context(), wrong); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("wrong tuple: %v", err)
			}
			q := &v1.ApproveRequest{Session: uid, ExecutionId: ref.ExecutionId, ToolCallId: ref.ToolCallId, RequestSeq: ref.RequestSeq, Approved: proto.Bool(approved)}
			decided, err := c.Approve(t.Context(), q)
			if err != nil {
				t.Fatal(err)
			}
			after := announcementRecords(t, h, uid)
			if len(after) != len(records)+1 || after[len(after)-1].Event.Kind != api.EventApprovalResult {
				t.Fatal("Approve must record decision only")
			}
			if decided.GetSession().GetLastSeq() != after[len(after)-1].Seq {
				t.Fatal("decision cursor mismatch")
			}
			if _, err := c.Resume(t.Context(), &v1.ResumeRequest{Session: uid, ExpectedLastSeq: proto.Int64(paused.LastSeq)}); status.Code(err) != codes.Aborted {
				t.Fatalf("stale resume: %v", err)
			}
			if !reflect.DeepEqual(after, announcementRecords(t, h, uid)) {
				t.Fatal("stale CAS mutated journal")
			}
			_, resumeErr := c.Resume(t.Context(), &v1.ResumeRequest{Session: uid, ExpectedLastSeq: proto.Int64(decided.GetSession().GetLastSeq())})
			if approved && resumeErr != nil {
				t.Fatal(resumeErr)
			}
			if !approved && (status.Code(resumeErr) != codes.Internal || !strings.Contains(status.Convert(resumeErr).Message(), "simulation did not succeed")) {
				t.Fatalf("denied resume must fail without success output: %v", resumeErr)
			}
			final := announcementRecords(t, h, uid)
			receipts := 0
			for _, r := range final {
				if r.Event.Kind == api.EventModelCall || r.Event.Kind == api.EventOutput {
					t.Fatalf("native harness emitted %s", r.Event.Kind)
				}
				if r.Event.Result != nil {
					receipts++
					result := r.Event.Result
					if result.ID != snapshot.Operation || result.ApprovalRequestSeq != ref.RequestSeq || result.ApprovalDecisionSeq != decided.GetDecision().GetSeq() {
						t.Fatalf("unbound host receipt: %+v", result)
					}
					if approved {
						receipt, err := announcement.DecodeReceipt(result.Output)
						if err != nil || receipt.Snapshot != snapshot || result.IsError || result.Code != api.ToolResultCodeUnspecified {
							t.Fatalf("wrong exact receipt: %+v %v", result, err)
						}
					} else {
						if !result.IsError || result.Code != api.ToolResultCodeApprovalDenied || result.Output != nil {
							t.Fatalf("denial is not success: %+v", result)
						}
					}
				}
			}
			if receipts != 1 || models.Load() != 0 {
				t.Fatalf("receipts=%d models=%d", receipts, models.Load())
			}
			if approved {
				proof, err := sessionclient.Verify(t.Context(), h.Address(), uid)
				if err != nil {
					_, localErr := verifyRecords(t.Context(), final, announcementHarness{})
					t.Fatalf("completed action reconstruction: %v (local: %v)", err, localErr)
				}
				if !proof.Reconstructed || proof.ModelCalls != 0 || proof.ExecutorCalls == nil || *proof.ExecutorCalls != 0 || !reflect.DeepEqual(proof.ExecutionIDs, []string{ref.ExecutionId}) || !reflect.DeepEqual(final, announcementRecords(t, h, uid)) || models.Load() != 0 {
					t.Fatal("replay failed or changed live state")
				}
			}
		})
	}
}

func TestAnnouncementFactoryModeUnavailableAndDefaultUnchanged(t *testing.T) {
	b := bundle(t, "unchanged", "fixture-model")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h, err := StartWithOptions(t.TempDir(), []contract.Bundle{b}, archiveProvider, Options{Listener: listener, BackendFactory: func(b contract.Bundle, p int) (placement.Backend, func() error, error) {
		har, err := NewBundleHarness(b, p)
		if err != nil {
			return nil, nil, err
		}
		backend := local.New(har)
		return backend, backend.Close, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	c := connect(t, h)
	_, err = c.CreateSession(t.Context(), &v1.CreateSessionRequest{Session: &v1.Session{Harness: announcement.HarnessName}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("factory advertised native harness: %v", err)
	}
	s, err := c.CreateSession(t.Context(), &v1.CreateSessionRequest{Session: &v1.Session{}})
	if err != nil || s.Harness != contract.HarnessName(b.Digest, contract.Profile(b)) {
		t.Fatalf("default changed: %v %v", s, err)
	}
	if _, ok := h.harnesses[announcement.HarnessName]; ok {
		t.Fatal("announcement leaked into model diagnostics")
	}
}

func TestAnnouncementMalformedInputNeverRequestsApproval(t *testing.T) {
	b := bundle(t, "unchanged", "fixture-model")
	h, err := Start(t.TempDir(), []contract.Bundle{b}, func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		t.Error("model called")
		return api.ModelResponse{}, errors.New("forbidden")
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	c := connect(t, h)
	s := announcementSession(t, c)
	_, err = run(t.Context(), c, s.Metadata.Uid, "{}")
	if err == nil {
		t.Fatal("malformed input succeeded")
	}
	for _, r := range announcementRecords(t, h, s.Metadata.Uid) {
		if r.Event.ToolCall != nil || r.Event.Approval != nil || r.Event.Result != nil {
			t.Fatal("malformed input reached tool")
		}
	}
}

func TestAnnouncementExecutorValidatesHostScopeAndCall(t *testing.T) {
	s := announcementSnapshot()
	good := api.ToolCall{ID: s.Operation, Tool: announcement.ToolName, IdempotencyKey: s.Operation, Mediation: api.MediationRequiresApproval, Args: announcement.Args(s)}
	for _, tc := range []struct {
		name   string
		scope  controller.ToolCallContext
		change func(*api.ToolCall)
	}{
		{"no session", controller.ToolCallContext{}, func(*api.ToolCall) {}},
		{"wrong name", controller.ToolCallContext{SessionUID: "session"}, func(c *api.ToolCall) { c.Tool = "other" }},
		{"empty name", controller.ToolCallContext{SessionUID: "session"}, func(c *api.ToolCall) { c.Tool = "" }},
		{"wrong mediation", controller.ToolCallContext{SessionUID: "session"}, func(c *api.ToolCall) { c.Mediation = api.MediationControllerMediated }},
		{"wrong ID", controller.ToolCallContext{SessionUID: "session"}, func(c *api.ToolCall) { c.ID = "other" }},
		{"wrong key", controller.ToolCallContext{SessionUID: "session"}, func(c *api.ToolCall) { c.IdempotencyKey = "other" }},
		{"wrong args", controller.ToolCallContext{SessionUID: "session"}, func(c *api.ToolCall) { c.Args = map[string]any{"operation": s.Operation} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := good
			tc.change(&call)
			r, err := executeAnnouncement(t.Context(), tc.scope, call)
			if err == nil || r.Output != nil {
				t.Fatalf("invalid call produced receipt: %+v %v", r, err)
			}
		})
	}
	result, err := executeAnnouncement(t.Context(), controller.ToolCallContext{SessionUID: "session"}, good)
	if err != nil || result.ApprovalRequestSeq != 0 || result.ApprovalDecisionSeq != 0 {
		t.Fatalf("executor manufactured native sequences: %+v %v", result, err)
	}
	receipt, err := announcement.DecodeReceipt(result.Output)
	if err != nil || receipt.Snapshot != s {
		t.Fatalf("receipt: %+v %v", result, err)
	}
}

func TestAnnouncementHarnessDescriptorAndErrors(t *testing.T) {
	har := announcementHarness{}
	desc, err := har.Describe(t.Context())
	if err != nil || desc.ID != announcement.HarnessName || desc.Version != "1" || len(desc.Models) != 0 || desc.Capabilities.Resumability != api.ResumabilityStatelessReplay || desc.Capabilities.ForkSafe || len(desc.Tools) != 1 || desc.Tools[0].Name != announcement.ToolName || desc.Tools[0].Mediation != api.MediationRequiresApproval {
		t.Fatalf("descriptor: %+v %v", desc, err)
	}
	if _, err := refuseAnnouncementModel(t.Context(), api.ModelRequest{}); err == nil {
		t.Fatal("model function allowed request")
	}
	s := announcementSnapshot()
	raw, err := announcement.Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []api.ToolResult{
		{IsError: true, Code: api.ToolResultCodeApprovalDenied},
		{IsError: true, Code: api.ToolResultCodeExecutorError, Error: "private text"},
		{Output: map[string]any{"kind": "not a receipt"}},
	} {
		sink := &announcementSink{result: result}
		if err := har.Run(t.Context(), &api.Start{Inputs: []api.Message{*api.TextMessage("user", string(raw))}}, sink); err == nil || strings.Contains(err.Error(), "private text") {
			t.Fatalf("invalid result: %v", err)
		}
		if sink.outputs != 0 || sink.calls != 1 {
			t.Fatalf("outputs=%d calls=%d", sink.outputs, sink.calls)
		}
	}
	sentinel := &api.ApprovalParkedError{Ref: api.ApprovalRef{ExecutionID: "execution", ToolCallID: s.Operation, RequestSeq: 3}}
	sink := &announcementSink{err: sentinel}
	if err := har.Run(t.Context(), &api.Start{Inputs: []api.Message{*api.TextMessage("user", string(raw))}}, sink); !errors.Is(err, sentinel) {
		t.Fatalf("park not propagated: %v", err)
	}
	// A valid but different snapshot cannot become the current operation's success.
	other := s
	other.Operation = "other"
	sink = &announcementSink{result: api.ToolResult{Output: map[string]any{"kind": announcement.Kind, "snapshot": announcement.Args(other)}}}
	if err := har.Run(t.Context(), &api.Start{Inputs: []api.Message{*api.TextMessage("user", string(raw))}}, sink); err == nil {
		t.Fatal("foreign snapshot accepted")
	}
}

func TestAnnouncementNativeRecoveryCuts(t *testing.T) {
	for _, cut := range []string{"pre-tool", "call-only", "descriptor-version"} {
		t.Run(cut, func(t *testing.T) {
			var models atomic.Int32
			b := bundle(t, "unchanged", "fixture-model")
			h, err := Start(t.TempDir(), []contract.Bundle{b}, func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
				models.Add(1)
				return api.ModelResponse{}, errors.New("forbidden")
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { h.Close() })
			c := connect(t, h)
			s := announcementSession(t, c)
			uid := s.Metadata.Uid
			snapshot := announcementSnapshot()
			raw, err := announcement.Encode(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			log := h.store.Session(uid)
			fence, err := log.NewFence()
			if err != nil {
				t.Fatal(err)
			}
			events := []api.Event{
				{Kind: api.EventExecutionStart, ExecutionStart: &api.ExecutionStart{Harness: announcement.HarnessName, HarnessVersion: "1", InputCount: proto.Int64(1)}},
				{Kind: api.EventInput, Message: api.TextMessage("user", string(raw))},
			}
			if cut == "descriptor-version" {
				events[0].ExecutionStart.HarnessVersion = "different-version"
			}
			if cut == "call-only" {
				events = append(events, api.Event{Kind: api.EventToolCall, ToolCall: &api.ToolCall{ID: snapshot.Operation, Tool: announcement.ToolName, IdempotencyKey: snapshot.Operation, Mediation: api.MediationRequiresApproval, Args: announcement.Args(snapshot)}})
			}
			for i, ev := range events {
				ev.ExecutionID = "interrupted"
				if _, err := log.Append(int64(i), fence, ev); err != nil {
					t.Fatal(err)
				}
			}
			// Recovery is an explicit guarded native Resume, never a new input/decision.
			resumed, err := c.Resume(t.Context(), &v1.ResumeRequest{Session: uid, ExpectedLastSeq: proto.Int64(int64(len(events)))})
			if cut == "descriptor-version" {
				if status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), "version") {
					t.Fatalf("mismatched descriptor resumed: %v %v", resumed, err)
				}
				if len(announcementRecords(t, h, uid)) != len(events) || models.Load() != 0 {
					t.Fatal("descriptor mismatch dispatched or mutated journal")
				}
				return
			}
			if err != nil || resumed.PendingApproval == nil || resumed.ExecState != v1.ExecState_EXEC_AWAITING {
				t.Fatalf("recovery=%v %v", resumed, err)
			}
			calls, requests := 0, 0
			for _, r := range announcementRecords(t, h, uid) {
				switch r.Event.Kind {
				case api.EventToolCall:
					calls++
				case api.EventApprovalRequest:
					requests++
					if r.Seq != resumed.PendingApproval.RequestSeq {
						t.Fatal("recovery tuple not native")
					}
				case api.EventApprovalResult, api.EventToolResult, api.EventOutput, api.EventModelCall:
					t.Fatalf("recovery dispatched %s", r.Event.Kind)
				}
			}
			if calls != 1 || requests != 1 || models.Load() != 0 {
				t.Fatalf("calls=%d requests=%d models=%d", calls, requests, models.Load())
			}
		})
	}
}

// This counted native-controller characterization exercises our real harness and
// executor without substituting the journal or native approval implementation.
func TestAnnouncementNativeExecutorCountsAndFailureReceipt(t *testing.T) {
	for _, tc := range []struct {
		name           string
		approved, fail bool
		wantCalls      int
	}{
		{"approved", true, false, 1}, {"denied", false, false, 0}, {"executor-error", true, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := eventlog.AsStore(eventlog.New())
			calls, models := 0, 0
			executor := func(ctx context.Context, scope controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
				calls++
				if tc.fail {
					return api.ToolResult{}, errors.New("private executor detail")
				}
				return executeAnnouncement(ctx, scope, call)
			}
			fresh := func() *controller.Controller {
				c, err := controller.New(log, func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
					models++
					return api.ModelResponse{}, errors.New("model forbidden")
				},
					controller.WithSessionUID("local-session"), controller.WithHarness(announcement.HarnessName), controller.WithToolExecutor(executor))
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			raw, err := announcement.Encode(announcementSnapshot())
			if err != nil {
				t.Fatal(err)
			}
			if err := fresh().Exec(t.Context(), announcementHarness{}, []api.Message{*api.TextMessage("user", string(raw))}, 0); !errors.Is(err, api.ErrApprovalParked) {
				t.Fatalf("park: %v", err)
			}
			state, err := controller.InspectApproval(log, 0)
			if err != nil || state == nil || state.Request == nil {
				t.Fatalf("state: %+v %v", state, err)
			}
			if calls != 0 {
				t.Fatal("executor before decision")
			}
			if _, err := controller.Approve(log, api.ApprovalDecision{ExecutionID: state.ExecutionID, ToolCallID: state.Call.ID, RequestSeq: state.Request.Seq, Approved: tc.approved}); err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatal("Approve dispatched executor")
			}
			_, resumeErr := fresh().Resume(t.Context(), announcementHarness{})
			if tc.approved && !tc.fail && resumeErr != nil {
				t.Fatal(resumeErr)
			}
			if (!tc.approved || tc.fail) && (resumeErr == nil || strings.Contains(resumeErr.Error(), "private executor detail")) {
				t.Fatalf("failure leaked or succeeded: %v", resumeErr)
			}
			if calls != tc.wantCalls || models != 0 {
				t.Fatalf("executor=%d want=%d models=%d", calls, tc.wantCalls, models)
			}
			records, err := log.Read(1)
			if err != nil {
				t.Fatal(err)
			}
			receipts := 0
			for _, r := range records {
				if r.Event.Result != nil {
					receipts++
					if tc.fail && (!r.Event.Result.IsError || r.Event.Result.Code != api.ToolResultCodeExecutorError || r.Event.Result.Output != nil) {
						t.Fatalf("executor error became success: %+v", r.Event.Result)
					}
				}
				if r.Event.Kind == api.EventOutput || r.Event.Kind == api.EventModelCall {
					t.Fatalf("unexpected %s", r.Event.Kind)
				}
			}
			if receipts != 1 {
				t.Fatalf("receipts=%d", receipts)
			}
		})
	}
}

type announcementSink struct {
	api.EventSink
	result         api.ToolResult
	err            error
	outputs, calls int
}

func (s *announcementSink) ToolCall(context.Context, api.ToolCall) (api.ToolResult, error) {
	s.calls++
	return s.result, s.err
}
func (s *announcementSink) Output(context.Context, string) error { s.outputs++; return nil }
