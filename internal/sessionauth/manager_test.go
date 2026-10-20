package sessionauth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type verifierFunc func(context.Context, string) (Identity, time.Time, error)

func (f verifierFunc) Verify(ctx context.Context, token string) (Identity, time.Time, error) {
	return f(ctx, token)
}

func TestManagerBindPreservesBoundTokenAndCaller(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	issued := struct {
		sync.Mutex
		token string
		id    Identity
		exp   time.Time
	}{token: "first.token", id: Identity{Issuer: "https://issuer.example", Subject: "subject-a"}, exp: now.Add(2 * time.Minute)}
	src := SourceFunc(func(context.Context) (string, time.Time, error) {
		issued.Lock()
		defer issued.Unlock()
		return issued.token, issued.exp, nil
	})
	verify := verifierFunc(func(_ context.Context, token string) (Identity, time.Time, error) {
		issued.Lock()
		defer issued.Unlock()
		if token != issued.token {
			return Identity{}, time.Time{}, status.Error(codes.Unauthenticated, "authentication required")
		}
		return issued.id, issued.exp, nil
	})
	m := New(src, verify)
	m.now = func() time.Time { return now }
	ctx, id, err := m.Bind(context.Background())
	if err != nil || id != issued.id {
		t.Fatalf("Bind() = %+v %v", id, err)
	}
	if caller, ok := Caller(ctx); !ok || caller != issued.id {
		t.Fatalf("Caller() = %+v %v", caller, ok)
	}
	issued.Lock()
	issued.token = "second.token"
	issued.id = Identity{Issuer: "https://issuer.example", Subject: "subject-b"}
	issued.exp = now.Add(4 * time.Minute)
	issued.Unlock()
	md, err := m.GetRequestMetadata(ctx)
	if err != nil || md["authorization"] != "Bearer first.token" {
		t.Fatalf("GetRequestMetadata(bound) = %#v %v", md, err)
	}
	if _, err := m.GetRequestMetadata(Expect(ctx, issued.id)); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("bound identity mismatch = %v", err)
	}
	m.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := m.GetRequestMetadata(ctx); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expired bound token = %v", err)
	}
}

func TestManagerCachesAndCoalescesRefresh(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	id := Identity{Issuer: "https://issuer.example", Subject: "subject-a"}
	var sourceCalls atomic.Int32
	release := make(chan struct{})
	src := SourceFunc(func(context.Context) (string, time.Time, error) {
		sourceCalls.Add(1)
		<-release
		return "coalesced.token", now.Add(2 * time.Minute), nil
	})
	verify := verifierFunc(func(context.Context, string) (Identity, time.Time, error) {
		return id, now.Add(2 * time.Minute), nil
	})
	m := New(src, verify)
	m.now = func() time.Time { return now }
	ctx := Expect(context.Background(), id)
	errCh := make(chan error, 4)
	for range 4 {
		go func() {
			_, err := m.GetRequestMetadata(ctx)
			errCh <- err
		}()
	}
	for sourceCalls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	for range 4 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if sourceCalls.Load() != 1 {
		t.Fatalf("source calls = %d, want 1", sourceCalls.Load())
	}
	if _, err := m.GetRequestMetadata(ctx); err != nil || sourceCalls.Load() != 1 {
		t.Fatalf("cached metadata err=%v calls=%d", err, sourceCalls.Load())
	}
}

func TestManagerRejectsExpiredCredentialsAndIdentitySwitch(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	id := Identity{Issuer: "https://issuer.example", Subject: "subject-a"}
	t.Run("expired token", func(t *testing.T) {
		m := New(SourceFunc(func(context.Context) (string, time.Time, error) {
			return "expired.token", now.Add(time.Minute), nil
		}), verifierFunc(func(context.Context, string) (Identity, time.Time, error) {
			return id, now, nil
		}))
		m.now = func() time.Time { return now }
		_, err := m.GetRequestMetadata(context.Background())
		if status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != unauthenticatedMessage {
			t.Fatalf("expired status = %v", err)
		}
	})
	t.Run("no fallback during refresh window", func(t *testing.T) {
		m := New(SourceFunc(func(context.Context) (string, time.Time, error) {
			return "", time.Time{}, errors.New(sourceSecret)
		}), verifierFunc(func(context.Context, string) (Identity, time.Time, error) {
			return id, now.Add(time.Minute), nil
		}))
		m.now = func() time.Time { return now }
		m.cached = &credential{token: "old.token", identity: id, expiry: now.Add(20 * time.Second)}
		_, err := m.GetRequestMetadata(Expect(context.Background(), id))
		if status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != unauthenticatedMessage || stringsContains(err.Error(), sourceSecret) {
			t.Fatalf("refresh failure = %v", err)
		}
	})
	t.Run("identity switch", func(t *testing.T) {
		m := New(SourceFunc(func(context.Context) (string, time.Time, error) {
			return "fresh.token", now.Add(time.Minute), nil
		}), verifierFunc(func(context.Context, string) (Identity, time.Time, error) {
			return Identity{Issuer: id.Issuer, Subject: "subject-b"}, now.Add(time.Minute), nil
		}))
		m.now = func() time.Time { return now }
		_, err := m.GetRequestMetadata(Expect(context.Background(), id))
		if status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != identityChangedMessage {
			t.Fatalf("identity switch = %v", err)
		}
	})
}

func TestManagersRejectForeignCredentialPins(t *testing.T) {
	for _, tc := range []struct{ from, to string }{{"human", "service"}, {"service", "human"}, {"same", "same"}} {
		t.Run(tc.from+"-to-"+tc.to, func(t *testing.T) {
			expiry := time.Now().Add(time.Hour)
			var fromCalls, toCalls atomic.Int32
			manager := func(subject, token string, calls *atomic.Int32) *Manager {
				return New(SourceFunc(func(context.Context) (string, time.Time, error) {
					calls.Add(1)
					return token, expiry, nil
				}), verifierFunc(func(_ context.Context, raw string) (Identity, time.Time, error) {
					if raw != token {
						return Identity{}, time.Time{}, unauthenticated()
					}
					return Identity{Issuer: "https://issuer.example", Subject: subject}, expiry, nil
				}))
			}
			from := manager(tc.from, "from.token", &fromCalls)
			to := manager(tc.to, "to.token", &toCalls)
			ctx, identity, err := from.Bind(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := to.GetRequestMetadata(ctx)
			if metadata != nil || status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != unauthenticatedMessage {
				t.Error("foreign credential pin must be refused without metadata or credential details")
			}
			rebound, got, err := to.Bind(ctx)
			if rebound != ctx || got != (Identity{}) || status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != unauthenticatedMessage {
				t.Error("Bind must refuse a foreign pin and return the original context without a new identity")
			}
			if fromCalls.Load() != 1 || toCalls.Load() != 0 {
				t.Errorf("foreign pin caused fallback acquisition: from=%d to=%d", fromCalls.Load(), toCalls.Load())
			}
			if caller, ok := Caller(ctx); !ok || caller != identity {
				t.Fatal("original caller changed")
			}
			metadata, err = from.GetRequestMetadata(ctx)
			if err != nil || metadata["authorization"] != "Bearer from.token" {
				t.Fatal("original manager lost its pinned token", err)
			}
			rebound, got, err = from.Bind(ctx)
			if err != nil || got != identity {
				t.Fatal("same-manager rebind failed", err)
			}
			metadata, err = from.GetRequestMetadata(rebound)
			if err != nil || metadata["authorization"] != "Bearer from.token" {
				t.Fatal("same-manager rebind changed the cached credential", err)
			}
			ctx, got, err = to.Bind(context.Background())
			if err != nil || got.Subject != tc.to {
				t.Fatal("independent binding failed", err)
			}
			metadata, err = to.GetRequestMetadata(ctx)
			if err != nil || metadata["authorization"] != "Bearer to.token" {
				t.Fatal("independent manager lost its own token", err)
			}
		})
	}
}

func TestManagerRejectsMalformedCredentialPin(t *testing.T) {
	var calls atomic.Int32
	expiry := time.Now().Add(time.Hour)
	manager := New(SourceFunc(func(context.Context) (string, time.Time, error) {
		calls.Add(1)
		return "fallback.token", expiry, nil
	}), verifierFunc(func(context.Context, string) (Identity, time.Time, error) {
		return Identity{Issuer: "https://issuer.example", Subject: "service"}, expiry, nil
	}))
	ctx := context.WithValue(context.Background(), tokenKey{}, struct{}{})
	metadata, err := manager.GetRequestMetadata(ctx)
	if metadata != nil || status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != unauthenticatedMessage {
		t.Error("present malformed pin must not fall back to the configured source")
	}
	rebound, identity, err := manager.Bind(ctx)
	if rebound != ctx || identity != (Identity{}) || status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != unauthenticatedMessage {
		t.Error("Bind must refuse a present malformed pin")
	}
	if calls.Load() != 0 {
		t.Fatal("malformed pin caused fallback acquisition")
	}
}

func stringsContains(s, sub string) bool { return len(sub) > 0 && strings.Index(s, sub) >= 0 }
