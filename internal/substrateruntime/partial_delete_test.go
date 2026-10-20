package substrateruntime

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/harnesswire"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/substrate"
	"github.com/agent-primitives/sessions/sqlitelog"
	atepb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type resumeCall struct {
	actor substrate.ActorRef
}

// fixtureControlConn keeps the real factory and pinned adapter/backend in the
// test, replacing only the external Control RPCs with an actor lifecycle double.
type fixtureControlConn struct {
	grpc.ClientConnInterface
	ctl   *controlFixture
	calls int
}

func (c *fixtureControlConn) Invoke(ctx context.Context, _ string, args, reply any, _ ...grpc.CallOption) error {
	c.calls++
	ref := func(a *atepb.ObjectRef) substrate.ActorRef {
		return substrate.ActorRef{Atespace: a.GetAtespace(), Name: a.GetName()}
	}
	actor := func(i substrate.ActorInfo) *atepb.Actor {
		s := atepb.ActorState_ACTOR_STATE_SUSPENDED
		if i.Status == substrate.StatusRunning {
			s = atepb.ActorState_ACTOR_STATE_RUNNING
		}
		st := &atepb.ActorStatus{State: s}
		if i.Snapshot != "" {
			st.Snapshots = []*atepb.Snapshot{{
				Generation: 1,
				DurableSnapshot: &atepb.SnapshotStorage{
					Status: atepb.SnapshotStorageStatus_SNAPSHOT_STORAGE_STATUS_COMPLETED,
					Object: &atepb.ObjectSnapshot{SnapshotUri: i.Snapshot},
				},
			}}
			st.LastAssignedGeneration = 1
		}
		return &atepb.Actor{Status: st}
	}
	switch req := args.(type) {
	case *atepb.GetActorRequest:
		i, err := c.ctl.GetActor(ctx, ref(req.GetActor()))
		if errors.Is(err, substrate.ErrActorNotFound) {
			return status.Error(codes.NotFound, "fixture actor absent")
		}
		if err == nil {
			*reply.(*atepb.Actor) = *actor(i)
		}
		return err
	case *atepb.CreateActorRequest:
		a := req.GetActor()
		return c.ctl.CreateActor(ctx, substrate.ActorRef{Atespace: a.GetMetadata().GetAtespace(), Name: a.GetMetadata().GetName()}, substrate.ObjectRef{Atespace: a.GetActorTemplate().GetAtespace(), Name: a.GetActorTemplate().GetName()})
	case *atepb.ResumeActorRequest:
		i, err := c.ctl.ResumeActor(ctx, ref(req.GetActor()))
		if err == nil {
			reply.(*atepb.ResumeActorResponse).Actor = actor(i)
		}
		return err
	case *atepb.SuspendActorRequest:
		info, err := c.ctl.SuspendActor(ctx, ref(req.GetActor()))
		if err == nil {
			reply.(*atepb.SuspendActorResponse).Actor = actor(info)
		}
		return err
	case *atepb.DeleteActorRequest:
		return c.ctl.DeleteActor(ctx, ref(req.GetActor()))
	case *atepb.DeleteTagRequest:
		return status.Error(codes.NotFound, "stateless fixture has no snapshot tags")
	default:
		return errors.New("unexpected Control RPC")
	}
}

func TestFactoryPartialDeleteRecoveryUsesSameActorAndJournalReplayPreservesOutput(t *testing.T) {
	bundle, h, d := fixtureHarness(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	v1.RegisterHarnessServer(server, harnesswire.NewServer(h))
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
	control := &fixtureControlConn{ctl: ctl}
	backend, cleanup, err := Factory(control, "fixture-namespace")(bundle, 2)
	if err != nil || cleanup != nil {
		t.Fatalf("factory failed: %v", err)
	}
	described, err := backend.Describe(t.Context())
	if err != nil || described.ID != d.ID || control.calls != 0 {
		t.Fatal("constructor/Describe made a Control RPC or lost the cached descriptor")
	}
	if backend.Capabilities() != (api.RuntimeCapabilities{}) {
		t.Fatal("memory capabilities advertised")
	}

	calls := 0
	model := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls++
		if calls != 1 {
			return api.ModelResponse{}, errors.New("unexpected live model call")
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", `{"strategy":"fixture"}`)}, nil
	}
	p := placement.New(backend, model, placement.WithDialer(func(inc api.Incarnation) (api.Harness, func() error, error) {
		assertActorRoute(t, inc)
		return remote, func() error { return nil }, nil
	}))
	ctx := t.Context()
	store, err := sqlitelog.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	parent := store.Session("parent")
	if _, err := p.Exec(ctx, parent, "parent", []api.Message{*api.TextMessage("user", `{"mode":"scenario"}`)}, 0); err != nil {
		t.Fatal(err)
	}
	deleteErr := errors.New("fixture delete failure")
	ctl.deleteErr = deleteErr
	if _, err := p.Suspend(ctx, parent, "parent"); !errors.Is(err, deleteErr) {
		t.Fatalf("partial delete failure not visible: %v", err)
	}
	if ctl.suspends != 1 || ctl.deletes != 1 || ctl.actors["parent"].Status != substrate.StatusSuspended {
		t.Fatal("fixture did not leave one suspended actor after a single suspend/delete")
	}
	// Snapshot failed before publishing a checkpoint; Resume recovers via the session UID.
	if records, err := parent.Read(1); err != nil {
		t.Fatal(err)
	} else {
		for _, record := range records {
			if record.Event.Lifecycle != nil && record.Event.Lifecycle.Kind == api.LifecycleSuspend {
				t.Fatal("failed delete published a SUSPEND checkpoint")
			}
		}
	}
	resumeErr := errors.New("fixture scheduling failure")
	ctl.resumeErr = resumeErr
	if err := p.Resume(ctx, parent, "parent"); !errors.Is(err, resumeErr) {
		t.Fatalf("resume failure not visible: %v", err)
	}
	ctl.resumeErr = nil
	if err := p.Resume(ctx, parent, "parent"); err != nil {
		t.Fatal(err)
	}
	if ctl.creates != 1 || calls != 1 {
		t.Fatal("partial-delete recovery recreated the actor or called the model")
	}
	assertResumeActors(t, ctl.resumes)

	head, err := parent.Head()
	if err != nil {
		t.Fatal(err)
	}
	child := store.Session("child")
	if err := p.Fork(ctx, parent, "parent", []placement.ForkChild{{UID: "child", Log: child}}, head); err != nil {
		t.Fatal(err)
	}
	if err := p.Resume(ctx, child, "child"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Suspend(ctx, child, "child"); !errors.Is(err, deleteErr) {
		t.Fatalf("child partial delete failure not visible: %v", err)
	}
	// Exercise Create's retry branch too, independently of Restore.
	inc, err := backend.Create(ctx, &api.SessionSpec{SessionUID: "child"})
	if err != nil || inc.ID != "child" {
		t.Fatalf("child retry lost its UID: %v", err)
	}
	if err := p.Resume(ctx, child, "child"); err != nil {
		t.Fatal(err)
	}
	if ctl.creates != 2 || ctl.suspends != 2 || ctl.deletes != 2 || calls != 1 {
		t.Fatal("child replay provisioned the parent, double-suspended, or called the model")
	}
	if last := ctl.resumes[len(ctl.resumes)-1]; last.actor != (substrate.ActorRef{Atespace: "canvas-draft", Name: "child"}) {
		t.Fatal("child recovery resumed the wrong actor")
	}
	assertResumeActors(t, ctl.resumes)

	for name, log := range map[string]eventlog.Store{"parent": parent, "child": child} {
		t.Run(name, func(t *testing.T) {
			if err := log.Verify(); err != nil {
				t.Fatal(err)
			}
			replayCalls := 0
			journal, err := controller.New(log, func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
				replayCalls++
				return api.ModelResponse{}, errors.New("journal replay must not invoke model")
			})
			if err != nil {
				t.Fatal(err)
			}
			outputs, err := journal.Replay(ctx, remote)
			if err != nil || !slices.Equal(outputs, []string{`{"strategy":"fixture"}`}) {
				t.Fatalf("journal replay did not reconstruct the recorded output: %v", err)
			}
			if replayCalls != 0 || journal.ModelInvocations() != 0 {
				t.Fatal("journal replay invoked the model")
			}
		})
	}
}

func assertResumeActors(t *testing.T, resumes []resumeCall) {
	t.Helper()
	if len(resumes) == 0 {
		t.Fatal("no actor was resumed")
	}
	for _, call := range resumes {
		if call.actor.Atespace != "canvas-draft" || call.actor.Name == "" {
			t.Fatalf("invalid resume target: %+v", call.actor)
		}
	}
}

func assertActorRoute(t *testing.T, inc api.Incarnation) {
	t.Helper()
	if inc.ID == "" || inc.Address != "atenet-router.ate-system.svc:80" || len(inc.CallMetadata) != 1 || inc.CallMetadata["ate-target-actor"] != "canvas-draft/"+inc.ID {
		t.Fatalf("wrong actor router address or metadata: %+v", inc)
	}
}
