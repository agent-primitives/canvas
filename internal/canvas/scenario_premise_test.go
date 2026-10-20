package canvas

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	v1 "github.com/agent-primitives/sessions/api/genpb"
)

// The retired scenario creator no longer owns this invariant: explicit generic
// requests must still survive snapshot, journal, regeneration and restart.
func TestNativeRequestsAreDistinctRecordedAndRetainedOnRegeneration(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	var originals []attempt
	var commonEvidence map[string]string
	for _, request := range []string{"Draft requirements for the fictional pilot; keep missing evidence explicit.", "Draft an alternative fictional proposal; do not invent achieved results."} {
		id, err := c.Manual("writer", "", request, "GM")
		if err != nil {
			t.Fatal(err)
		}
		r := completed(t, c, id)
		if r.Status != "proposed" {
			t.Fatalf("fixture: %+v", r)
		}
		c.wg.Wait()
		a := *c.findLocked(id)
		originals = append(originals, a)
		var input struct {
			Request  string
			Evidence map[string]string
		}
		if err := json.Unmarshal([]byte(a.Input), &input); err != nil || input.Request != request || a.Request != request || a.RequestSummary != request {
			t.Fatal("visible/stored request differs from actual premise")
		}
		if commonEvidence == nil {
			commonEvidence = input.Evidence
		} else if !reflect.DeepEqual(commonEvidence, input.Evidence) {
			t.Fatal("unaccepted proposal altered subsequent accepted evidence")
		}
		records, err := c.client.Replay(context.Background(), a.Session, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		journaled := false
		for _, record := range records {
			if record.GetEvent().GetKind() == v1.EventKind_EVENT_INPUT {
				raw, _ := json.Marshal(record.GetEvent().GetMessage())
				if strings.Contains(string(raw), request) {
					journaled = true
				}
			}
		}
		if !journaled {
			t.Fatal("actual request was not journaled on its session")
		}
	}
	if originals[0].Input == originals[1].Input || originals[0].Session == originals[1].Session || originals[0].Base != originals[1].Base {
		t.Fatal("distinct explicit requests lost identity or common accepted snapshot")
	}
	if _, err := c.space.Ingest("feedback/new-premise.json", feedbackJSON, "GM"); err != nil {
		t.Fatal(err)
	}
	for _, a := range originals {
		next, err := c.Regenerate(a.ID, "owner")
		if err != nil {
			t.Fatal(err)
		}
		r := completed(t, c, next)
		c.wg.Wait()
		regen := *c.findLocked(next)
		if r.Status != "proposed" || regen.Request != a.Request || regen.RequestSummary != a.RequestSummary || regen.PreviousAttempt != a.ID || regen.Base == a.Base || regen.Session == a.Session || regen.ParentSession != "" {
			t.Fatal("regeneration changed premise/lineage or invented a fork")
		}
		if _, err := c.Verify(context.Background(), a.ID); err != nil {
			t.Fatal(err)
		}
	}
	c.Close()
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, c.definition, contract.LocalBinder{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, a := range originals {
		r, ok := reopened.GetRun(a.ID)
		if !ok || r.RequestSummary != a.Request || r.Session != a.Session {
			t.Fatal("restart lost original request or session")
		}
	}
}
