package canvas

import (
	"testing"
	"time"
)

// execute publishes the proposal before it saves Completed and Seconds. Exercise
// that real ledger transition with a controlled gap, rather than relying on CPU load.
func TestCompletedHelperWaitsForExecutionMetadataAfterProposalPublication(t *testing.T) {
	c, cleanup := setup(t, false)
	defer cleanup()
	a, err := c.snapshotGeneric("writer", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	a.Status = "proposed"
	a.Session = "recorded-fixture-session"
	a.Proposal = "recorded-fixture-proposal"
	c.mu.Lock()
	c.data.Attempts = append(c.data.Attempts, a)
	err = c.saveLocked()
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	defer func() { <-finished }()
	go func() {
		defer close(finished)
		time.Sleep(60 * time.Millisecond)
		if err := c.update(a.ID, func(saved *attempt) { saved.Completed = time.Now().UTC(); saved.Seconds = 0.06 }); err != nil {
			t.Error(err)
		}
	}()
	run := completed(t, c, a.ID)
	if run.Completed.IsZero() || run.Seconds <= 0 {
		t.Fatal("completed helper returned publication before execution metadata")
	}
}
