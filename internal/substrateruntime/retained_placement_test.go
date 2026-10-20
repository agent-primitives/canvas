//go:build aks_demo

package substrateruntime

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/harnesswire"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/substrate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Completed turns are not re-executed on resume or stateless fork. This checks
// placement and zero additional model calls, not interrupted-turn reconstruction
// or the Substrate server's snapshot-selection behavior.
func TestRetainedPlacementCompletedResumeReschedulesWithoutModelCalls(t *testing.T) {
	bundle, original, _ := fixtureHarness(t)
	harness := &retainedRunWitness{Harness: original}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	v1.RegisterHarnessServer(server, harnesswire.NewServer(harness))
	go server.Serve(lis)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	remote := harnesswire.NewClientHarness(v1.NewHarnessClient(conn))
	template, err := TemplateName(bundle, 2)
	if err != nil {
		t.Fatal(err)
	}
	ctl := &controlFixture{actors: map[string]substrate.ActorInfo{}, templateName: template}
	backend, _, err := RetainedFactory(&fixtureControlConn{ctl: ctl}, "fixture-namespace")(bundle, 2)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	p := placement.New(backend, func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls++
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{"strategy":"fixture"}`)}, nil
	}, placement.WithDialer(func(inc api.Incarnation) (api.Harness, func() error, error) {
		assertActorRoute(t, inc)
		return remote, func() error { return nil }, nil
	}))
	log := eventlog.AsStore(eventlog.New())
	ctx := context.Background()
	if _, err := p.Exec(ctx, log, "parent", []api.Message{*api.TextMessage("user", `{"mode":"scenario"}`)}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Suspend(ctx, log, "parent"); err != nil {
		t.Fatal(err)
	}
	if ctl.suspends != 1 || ctl.deletes != 0 || ctl.actors["parent"].Status != substrate.StatusSuspended {
		t.Fatal("suspend did not retain a cold actor")
	}
	if err := p.Resume(ctx, log, "parent"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || ctl.creates != 1 || harness.runs.Load() != 1 {
		t.Fatal("completed resume re-executed a turn, called the model, or replaced the actor")
	}
	assertResumeActors(t, ctl.resumes)
	if err := log.Verify(); err != nil {
		t.Fatal(err)
	}
	head, err := log.Head()
	if err != nil {
		t.Fatal(err)
	}
	children := []placement.ForkChild{{UID: "child-a", Log: eventlog.AsStore(eventlog.New())}, {UID: "child-b", Log: eventlog.AsStore(eventlog.New())}}
	if err := p.Fork(ctx, log, "parent", children, head); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || ctl.creates != 3 || len(ctl.actors) != 3 || harness.runs.Load() != 1 {
		t.Fatal("fork failed to copy two prefixes without re-executing completed turns")
	}
	for _, child := range children {
		if err := child.Log.Verify(); err != nil {
			t.Fatal(err)
		}
	}
}

var errRetainedInterruption = errors.New("fixture interrupted after recorded model result")

type retainedRunWitness struct {
	api.Harness
	runs           atomic.Int32
	interruptFirst bool
}

func (h *retainedRunWitness) Run(ctx context.Context, start *api.Start, sink api.EventSink) error {
	run := h.runs.Add(1)
	if err := h.Harness.Run(ctx, start, sink); err != nil {
		return err
	}
	if h.interruptFirst && run == 1 {
		return errRetainedInterruption
	}
	return nil
}

func TestRetainedPlacementInterruptedResumeServesRecordedModelResult(t *testing.T) {
	bundle, original, _ := fixtureHarness(t)
	harness := &retainedRunWitness{Harness: original, interruptFirst: true}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	v1.RegisterHarnessServer(server, harnesswire.NewServer(harness))
	go server.Serve(lis)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	remote := harnesswire.NewClientHarness(v1.NewHarnessClient(conn))
	template, err := TemplateName(bundle, 2)
	if err != nil {
		t.Fatal(err)
	}
	ctl := &controlFixture{actors: map[string]substrate.ActorInfo{}, templateName: template}
	backend, _, err := RetainedFactory(&fixtureControlConn{ctl: ctl}, "fixture-namespace")(bundle, 2)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	p := placement.New(backend, func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls++
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{"strategy":"fixture"}`)}, nil
	}, placement.WithDialer(func(inc api.Incarnation) (api.Harness, func() error, error) {
		assertActorRoute(t, inc)
		return remote, func() error { return nil }, nil
	}))
	log := eventlog.AsStore(eventlog.New())
	ctx := context.Background()
	if _, err := p.Exec(ctx, log, "parent", []api.Message{*api.TextMessage("user", `{"mode":"scenario"}`)}, 0); err == nil || !strings.Contains(err.Error(), errRetainedInterruption.Error()) {
		t.Fatalf("expected the fixture's post-model interruption, got %v", err)
	}
	if calls != 1 || harness.runs.Load() != 1 {
		t.Fatal("interruption did not follow exactly one recorded model result")
	}
	if _, err := p.Suspend(ctx, log, "parent"); err != nil {
		t.Fatal(err)
	}
	if err := p.Resume(ctx, log, "parent"); err != nil {
		t.Fatal(err)
	}
	if harness.runs.Load() != 2 || calls != 1 {
		t.Fatalf("resume did not re-drive the harness with its recorded model result: runs=%d model calls=%d", harness.runs.Load(), calls)
	}
	records, err := log.Read(1)
	if err != nil {
		t.Fatal(err)
	}
	modelRecords, ends := 0, 0
	for _, record := range records {
		switch record.Event.Kind {
		case api.EventModelCall:
			modelRecords++
		case api.EventEnd:
			ends++
		}
	}
	if modelRecords != 1 || ends != 1 {
		t.Fatalf("recovery journal has model calls=%d ends=%d, want one recorded result and completed turn", modelRecords, ends)
	}
	if err := p.Resume(ctx, log, "parent"); err != nil {
		t.Fatal(err)
	}
	if harness.runs.Load() != 2 || calls != 1 {
		t.Fatal("completed recovery was re-executed")
	}
	if err := log.Verify(); err != nil {
		t.Fatal(err)
	}
}
