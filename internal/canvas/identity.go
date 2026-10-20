package canvas

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EnableAuthentication is configured before serving any actions. Authentication
// derives attribution; document-review policy and sessions grant authority.
func (c *Canvas) EnableAuthentication(policy ...ReviewAuthorizer) {
	c.authenticated = true
	c.EnableRemoteSessions()
	c.reviewAuthorizer = nil
	if len(policy) == 1 {
		c.reviewAuthorizer = policy[0]
	}
}

// EnableRemoteSessions disables local-only plaintext Diagnostics. Configure it
// before serving. It deliberately does not assert authenticated identity.
func (c *Canvas) EnableRemoteSessions() { c.remoteSessions = true }

func (c *Canvas) actionIdentity(ctx context.Context, role string) (api.IdentityRef, error) {
	if id, ok := sessionauth.Caller(ctx); ok {
		// Ownership grants no decision rights, even with an external grant.
		if c.isServiceIdentity(id) {
			return api.IdentityRef{}, errServiceActor
		}
		return id.Ref(), nil
	}
	if c.authenticated {
		return api.IdentityRef{}, status.Error(codes.Unauthenticated, "Sign in again: sessions credentials are missing, invalid or expired.")
	}
	return api.IdentityRef{Principal: "demo:" + role}, nil
}

func sessionsAuthError(err error) error {
	switch status.Code(err) {
	case codes.PermissionDenied:
		return status.Error(codes.PermissionDenied, "You're not an approver for this session.")
	case codes.Unauthenticated:
		if sessionauth.IsIdentityChanged(err) {
			return status.Error(codes.Unauthenticated, "Credential identity changed. Reinspect this action with the intended reviewer.")
		}
		return status.Error(codes.Unauthenticated, "Sign in again: sessions credentials are missing, invalid or expired.")
	}
	return nil
}
func sessionsFailure(err, fallback error) error {
	if auth := sessionsAuthError(err); auth != nil {
		return auth
	}
	return fallback
}
func sendAuthError(w http.ResponseWriter, err error) bool {
	auth := sessionsAuthError(err)
	if auth == nil {
		return false
	}
	code, httpCode := "UNAUTHENTICATED", http.StatusUnauthorized
	if status.Code(auth) == codes.PermissionDenied {
		code, httpCode = "PERMISSION_DENIED", http.StatusForbidden
	}
	sendJSON(w, httpCode, map[string]string{"error": status.Convert(auth).Message(), "code": code})
	return true
}

var errRemoteDiagnostics = errors.New("Replay diagnostics are unavailable on this remote sessions host; no plaintext connection was attempted.")

type reviewedSpace interface {
	AcceptReviewed(string, string, sharedspace.ReviewActor) (sharedspace.Proposal, error)
	RejectReviewed(string, sharedspace.ReviewActor) (sharedspace.Proposal, error)
}

func (c *Canvas) ReviewContext(ctx context.Context, id, action, role string) (sharedspace.Proposal, error) {
	if _, err := c.actionIdentity(ctx, role); err != nil {
		return sharedspace.Proposal{}, err
	}
	c.publicationMu.Lock()
	defer c.publicationMu.Unlock()
	if !validRole(role) {
		return sharedspace.Proposal{}, errors.New("invalid demo role")
	}
	identity, verified := sessionauth.Caller(ctx)
	if !verified {
		return c.review(id, action, role)
	}
	if c.authenticated {
		if c.reviewAuthorizer == nil {
			return sharedspace.Proposal{}, errDocumentReviewDenied
		}
		proposals, err := c.space.Proposals()
		if err != nil {
			return sharedspace.Proposal{}, internalError(err)
		}
		var paths []string
		found := false
		for _, p := range proposals {
			if p.ID == id {
				found = true
				for path := range p.Changes {
					paths = append(paths, path)
				}
				break
			}
		}
		if !found {
			return sharedspace.Proposal{}, errNotFound
		}
		sort.Strings(paths)
		request := ReviewAuthorization{Reviewer: identity, Role: role, Proposal: id, Action: action, Paths: paths}
		if c.reviewAuthorizer.AuthorizeReview(ctx, request) != nil {
			return sharedspace.Proposal{}, errDocumentReviewDenied
		}
	}
	store, ok := c.space.(reviewedSpace)
	if !ok {
		return sharedspace.Proposal{}, errors.New("workspace cannot retain verified review identity")
	}
	reviewer := sharedspace.ReviewActor{Issuer: identity.Issuer, Subject: identity.Subject, Role: role}
	if action == "reject" {
		return store.RejectReviewed(id, reviewer)
	}
	if action != "accept" {
		return sharedspace.Proposal{}, errors.New("unknown review action")
	}
	head, err := c.space.Head()
	if err != nil {
		return sharedspace.Proposal{}, internalError(err)
	}
	return store.AcceptReviewed(id, head, reviewer)
}
