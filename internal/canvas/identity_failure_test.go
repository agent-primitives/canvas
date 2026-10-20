package canvas

import (
	"context"
	"net"
	"testing"

	"github.com/agent-primitives/canvas/internal/sessionclient"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type authDecisionServer struct {
	*resumeReadServer
	approveCode codes.Code
	readCode    codes.Code
	received    *v1.IdentityRef
	real        v1.SessionsClient
}

func (s *authDecisionServer) GetSession(ctx context.Context, r *v1.GetSessionRequest) (*v1.Session, error) {
	if s.readCode != codes.OK {
		return nil, status.Error(s.readCode, "SECRET-READ-ERROR")
	}
	return s.resumeReadServer.GetSession(ctx, r)
}
func (s *authDecisionServer) Approve(ctx context.Context, r *v1.ApproveRequest) (*v1.ApproveResponse, error) {
	s.received = r.GetIdentity()
	if s.approveCode != codes.OK {
		return nil, status.Error(s.approveCode, "SECRET-APPROVAL-ERROR")
	}
	return s.real.Approve(ctx, r)
}

func TestAuthRejectedApprovalLeavesNoIntentAndAnotherReviewerCanDecide(t *testing.T) {
	for _, code := range []codes.Code{codes.PermissionDenied, codes.Unauthenticated, codes.DeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			c, _, _ := definedFixtureAt(t, actionDefinitionFixture(t), nil)
			p := acceptedAnnouncement(t, c, "# Retry by another reviewer\n")
			c.EnableAuthentication()
			useTestService(t, c)
			id, err := c.RequestAnnouncement(reviewerContext(t, "requester"), "announce", p.ID, "GM")
			if err != nil {
				t.Fatal(err)
			}
			op, _ := c.announcementOperation(id)
			real := c.client
			sess, err := real.GetSession(context.Background(), op.Session)
			if err != nil {
				t.Fatal(err)
			}
			records, err := sessionclient.ReadRecovery(context.Background(), real.Sessions(), op.Session, sess.GetLastSeq())
			if err != nil {
				t.Fatal(err)
			}
			srv := &authDecisionServer{resumeReadServer: &resumeReadServer{session: sess, records: records}, approveCode: code, real: real.Sessions()}
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer()
			v1.RegisterSessionsServer(server, srv)
			go server.Serve(lis)
			defer server.Stop()
			proxy, err := client.Dial(lis.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer proxy.Close()
			setClients(c, proxy)
			defer setClients(c, real)
			ctx := reviewerContext(t, "first-reviewer")
			if code != codes.DeadlineExceeded {
				srv.readCode = code
				if _, readErr := c.InspectAnnouncement(ctx, id); status.Code(readErr) != code {
					t.Fatalf("inspection hid auth error: %v", readErr)
				}
				srv.readCode = codes.OK
			}
			v, err := c.InspectAnnouncement(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			v.Confirmation.Confirmed = true
			err = c.DecideAnnouncement(ctx, id, "GM", true, v.Confirmation)
			op, _ = c.announcementOperation(id)
			if code == codes.DeadlineExceeded {
				if op.Stage != "uncertain" || op.Decision == nil {
					t.Fatal("ambiguous outcome was cleared")
				}
				return
			}
			if status.Code(err) != code || op.Stage != "idle" || op.Decision != nil {
				t.Fatalf("rejected intent stuck: %+v %v", op, err)
			}
			srv.approveCode = codes.OK
			if err = c.DecideAnnouncement(reviewerContext(t, "second-reviewer"), id, "GM", true, v.Confirmation); err != nil {
				t.Fatal(err)
			}
			if srv.received.GetPrincipal() != "second-reviewer" || srv.received.GetSubject() != "second-reviewer" || srv.received.GetIssuer() != "https://issuer.example" {
				t.Fatalf("wrong approval actor: %+v", srv.received)
			}
		})
	}
}
