package canvas

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/canon"
	"github.com/agent-primitives/sessions/client"
	"github.com/agent-primitives/sessions/wire"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type resumeReadServer struct {
	v1.UnimplementedSessionsServer
	session     *v1.Session
	nextSession *v1.Session
	gets        atomic.Int32
	records     []*v1.LogRecord
	resumes     atomic.Int32
}

func (s *resumeReadServer) GetSession(context.Context, *v1.GetSessionRequest) (*v1.Session, error) {
	if s.gets.Add(1) > 1 && s.nextSession != nil {
		return s.nextSession, nil
	}
	return s.session, nil
}
func (s *resumeReadServer) Replay(r *v1.ReplayRequest, stream grpc.ServerStreamingServer[v1.LogRecord]) error {
	for _, record := range s.records {
		if err := stream.Send(record); err != nil {
			return err
		}
	}
	return nil
}
func (s *resumeReadServer) Resume(context.Context, *v1.ResumeRequest) (*v1.Session, error) {
	s.resumes.Add(1)
	return s.session, nil
}
func rechainResume(t *testing.T, records []*v1.LogRecord) {
	t.Helper()
	previous := ""
	for i, r := range records {
		r.Seq = int64(i + 1)
		r.PrevHash = previous
		hash, err := canon.HashRecord(previous, r.Seq, wire.EventFromProto(r.Event))
		if err != nil {
			t.Fatal(err)
		}
		r.ContentHash = hash
		previous = hash
	}
}

func TestResumeUntrustedEvidenceRefusesBeforeRPCOrProvider(t *testing.T) {
	c, calls, _, cleanup := resumeCanvas(t, false)
	defer cleanup()
	id := failedResumeRun(t, c)
	run, _ := c.GetRun(id)
	session, err := c.client.GetSession(context.Background(), run.Session)
	if err != nil {
		t.Fatal(err)
	}
	records, err := sessionclient.ReadRecovery(context.Background(), c.client.Sessions(), run.Session, session.GetLastSeq())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"foreign-harness", "foreign-session", "foreign-project", "pending-session", "pending-after-replay", "unknown-session", "legacy", "missing-input", "wrong-input", "keyed-tool", "approval", "malformed", "oversize", "truncated", "gap", "recorded-output", "same-input-foreign-execution", "ambiguous-executions", "missing-cursor"} {
		t.Run(kind, func(t *testing.T) {
			fixture := make([]*v1.LogRecord, len(records))
			for i, r := range records {
				fixture[i] = proto.Clone(r).(*v1.LogRecord)
			}
			s := proto.Clone(session).(*v1.Session)
			execution := fixture[0].Event.GetExecutionId()
			switch kind {
			case "foreign-harness":
				fixture[0].Event.GetExecutionStart().Harness = "arbitrary-import"
			case "foreign-session":
				s.Metadata.Uid = "other"
			case "foreign-project":
				s.Metadata.Project = "other"
			case "pending-session":
				s.ExecState = v1.ExecState_EXEC_PENDING
			case "unknown-session":
				s.ExecState = v1.ExecState(999)
			case "legacy":
				for _, r := range fixture {
					r.Event.ExecutionId = ""
				}
			case "missing-input":
				fixture = append(fixture[:1], fixture[2:]...)
			case "wrong-input":
				fixture[1].Event.GetMessage().Parts[0].GetText().Text = `{"mode":"question"}`
			case "keyed-tool":
				fixture = append(fixture, &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: execution, Kind: api.EventToolCall, ToolCall: &api.ToolCall{ID: "write", Tool: "side-effect", IdempotencyKey: "present-is-not-dedup", Mediation: api.MediationControllerMediated}})})
			case "approval":
				fixture = append(fixture, &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: execution, Kind: api.EventApprovalRequest, Approval: &api.ApprovalRequest{ToolCallID: "write"}})})
			case "malformed":
				fixture[2].Event.Body = nil
			case "oversize":
				fixture[3].Event.GetError().Description = strings.Repeat("x", 8<<20)
			case "recorded-output":
				fixture = append(fixture, &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: execution, Kind: api.EventOutput, Message: api.TextMessage("assistant", "done")})})
			case "same-input-foreign-execution":
				fixture = append(fixture, &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: execution, Kind: api.EventOutput, Message: api.TextMessage("assistant", "done")})}, &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: execution, Kind: api.EventEnd, End: &api.HarnessEnd{State: "COMPLETED"}})})
				for _, r := range records {
					next := proto.Clone(r).(*v1.LogRecord)
					next.Event.ExecutionId = "foreign"
					fixture = append(fixture, next)
				}
			case "ambiguous-executions":
				for _, r := range records {
					next := proto.Clone(r).(*v1.LogRecord)
					next.Event.ExecutionId = "foreign"
					fixture = append(fixture, next)
				}
			}
			rechainResume(t, fixture)
			s.LastSeq = int64(len(fixture))
			if kind == "truncated" {
				fixture = fixture[:len(fixture)-1]
			}
			if kind == "gap" {
				fixture[1].Seq++
			}
			read := &resumeReadServer{session: s, records: fixture}
			if kind == "pending-after-replay" {
				read.nextSession = proto.Clone(s).(*v1.Session)
				read.nextSession.ExecState = v1.ExecState_EXEC_PENDING
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer()
			v1.RegisterSessionsServer(server, read)
			go server.Serve(listener)
			defer server.Stop()
			cl, err := client.Dial(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer cl.Close()
			previous := c.client
			c.client = cl
			defer func() { c.client = previous }()
			var cursor *int64
			if kind == "missing-cursor" {
				c.mu.Lock()
				cursor = c.findLocked(id).ExpectedSeq
				c.findLocked(id).ExpectedSeq = nil
				c.mu.Unlock()
				defer func() { c.mu.Lock(); c.findLocked(id).ExpectedSeq = cursor; c.mu.Unlock() }()
			}
			inspection := c.InspectResume(context.Background(), id, "")
			if inspection.Available || inspection.Execution != "" || inspection.Head != "" || inspection.Reason == "" {
				t.Fatalf("unsafe evidence authorized: %+v", inspection)
			}
			confirmation := ResumeConfirmation{Confirmed: true, ActionID: ID() + ID(), Session: run.Session, Execution: execution, LastSeq: s.LastSeq, Head: fixture[len(fixture)-1].ContentHash}
			if err := c.ConfirmResume(context.Background(), id, "", "GM", confirmation); err == nil {
				t.Fatal("unsafe evidence accepted")
			}
			c.wg.Wait()
			if read.resumes.Load() != 0 || calls.Load() != 1 {
				t.Fatal("refusal invoked runtime/provider")
			}
			if kind == "keyed-tool" && inspection.Reason != resumeToolRefusal {
				t.Fatalf("tool uncertainty not explained: %q", inspection.Reason)
			}
		})
	}
}
