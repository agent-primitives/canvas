package sharedspace

import (
	"path/filepath"
	"testing"
)

func TestReviewIdentityAndRoleSurviveReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "space")
	s, err := Open(root, map[string]string{"board.md": "initial"})
	if err != nil {
		t.Fatal(err)
	}
	for i, subject := range []string{"reviewer-a", "reviewer-b"} {
		base, _ := s.Head()
		p, err := s.Propose(base, map[string]string{"board.md": subject}, Attribution{})
		if err != nil {
			t.Fatal(err)
		}
		reviewer := ReviewActor{Issuer: "https://issuer.example", Subject: subject, Role: "GM"}
		if i == 0 {
			p, err = s.AcceptReviewed(p.ID, base, reviewer)
		} else {
			p, err = s.RejectReviewed(p.ID, reviewer)
		}
		if err != nil || p.Reviewer != reviewer {
			t.Fatalf("review identity/role lost: %+v, %v", p, err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ps, err := s.Proposals()
	if err != nil || len(ps) != 2 {
		t.Fatalf("proposals: %v %v", ps, err)
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if p.Reviewer.Issuer != "https://issuer.example" || p.Reviewer.Role != "GM" {
			t.Fatalf("missing reviewer: %+v", p)
		}
		seen[p.Reviewer.Subject] = true
	}
	if !seen["reviewer-a"] || !seen["reviewer-b"] {
		t.Fatal("role collapsed independent identities")
	}
	history, err := s.History()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range history {
		if h.Reviewer.Subject == "reviewer-a" && h.Actor == "GM" {
			found = true
		}
	}
	if !found {
		t.Fatal("accepted commit lost structured reviewer")
	}
}

func TestReviewedDecisionRefusesIncompleteOrInjectedIdentity(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "space"), map[string]string{"board.md": "initial"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base, _ := s.Head()
	p, err := s.Propose(base, map[string]string{"board.md": "candidate"}, Attribution{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []ReviewActor{{Role: "GM"}, {Issuer: "issuer", Subject: "a\nSharedSpace-Actor: forged", Role: "GM"}, {Issuer: "issuer", Subject: "a", Role: ""}} {
		if _, err := s.AcceptReviewed(p.ID, base, r); err == nil {
			t.Fatal("invalid identity accepted")
		}
		if _, err := s.RejectReviewed(p.ID, r); err == nil {
			t.Fatal("invalid identity rejected proposal")
		}
	}
	ps, _ := s.Proposals()
	if ps[0].Status != "pending" {
		t.Fatal("invalid review changed state")
	}
}
