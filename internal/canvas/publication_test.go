package canvas

import (
	"errors"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/sharedspace"
)

// Preserve real Git publication; inject only the post-publication boundary error.
// A pending proposal does not itself change the review ledger, so blocking that
// file's writes alone would not reliably exercise this producer error path.
type publicationError struct{ sharedspace.SharedSpace }

func (s publicationError) Propose(base string, changes map[string]string, attribution sharedspace.Attribution) (sharedspace.Proposal, error) {
	p, err := s.SharedSpace.Propose(base, changes, attribution)
	if err != nil {
		return p, err
	}
	return p, errors.New("synthetic post-publication bookkeeping failure")
}

func TestSharedSpacePostPublicationErrorRetainsRunProposal(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	c.space = publicationError{c.space}
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.wg.Wait()
	r, _ := c.GetRun(id)
	for _, activity := range r.Activity {
		if activity.Kind == "finished" {
			t.Fatal("post-publication failure falsely marked clean completion")
		}
	}
	if r.Error == "" {
		t.Fatal("post-publication failure hidden")
	}
	state, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Proposals) != 1 || r.Proposal != state.Proposals[0].ID || r.Status != "proposed" || strings.Contains(r.Summary, "No proposal") {
		t.Fatal("published proposal identity/summary lost at shared-space error boundary")
	}
}
