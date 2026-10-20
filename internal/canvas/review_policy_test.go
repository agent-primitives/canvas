package canvas

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sharedspace"
)

type recordingReviewPolicy struct {
	request ReviewAuthorization
	deny    bool
}

func (p *recordingReviewPolicy) AuthorizeReview(_ context.Context, r ReviewAuthorization) error {
	p.request = r
	if p.deny {
		return errors.New("private policy failure")
	}
	return nil
}

func TestDocumentReviewRolePolicyIsEngineOwnedAndSeesWholeProposal(t *testing.T) {
	for _, action := range []string{"accept", "reject"} {
		t.Run(action, func(t *testing.T) {
			c, _, _ := definedFixtureAt(t, actionDefinitionFixture(t), nil)
			head, _ := c.space.Head()
			p, err := c.space.Propose(head, map[string]string{"prd.json": c.definition.Seed["prd.json"], "announcement.md": "# Proposed\n"}, sharedspace.Attribution{})
			if err != nil {
				t.Fatal(err)
			}
			ctx := reviewerContext(t, "allowed-reviewer")
			c.EnableAuthentication()
			if _, err = c.ReviewContext(ctx, p.ID, action, "GM"); !errors.Is(err, errDocumentReviewDenied) {
				t.Fatalf("missing policy did not deny: %v", err)
			}
			policy := &recordingReviewPolicy{deny: true}
			c.EnableAuthentication(policy)
			if _, err = c.ReviewContext(ctx, p.ID, action, "GM"); !errors.Is(err, errDocumentReviewDenied) {
				t.Fatalf("denial hidden: %v", err)
			}
			if policy.request.Reviewer.Subject != "allowed-reviewer" || policy.request.Role != "GM" || policy.request.Proposal != p.ID || policy.request.Action != action || !reflect.DeepEqual(policy.request.Paths, []string{"announcement.md", "prd.json"}) {
				t.Fatalf("incomplete policy scope: %+v", policy.request)
			}
			ps, _ := c.space.Proposals()
			if ps[0].Status != "pending" {
				t.Fatal("denied review mutated proposal")
			}
			policy.deny = false
			if _, err = c.ReviewContext(ctx, p.ID, action, "GM"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReviewerPolicyRequiresMembershipAndExactRole(t *testing.T) {
	allowed := sessionauth.Identity{Issuer: "https://issuer.example", Subject: "allowed"}
	policy, err := NewReviewerPolicy("GM", []sessionauth.Identity{allowed})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []ReviewAuthorization{{Reviewer: allowed, Role: "owner"}, {Reviewer: sessionauth.Identity{Issuer: allowed.Issuer, Subject: "other"}, Role: "GM"}, {Reviewer: sessionauth.Identity{Issuer: "https://other.example", Subject: allowed.Subject}, Role: "GM"}} {
		if policy.AuthorizeReview(context.Background(), r) == nil {
			t.Fatalf("unauthorized role accepted: %+v", r)
		}
	}
	if err = policy.AuthorizeReview(context.Background(), ReviewAuthorization{Reviewer: allowed, Role: "GM"}); err != nil {
		t.Fatal(err)
	}
	if _, err = NewReviewerPolicy("GM", nil); err == nil {
		t.Fatal("empty membership accepted")
	}
}
