package canvas

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
)

func TestDocumentShowsDiffAgainstProposalBase(t *testing.T) {
	d := nativeData(t)
	c, close := nativeProviderFixture(t, d, func(_ context.Context, _ api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{Message: *api.TextMessage("assistant", changeEnvelope("announcement", "Proposed announcement\n"))}, nil
	})
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	r := completed(t, c, id)
	doc, err := c.Document("announcement.md", r.Proposal)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc["diff"], "--- announcement.md") || !strings.Contains(doc["diff"], "-# Simulated announcement") || !strings.Contains(doc["diff"], "+Proposed announcement") {
		t.Fatal("proposal diff or its base missing")
	}
	if doc["base"] == "" || doc["proposed"] == "" {
		t.Fatal("base/replacement missing")
	}
	// Another accepted draft must not silently replace this proposal's base.
	const accepted = "A different accepted dossier.\n"
	other, err := c.space.Propose(doc["baseCommit"], map[string]string{"announcement.md": accepted}, sharedspace.Attribution{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.space.Accept(other.ID, doc["baseCommit"], "GM"); err != nil {
		t.Fatal(err)
	}
	changed, err := c.Document("announcement.md", r.Proposal)
	if err != nil {
		t.Fatal(err)
	}
	if changed["accepted"] != accepted || changed["base"] != doc["base"] || changed["proposed"] != doc["proposed"] || changed["diff"] != doc["diff"] {
		t.Fatal("exact proposal evidence changed with accepted head")
	}
	if changed["comparisonKind"] != "text-lines" || !strings.Contains(changed["comparison"], "-# Simulated announcement") || !strings.Contains(changed["comparison"], "+Proposed announcement") || strings.Contains(changed["comparison"], accepted) {
		t.Fatal("comparison missing or uses accepted head instead of proposal base", changed)
	}
}
