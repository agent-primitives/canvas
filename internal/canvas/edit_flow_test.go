package canvas

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestEditContinueRaceBothOrders(t *testing.T) {
	for _, first := range []string{"edit", "continue-unknown"} {
		t.Run(first, func(t *testing.T) {
			c, _ := workflowFixture(t, true, nil)
			p := acceptedAnnouncement(t, c, "# Exact action before edit\n")
			id, e := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
			if e != nil {
				t.Fatal(e)
			}
			v := confirmedAnnouncement(t, c, id)
			if e = c.DecideAnnouncement(context.Background(), id, "GM", true, v.Confirmation); e != nil {
				t.Fatal(e)
			}
			v = confirmedAnnouncement(t, c, id)
			q := editRequest(t, c, "", "edit-race-key")
			if first == "edit" {
				c.sem <- struct{}{}
				defer func() { <-c.sem }()
				if _, e = c.RequestEdit(context.Background(), q); e != nil {
					t.Fatal(e)
				}
				if e = c.ContinueAnnouncement(context.Background(), id, "GM", v.Confirmation); e == nil {
					t.Fatal("Continue won after edit reserved the slot")
				}
				op, e := c.announcementOperation(id)
				if e != nil || op.EffectReserved {
					t.Fatal("edit-first reserved an effect", op, e)
				}
			} else {
				cl, e := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
					if m == v1.Sessions_Resume_FullMethodName {
						return status.Error(codes.Unavailable, "synthetic lost native reply")
					}
					return next(ctx, m, req, reply, cc, opts...)
				})))
				if e != nil {
					t.Fatal(e)
				}
				defer cl.Close()
				c.client = cl
				if e = c.ContinueAnnouncement(context.Background(), id, "GM", v.Confirmation); e == nil {
					t.Fatal("expected unknown native outcome")
				}
				if _, e = c.RequestEdit(context.Background(), q); !errors.Is(e, ErrEditConflict) {
					t.Fatal("edit crossed reserved unknown effect", e)
				}
				op, e := c.announcementOperation(id)
				if e != nil || op.Phase != "continue" || op.Stage != "uncertain" || op.EffectSettled {
					t.Fatal("unknown effect fence lost", op, e)
				}
			}
		})
	}
}
