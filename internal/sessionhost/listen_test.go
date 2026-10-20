package sessionhost

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/client"
)

func TestStartAtRefusesNonLoopbackBeforeWritingState(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8766", ":8766", "localhost:8766", "[::]:8766", "192.0.2.1:8766", "127.0.0.1:invalid"} {
		root := filepath.Join(t.TempDir(), "host")
		provider := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
			t.Fatal("unexpected model call")
			return api.ModelResponse{}, nil
		}
		if _, err := StartAt(root, address, nil, provider); err == nil {
			t.Fatal("unsafe listener accepted")
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("invalid listener touched private state")
		}
	}
}

func TestStartAtUsesRequestedAddressAndReleasesListener(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("cannot reserve listener")
	}
	address := reserved.Addr().String()
	reserved.Close()
	b := bundle(t, "Assess fictional evidence.", "test-model")
	provider := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		t.Fatal("unexpected model call")
		return api.ModelResponse{}, nil
	}
	h, err := StartAt(t.TempDir(), address, []contract.Bundle{b}, provider)
	if err != nil {
		t.Fatal("host failed to start")
	}
	t.Cleanup(func() { h.Close() })
	if h.Address() != address {
		t.Fatal("requested address not used")
	}
	cl, err := client.Dial(address)
	if err != nil {
		t.Fatal("cannot dial host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = cl.ListSessions(ctx, "fixture")
	cl.Close()
	if err != nil {
		t.Fatal("gRPC host unavailable")
	}
	if err := h.Close(); err != nil {
		t.Fatal("host close failed")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal("listener not released")
	}
	listener.Close()
}

func TestDoneReportsServerFailure(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := bundle(t, "Assess fictional evidence.", "test-model")
	h, err := StartWithOptions(t.TempDir(), []contract.Bundle{b}, func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{}, nil
	}, Options{Listener: lis})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	select {
	case <-h.Done():
		t.Fatal("server reported done while serving")
	default:
	}
	lis.Close()
	select {
	case err := <-h.Done():
		if err == nil {
			t.Fatal("server failure reported without an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server failure not reported")
	}
}
