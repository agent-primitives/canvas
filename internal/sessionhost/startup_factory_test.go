package sessionhost

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/sessions/placement"
)

func TestBackendFactoryLocalOnlyErrorRetainsContextAndCloses(t *testing.T) {
	b := nativeBundle(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var reason error
	closed := 0
	h, err := StartWithOptions(t.TempDir(), []contract.Bundle{b}, archiveProvider, Options{
		Listener: listener,
		BackendFactory: func(b contract.Bundle, profile int) (placement.Backend, func() error, error) {
			_, reason = BundleDescriptor(b, profile)
			return nil, func() error { closed++; return nil }, reason
		},
	})
	if h != nil {
		h.Close()
		t.Fatal("local-only remote factory unexpectedly started")
	}
	if err == nil || !strings.Contains(err.Error(), "sessionhost: cannot configure backend") || !strings.Contains(err.Error(), "local-only") || reason == nil || !errors.Is(err, reason) {
		t.Fatalf("factory refusal lost its local-only reason: %v", err)
	}
	if closed != 1 {
		t.Fatalf("factory closer called %d times, want once", closed)
	}
	if conn, err := net.Dial("tcp", listener.Addr().String()); err == nil {
		conn.Close()
		t.Fatal("failed startup leaked its listener")
	}
}
