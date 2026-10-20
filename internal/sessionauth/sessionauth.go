// Package sessionauth acquires, verifies, and binds Entra bearer credentials
// for authenticated sessions RPCs.
package sessionauth

import (
	"context"
	"errors"
	"time"

	"github.com/agent-primitives/sessions/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxSourceJSON   = 64 << 10
	maxHTTPResponse = 1 << 20
	// Token commands such as CLI credential helpers can be slow to start, but
	// they must never stall a session indefinitely.
	commandTimeout         = 3 * time.Second
	httpTimeout            = 10 * time.Second
	refreshSkew            = 30 * time.Second
	unauthenticatedMessage = "authentication required"
	identityChangedMessage = "caller identity changed"
)

var errSourceInvalid = errors.New("sessionauth: invalid token source")

// Identity is a verified caller principal.
type Identity struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

func (i Identity) Valid() bool { return i.Issuer != "" && i.Subject != "" }

func (i Identity) Ref() api.IdentityRef {
	return api.IdentityRef{Principal: i.Subject, Issuer: i.Issuer, Subject: i.Subject}
}

// Source yields a raw access token and the source-reported expiry.
type Source interface {
	Token(context.Context) (string, time.Time, error)
}

type SourceFunc func(context.Context) (string, time.Time, error)

func (f SourceFunc) Token(ctx context.Context) (string, time.Time, error) { return f(ctx) }

// Verifier validates a raw access token and returns its identity and expiry.
type Verifier interface {
	Verify(context.Context, string) (Identity, time.Time, error)
}

type callerKey struct{}
type expectedKey struct{}

// Caller returns the identity bound to ctx by Manager.Bind.
func Caller(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(callerKey{}).(Identity)
	return id, ok && id.Valid()
}

// Expect records the identity that later credentials must match; a different
// principal is refused instead of silently switching callers.
func Expect(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, expectedKey{}, id)
}

func expected(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(expectedKey{}).(Identity)
	return id, ok && id.Valid()
}

func unauthenticated() error { return status.Error(codes.Unauthenticated, unauthenticatedMessage) }

// Credential failures must use a gRPC credential-legal code; FailedPrecondition
// is rewritten to Internal by grpc-go before the engine can classify it.
func identityChanged() error { return status.Error(codes.Unauthenticated, identityChangedMessage) }

func IsIdentityChanged(err error) bool {
	return status.Code(err) == codes.Unauthenticated && status.Convert(err).Message() == identityChangedMessage
}
