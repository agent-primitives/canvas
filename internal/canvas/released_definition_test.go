package canvas

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sharedspace"
)

func releasedDefinitionFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join("..", "..", "examples", "northwind-prd"))); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, "canvas.yaml")
	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "name: northwind-prd", "name: northwind-released-test", 1)
	text = strings.ReplaceAll(text, "        directory: suites/writer\n        artifact: layouts/writer\n        digest:", "        layout: layouts/writer\n        platform: linux/amd64\n        ref:")
	text = strings.ReplaceAll(text, "        directory: suites/reviewer\n        artifact: layouts/reviewer\n        digest:", "        layout: layouts/reviewer\n        platform: linux/amd64\n        ref:")
	if err := os.WriteFile(filename, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestReleasedDefinitionExactSelectionAndEvidence(t *testing.T) {
	filename := releasedDefinitionFixture(t)
	d, err := LoadGenericDefinitionWithResolver(filename, contract.NewReleaseResolver(filepath.Join(t.TempDir(), "private", "suite-cache")))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Bundles) != 2 {
		t.Fatal("missing aliases")
	}
	for _, b := range d.Bundles {
		if b.Suite.Release == nil || b.Suite.Release.Evaluation.Status != "not verified" || len(b.Suite.Compositions) != 1 {
			t.Fatal("wrong selected identity")
		}
	}
	raw, _ := os.ReadFile(filename)
	text := strings.Replace(string(raw), "      read: [brief, feedback, prd]", "      evaluation: {expected: evidence/expected.json, evidence: evidence/result.json}\n      read: [brief, feedback, prd]", 1)
	os.Mkdir(filepath.Join(filepath.Dir(filename), "evidence"), 0700)
	os.WriteFile(filepath.Join(filepath.Dir(filename), "evidence", "expected.json"), []byte(`{"forged":true}`), 0600)
	os.WriteFile(filepath.Join(filepath.Dir(filename), "evidence", "result.json"), []byte(`{"forged":true}`), 0600)
	os.WriteFile(filename, []byte(text), 0600)
	changed, err := LoadGenericDefinition(filename)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Digest == d.Digest {
		t.Fatal("evidence bytes not bound to definition")
	}
	if changed.Bundles[1].Suite.Release.Evaluation.Reason != "malformed" {
		t.Fatal("mismatched evaluation trusted")
	}
	for _, replacement := range []string{"../expected.json", "/tmp/expected.json"} {
		os.WriteFile(filename, []byte(strings.Replace(text, "evidence/expected.json", replacement, 1)), 0600)
		if _, err := LoadGenericDefinition(filename); err == nil {
			t.Fatal("escaping evaluation file accepted")
		}
	}
}

type unassociatedProposalSpace struct {
	sharedspace.SharedSpace
	saved []sharedspace.Proposal
}

func (s *unassociatedProposalSpace) Proposals() ([]sharedspace.Proposal, error) {
	return append([]sharedspace.Proposal(nil), s.saved...), nil
}

func TestUnassociatedProposalCannotSupplyReleaseEvaluation(t *testing.T) {
	c, d, calls := definedFixture(t, nil)
	suite := d.Bundles[0].Suite.Clone()
	suite.Release = &contract.ReleaseIdentity{Evaluation: contract.EvaluationInfo{Status: "evaluation consistent; unsigned", Unsigned: true}}
	source := &unassociatedProposalSpace{SharedSpace: c.space, saved: []sharedspace.Proposal{{ID: "unassociated", Status: "pending", Attribution: sharedspace.Attribution{Run: "unknown", Suite: suite}}}}
	c.space = source
	state, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Proposals) != 1 || state.Proposals[0].Attribution.Suite.Release != nil {
		t.Fatal("unassociated proposal supplied evaluation authority")
	}
	if source.saved[0].Attribution.Suite.Release == nil {
		t.Fatal("presentation projection mutated stored attribution")
	}
	if calls.Load() != 0 {
		t.Fatal("provenance inspection performed model work")
	}
}

func TestLegacyArchiveValidationIsMemoizedAcrossStateReads(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	id, err := c.Manual("writer", "", "Draft fictional requirements", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	bundle := c.bundles["writer"]
	root := t.TempDir()
	directory := filepath.Join(root, bundle.Digest)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(directory, "agent.yaml")
	if err := os.WriteFile(filename, bundle.Source, 0600); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.definition.archives = &contract.ArchiveResolver{Directory: root}
	c.mu.Unlock()
	first, ok := c.GetRun(id)
	if !ok || first.Suite == nil || first.Suite.GraphDigest != bundle.Suite.GraphDigest {
		t.Fatal("archive not validated")
	}
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.findLocked(id).Suite.GraphDigest = "forged-saved-identity"
	c.mu.Unlock()
	again, ok := c.GetRun(id)
	if !ok || again.Suite == nil || again.Suite.GraphDigest != bundle.Suite.GraphDigest || again.Suite.Release != nil {
		t.Fatal("validated legacy identity was not retained in memory")
	}
	state, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Runs[0].Suite.GraphDigest != bundle.Suite.GraphDigest {
		t.Fatal("state reopened or substituted saved legacy archive")
	}
}

func TestLegacyDefinitionCannotAcquireSavedReleasedEvaluation(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	id, err := c.Manual("writer", "", "Draft fictional requirements", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.mu.Lock()
	c.findLocked(id).Suite.Release = &contract.ReleaseIdentity{Evaluation: contract.EvaluationInfo{Status: "evaluation consistent; unsigned", Unsigned: true, Cases: 2, Runs: 2, CompletedAt: "2026-10-17T16:02:00Z"}}
	c.mu.Unlock()
	r, ok := c.GetRun(id)
	if !ok || r.Suite == nil || r.Suite.Release != nil || r.Evaluation != "not verified" {
		t.Fatal("legacy saved claim upgraded to release evaluation")
	}
	state, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Runs[0].Suite.Release != nil || state.Runs[0].Evaluation != "not verified" {
		t.Fatal("state trusts saved legacy release claim")
	}
}

func TestCachedReleaseIdentityCannotCrossRecordedAlias(t *testing.T) {
	c, _, _ := definedFixtureAt(t, releasedDefinitionFixture(t), nil)
	id, err := c.Manual("writer", "", "Draft fictional requirements", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.mu.Lock()
	c.findLocked(id).Agent = "reviewer"
	c.mu.Unlock()
	observed, ok := c.GetRun(id)
	if !ok || (observed.Suite != nil && observed.Suite.Release != nil) {
		t.Fatal("cached archive crossed the recorded agent alias")
	}
}

func TestReleasedDefinitionRunProvenanceCannotBeForged(t *testing.T) {
	c, _, _ := definedFixtureAt(t, releasedDefinitionFixture(t), nil)
	id, err := c.Manual("writer", "", "Draft a fictional release PRD", "GM")
	if err != nil {
		t.Fatal(err)
	}
	r := completed(t, c, id)
	if r.Suite.Release == nil || r.Suite.Release.Evaluation.Status != "not verified" {
		t.Fatal("release provenance absent")
	}
	c.mu.Lock()
	a := c.findLocked(id)
	a.Suite.Release.Evaluation.Status = "evaluation consistent; unsigned"
	a.Suite.Release.Evaluation.Unsigned = true
	c.mu.Unlock()
	observed, ok := c.GetRun(id)
	if !ok || observed.Suite.Release.Evaluation.Status != "not verified" {
		t.Fatal("persisted/UI claim treated as evaluation authority")
	}
}
