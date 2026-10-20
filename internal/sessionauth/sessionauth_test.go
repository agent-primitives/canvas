package sessionauth

import (
	"context"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
)

func TestIdentityRefAndSourceFunc(t *testing.T) {
	id := Identity{Issuer: "https://issuer.example", Subject: "subject-a"}
	if !id.Valid() {
		t.Fatal("valid identity rejected")
	}
	if (Identity{}).Valid() {
		t.Fatal("empty identity accepted")
	}
	if got := id.Ref(); got != (api.IdentityRef{Principal: "subject-a", Issuer: id.Issuer, Subject: id.Subject}) {
		t.Fatalf("ref = %+v", got)
	}
	ctx := Expect(context.Background(), id)
	if _, ok := Caller(ctx); ok {
		t.Fatal("expected identity surfaced as caller")
	}
	expires := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	src := SourceFunc(func(ctx context.Context) (string, time.Time, error) {
		if ctx == nil {
			t.Fatal("nil context")
		}
		return "fixture-token", expires, nil
	})
	token, until, err := src.Token(context.Background())
	if err != nil || token != "fixture-token" || !until.Equal(expires) {
		t.Fatalf("Token() = %q %v %v", token, until, err)
	}
}
