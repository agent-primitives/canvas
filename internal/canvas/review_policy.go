package canvas

import (
	"context"
	"errors"
	"slices"

	"github.com/agent-primitives/canvas/internal/sessionauth"
)

var errDocumentReviewDenied = errors.New("You're not authorized to review these documents in this role.")

// ReviewAuthorization is assembled from the stored proposal, never from browser
// claims about its paths. Future document-scoped policies can inspect this scope.
type ReviewAuthorization struct {
	Reviewer sessionauth.Identity
	Role     string
	Proposal string
	Action   string
	Paths    []string
}

type ReviewAuthorizer interface {
	AuthorizeReview(context.Context, ReviewAuthorization) error
}

type reviewerPolicy struct {
	role      string
	reviewers []sessionauth.Identity
}

// NewReviewerPolicy grants one workspace review role to explicit verified
// principals. Neither successful sign-in nor selecting a role grants membership.
func NewReviewerPolicy(role string, reviewers []sessionauth.Identity) (ReviewAuthorizer, error) {
	if !validRole(role) || len(reviewers) == 0 {
		return nil, errors.New("review policy requires a supported role and explicit reviewers")
	}
	for _, id := range reviewers {
		if !id.Valid() {
			return nil, errors.New("review policy requires complete issuer/subject identities")
		}
	}
	return reviewerPolicy{role: role, reviewers: slices.Clone(reviewers)}, nil
}
func (p reviewerPolicy) AuthorizeReview(_ context.Context, r ReviewAuthorization) error {
	if r.Role != p.role || !slices.Contains(p.reviewers, r.Reviewer) {
		return errDocumentReviewDenied
	}
	return nil
}
