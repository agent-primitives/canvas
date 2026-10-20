package canvas

import (
	"context"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
)

type configBinder struct{ contract.LocalBinder }

func (b configBinder) Resolve(bundle contract.Bundle) (contract.Binding, error) {
	binding, err := b.LocalBinder.Resolve(bundle)
	binding.Config = []byte("{}")
	return binding, err
}

func TestBinderExecutionConfigIsRefusedBeforeSessionCreation(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	c.binder = configBinder{}
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	if r := completed(t, c, id); r.Status != "failed" || r.Session != "" {
		t.Fatalf("config binding ran: %s %q", r.Status, r.Session)
	}
	sessions, err := c.client.ListSessions(context.Background(), c.project())
	if err != nil || len(sessions) != 0 {
		t.Fatal("session created for an unsupported binding")
	}
}
