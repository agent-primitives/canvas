package canvas

import (
	"context"
	"sync"
	"testing"

	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestEditParksInflightStep(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c, calls := workflowFixture(t, false, func(input map[string]any) string {
		if input["edit"] == nil {
			once.Do(func() { close(entered); <-release })
		}
		return ""
	})
	f := startWorkflow(t, c, "park-inflight-flow", 12)
	<-entered
	r, e := c.RequestEdit(context.Background(), editRequest(t, c, f.ID, "park-inflight-edit"))
	if e != nil {
		close(release)
		t.Fatal(e)
	}
	close(release)
	r = waitEdit(t, c, r.ID, "failed")
	c.wg.Wait()
	before, e := c.GetFlow(context.Background(), f.ID)
	if e != nil || before.State != "edit-paused" || before.Proposal != "" {
		t.Fatal("inflight output advanced through edit", before, e)
	}
	original := c.client
	cl, e := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if m == v1.Sessions_GetSession_FullMethodName {
			return status.Error(codes.Unavailable, "synthetic read outage")
		}
		return next(ctx, m, req, reply, cc, opts...)
	})))
	if e != nil {
		t.Fatal(e)
	}
	defer cl.Close()
	c.client = cl
	q := EditMaintenance{Edit: r.ID, Role: "GM", RequestKey: "resume-inflight-exact", ExpectedHead: mustHead(t, c), Confirmed: true}
	if _, e = c.ResumeAfterEdit(context.Background(), q); e == nil {
		t.Fatal("expected native inspection outage")
	}
	c.client = original
	if _, e = c.ResumeAfterEdit(context.Background(), q); e != nil {
		t.Fatal("exact pending resume refused", e)
	}
	after := waitWorkflow(t, c, f.ID, "review")
	if after.Attempts != 2 || calls.Load() != 2 {
		t.Fatal("resume reran a producer or refunded edit budget", after, calls.Load())
	}
	ps, e := c.space.Proposals()
	if e != nil || len(ps) != 1 {
		t.Fatal("resume duplicated ordinary proposal", ps, e)
	}
}
