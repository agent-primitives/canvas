package substrateruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/runtime/substrate"
)

// Removing the native Snapshot call would leave the worker live. This Control
// fixture checks the identity reference and resume target; it does not
// exercise the pinned server's native snapshot-selection behavior.
func TestRetainedSnapshotReleasesWorkerAndResumesSameActor(t *testing.T) {
	bundle, _, _ := fixtureHarness(t)
	template, err := TemplateName(bundle, 2)
	if err != nil {
		t.Fatal(err)
	}
	ctl := &controlFixture{actors: map[string]substrate.ActorInfo{}, templateName: template}
	backend, _, err := RetainedFactory(&fixtureControlConn{ctl: ctl}, "fixture-namespace")(bundle, 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	inc, err := backend.Create(ctx, &api.SessionSpec{SessionUID: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := backend.Snapshot(ctx, inc, api.SnapshotExternal)
	if err != nil {
		t.Fatal(err)
	}
	if ref != (api.SnapshotRef{Local: "parent"}) {
		t.Fatalf("not a journal checkpoint: %+v", ref)
	}
	cold, err := backend.Status(ctx, inc)
	if err != nil || cold != api.ComputeCold || ctl.suspends != 1 || ctl.deletes != 0 || len(ctl.actors) != 1 {
		t.Fatalf("snapshot did not release compute and retain actor: %s %v", cold, err)
	}
	if _, err := backend.Restore(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if ctl.creates != 1 || ctl.actors["parent"].Status != substrate.StatusRunning {
		t.Fatal("restore replaced the actor or failed to resume")
	}
	if len(ctl.resumes) != 2 || ctl.resumes[0].actor != (substrate.ActorRef{Atespace: "canvas-draft", Name: "parent"}) || ctl.resumes[1].actor != ctl.resumes[0].actor {
		t.Fatalf("restore resumed a different actor: %+v", ctl.resumes)
	}
	if backend.Capabilities() != (api.RuntimeCapabilities{}) {
		t.Fatal("stateless wrapper advertised RAM restoration")
	}
}

func TestRetainedSnapshotFailureDoesNotInventCheckpoint(t *testing.T) {
	bundle, _, _ := fixtureHarness(t)
	backend, _, err := RetainedFactory(&fixtureControlConn{ctl: &controlFixture{actors: map[string]substrate.ActorInfo{}}}, "fixture-namespace")(bundle, 2)
	if err != nil {
		t.Fatal(err)
	}
	if ref, err := backend.Snapshot(context.Background(), api.Incarnation{ID: "absent"}, api.SnapshotExternal); err == nil || ref != (api.SnapshotRef{}) {
		t.Fatalf("failed snapshot returned a checkpoint: %+v %v", ref, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := backend.Snapshot(ctx, api.Incarnation{ID: "absent"}, api.SnapshotExternal); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
}
