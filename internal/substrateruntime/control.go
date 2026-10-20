package substrateruntime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"strings"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/sessions/api"
	ateadapter "github.com/agent-primitives/sessions/integrations/substrate"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/substrate"
	atepb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type ControlConfig struct{ Address, ServerName, CAFile, TokenFile string }

// DialControl uses only explicitly supplied files. The projected token is read
// for every RPC, so kubelet rotation is observed without retaining bearer bytes.
func DialControl(c ControlConfig) (*grpc.ClientConn, error) {
	if c.Address == "" || c.ServerName == "" || c.CAFile == "" || c.TokenFile == "" {
		return nil, errors.New("substrate: explicit Control address, verified trust and projected token required")
	}
	pem, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, errors.New("substrate: cannot read Control trust bundle")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("substrate: invalid Control trust bundle")
	}
	token := fileToken{path: c.TokenFile}
	if _, err := token.GetRequestMetadata(context.Background()); err != nil {
		return nil, err
	}
	return grpc.NewClient(c.Address, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: pool, ServerName: c.ServerName, MinVersion: tls.VersionTLS12})), grpc.WithPerRPCCredentials(token))
}

type fileToken struct{ path string }

func (t fileToken) RequireTransportSecurity() bool { return true }
func (t fileToken) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bytes, err := os.ReadFile(t.path)
	if err != nil || len(bytes) > 16384 {
		return nil, errors.New("substrate: cannot read projected Control token")
	}
	token := strings.TrimSpace(string(bytes))
	if token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return nil, errors.New("substrate: invalid projected Control token")
	}
	return map[string]string{"authorization": "Bearer " + token}, nil
}

// EnsureAtespace makes a bounded authenticated RPC before exposing Sessions.
func EnsureAtespace(ctx context.Context, conn grpc.ClientConnInterface) error {
	_, err := atepb.NewControlClient(conn).CreateAtespace(ctx, &atepb.CreateAtespaceRequest{Atespace: &atepb.Atespace{Metadata: &atepb.ResourceMetadata{Name: "canvas-draft"}}})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return errors.New("substrate: Control authentication or atespace provisioning failed")
	}
	return nil
}

// RetainedFactory releases compute through native Snapshot, retaining the actor.
// Activation may restore its native checkpoint; an identity-only Sessions
// reference does not control Substrate's snapshot selection.
func RetainedFactory(conn grpc.ClientConnInterface, namespace string) func(contract.Bundle, int) (placement.Backend, func() error, error) {
	return factory(conn, namespace, true)
}

// Factory preserves the default delete-and-recreate policy on suspension.
func Factory(conn grpc.ClientConnInterface, namespace string) func(contract.Bundle, int) (placement.Backend, func() error, error) {
	return factory(conn, namespace, false)
}

func factory(conn grpc.ClientConnInterface, namespace string, retain bool) func(contract.Bundle, int) (placement.Backend, func() error, error) {
	return func(b contract.Bundle, profile int) (placement.Backend, func() error, error) {
		if b.Suite != nil || profile == 4 {
			return nil, nil, errors.New("substrate: native suite/profile 4 is local-only; remote templates unsupported")
		}
		d, err := sessionhost.BundleDescriptor(b, profile)
		if err != nil {
			return nil, nil, err
		}
		if d.Capabilities.Resumability != api.ResumabilityStatelessReplay {
			return nil, nil, errors.New("substrate: only stateless replay descriptors supported")
		}
		name, err := TemplateName(b, profile)
		if err != nil {
			return nil, nil, err
		}
		native := substrate.New(ateadapter.New(conn), "canvas-draft", substrate.ObjectRef{Atespace: namespace, Name: name}, d)
		backend, err := StatelessReplay(native)
		if err == nil && retain {
			backend = retainedReplayBackend{replayBackend{native}}
		}
		return backend, nil, err
	}
}
