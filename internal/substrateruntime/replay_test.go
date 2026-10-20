package substrateruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/harnesswire"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/substrate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type controlFixture struct {
	actors                     map[string]substrate.ActorInfo
	creates, suspends, deletes int
	failChild                  string
	templateName               string
	deleteErr, resumeErr       error
	resumes                    []resumeCall
}

func (c *controlFixture) GetActor(_ context.Context, a substrate.ActorRef) (substrate.ActorInfo, error) {
	i, ok := c.actors[a.Name]
	if !ok {
		return i, substrate.ErrActorNotFound
	}
	return i, nil
}
func (c *controlFixture) CreateActor(_ context.Context, a substrate.ActorRef, template substrate.ObjectRef) error {
	name := c.templateName
	if name == "" {
		name = "fixture"
	}
	if a.Atespace != "canvas-draft" || template.Atespace != "fixture-namespace" || template.Name != name {
		return errors.New("wrong routing")
	}
	c.creates++
	c.actors[a.Name] = substrate.ActorInfo{Status: substrate.StatusSuspended}
	return nil
}
func (c *controlFixture) ResumeActor(_ context.Context, a substrate.ActorRef) (substrate.ActorInfo, error) {
	c.resumes = append(c.resumes, resumeCall{actor: a})
	if c.resumeErr != nil {
		return substrate.ActorInfo{}, c.resumeErr
	}
	if _, ok := c.actors[a.Name]; !ok {
		return substrate.ActorInfo{}, substrate.ErrActorNotFound
	}
	i := substrate.ActorInfo{Status: substrate.StatusRunning, Worker: "fixture-worker"}
	c.actors[a.Name] = i
	if a.Name == c.failChild {
		i.Status = substrate.StatusUnknown
	}
	return i, nil
}
func (c *controlFixture) SuspendActor(_ context.Context, a substrate.ActorRef) (substrate.ActorInfo, error) {
	i, ok := c.actors[a.Name]
	if !ok || i.Status != substrate.StatusRunning {
		return substrate.ActorInfo{}, errors.New("not running (double suspend)")
	}
	c.suspends++
	i.Status = substrate.StatusSuspended
	i.Snapshot = "snapshot"
	c.actors[a.Name] = i
	return i, nil
}
func (c *controlFixture) DeleteActor(_ context.Context, a substrate.ActorRef) error {
	if c.actors[a.Name].Status != substrate.StatusSuspended {
		return errors.New("not suspended")
	}
	c.deletes++
	if c.deleteErr != nil {
		return c.deleteErr
	}
	delete(c.actors, a.Name)
	return nil
}
func fixtureHarness(t *testing.T) (contract.Bundle, api.Harness, api.Descriptor) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata: {name: replay-fixture}\nspec:\n  instructions: Summarize the supplied synthetic document.\n  model: {name: qwen3:8b}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := contract.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := sessionhost.NewBundleHarness(b, 2)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := h.Describe(context.Background())
	return b, h, d
}
func TestPlacementSuspendDeletesOnceAndResumeRecreatesWithoutModelCalls(t *testing.T) {
	_, h, d := fixtureHarness(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	v1.RegisterHarnessServer(server, harnesswire.NewServer(h))
	go server.Serve(lis)
	defer server.Stop()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	remote := harnesswire.NewClientHarness(v1.NewHarnessClient(conn))
	ctl := &controlFixture{actors: map[string]substrate.ActorInfo{}}
	native := substrate.New(ctl, "canvas-draft", substrate.ObjectRef{Atespace: "fixture-namespace", Name: "fixture"}, d)
	backend, err := StatelessReplay(native)
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
	if _, err = p.Exec(ctx, log, "parent", []api.Message{*api.TextMessage("user", `{"mode":"scenario"}`)}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Suspend(ctx, log, "parent"); err != nil {
		t.Fatal(err)
	}
	if ctl.suspends != 1 || ctl.deletes != 1 || len(ctl.actors) != 0 {
		t.Fatal("suspend did not release exactly once")
	}
	if err = p.Resume(ctx, log, "parent"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || ctl.creates != 2 || len(ctl.actors) != 1 {
		t.Fatal("resume failed to recreate and replay")
	}
	head, _ := log.Head()
	child := eventlog.AsStore(eventlog.New())
	if err = p.Fork(ctx, log, "parent", []placement.ForkChild{{UID: "child", Log: child}}, head); err != nil {
		t.Fatal(err)
	}
	if ctl.creates != 3 || ctl.suspends != 1 || len(ctl.actors) != 2 {
		t.Fatal("stateless fork touched parent or failed fresh child")
	}
	if err = p.Resume(ctx, child, "child"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || ctl.creates != 3 {
		t.Fatal("child restored inherited parent checkpoint or called model")
	}
	state, err := backend.Status(ctx, api.Incarnation{ID: "child"})
	if err != nil || state != api.ComputeLive {
		t.Fatal("native status lost")
	}
	if err = backend.Stop(ctx, api.Incarnation{ID: "child"}); err != nil {
		t.Fatal(err)
	}
	ctl.failChild = "orphan"
	if _, err = backend.Fork(ctx, api.SnapshotRef{Local: "parent"}, api.ForkOpts{ChildSessionUID: "orphan"}); err == nil {
		t.Fatal("scheduling error accepted")
	}
	if _, ok := ctl.actors["orphan"]; ok {
		t.Fatal("failed fork leaked actor")
	}
	if ctl.suspends != 3 || ctl.deletes != 3 {
		t.Fatal("native fork cleanup not retained")
	}
	if err = backend.Stop(ctx, api.Incarnation{ID: "parent"}); err != nil {
		t.Fatal(err)
	}
}
func TestReplayWrapperRejectsMemoryInvalidSnapshotAndCanceledContext(t *testing.T) {
	_, _, d := fixtureHarness(t)
	ctl := &controlFixture{actors: map[string]substrate.ActorInfo{}}
	b, _ := StatelessReplay(substrate.New(ctl, "canvas-draft", substrate.ObjectRef{Atespace: "fixture-namespace", Name: "fixture"}, d))
	if b.Capabilities() != (api.RuntimeCapabilities{}) {
		t.Fatal("memory capabilities advertised")
	}
	for _, kind := range []api.SnapshotKind{api.SnapshotLocal, api.SnapshotKind("invalid")} {
		if _, err := b.Snapshot(context.Background(), api.Incarnation{ID: "session"}, kind); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
	if _, err := b.Snapshot(context.Background(), api.Incarnation{}, api.SnapshotExternal); err == nil {
		t.Fatal("empty identity accepted")
	}
	for _, ref := range []api.SnapshotRef{{}, {Local: "session", Memory: true}, {Local: "session", ExternalURI: "snapshot"}} {
		if _, err := b.Restore(context.Background(), ref); err == nil {
			t.Fatal("invalid restore accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Snapshot(ctx, api.Incarnation{ID: "session"}, api.SnapshotExternal); !errors.Is(err, context.Canceled) {
		t.Fatal("snapshot ignored cancellation")
	}
	if _, err := b.Restore(ctx, api.SnapshotRef{Local: "session"}); !errors.Is(err, context.Canceled) {
		t.Fatal("restore ignored cancellation")
	}
	d.Capabilities.Resumability = api.ResumabilityRequiresMemorySnapshot
	if _, err := StatelessReplay(substrate.New(ctl, "canvas-draft", substrate.ObjectRef{Atespace: "fixture-namespace", Name: "fixture"}, d)); err == nil {
		t.Fatal("memory descriptor accepted")
	}
}

// Snapshot must own compute release now that placement no longer calls Stop.
// Retained mode must validate without entering the destructive default path.
func TestSnapshotOwnsReleasePolicyAndPropagatesFailure(t *testing.T) {
	for _, retain := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("retain=%t/fail=%t", retain, fail), func(t *testing.T) {
				releaseErr := errors.New("fixture release failed")
				native := &releaseWitness{}
				if fail {
					native.err = releaseErr
				}
				var backend placement.Backend = replayBackend{native}
				if retain {
					backend = retainedReplayBackend{replayBackend{native}}
				}
				inc := api.Incarnation{ID: "session"}
				ref, err := backend.Snapshot(context.Background(), inc, api.SnapshotExternal)
				wantStops, wantSnapshots := 1, 0
				if retain {
					wantStops, wantSnapshots = 0, 1
				}
				if native.stops != wantStops || native.snapshots != wantSnapshots || native.inc.ID != "session" {
					t.Fatalf("release calls: stops=%d snapshots=%d incarnation=%+v", native.stops, native.snapshots, native.inc)
				}
				if fail {
					if !errors.Is(err, releaseErr) || ref != (api.SnapshotRef{}) {
						t.Fatalf("failed release invented checkpoint: %+v %v", ref, err)
					}
				} else if err != nil || ref != (api.SnapshotRef{Local: "session"}) {
					t.Fatalf("identity checkpoint = %+v, %v", ref, err)
				}
				for _, kind := range []api.SnapshotKind{api.SnapshotLocal, api.SnapshotKind("invalid")} {
					if _, err := backend.Snapshot(context.Background(), inc, kind); err == nil {
						t.Fatal("invalid kind accepted")
					}
				}
				if _, err := backend.Snapshot(context.Background(), api.Incarnation{}, api.SnapshotExternal); err == nil {
					t.Fatal("empty identity accepted")
				}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, err := backend.Snapshot(ctx, inc, api.SnapshotExternal); !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation ignored")
				}
				if native.stops != wantStops || native.snapshots != wantSnapshots {
					t.Fatal("invalid snapshot touched compute")
				}
			})
		}
	}
}

type releaseWitness struct {
	placement.Backend
	inc              api.Incarnation
	stops, snapshots int
	err              error
}

func (b *releaseWitness) Stop(_ context.Context, inc api.Incarnation) error {
	b.inc = inc
	b.stops++
	return b.err
}

func (b *releaseWitness) Snapshot(_ context.Context, inc api.Incarnation, _ api.SnapshotKind) (api.SnapshotRef, error) {
	b.inc = inc
	b.snapshots++
	return api.SnapshotRef{Local: inc.ID, Memory: true, ExternalURI: "native-snapshot"}, b.err
}

func TestTemplateNamesAreBoundedAndProfileSpecific(t *testing.T) {
	b, _, _ := fixtureHarness(t)
	a, err := TemplateName(b, 1)
	if err != nil {
		t.Fatal(err)
	}
	two, err := TemplateName(b, 2)
	if err != nil || a == two || len(a) > 63 || len(two) > 63 {
		t.Fatal("template name collides or exceeds label bound")
	}
	again, _ := TemplateName(b, 1)
	if a != again {
		t.Fatal("nondeterministic template")
	}
	b.Digest = "forged"
	if _, err = TemplateName(b, 1); err == nil {
		t.Fatal("forged identity accepted")
	}
}
