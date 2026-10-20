package canvas

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/canvas/internal/sessionauth"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type actionVerifier struct{ id sessionauth.Identity }

func (v actionVerifier) Verify(context.Context, string) (sessionauth.Identity, time.Time, error) {
	return v.id, time.Now().Add(time.Hour), nil
}
func reviewerContext(t *testing.T, subject string) context.Context {
	t.Helper()
	m := sessionauth.New(sessionauth.SourceFunc(func(context.Context) (string, time.Time, error) {
		return "test-credential-not-persisted", time.Now().Add(time.Hour), nil
	}), actionVerifier{sessionauth.Identity{Issuer: "https://issuer.example", Subject: subject}})
	ctx, _, err := m.Bind(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestAuthenticatedDocumentReviewsKeepIdentitySeparateFromRole(t *testing.T) {
	c, _, _ := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	policy, err := NewReviewerPolicy("GM", []sessionauth.Identity{{Issuer: "https://issuer.example", Subject: "reviewer-a"}, {Issuer: "https://issuer.example", Subject: "reviewer-b"}})
	if err != nil {
		t.Fatal(err)
	}
	c.EnableAuthentication(policy)
	for _, subject := range []string{"reviewer-a", "reviewer-b"} {
		head, _ := c.space.Head()
		p, err := c.space.Propose(head, map[string]string{"announcement.md": subject}, sharedspace.Attribution{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Review(p.ID, "accept", "GM"); status.Code(err) != codes.Unauthenticated {
			t.Fatal("unverified direct action accepted")
		}
		p, err = c.ReviewContext(reviewerContext(t, subject), p.ID, "accept", "GM")
		if err != nil {
			t.Fatal(err)
		}
		if p.Reviewer.Subject != subject || p.Reviewer.Role != "GM" {
			t.Fatalf("review: %+v", p.Reviewer)
		}
	}
}

func TestAuthenticatedToolDecisionUsesCallerAndReturnsProvenance(t *testing.T) {
	c, _, _ := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Identity-bound approval\n")
	c.EnableAuthentication()
	useTestService(t, c)
	ctx := reviewerContext(t, "verified-approver")
	if _, err := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unverified request: %v", err)
	}
	id, err := c.RequestAnnouncement(ctx, "announce", p.ID, "GM")
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.InspectAnnouncement(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	v.Confirmation.Confirmed = true
	if err = c.DecideAnnouncement(ctx, id, "GM", true, v.Confirmation); err != nil {
		t.Fatal(err)
	}
	v, err = c.InspectAnnouncement(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if v.Reviewer.Subject != "verified-approver" || v.Reviewer.Role != "GM" || v.IdentitySource != "engine-verified" {
		t.Fatalf("decision provenance: %+v", v)
	}
	raw, _ := json.Marshal(c.data)
	if strings.Contains(string(raw), "test-credential-not-persisted") {
		t.Fatal("token persisted")
	}
}

func TestSessionsAuthenticationOutcomesAreDistinctAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		code  codes.Code
		http  int
		label string
	}{{codes.PermissionDenied, 403, "PERMISSION_DENIED"}, {codes.Unauthenticated, 401, "UNAUTHENTICATED"}} {
		w := httptest.NewRecorder()
		failHTTP(w, status.Error(tc.code, "SECRET-ERROR-MUST-NOT-LEAK"))
		var body map[string]string
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != tc.http || body["code"] != tc.label || strings.Contains(w.Body.String(), "SECRET") {
			t.Fatalf("outcome: %d %s", w.Code, w.Body.String())
		}
	}
}
