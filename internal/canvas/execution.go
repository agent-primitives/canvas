package canvas

import (
	"context"
	"errors"

	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ExecutionService is the trusted engine binding for service-owned Sessions
// RPCs (CreateSession, GetSession, Exec and Replay). Human decisions (Approve
// and the actual Resume) keep using the Canvas client with the verified human
// request context. Configure it before serving, after EnableAuthentication.
type ExecutionService struct {
	Client      *client.Client
	Credentials *sessionauth.Manager
	Identity    sessionauth.Identity
}

// localExecutionOwner is the stable unauthenticated demo engine principal. It
// is never derived from a persona label or Actor.
var localExecutionOwner = api.IdentityRef{Principal: "demo:canvas-engine"}

var (
	errExecutionServiceIncomplete = errors.New("canvas execution service requires a client, credentials and a verified identity")
	errExecutionServiceMissing    = errors.New("canvas execution service is not configured; no Sessions request was attempted")
	errExecutionOwnerChanged      = errors.New("canvas execution owner differs from recorded state; rotating the service identity requires new canvas state")
	errServiceActor               = status.Error(codes.PermissionDenied, "The canvas execution service cannot act as a human reviewer.")
)

// ConfigureExecutionService binds service-owned execution. Incomplete bindings,
// live reconfiguration and a journaled owner mismatch fail without fallback.
func (c *Canvas) ConfigureExecutionService(s ExecutionService) error {
	if s.Client == nil || s.Credentials == nil || !s.Identity.Valid() {
		return errExecutionServiceIncomplete
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.service != nil {
		if *c.service == s {
			return nil
		}
		return errExecutionOwnerChanged
	}
	if owner := c.data.ExecutionOwner; owner != (api.IdentityRef{}) && owner != s.Identity.Ref() {
		return errExecutionOwnerChanged
	}
	configured := s
	c.service = &configured
	return nil
}

// executionOwnerLocked selects the owner for new work. Authenticated mode
// never falls back to a human; a service-bound journal never runs locally.
func (c *Canvas) executionOwnerLocked() (api.IdentityRef, error) {
	if c.service != nil {
		owner := c.service.Identity.Ref()
		if c.data.ExecutionOwner != (api.IdentityRef{}) && c.data.ExecutionOwner != owner {
			return api.IdentityRef{}, errExecutionOwnerChanged
		}
		return owner, nil
	}
	if c.authenticated {
		return api.IdentityRef{}, errExecutionServiceMissing
	}
	if c.data.ExecutionOwner != (api.IdentityRef{}) {
		return api.IdentityRef{}, errExecutionOwnerChanged
	}
	return localExecutionOwner, nil
}

// ownedLocked reports whether recorded work may be read back, continued or
// resumed now. Ownerless legacy work lacks a durable binding and remains
// read-only history in every mode; a different owner is never relabelled.
func (c *Canvas) ownedLocked(recorded api.IdentityRef) bool {
	owner, err := c.executionOwnerLocked()
	return err == nil && recorded != (api.IdentityRef{}) && recorded == owner
}

// recordedOwner is a provenance consistency check only. Authority comes from
// the verified RPC caller, not this Session field.
func recordedOwner(s *v1.Session, recorded api.IdentityRef) bool {
	id := s.GetIdentity()
	return recorded != (api.IdentityRef{}) && id.GetPrincipal() == recorded.Principal && id.GetIssuer() == recorded.Issuer && id.GetSubject() == recorded.Subject
}

func (c *Canvas) isServiceIdentity(id sessionauth.Identity) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.service != nil && c.service.Identity == id
}

// serviceContext derives a fresh context from the value-free Canvas root. It
// forwards only the parent's cancellation and deadline, never human
// credentials or outgoing metadata, and pins the configured service identity.
// The returned cancel releases the forwarding callback.
func (c *Canvas) serviceContext(parent context.Context) (context.Context, *client.Client, context.CancelFunc, error) {
	if err := parent.Err(); err != nil {
		return nil, nil, nil, err
	}
	c.mu.Lock()
	service, sessions := c.service, c.client
	c.mu.Unlock()
	ctx, cancel := context.WithCancel(c.ctx)
	if deadline, ok := parent.Deadline(); ok {
		var cancelDeadline context.CancelFunc
		ctx, cancelDeadline = context.WithDeadline(ctx, deadline)
		root := cancel
		cancel = func() { cancelDeadline(); root() }
	}
	stop := context.AfterFunc(parent, cancel)
	release := func() { stop(); cancel() }
	if service == nil {
		if c.authenticated {
			release()
			return nil, nil, nil, errExecutionServiceMissing
		}
		return ctx, sessions, release, nil
	}
	bound, _, err := service.Credentials.Bind(sessionauth.Expect(ctx, service.Identity))
	if err != nil {
		release()
		return nil, nil, nil, err
	}
	return bound, service.Client, release, nil
}

// requester captures verified attribution during admission. Queued work keeps
// only this provenance, never the request credential or lifetime.
func (c *Canvas) requester(ctx context.Context, role string) (sessionauth.Identity, sharedspace.ReviewActor, error) {
	if _, err := c.actionIdentity(ctx, role); err != nil {
		return sessionauth.Identity{}, sharedspace.ReviewActor{}, err
	}
	id, ok := sessionauth.Caller(ctx)
	if !ok {
		return sessionauth.Identity{}, sharedspace.ReviewActor{}, nil
	}
	return id, sharedspace.ReviewActor{Issuer: id.Issuer, Subject: id.Subject, Role: role}, nil
}

// queueAfterReview queues follow-up work triggered by an accepted review. The
// trigger actor is loaded from the workspace, never taken from the caller or a
// model; the original requester is retained.
func (c *Canvas) queueAfterReview(a attempt, proposalID string) (string, error) {
	proposals, err := c.space.Proposals()
	if err != nil {
		return "", internalError(err)
	}
	for _, p := range proposals {
		if p.ID != proposalID || p.Status != "accepted" || p.AcceptedCommit == "" {
			continue
		}
		if c.authenticated && (p.Reviewer.Issuer == "" || p.Reviewer.Subject == "") {
			break
		}
		a.TriggeredBy, a.TriggeringReview = p.Reviewer, p.ID
		return c.queue(a, "")
	}
	return "", errors.New("follow-up work requires an accepted proposal review")
}
