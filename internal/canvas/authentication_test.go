package canvas

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func interceptedCanvasClient(t *testing.T, address string, unary grpc.UnaryClientInterceptor, stream grpc.StreamClientInterceptor) *client.Client {
	t.Helper()
	var opts []grpc.DialOption
	if unary != nil {
		opts = append(opts, grpc.WithUnaryInterceptor(unary))
	}
	if stream != nil {
		opts = append(opts, grpc.WithStreamInterceptor(stream))
	}
	cl, err := client.Dial(address, client.WithDialOptions(opts...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

func TestAuthenticatedExecAndResumeErrorsAreNotGenericConflicts(t *testing.T) {
	for _, method := range []string{"CreateSession", "Exec", "Resume"} {
		t.Run(method, func(t *testing.T) {
			c, _, _ := definedFixtureAt(t, actionDefinitionFixture(t), nil)
			p := acceptedAnnouncement(t, c, "# Authenticated failure\n")
			c.EnableAuthentication()
			useTestService(t, c)
			ctx := reviewerContext(t, "approver")
			real := c.client
			id := ""
			if method == "Resume" {
				var err error
				id, err = c.RequestAnnouncement(ctx, "announce", p.ID, "GM")
				if err != nil {
					t.Fatal(err)
				}
				v, err := c.InspectAnnouncement(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				v.Confirmation.Confirmed = true
				if err = c.DecideAnnouncement(ctx, id, "GM", true, v.Confirmation); err != nil {
					t.Fatal(err)
				}
			}
			blocked := interceptedCanvasClient(t, c.address, func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
				if strings.HasSuffix(m, "/"+method) {
					return status.Error(codes.Unauthenticated, "SECRET upstream failure")
				}
				return next(ctx, m, req, reply, cc, opts...)
			}, func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, m string, next grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
				if strings.HasSuffix(m, "/"+method) {
					return nil, status.Error(codes.Unauthenticated, "SECRET upstream failure")
				}
				return next(ctx, desc, cc, m, opts...)
			})
			setClients(c, blocked)
			defer setClients(c, real)
			var err error
			if method != "Resume" {
				id, err = c.RequestAnnouncement(ctx, "announce", p.ID, "GM")
			} else {
				v, e := c.InspectAnnouncement(ctx, id)
				if e != nil {
					t.Fatal(e)
				}
				v.Confirmation.Confirmed = true
				err = c.ContinueAnnouncement(ctx, id, "GM", v.Confirmation)
			}
			if status.Code(err) != codes.Unauthenticated || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("%s outcome: %v", method, err)
			}
			if method == "CreateSession" {
				op, e := c.announcementOperation(id)
				if e != nil || op.Stage != "abandoned" || op.Session != "" {
					t.Fatalf("auth rejection left a sticky request: %+v %v", op, e)
				}
				setClients(c, real)
				fresh, e := c.RequestAnnouncement(ctx, "announce", p.ID, "GM")
				if e != nil || fresh == id {
					t.Fatalf("fresh request after auth rejection: %s %v", fresh, e)
				}
			}
		})
	}
}

func TestAuthenticatedManualHTTPRefusesMissingCallerAndReplayFailsClosed(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	c.EnableAuthentication()
	h := c.APIHandler(apiTestHost)
	cookie := apiCookie(t, h)
	w := apiRequest(h, cookie, http.MethodPost, "/api/run", `{"agent":"writer","request":"Draft fictional requirements.","role":"GM"}`)
	if w.Code != 401 || !strings.Contains(w.Body.String(), "UNAUTHENTICATED") {
		t.Fatalf("unauthenticated action: %d %s", w.Code, w.Body.String())
	}
	if _, err := c.Manual("writer", "", "Draft", "GM"); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	if _, err := c.Verify(reviewerContext(t, "approver"), "missing"); err != errRemoteDiagnostics {
		t.Fatalf("replay did not fail closed: %v", err)
	}
}
