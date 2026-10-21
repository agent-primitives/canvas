package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc"
)

// Caller lookup lies between the old pre-lock fence and publicationMu. Holding
// it lets a real edit reservation win without sleeps or fabricated native state.
type deniedCleanupCallerBarrier struct {
	context.Context
	once             sync.Once
	entered, release chan struct{}
}

func (c *deniedCleanupCallerBarrier) Value(key any) any {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Context.Value(key)
}

func TestEditSlotFencesDeniedCleanupAtPublicationLock(t *testing.T) {
	c, f := readyFlowAction(t)
	c.wg.Wait()
	v, err := c.InspectAnnouncement(context.Background(), f.Action)
	if err != nil {
		t.Fatal(err)
	}
	v.Confirmation.Confirmed = true
	if err = c.DecideAnnouncement(context.Background(), f.Action, "reviewer", false, v.Confirmation); err != nil {
		t.Fatal(err)
	}
	v, err = c.InspectAnnouncement(context.Background(), f.Action)
	if err != nil {
		t.Fatal(err)
	}
	v.Confirmation.Confirmed = true
	before, err := c.announcementOperation(f.Action)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	head := mustHead(t, c)
	var resumes atomic.Int32
	cl, err := client.Dial(c.address, client.WithProject(c.project()), client.WithDialOptions(grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, next grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if method == v1.Sessions_Resume_FullMethodName {
			resumes.Add(1)
		}
		return next(ctx, method, req, reply, cc, opts...)
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	c.client = cl
	// Keep the newly admitted editor queued while checking the action fence.
	c.sem <- struct{}{}
	defer func() { <-c.sem }()
	barrier := &deniedCleanupCallerBarrier{Context: context.Background(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- c.ContinueAnnouncement(barrier, f.Action, "reviewer", v.Confirmation) }()
	<-barrier.entered
	r, err := c.RequestEdit(context.Background(), editRequest(t, c, "", "edit-before-denied-cleanup"))
	if err != nil {
		close(barrier.release)
		<-done
		t.Fatal(err)
	}
	close(barrier.release)
	err = <-done
	if !errors.Is(err, ErrEditConflict) || resumes.Load() != 0 {
		t.Fatalf("denied cleanup crossed edit fence: err=%v ResumeRPCs=%d", err, resumes.Load())
	}
	c.mu.Lock()
	slot := c.data.EditSlot
	c.mu.Unlock()
	after, err := c.announcementOperation(f.Action)
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, _ := json.Marshal(after)
	fresh, err := c.InspectAnnouncement(context.Background(), f.Action)
	if err != nil || fresh.Concluded || fresh.Confirmation.Head != v.Confirmation.Head || fresh.Confirmation.ExecutionID != v.Confirmation.ExecutionID {
		t.Fatal("refused cleanup changed native evidence", fresh, err)
	}
	if slot != r.ID || string(beforeJSON) != string(afterJSON) || head != mustHead(t, c) {
		t.Fatal("refused cleanup changed edit slot, action journal or accepted head")
	}
}
