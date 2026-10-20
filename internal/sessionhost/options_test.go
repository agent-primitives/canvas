package sessionhost

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/local"
)

func TestBackendFactoryIncludesArchivedProfilesAndClosesOnFailure(t *testing.T) {
	root := t.TempDir()
	old, current := bundle(t, "old instructions", "old-model"), bundle(t, "current instructions", "current-model")
	h, err := Start(root, []contract.Bundle{old}, archiveProvider)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	seen := map[string]bool{}
	closed := 0
	h, err = StartWithOptions(root, []contract.Bundle{current}, archiveProvider, Options{Listener: lis, BackendFactory: func(b contract.Bundle, profile int) (placement.Backend, func() error, error) {
		har, err := NewBundleHarness(b, profile)
		if err != nil {
			return nil, nil, err
		}
		d, _ := har.Describe(context.Background())
		seen[d.ID] = true
		backend := local.New(har)
		return backend, func() error { closed++; return backend.Close() }, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 6 {
		t.Fatal("archive profiles missing")
	}
	for _, b := range []contract.Bundle{old, current} {
		for _, p := range []int{1, 2, 3} {
			if !seen[contract.HarnessName(b.Digest, p)] {
				t.Fatal("wrong backend identity")
			}
		}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h.Close()
	if closed != 6 {
		t.Fatal("backend ownership not released exactly once")
	}
	lis2, _ := net.Listen("tcp", "127.0.0.1:0")
	calls := 0
	closed = 0
	_, err = StartWithOptions(root, nil, archiveProvider, Options{Listener: lis2, BackendFactory: func(b contract.Bundle, p int) (placement.Backend, func() error, error) {
		calls++
		if calls == 2 {
			return nil, nil, errors.New("fixture failure")
		}
		har, _ := NewBundleHarness(b, p)
		backend := local.New(har)
		return backend, func() error { closed++; return backend.Close() }, nil
	}})
	if err == nil || closed != 1 {
		t.Fatal("failed startup leaked prior backend")
	}
	if _, err := net.Dial("tcp", lis2.Addr().String()); err == nil {
		t.Fatal("failed startup leaked listener")
	}
}

func TestSharedBundleHarnessRejectsForgedIdentityAndProfiles(t *testing.T) {
	b := bundle(t, "fixture instructions", "fixture-model")
	har, err := NewBundleHarness(b, contract.Profile(b))
	if err != nil {
		t.Fatal(err)
	}
	d, err := har.Describe(context.Background())
	if err != nil || d.ID != "bundle-v3-"+b.Digest {
		t.Fatal("legacy default profile changed")
	}
	for _, p := range []int{0, 4, -1} {
		if _, err := NewBundleHarness(b, p); err == nil {
			t.Fatal("unknown profile accepted")
		}
	}
	forged := b
	forged.Model = "different-model"
	if _, err := NewBundleHarness(forged, 2); err == nil {
		t.Fatal("forged fields accepted")
	}
	for _, p := range []int{1, 2, 3} {
		d, err := BundleDescriptor(b, p)
		if err != nil || d.ID != contract.HarnessName(b.Digest, p) || d.Models[0] != b.Model || d.Capabilities.Resumability != api.ResumabilityStatelessReplay {
			t.Fatal("descriptor differs from local binding")
		}
	}
}
