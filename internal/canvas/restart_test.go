package canvas

import (
	"context"
	"testing"
)

func TestRestartDeduplicatesReportsAndRecoversProposals(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	if err := c.WriteInput("feedback/restart.json", feedbackJSON, "coach"); err != nil {
		t.Fatal(err)
	}
	if err := c.ScanInputs(); err != nil {
		t.Fatal(err)
	}
	state, _ := c.State()
	r := completed(t, c, state.Runs[0].ID)
	c.Close()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.ScanInputs(); err != nil {
		t.Fatal(err)
	}
	state, _ = reopened.State()
	if len(state.Runs) != 1 || state.Runs[0].Proposal != r.Proposal {
		t.Fatal("restart lost proposal or reran report")
	}
	proof, err := reopened.Verify(context.Background(), r.ID)
	if err != nil || !proof.OutputsMatch || proof.ModelCalls != 0 {
		t.Fatal("reopened proposal lost reconstruction proof")
	}
}
func TestInterruptedAttemptRequiresExplicitRegeneration(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	a, err := c.snapshotGeneric("writer", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	a.Status = "running"
	c.mu.Lock()
	c.data.Attempts = append(c.data.Attempts, a)
	if err := c.saveLocked(); err != nil {
		t.Fatal(err)
	}
	c.mu.Unlock()
	c.Close()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, c.binder)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	r, _ := reopened.GetRun(a.ID)
	if r.Status != "uncertain" || r.Session != "" {
		t.Fatal("interrupted attempt silently restarted")
	}
	id, err := reopened.Regenerate(a.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	fresh := completed(t, reopened, id)
	if fresh.Status != "proposed" || fresh.ID == a.ID || fresh.Session == "" {
		t.Fatal("explicit regeneration missing")
	}
}
