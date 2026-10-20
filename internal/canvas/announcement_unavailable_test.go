package canvas

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/canvas/internal/sessionclient"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"github.com/agent-primitives/sessions/wire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type announcementReadFailureServer struct {
	*resumeReadServer
	down      atomic.Bool
	failAt    int32
	execErr   error
	replays   atomic.Int32
	approvals atomic.Int32
}

func (s *announcementReadFailureServer) GetSession(ctx context.Context, r *v1.GetSessionRequest) (*v1.Session, error) {
	if s.down.Load() {
		s.gets.Add(1)
		return nil, status.Error(codes.Unavailable, "synthetic host unavailable")
	}
	if s.failAt > 0 && s.gets.Load()+1 == s.failAt {
		s.gets.Add(1)
		return nil, status.Error(codes.Unavailable, "synthetic stability read unavailable")
	}
	return s.resumeReadServer.GetSession(ctx, r)
}
func (s *announcementReadFailureServer) Replay(r *v1.ReplayRequest, stream v1.Sessions_ReplayServer) error {
	s.replays.Add(1)
	return s.resumeReadServer.Replay(r, stream)
}
func (s *announcementReadFailureServer) Exec(*v1.ExecRequest, v1.Sessions_ExecServer) error {
	return s.execErr
}
func (s *announcementReadFailureServer) Approve(context.Context, *v1.ApproveRequest) (*v1.ApproveResponse, error) {
	s.approvals.Add(1)
	return nil, status.Error(codes.Internal, "unexpected approval")
}
func announcementFailureFixture(t *testing.T) (*Canvas, string, *announcementReadFailureServer, *client.Client, *atomic.Int32) {
	t.Helper()
	c, _, calls := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Native session observation\n")
	id, err := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if err != nil {
		t.Fatal(err)
	}
	op, _ := c.announcementOperation(id)
	sess, err := c.client.GetSession(context.Background(), op.Session)
	if err != nil {
		t.Fatal(err)
	}
	records, err := sessionclient.ReadRecovery(context.Background(), c.client.Sessions(), op.Session, sess.GetLastSeq())
	if err != nil {
		t.Fatal(err)
	}
	s := &announcementReadFailureServer{resumeReadServer: &resumeReadServer{session: sess, records: records}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	v1.RegisterSessionsServer(server, s)
	go server.Serve(listener)
	fake, err := client.Dial(listener.Addr().String())
	if err != nil {
		server.Stop()
		listener.Close()
		t.Fatal(err)
	}
	real := c.client
	c.client = fake
	t.Cleanup(func() { c.client = real; fake.Close(); server.Stop(); listener.Close() })
	return c, id, s, real, calls
}
func assertUnavailableCannotDispatch(t *testing.T, c *Canvas, id string, v AnnouncementView, s *announcementReadFailureServer) {
	t.Helper()
	if err := c.DecideAnnouncement(context.Background(), id, "GM", true, v.Confirmation); err == nil {
		t.Fatal("unavailable gate approved")
	}
	if err := c.ContinueAnnouncement(context.Background(), id, "GM", v.Confirmation); err == nil {
		t.Fatal("unavailable gate continued")
	}
	if err := c.RecoverAnnouncement(context.Background(), id, "GM", v.Confirmation); err == nil {
		t.Fatal("unavailable gate recovered")
	}
	if s.resumes.Load() != 0 || s.approvals.Load() != 0 {
		t.Fatal("unavailable evidence dispatched native work")
	}
}
func TestAnnouncementAbandonUnreadableNativeSession(t *testing.T) {
	c, id, s, real, calls := announcementFailureFixture(t)
	before := confirmedAnnouncement(t, c, id)
	s.down.Store(true)
	v := confirmedAnnouncement(t, c, id)
	if v.Status != "unavailable" || !v.Confirmation.Unavailable || v.Concluded || v.Receipt != nil {
		t.Fatalf("not a local unavailable view: %+v", v)
	}
	assertUnavailableCannotDispatch(t, c, id, v, s)
	if err := c.AbandonAnnouncement(context.Background(), id, "GM", AnnouncementConfirmation{Confirmed: true}); err == nil {
		t.Fatal("unbound unavailable confirmation")
	}
	if err := c.AbandonAnnouncement(context.Background(), id, "GM", before.Confirmation); err == nil {
		t.Fatal("prior readable confirmation used while host unavailable")
	}
	s.down.Store(false)
	if err := c.AbandonAnnouncement(context.Background(), id, "GM", v.Confirmation); err == nil {
		t.Fatal("host-down confirmation used after pending gate restored")
	}
	s.down.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.AbandonAnnouncement(ctx, id, "GM", v.Confirmation); err == nil {
		t.Fatal("cancelled caller authorized abandonment")
	}
	withoutAvailability := v.Confirmation
	withoutAvailability.Unavailable = false
	if err := c.AbandonAnnouncement(context.Background(), id, "GM", withoutAvailability); err == nil {
		t.Fatal("unavailability not bound to confirmation")
	}
	body, _ := json.Marshal(map[string]any{"id": id, "role": "GM", "confirmation": v.Confirmation})
	if code, message := newTestServer(t, c).do("POST", "/api/announcement/abandon", string(body)); code != 200 {
		t.Fatalf("HTTP abandonment: %d %s", code, message)
	}
	abandoned, _ := c.announcementOperation(id)
	if abandoned.Stage != "abandoned" || abandoned.Session != v.Session || abandoned.Snapshot != v.Snapshot {
		t.Fatal("local audit or snapshot discarded")
	}
	c.client = real
	if proof := confirmedAnnouncement(t, c, id); proof.Status != "abandoned" {
		t.Fatal("restored pending native session revived abandoned operation")
	}
	next, err := c.RequestAnnouncement(context.Background(), "announce", v.Snapshot.Proposal, "GM")
	if err != nil || next == id {
		t.Fatal("same action/proposal pair stuck", err)
	}
	fresh := confirmedAnnouncement(t, c, next)
	if fresh.Status != "pending-approval" || fresh.Session == v.Session || calls.Load() != 0 {
		t.Fatal("replacement inherited approval or invoked model")
	}
}
func TestAnnouncementAbandonCancelledUndecidedNativeSession(t *testing.T) {
	c, id, s, real, calls := announcementFailureFixture(t)
	old := confirmedAnnouncement(t, c, id)
	// The pinned Sessions service has no Cancel implementation. Model an external
	// cancellation with its public event type and a valid hash-chained native prefix.
	execution := s.records[0].GetEvent().GetExecutionId()
	s.records = append(s.records, &v1.LogRecord{Event: wire.EventToProto(api.Event{ExecutionID: execution, Kind: api.EventEnd, End: &api.HarnessEnd{State: "CANCELED"}})})
	rechainResume(t, s.records)
	s.session = proto.Clone(s.session).(*v1.Session)
	s.session.PendingApproval = nil
	s.session.LastSeq = int64(len(s.records))
	s.session.ExecState = v1.ExecState_EXEC_CANCELED
	v := confirmedAnnouncement(t, c, id)
	if v.Status != "unavailable" || v.Confirmation.Head < 1 || v.Concluded {
		t.Fatal("inactive undecided gate not abandonable", v.Status)
	}
	assertUnavailableCannotDispatch(t, c, id, v, s)
	if err := c.AbandonAnnouncement(context.Background(), id, "GM", old.Confirmation); err == nil {
		t.Fatal("pre-cancellation confirmation accepted")
	}
	if err := c.AbandonAnnouncement(context.Background(), id, "GM", v.Confirmation); err != nil {
		t.Fatal(err)
	}
	c.client = real
	next, err := c.RequestAnnouncement(context.Background(), "announce", v.Snapshot.Proposal, "GM")
	if err != nil || next == id {
		t.Fatal("cancelled pair stuck", err)
	}
	if fresh := confirmedAnnouncement(t, c, next); fresh.Status != "pending-approval" || fresh.Session == v.Session || calls.Load() != 0 {
		t.Fatal("new gate inherited native state")
	}
}
func TestAnnouncementExecRequiresOnePendingInspection(t *testing.T) {
	for _, mode := range []string{"successful-without-gate", "successful-with-parked-gate", "transport-error-with-parked-gate"} {
		t.Run(mode, func(t *testing.T) {
			c, id, s, _, _ := announcementFailureFixture(t)
			op, _ := c.announcementOperation(id)
			if mode == "successful-without-gate" {
				s.session = proto.Clone(s.session).(*v1.Session)
				s.session.LastSeq = 0
				s.session.PendingApproval = nil
				s.records = nil
				if _, err := c.client.Exec(context.Background(), client.ExecOptions{Session: op.Session}); err != nil {
					t.Fatal("fixture did not deliver successful Exec transport", err)
				}
			} else if mode == "transport-error-with-parked-gate" {
				s.execErr = status.Error(codes.Unavailable, "synthetic lost Exec response")
			}
			err := c.execAnnouncement(context.Background(), op)
			if mode == "successful-without-gate" && (err == nil || s.gets.Load() != 2 || s.replays.Load() != 0) {
				t.Fatal("successful transport without pending native request accepted")
			}
			if mode != "successful-without-gate" && (err != nil || s.gets.Load() != 2 || s.replays.Load() != 1) {
				t.Fatalf("expected one parked inspection: err=%v Get=%d Replay=%d", err, s.gets.Load(), s.replays.Load())
			}
		})
	}
}
func TestAnnouncementAbandonRefusesReadablePendingAndDecidedGates(t *testing.T) {
	c, id, _, real, _ := announcementFailureFixture(t)
	c.client = real
	for _, stage := range []string{"pending-approval", "approved", "simulated"} {
		v := confirmedAnnouncement(t, c, id)
		if v.Status != stage {
			t.Fatalf("want %s got %s", stage, v.Status)
		}
		if err := c.AbandonAnnouncement(context.Background(), id, "GM", v.Confirmation); err == nil {
			t.Fatal("abandoned readable gate", stage)
		}
		switch stage {
		case "pending-approval":
			if err := c.DecideAnnouncement(context.Background(), id, "GM", true, v.Confirmation); err != nil {
				t.Fatal(err)
			}
		case "approved":
			if err := c.ContinueAnnouncement(context.Background(), id, "GM", v.Confirmation); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestAnnouncementUnavailableDoesNotMaskInvalidOrMovingProof(t *testing.T) {
	for _, mode := range []string{"foreign", "malformed", "reread-failure", "moving-head"} {
		t.Run(mode, func(t *testing.T) {
			c, id, s, _, _ := announcementFailureFixture(t)
			switch mode {
			case "foreign":
				s.session = proto.Clone(s.session).(*v1.Session)
				s.session.Metadata.Project = "foreign-project"
			case "malformed":
				s.records[0] = proto.Clone(s.records[0]).(*v1.LogRecord)
				s.records[0].ContentHash = "bad"
			case "reread-failure":
				s.session = proto.Clone(s.session).(*v1.Session)
				s.session.PendingApproval = nil
				s.failAt = 2
			case "moving-head":
				s.session = proto.Clone(s.session).(*v1.Session)
				s.session.PendingApproval = nil
				s.nextSession = proto.Clone(s.session).(*v1.Session)
				s.nextSession.LastSeq++
			}
			if _, err := c.InspectAnnouncement(context.Background(), id); err == nil {
				t.Fatal("invalid or unstable native proof exposed as abandonable")
			}
		})
	}
}
