package sessionauth

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
)

// Manager caches one verified credential, refreshes it refreshSkew before
// expiry (coalescing concurrent refreshes), and supplies per-RPC metadata.
type Manager struct {
	source   Source
	verifier Verifier
	now      func() time.Time

	mu      sync.Mutex
	cached  *credential
	refresh chan struct{}
}

type credential struct {
	token    string
	identity Identity
	expiry   time.Time
}

type boundCredential struct {
	manager    *Manager
	credential *credential
}

type tokenKey struct{}

var _ credentials.PerRPCCredentials = (*Manager)(nil)

func New(source Source, verifier Verifier) *Manager {
	return &Manager{source: source, verifier: verifier, now: time.Now}
}

// Bind pins the current verified token and caller identity to ctx so every
// RPC of one action uses the exact credential that was checked. A foreign pin
// is refused; on error the original ctx is returned unchanged.
func (m *Manager) Bind(ctx context.Context) (context.Context, Identity, error) {
	if _, err := m.pinned(ctx); err != nil {
		return ctx, Identity{}, err
	}
	cred, err := m.current(ctx)
	if err != nil {
		return ctx, Identity{}, err
	}
	ctx = context.WithValue(ctx, callerKey{}, cred.identity)
	return context.WithValue(ctx, tokenKey{}, &boundCredential{manager: m, credential: cred}), cred.identity, nil
}

// A context from another authority must never select this manager's source or
// supply its token. Presence, including a malformed pin, fails closed.
func (m *Manager) pinned(ctx context.Context) (*credential, error) {
	value := ctx.Value(tokenKey{})
	if value == nil {
		return nil, nil
	}
	pin, ok := value.(*boundCredential)
	if !ok || pin == nil || pin.manager != m || pin.credential == nil {
		return nil, unauthenticated()
	}
	return pin.credential, nil
}

func (m *Manager) RequireTransportSecurity() bool { return true }

func (m *Manager) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	cred, err := m.pinned(ctx)
	if err != nil {
		return nil, err
	}
	if cred != nil {
		if want, has := expected(ctx); has && want != cred.identity {
			return nil, identityChanged()
		}
		// A pinned token is never silently replaced, but it is not sent once expired.
		if !cred.expiry.After(m.now()) {
			return nil, unauthenticated()
		}
	} else {
		var err error
		if cred, err = m.current(ctx); err != nil {
			return nil, err
		}
	}
	return map[string]string{"authorization": "Bearer " + cred.token}, nil
}

// current returns a fresh credential that matches any expected identity.
func (m *Manager) current(ctx context.Context) (*credential, error) {
	cred, err := m.acquire(ctx)
	if err != nil {
		return nil, err
	}
	if want, has := expected(ctx); has && want != cred.identity {
		return nil, identityChanged()
	}
	return cred, nil
}

func (m *Manager) acquire(ctx context.Context) (*credential, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m.mu.Lock()
		if c := m.cached; c != nil && c.expiry.After(m.now().Add(refreshSkew)) {
			m.mu.Unlock()
			return c, nil
		}
		if wait := m.refresh; wait != nil {
			m.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		done := make(chan struct{})
		m.refresh = done
		m.mu.Unlock()

		cred, err := m.load(ctx)
		m.mu.Lock()
		if err == nil {
			m.cached = cred
		}
		m.refresh = nil
		close(done)
		m.mu.Unlock()
		return cred, err
	}
}

// load fetches and verifies a token. Source and verifier details never reach
// the caller: errors collapse to fixed gRPC statuses.
func (m *Manager) load(ctx context.Context) (*credential, error) {
	raw, sourceExp, err := m.source.Token(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, unauthenticated()
	}
	id, exp, err := m.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, unauthenticated()
	}
	if !sourceExp.IsZero() && sourceExp.Before(exp) {
		exp = sourceExp
	}
	if !id.Valid() || !exp.After(m.now()) {
		return nil, unauthenticated()
	}
	return &credential{token: raw, identity: id, expiry: exp}, nil
}
