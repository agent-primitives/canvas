package sessionclient

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMissingDiagnosticsIsMissingEvidence(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen failed")
	}
	srv := grpc.NewServer()
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := Verify(ctx, lis.Addr().String(), "fixture-session"); status.Code(err) != codes.Unimplemented {
		t.Fatal("missing diagnostics must report unavailable evidence")
	}
}
