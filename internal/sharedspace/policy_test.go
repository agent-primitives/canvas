package sharedspace

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func declaredPolicy() Policy {
	return Policy{Documents: []string{"docs/prd.json", "docs/review.md", "feedback/*.json"}, Inputs: []string{"feedback/*.json"}}
}

func TestDeclaredPolicyAtomicReviewAndInputProjection(t *testing.T) {
	root := t.TempDir()
	s, err := OpenWithPolicy(root, map[string]string{"docs/prd.json": `{}`, "docs/review.md": "Unreviewed", "feedback/initial.json": `{"version":1}`}, declaredPolicy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	base := head(t, s)
	paths, err := s.Paths(base)
	if err != nil || !reflect.DeepEqual(paths, []string{"docs/prd.json", "docs/review.md", "feedback/initial.json"}) {
		t.Fatalf("declared paths: %v %v", paths, err)
	}
	p, err := s.Propose(base, map[string]string{"docs/prd.json": `{"draft":true}`, "docs/review.md": "Proposed review"}, Attribution{Agent: "writer", Session: "synthetic", Run: "run-1"})
	if err != nil || len(p.Changes) != 2 || head(t, s) != base {
		t.Fatalf("atomic proposal: %+v %v", p, err)
	}
	if err := os.WriteFile(filepath.Join(root, "feedback/initial.json"), []byte(`{"version":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(p.ID, base, "GM"); err != nil {
		t.Fatal(err)
	}
	assertRead(t, s, "", "docs/prd.json", `{"draft":true}`)
	newer, err := os.ReadFile(filepath.Join(root, "feedback/initial.json"))
	if err != nil || string(newer) != `{"version":2}` {
		t.Fatal("projection rewound a newer declared input")
	}
	if _, err := s.Ingest("feedback/initial.json", string(newer), "GM"); err != nil {
		t.Fatal(err)
	}
	assertRead(t, s, "", "feedback/initial.json", `{"version":2}`)
}

func TestDeclaredPolicyRefusesUnsafeOrInputProposalsAtomically(t *testing.T) {
	s, err := OpenWithPolicy(t.TempDir(), map[string]string{"docs/prd.json": `{}`}, declaredPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := head(t, s)
	for _, path := range []string{"other.json", "../escape", "/tmp/escape", "docs/../escape", "docs/.hidden", ".git/config", "docs/a\\b", "feedback/new.json"} {
		if _, err := s.Propose(base, map[string]string{"docs/prd.json": "valid", path: "forbidden"}, Attribution{}); err == nil {
			t.Fatalf("proposed forbidden path %q", path)
		}
	}
	if ps, err := s.Proposals(); err != nil || len(ps) != 0 || head(t, s) != base {
		t.Fatal("partial invalid proposal was published")
	}
	if _, err := s.Ingest("docs/prd.json", "bypass review", "GM"); err == nil {
		t.Fatal("ingest bypassed review for an output document")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.Root(), "docs/review.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Propose(base, map[string]string{"docs/review.md": "new"}, Attribution{}); err == nil {
		t.Fatal("symlink output accepted")
	}
}

func TestDeclaredPolicyIsImmutableAndBoundAcrossReopen(t *testing.T) {
	root := t.TempDir()
	policy := declaredPolicy()
	s, err := OpenWithPolicy(root, map[string]string{"docs/prd.json": `{}`}, policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.Documents[0] = "other.json"
	if _, err := s.Propose(head(t, s), map[string]string{"docs/prd.json": "still declared"}, Attribution{}); err != nil {
		t.Fatal("caller mutation changed active policy")
	}
	s.Close()
	changed := declaredPolicy()
	changed.Documents = append(changed.Documents, "extra.json")
	if other, err := OpenWithPolicy(root, nil, changed); err == nil {
		other.Close()
		t.Fatal("changed policy silently adopted existing state")
	}
	if other, err := Open(root, nil); err == nil {
		other.Close()
		t.Fatal("legacy opener adopted declared-policy state")
	}
	reopened, err := OpenWithPolicy(root, nil, declaredPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertRead(t, reopened, "", "docs/prd.json", `{}`)
}

func TestDeclaredInputPublishedButSyncFailedIsExplicit(t *testing.T) {
	s, err := OpenWithPolicy(t.TempDir(), map[string]string{"docs/prd.json": `{}`}, declaredPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.syncInputDirectory = func(string) error { return errors.New("synthetic directory sync failure") }
	err = s.WriteInput("feedback/new.json", `{"version":2}`)
	if !errors.Is(err, ErrCommitted) {
		t.Fatalf("published file uncertainty not distinguished: %v", err)
	}
	raw, readErr := os.ReadFile(filepath.Join(s.Root(), "feedback/new.json"))
	if readErr != nil || string(raw) != `{"version":2}` {
		t.Fatal("fixture did not actually publish input bytes")
	}
}

func TestDeclaredPolicyValidationAndStaleAcceptance(t *testing.T) {
	for _, policy := range []Policy{
		{},
		{Documents: []string{"../escape"}},
		{Documents: []string{".sharedspace/metadata"}},
		{Documents: []string{"feedback/**/input.json"}},
		{Documents: []string{"feedback/*.json"}},
		{Documents: []string{"docs/prd.json"}, Inputs: []string{"other/*.json"}},
		{Documents: []string{"docs/prd.json", "docs/prd.json"}},
		{Documents: []string{"incoming/*.json", "Incoming/secret.json"}, Inputs: []string{"incoming/*.json"}},
		{Documents: []string{"feedback/*.json", "feedback/item.json/review.md"}, Inputs: []string{"feedback/*.json"}},
		{Documents: []string{"Docs", "docs/prd.json"}},
		{Documents: []string{"feedback/*.json", "Feedback/item.JSON/review.md"}, Inputs: []string{"feedback/*.json"}},
	} {
		if err := policy.Validate(); err == nil {
			t.Fatalf("invalid policy accepted: %+v", policy)
		}
	}
	s, err := OpenWithPolicy(t.TempDir(), map[string]string{"docs/prd.json": `{}`}, declaredPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := head(t, s)
	a, err := s.Propose(base, map[string]string{"docs/prd.json": "first"}, Attribution{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Propose(base, map[string]string{"docs/prd.json": "second"}, Attribution{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Accept(a.ID, base, "GM"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Accept(b.ID, head(t, s), "owner"); !errors.Is(err, ErrStale) {
		t.Fatalf("stale declared proposal accepted: %v", err)
	}
}
