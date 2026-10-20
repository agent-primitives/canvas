package canvas

import (
	"testing"
)

func TestQueuedRunsHaveCreationOrderBeforeExecution(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	c.sem <- struct{}{}
	id, e := c.Manual("writer", "", "Assess evidence.", "GM")
	if e != nil {
		t.Fatal(e)
	}
	r, _ := c.GetRun(id)
	if r.Created.IsZero() || !r.Started.IsZero() {
		t.Fatal("queued creation time missing")
	}
	s, _ := c.State()
	if s.Runs[len(s.Runs)-1].ID != id {
		t.Fatal("queued latest not last")
	}
}
