package substrateruntime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	atepb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestProjectedTokenRotatesAndNeverFallsBackToEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv("SUBSTRATE_TOKEN", "ambient-not-allowed")
	creds := fileToken{path: path}
	if !creds.RequireTransportSecurity() {
		t.Fatal("insecure token transport")
	}
	if _, err := creds.GetRequestMetadata(context.Background()); err == nil {
		t.Fatal("ambient token used")
	}
	for _, token := range []string{"fixture-first", "fixture-rotated"} {
		if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		m, err := creds.GetRequestMetadata(context.Background())
		if err != nil || m["authorization"] != "Bearer "+token {
			t.Fatal("projected token did not rotate")
		}
	}
	os.WriteFile(path, []byte("bad\r\nheader"), 0600)
	if _, err := creds.GetRequestMetadata(context.Background()); err == nil {
		t.Fatal("invalid header token accepted")
	}
}

type controlServer struct {
	atepb.UnimplementedControlServer
	tokens []string
	mu     sync.Mutex
}

func (s *controlServer) CreateAtespace(ctx context.Context, req *atepb.CreateAtespaceRequest) (*atepb.Atespace, error) {
	if req.GetAtespace().GetMetadata().GetName() != "canvas-draft" {
		return nil, status.Error(codes.InvalidArgument, "wrong identity")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	tokens := md.Get("authorization")
	if len(tokens) != 1 {
		return nil, status.Error(codes.Unauthenticated, "missing token")
	}
	s.mu.Lock()
	s.tokens = append(s.tokens, tokens[0])
	s.mu.Unlock()
	return nil, status.Error(codes.AlreadyExists, "fixture exists")
}
func TestControlVerifiedTLSAndTokenRotationOverActualRPC(t *testing.T) {
	fixture := httptest.NewTLSServer(nil)
	cert := fixture.TLS.Certificates[0]
	fixture.Close()
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ca := filepath.Join(root, "ca.pem")
	token := filepath.Join(root, "token")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(token, []byte("fixture-first"), 0600)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})))
	control := &controlServer{}
	atepb.RegisterControlServer(server, control)
	go server.Serve(lis)
	defer server.Stop()
	config := ControlConfig{Address: lis.Addr().String(), ServerName: parsed.DNSNames[0], CAFile: ca, TokenFile: token}
	conn, err := DialControl(config)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, value := range []string{"fixture-first", "fixture-rotated"} {
		os.WriteFile(token, []byte(value), 0600)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = EnsureAtespace(ctx, conn)
		cancel()
		if err != nil {
			t.Fatal("verified RPC failed")
		}
	}
	control.mu.Lock()
	valid := len(control.tokens) == 2 && control.tokens[0] == "Bearer fixture-first" && control.tokens[1] == "Bearer fixture-rotated"
	control.mu.Unlock()
	if !valid {
		t.Fatal("RPC did not use rotated explicit token")
	}
	config.ServerName = "wrong.invalid"
	wrong, err := DialControl(config)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err = EnsureAtespace(ctx, wrong); err == nil {
		t.Fatal("wrong TLS server identity accepted")
	}
}

func TestControlRequiresExplicitVerifiedTrustAndServerName(t *testing.T) {
	for _, c := range []ControlConfig{{}, {Address: "api.ate-system.svc:443"}, {Address: "api.ate-system.svc:443", ServerName: "api.ate-system.svc", CAFile: "missing", TokenFile: "missing"}} {
		conn, err := DialControl(c)
		if err == nil {
			conn.Close()
			t.Fatal("unverified control accepted")
		}
	}
}
