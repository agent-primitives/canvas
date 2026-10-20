// Package substrateruntime assembles the pinned Substrate backend for Canvas.
package substrateruntime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/placement"
)

// replayBackend hides native memory snapshots. Snapshot owns compute release:
// native Stop suspends then attempts deletion, preserving the default policy.
// Restore creates or attaches by session identity before journal replay.
type replayBackend struct{ placement.Backend }

// StatelessReplay hides memory-snapshot references from Sessions and restores by
// session identity. Substrate chooses the activation source: an existing actor's
// checkpoint, a template snapshot, or a fresh start. This is not a snapshot-free
// activation guarantee.
func StatelessReplay(native placement.Backend) (placement.Backend, error) {
	if native == nil {
		return nil, errors.New("substrate: backend required")
	}
	d, err := native.Describe(context.Background())
	if err != nil || d.Capabilities.Resumability != api.ResumabilityStatelessReplay {
		return nil, errors.New("substrate: only stateless replay descriptors supported")
	}
	return replayBackend{native}, nil
}
func (b replayBackend) Capabilities() api.RuntimeCapabilities { return api.RuntimeCapabilities{} }
func (b replayBackend) Snapshot(ctx context.Context, in api.Incarnation, kind api.SnapshotKind) (api.SnapshotRef, error) {
	ref, err := identitySnapshot(ctx, in, kind)
	if err != nil {
		return api.SnapshotRef{}, err
	}
	if err := b.Backend.Stop(ctx, in); err != nil {
		return api.SnapshotRef{}, err
	}
	return ref, nil
}

// identitySnapshot validates without touching compute, shared by both policies.
func identitySnapshot(ctx context.Context, in api.Incarnation, kind api.SnapshotKind) (api.SnapshotRef, error) {
	if err := ctx.Err(); err != nil {
		return api.SnapshotRef{}, err
	}
	if kind != api.SnapshotExternal || in.ID == "" {
		return api.SnapshotRef{}, errors.New("substrate: external snapshot and incarnation identity required")
	}
	return api.SnapshotRef{Local: in.ID, Memory: false}, nil
}
func (b replayBackend) Restore(ctx context.Context, ref api.SnapshotRef) (api.Incarnation, error) {
	if err := ctx.Err(); err != nil {
		return api.Incarnation{}, err
	}
	if ref.Local == "" || ref.Memory || ref.ExternalURI != "" {
		return api.Incarnation{}, errors.New("substrate: stateless identity snapshot required")
	}
	return b.Create(ctx, &api.SessionSpec{SessionUID: ref.Local})
}

// retainedReplayBackend checkpoints through Substrate to release the worker and
// retain the actor/snapshot, but returns a journal identity checkpoint. Restore
// uses Create with that identity; the pinned server can still select the actor's
// native snapshot. The Sessions reference alone does not bypass RAM restoration.
// Stop remains destructive teardown, including failed-fork cleanup.
type retainedReplayBackend struct{ replayBackend }

func (b retainedReplayBackend) Snapshot(ctx context.Context, in api.Incarnation, kind api.SnapshotKind) (api.SnapshotRef, error) {
	ref, err := identitySnapshot(ctx, in, kind)
	if err != nil {
		return api.SnapshotRef{}, err
	}
	if _, err := b.Backend.Snapshot(ctx, in, kind); err != nil {
		return api.SnapshotRef{}, err
	}
	return ref, nil
}

// TemplateName hashes the immutable binding (digest plus execution profile), not
// a mutable image tag. The full Sessions harness identity remains in the journal.
func TemplateName(b contract.Bundle, profile int) (string, error) {
	d, err := sessionhost.BundleDescriptor(b, profile)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(d.ID))
	return fmt.Sprintf("canvas-%x", sum[:28]), nil
}
