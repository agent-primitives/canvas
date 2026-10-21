package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"testing"
)

type editSnapshotFailureSpace struct {
	sharedspace.SharedSpace
	calls int
}

func (s *editSnapshotFailureSpace) Head() (string, error) {
	s.calls++
	if s.calls == 2 {
		return "", errors.New("synthetic initial snapshot failure")
	}
	return s.SharedSpace.Head()
}
func TestEditRefusesFlowWithoutAdmittedAttempt(t *testing.T) {
	c, calls := workflowFixture(t, false, nil)
	original := c.space
	c.space = &editSnapshotFailureSpace{SharedSpace: original}
	f, e := c.StartFlow(context.Background(), FlowStart{Flow: "release", RequestKey: "initial-snapshot-refusal", Inputs: json.RawMessage(`{"topic":"Fictional pilot"}`), AgentRuns: 12, Role: "GM"})
	c.space = original
	if e == nil || f.ID == "" || f.State != "paused" || f.Attempt != "" || f.Attempts != 0 {
		t.Fatal("initial snapshot cut not reached", f, e)
	}
	q := editRequest(t, c, f.ID, "no-attempt-edit")
	if _, e = c.RequestEdit(context.Background(), q); !errors.Is(e, ErrEditConflict) {
		t.Fatal("edit admitted generation-zero flow with no ordinary attempt", e)
	}
	if calls.Load() != 0 || c.data.EditSlot != "" || len(c.data.Edits) != 0 {
		t.Fatal("refused edit consumed slot or dispatched")
	}
}
