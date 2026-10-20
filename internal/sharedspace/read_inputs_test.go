package sharedspace

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestDeclaredReadInputScopesPersistAndIgnoreUnrelatedIngests(t *testing.T) {
	root := filepath.Join(t.TempDir(), "space")
	policy := Policy{Documents: []string{"prd.json", "feedback/*.json", "unrelated/*.json"}, Inputs: []string{"feedback/*.json", "unrelated/*.json"}}
	s, err := OpenWithPolicy(root, map[string]string{"prd.json": "{}", "feedback/initial.json": "{}"}, policy)
	if err != nil {
		t.Fatal(err)
	}
	base := head(t, s)
	inputs := []string{"feedback/*.json"}
	p, err := s.Propose(base, map[string]string{"prd.json": "{\"draft\":true}"}, Attribution{Agent: "writer", ReadInputs: inputs, Sources: map[string]string{"feedback/initial.json": base}})
	if err != nil {
		t.Fatal(err)
	}
	inputs[0] = "unrelated/*.json"
	p.Attribution.ReadInputs[0] = "unrelated/*.json"
	ps, err := s.Proposals()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || len(ps[0].Attribution.ReadInputs) != 1 || ps[0].Attribution.ReadInputs[0] != "feedback/*.json" {
		t.Fatal("caller mutated persisted read authority")
	}
	if _, err = s.Ingest("unrelated/one.json", "{}", "GM"); err != nil {
		t.Fatal(err)
	}
	if statusOf(t, s, p.ID) == "stale" {
		t.Fatal("unread input made proposal stale")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithPolicy(root, nil, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Ingest("feedback/new.json", "{}", "GM"); err != nil {
		t.Fatal(err)
	}
	if statusOf(t, s, p.ID) != "stale" {
		t.Fatal("new member of recorded read collection was not stale after reopen")
	}
	if _, err = s.Accept(p.ID, head(t, s), "GM"); !errors.Is(err, ErrStale) {
		t.Fatalf("accepted changed premise: %v", err)
	}
}

func TestHistoricalDeclaredAcceptanceKeepsOriginalReviewRules(t *testing.T) {
	s, err := OpenWithPolicy(t.TempDir(), map[string]string{"docs/prd.json": "{}", "feedback/initial.json": "{}"}, declaredPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := head(t, s)
	p, err := s.Propose(base, map[string]string{"docs/prd.json": "{\"draft\":true}"}, Attribution{Agent: "writer", Sources: map[string]string{"feedback/initial.json": base}})
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Ingest("feedback/initial.json", "{\"changed\":true}", "GM")
	if err != nil {
		t.Fatal(err)
	}
	// Materialize the acceptance the pre-read-scope store allowed, preserving its
	// original Git metadata. New policy must not erase an already accepted decision.
	tree, err := s.buildTree(current, p.Changes)
	if err != nil {
		t.Fatal(err)
	}
	merge, err := s.commit(tree, []string{current, p.Commit}, "Accept proposal\n\nSharedSpace-Proposal: "+p.ID+"\nSharedSpace-Actor: GM\n")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.updateRef(acceptedRef, merge, current); err != nil {
		t.Fatal(err)
	}
	if statusOf(t, s, p.ID) != "accepted" {
		t.Fatal("new read guards reinterpreted an old acceptance")
	}
}

func TestOlderDeclaredProposalExactReadInputStillGuardsAcceptance(t *testing.T) {
	s, err := OpenWithPolicy(t.TempDir(), map[string]string{"docs/prd.json": "{}", "feedback/initial.json": "{}"}, declaredPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := head(t, s)
	p, err := s.Propose(base, map[string]string{"docs/prd.json": "{\"draft\":true}"}, Attribution{Agent: "writer", Sources: map[string]string{"feedback/initial.json": base}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Ingest("feedback/initial.json", "{\"changed\":true}", "GM"); err != nil {
		t.Fatal(err)
	}
	if statusOf(t, s, p.ID) != "stale" {
		t.Fatal("historical generic exact read lost its premise guard")
	}
}
