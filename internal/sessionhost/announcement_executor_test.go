package sessionhost

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/canvas/internal/announcementexec"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/wire"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestDurableAnnouncementApprovalContinueRestart(t *testing.T) {
	for _, approved := range []bool{true, false} {
		t.Run(map[bool]string{true: "approved", false: "denied"}[approved], func(t *testing.T) {
			root := t.TempDir()
			executorRoot := filepath.Join(root, "executor")
			store, err := announcementexec.Open(executorRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			b := bundle(t, "unchanged", "fixture-model")
			var models atomic.Int32
			provider := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
				models.Add(1)
				return api.ModelResponse{}, errors.New("model forbidden")
			}
			start := func() *Host {
				lis, e := net.Listen("tcp", "127.0.0.1:0")
				if e != nil {
					t.Fatal(e)
				}
				h, e := StartWithOptions(filepath.Join(root, "sessions"), []contract.Bundle{b}, provider, Options{Listener: lis, AnnouncementExecutor: store.Execute})
				if e != nil {
					t.Fatal(e)
				}
				return h
			}
			check := func(i, o int64) {
				ev, e := announcementexec.ReadEvidence(t.Context(), executorRoot)
				if e != nil || ev.Invocations != i || ev.Outcomes != o {
					t.Fatalf("evidence %+v %v want%d/%d", ev, e, i, o)
				}
			}
			h := start()
			defer func() { h.Close() }()
			c := connect(t, h)
			session := announcementSession(t, c)
			uid := session.Metadata.Uid
			raw, _ := announcement.Encode(announcementSnapshot())
			stream, e := c.Exec(t.Context(), &v1.ExecRequest{Session: uid, ExpectedLastSeq: proto.Int64(session.LastSeq), Inputs: []*v1.Message{wire.MessageToProto(api.TextMessage("user", string(raw)))}})
			if e != nil {
				t.Fatal(e)
			}
			for {
				_, e = stream.Recv()
				if errors.Is(e, io.EOF) {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
			}
			pending, e := c.GetSession(t.Context(), &v1.GetSessionRequest{Uid: uid})
			if e != nil || pending.PendingApproval == nil {
				t.Fatal(pending, e)
			}
			check(0, 0)
			h.Close()
			h = start()
			c = connect(t, h)
			reopened, e := c.GetSession(t.Context(), &v1.GetSessionRequest{Uid: uid})
			if e != nil || !proto.Equal(pending.PendingApproval, reopened.PendingApproval) {
				t.Fatal("pending binding lost", e)
			}
			check(0, 0)
			ref := reopened.PendingApproval
			decision := &v1.ApproveRequest{Session: uid, ExecutionId: ref.ExecutionId, ToolCallId: "wrong", RequestSeq: ref.RequestSeq, Approved: proto.Bool(approved)}
			if _, e = c.Approve(t.Context(), decision); status.Code(e) != codes.FailedPrecondition {
				t.Fatal(e)
			}
			check(0, 0)
			decision.ToolCallId = ref.ToolCallId
			result, e := c.Approve(t.Context(), decision)
			if e != nil {
				t.Fatal(e)
			}
			check(0, 0)
			_, e = c.Resume(t.Context(), &v1.ResumeRequest{Session: uid, ExpectedLastSeq: proto.Int64(result.Session.LastSeq)})
			if approved && e != nil {
				t.Fatal(e)
			}
			if !approved && e == nil {
				t.Fatal("denial succeeded")
			}
			if approved {
				check(1, 1)
			} else {
				check(0, 0)
			}
			native := announcementRecords(t, h, uid)
			results := 0
			for _, r := range native {
				if r.Event.Result != nil {
					results++
					out := r.Event.Result
					if approved {
						receipt, e := announcement.DecodeReceipt(out.Output)
						if e != nil || receipt.Snapshot != announcementSnapshot() || out.IsError {
							t.Fatal("bad native receipt", out, e)
						}
					} else if !out.IsError || out.Code != api.ToolResultCodeApprovalDenied {
						t.Fatal("denial became receipt", out)
					}
					if out.ApprovalRequestSeq != ref.RequestSeq || out.ApprovalDecisionSeq != result.Decision.Seq {
						t.Fatal("receipt lost approval binding")
					}
				}
			}
			if results != 1 || models.Load() != 0 {
				t.Fatal("unexpected model/results", models.Load(), results)
			}
		})
	}
}

func TestAnnouncementExecutorOptInRejectsRemoteFactoryBeforeIO(t *testing.T) {
	lis, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(t.TempDir(), "untouched")
	h, e := StartWithOptions(root, nil, archiveProvider, Options{Listener: lis, AnnouncementExecutor: executeAnnouncement, BackendFactory: func(contract.Bundle, int) (placement.Backend, func() error, error) {
		t.Fatal("factory invoked")
		return nil, nil, nil
	}})
	if e == nil {
		h.Close()
		t.Fatal("remote factory accepted local executor")
	}
	if _, e = os.Lstat(root); !os.IsNotExist(e) {
		t.Fatal("invalid options touched state", e)
	}
	if e = lis.Close(); !errors.Is(e, net.ErrClosed) {
		t.Fatal("invalid options retained listener", e)
	}
}
